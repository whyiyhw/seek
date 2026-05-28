package routines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// triggerMtimeQuiet is the minimum age a trigger file must
// have before tick processes it. Defends against the
// half-written-file race per PRD §8: an external producer
// (CI / IDE plugin) writing the JSON with stat+rename or
// streaming chunks could be mid-write when tick scans. One
// second is the conventional "writer is done" signal in
// file-dropping inboxes.
const triggerMtimeQuiet = 1 * time.Second

// Trigger is the on-disk shape of one external trigger file
// (~/.seek/cron/triggers/<trigger-id>.json). External systems
// (CI hooks, IDE plugins, "I just merged a PR" tools) drop
// these; tick consumes + deletes. PRD §3.4.
//
// Wire format: field renames break in-flight triggers from
// existing producers. Add new optional fields with omitempty;
// never rename or drop.
type Trigger struct {
	TriggerID   string    `json:"trigger_id"`
	Prompt      string    `json:"prompt"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
	ProjectRoot string    `json:"project_root,omitempty"`
	// TTLMinutes caps how long a trigger waits in the inbox
	// before being abandoned. 0 = no TTL (tick consumes
	// whenever it next runs). Producers set this to bound
	// "what if seek is offline for a week" — the trigger
	// auto-expires instead of running with stale context.
	TTLMinutes int `json:"ttl_minutes,omitempty"`
}

// processTriggers scans triggersDir for .json files, parses
// each, and dispatches the valid + non-expired ones via the
// supplied SubprocessFn. Side effects:
//
//   - Valid + non-expired → spawn subprocess, write run record,
//     delete the trigger file (consumed).
//   - TTL expired → delete the trigger file + write stderr
//     WARN naming the trigger_id + age.
//   - Malformed JSON → rename to triggers/.malformed/<base>
//     so a human can inspect; stderr WARN.
//   - mtime within triggerMtimeQuiet of now → skip this tick;
//     next tick will retry (defends against half-written
//     files from CI scripts that don't atomic-rename).
//
// Returns the count of triggers ACTUALLY dispatched (excluded:
// skipped-as-too-fresh, expired, malformed). Errors during
// directory listing surface; per-file errors are stderr WARN
// and processing continues (one broken trigger doesn't block
// the rest).
func processTriggers(ctx context.Context, triggersDir, runsDir string, now time.Time, subFn SubprocessFn, notify Notifier) (int, error) {
	entries, err := os.ReadDir(triggersDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No triggers/ dir yet → no triggers to process.
			// First-ever use of the trigger feature; not an
			// error.
			return 0, nil
		}
		return 0, fmt.Errorf("routines: read triggers dir: %w", err)
	}

	dispatched := 0
	for _, ent := range entries {
		if ent.IsDir() {
			// Skip nested dirs (incl. .malformed/ quarantine).
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".json") {
			continue // ignore non-JSON files in the inbox
		}
		path := filepath.Join(triggersDir, name)
		if processed := processOneTrigger(ctx, path, runsDir, now, subFn, notify); processed {
			dispatched++
		}
	}
	return dispatched, nil
}

// processOneTrigger handles one trigger file. Returns true
// iff the trigger was actually dispatched (subprocess fired);
// false on skip/expire/malformed paths. Each non-dispatch
// reason gets a stderr WARN naming the file so launchd /
// systemd logs surface actionable hints.
func processOneTrigger(ctx context.Context, path, runsDir string, now time.Time, subFn SubprocessFn, notify Notifier) bool {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "routines: trigger %s: stat: %v\n", path, err)
		return false
	}

	// Quiescence check — file must be at least triggerMtimeQuiet
	// old. Defends against partial writes from producers that
	// stream instead of stat+rename.
	if now.Sub(info.ModTime()) < triggerMtimeQuiet {
		// Don't WARN here — this is the steady-state
		// "producer just wrote, will be ready next tick"
		// signal, not an error.
		return false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "routines: trigger %s: read: %v\n", path, err)
		return false
	}

	var trig Trigger
	if err := json.Unmarshal(data, &trig); err != nil {
		quarantine(path, "malformed JSON")
		fmt.Fprintf(os.Stderr, "routines: trigger %s: malformed JSON, quarantined: %v\n", path, err)
		return false
	}
	if trig.TriggerID == "" || trig.Prompt == "" {
		quarantine(path, "missing required fields")
		fmt.Fprintf(os.Stderr, "routines: trigger %s: missing trigger_id or prompt, quarantined\n", path)
		return false
	}

	// TTL check. Zero = no TTL.
	if trig.TTLMinutes > 0 && !trig.CreatedAt.IsZero() {
		expiry := trig.CreatedAt.Add(time.Duration(trig.TTLMinutes) * time.Minute)
		if now.After(expiry) {
			_ = os.Remove(path)
			fmt.Fprintf(os.Stderr, "routines: trigger %s expired (age %s > TTL %dm); deleted\n",
				trig.TriggerID, now.Sub(trig.CreatedAt).Round(time.Second), trig.TTLMinutes)
			return false
		}
	}

	// Dispatch as a one-shot fire. Synthesize a Job-shaped
	// view so we can reuse SubprocessFn (which builds the
	// `seek -p` command from Job.Prompt / Job.ProjectRoot /
	// Job.Yolo). The synthetic job is NEVER persisted to
	// jobs.jsonl — triggers are ad-hoc, not registered.
	synthJob := Job{
		Name:        "trigger-" + trig.TriggerID,
		Prompt:      trig.Prompt,
		ProjectRoot: trig.ProjectRoot,
		Yolo:        true, // triggers come from external systems; no user to ask
		Notify:      NotifyAlways,
	}
	runID := NewRunID(now)

	cmd, subErr := subFn(ctx, synthJob, runID)
	if subErr != nil {
		fmt.Fprintf(os.Stderr, "routines: trigger %s: subprocess build: %v\n", trig.TriggerID, subErr)
		_ = os.Remove(path)
		return false
	}

	// Run record for the trigger fire. Lives in the same
	// runs/ dir as cron job runs; the header's job_name
	// "trigger-<id>" disambiguates.
	rr, err := NewRecorder(runsDir, runID, synthJob.Name, synthJob.ProjectRoot,
		append([]string{cmd.Path}, cmd.Args[1:]...), synthJob.Yolo, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "routines: trigger %s: run record: %v\n", trig.TriggerID, err)
		_ = os.Remove(path)
		return false
	}
	defer rr.Close()

	// Trigger is consumed regardless of outcome — leaving the
	// file would re-fire it on the next tick. Producer can
	// re-drop if they want a retry.
	defer os.Remove(path)

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		_ = rr.WriteFailed(-1, 0, "start: "+err.Error())
		return false
	}
	startedAt := now
	var (
		streamersWG sync.WaitGroup
		summaryBuf  strings.Builder
		summaryMu   sync.Mutex
	)
	streamersWG.Add(2)
	go drainStream(&streamersWG, stdoutPipe, func(chunk string) {
		_ = rr.WriteStdout(chunk)
		summaryMu.Lock()
		if summaryBuf.Len() < summaryHeadBytes {
			remaining := summaryHeadBytes - summaryBuf.Len()
			if len(chunk) > remaining {
				chunk = chunk[:remaining]
			}
			summaryBuf.WriteString(chunk)
		}
		summaryMu.Unlock()
	})
	go drainStream(&streamersWG, stderrPipe, func(chunk string) {
		_ = rr.WriteStderr(chunk)
	})

	waitErr := cmd.Wait()
	streamersWG.Wait()
	dur := time.Since(startedAt)

	var (
		terminalStatus string
		terminalNote   string
	)
	switch {
	case waitErr == nil:
		exit := cmd.ProcessState.ExitCode()
		summaryMu.Lock()
		summary := strings.TrimRight(summaryBuf.String(), "\n")
		summaryMu.Unlock()
		_ = rr.WriteCompleted(exit, dur, summary)
		terminalStatus = StatusCompleted
		terminalNote = summary
		if terminalNote == "" {
			terminalNote = "trigger completed"
		}
	default:
		exit := -1
		if ps := cmd.ProcessState; ps != nil {
			exit = ps.ExitCode()
		}
		errMsg := waitErr.Error()
		_ = rr.WriteFailed(exit, dur, errMsg)
		terminalStatus = StatusFailed
		terminalNote = errMsg
	}

	if notify != nil {
		title := fmt.Sprintf("seek trigger: %s (%s)", trig.TriggerID, terminalStatus)
		if err := notify(title, terminalNote); err != nil {
			fmt.Fprintf(os.Stderr, "routines: notify failed for trigger %s: %v\n", trig.TriggerID, err)
		}
	}
	return true
}

// quarantine renames the trigger file into triggers/.malformed/
// so a human can inspect what went wrong without losing the
// payload. Failure to MkdirAll / rename gets stderr-logged but
// processing continues — the trigger is going to be deleted
// anyway (it's broken), the .malformed copy is a courtesy.
func quarantine(path, reason string) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	malformedDir := filepath.Join(dir, ".malformed")
	if err := os.MkdirAll(malformedDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "routines: quarantine mkdir: %v\n", err)
		_ = os.Remove(path)
		return
	}
	dst := filepath.Join(malformedDir, base)
	if err := os.Rename(path, dst); err != nil {
		fmt.Fprintf(os.Stderr, "routines: quarantine rename %s: %v\n", reason, err)
		_ = os.Remove(path)
	}
}

// triggerSubprocess is the default SubprocessFn shape for the
// trigger path. Reuses DefaultSubprocess from tick.go — the
// `seek -p '<prompt>'` shell-out is identical to cron jobs.
// Exposed as a separate symbol only so future trigger-specific
// flags (e.g. always-set --no-save regardless of job config)
// can attach here without touching the cron path.
var triggerSubprocess SubprocessFn = func(ctx context.Context, job Job, runID string) (*exec.Cmd, error) {
	return DefaultSubprocess(ctx, job, runID)
}
