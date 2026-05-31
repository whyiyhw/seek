package routines

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// envOverlayPath is overridable for tests so DefaultSubprocess can
// be exercised without writing into the real ~/.seek/cron/env. The
// production default goes through paths.CronEnv. Tests assign a
// temp-file path before calling DefaultSubprocess and restore after.
var envOverlayPath = func() (string, error) { return paths.CronEnv() }

// DefaultRunTimeout is the per-run wall-clock cap (PRD §3.6
// "Subprocess timeout: hard 30 min default"). After timeout
// the engine sends SIGTERM, waits gracePeriod for clean exit,
// then SIGKILL.
const (
	DefaultRunTimeout = 30 * time.Minute
	summaryHeadBytes  = 1024
)

// Future work (M11.3 / dot release): explicit SIGTERM →
// terminateGrace → SIGKILL escalation. Today exec.CommandContext's
// cancel sends SIGKILL directly when ctx Deadline expires — which
// works but doesn't give the child a chance to flush state. A
// kinder escalation lives in feature-routines.md §9 follow-ups.

// SubprocessFn builds the *exec.Cmd used to actually run a job.
// Injectable so tests use /bin/sh stubs instead of needing a
// real `seek` binary on PATH. Production callers should use
// DefaultSubprocess (defined in cmd/seek when the binary path
// is known); the package-level default below uses os.Executable.
//
// ctx is per-run (already wrapped with the timeout); the
// function MUST use exec.CommandContext(ctx, ...) so cancellation
// propagates to the child via SIGKILL on context Done.
type SubprocessFn func(ctx context.Context, job Job, runID string) (*exec.Cmd, error)

// DefaultSubprocess builds the cron child process: `seek -p
// '<prompt>' [--yolo] [--no-save]` running with cmd.Dir set to
// the job's ProjectRoot (or the current working dir if
// unspecified). Adds --no-save by default (PRD §5.3); a future
// SaveSession field on Job will let users opt back in.
//
// Environment (feature-routines.md §3.9 "G3: subprocess env"):
// the child's env is built EXPLICITLY rather than left to Go's
// implicit "inherit parent env when cmd.Env is nil" — the parent
// here is `seek cron tick`, which itself was invoked by launchd /
// systemd / cron / Task Scheduler with whatever (often minimal)
// env that scheduler chose. Making it explicit makes the path
// auditable: `cmd.Env = os.Environ() + ~/.seek/cron/env overlay`.
//
// The overlay file is opt-in: when ~/.seek/cron/env exists, its
// KEY=VALUE entries override entries inherited from the parent
// (last-wins via cmd.Env semantics). This is the documented
// escape hatch for launchd/systemd users who can't easily inject
// DEEPSEEK_API_KEY into their unit file — the alternative would
// be embedding the secret in a plist that Time Machine backs up.
// A read-failure on the overlay file (parse error, permissions)
// is fatal to the spawn so the user notices the mis-configuration
// immediately rather than after every job silently runs without
// API auth.
//
// Returns an error if os.Executable lookup fails — better to
// surface that loudly than silently fall back to PATH and
// inherit ambiguity about which seek binary is running.
func DefaultSubprocess(ctx context.Context, job Job, runID string) (*exec.Cmd, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("routines: locate seek binary: %w", err)
	}
	// v7 柱 N: autopilot jobs fire `seek autopilot run <goal>` (its
	// subagents are no-remote-guarded for the whole run); plain jobs fire
	// `seek -p <prompt>`.
	var args []string
	if job.Autopilot {
		args = []string{"autopilot", "run", job.Prompt}
	} else {
		args = []string{"-p", job.Prompt, "--no-save"}
		if job.Yolo {
			args = append(args, "--yolo")
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if job.ProjectRoot != "" {
		cmd.Dir = job.ProjectRoot
	}

	// G3: explicit env composition. os.Environ() captures whatever
	// the OS scheduler handed us (often very little); overlay file
	// fills the gaps the scheduler couldn't see (user shell secrets).
	envPath, err := envOverlayPath()
	if err != nil {
		return nil, fmt.Errorf("routines: resolve env overlay path: %w", err)
	}
	overlay, err := LoadEnvFile(envPath)
	if err != nil {
		return nil, fmt.Errorf("routines: load env overlay: %w", err)
	}
	cmd.Env = MergeEnv(os.Environ(), overlay)
	return cmd, nil
}

// TickOptions configures one Tick invocation. All fields are
// optional with sensible defaults; tests inject the bits they
// need to drive specific paths (deterministic Now, scripted
// Subprocess, custom RunTimeout).
type TickOptions struct {
	// Now is the moment Tick treats as "current time" for
	// due-job filtering + run record timestamps. Defaults to
	// time.Now().UTC. Tests inject to get reproducible IDs.
	Now func() time.Time

	// Subprocess builds the *exec.Cmd for each due job.
	// Defaults to DefaultSubprocess. Tests pass a fake that
	// uses /bin/sh.
	Subprocess SubprocessFn

	// RunTimeout caps each job's wall-clock duration. Defaults
	// to DefaultRunTimeout (30 min). Per-job override is a
	// Step 3+ CLI feature.
	RunTimeout time.Duration

	// CronDir overrides ~/.seek/cron/ for tests. Empty →
	// paths.CronDir(). When set, RunsDir + TickLockPath are
	// derived from this root rather than the host-global path.
	CronDir string

	// Notifier dispatches the OS notification on terminal-event
	// (per Job.Notify policy). nil → DefaultNotifier (per-build-
	// tag platform shim: osascript on darwin, notify-send on
	// linux, no-op on windows + other). Tests inject a stub
	// recorder to assert without popping real banners.
	Notifier Notifier

	// Webhook is the optional push-webhook sibling of Notifier
	// (feature-mobile-push.md §D1). nil → no webhooks (the common
	// case; only set when the user configured push_webhooks).
	// Unlike Notifier it carries the event ("cron.failed" etc.)
	// so a webhook can filter, and it fires INDEPENDENT of
	// Job.Notify — the desktop popup and the remote channel are
	// orthogonal knobs (§D4).
	Webhook WebhookDispatcher
}

func (o *TickOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

func (o *TickOptions) subprocess() SubprocessFn {
	if o.Subprocess != nil {
		return o.Subprocess
	}
	return DefaultSubprocess
}

func (o *TickOptions) runTimeout() time.Duration {
	if o.RunTimeout > 0 {
		return o.RunTimeout
	}
	return DefaultRunTimeout
}

func (o *TickOptions) notifier() Notifier {
	if o.Notifier != nil {
		return o.Notifier
	}
	return DefaultNotifier
}

// tickPaths bundles the four paths Tick writes under. Either
// derived from paths.CronDir() (production) or from opts.CronDir
// (tests).
type tickPaths struct {
	cronDir      string
	runsDir      string
	tickLockPath string
}

func resolveTickPaths(opts TickOptions) (tickPaths, error) {
	if opts.CronDir != "" {
		return tickPaths{
			cronDir:      opts.CronDir,
			runsDir:      filepath.Join(opts.CronDir, "runs"),
			tickLockPath: filepath.Join(opts.CronDir, "tick.lock"),
		}, nil
	}
	dir, err := paths.CronDir()
	if err != nil {
		return tickPaths{}, err
	}
	return tickPaths{
		cronDir:      dir,
		runsDir:      filepath.Join(dir, "runs"),
		tickLockPath: filepath.Join(dir, "tick.lock"),
	}, nil
}

// TickResult summarises one Tick invocation.
type TickResult struct {
	// Skipped is true when tick.lock was held by another
	// process; this Tick was a silent no-op.
	Skipped bool
	// Considered is the count of jobs Tick looked at.
	Considered int
	// Fired is the count of jobs that crossed NextRun and got
	// a goroutine. Note: Fired counts ATTEMPTS — a job whose
	// per-job lock was held still increments this so the
	// caller can tell "tick wasn't idle" apart from "tick
	// idle".
	Fired int
	// TriggersDispatched is the count of triggers/<id>.json
	// files actually run this tick (M11.3 file bridge).
	// Excludes skipped-as-too-fresh, TTL-expired, malformed.
	TriggersDispatched int
	// GCRemoved aggregates how many old run + malformed-trigger
	// files this tick swept (G4 + G5 — v0.6.x housekeeping).
	// Always >= 0; zero is the steady-state on a quiet host.
	GCRemoved int
}

// Tick runs one scan-and-fire cycle. Acquires tick.lock LOCK_NB
// (skip silently if held); reads jobs from store; for each due
// job, spawns a goroutine that acquires the per-job lock,
// records the run, and updates the store on completion. Waits
// for all goroutines before returning.
//
// Cancellable: ctx Done before all jobs finish → outstanding
// subprocesses get SIGKILL via their per-run ctx; recorders
// flush "killed" terminal events; Tick returns ctx.Err() after
// goroutine drain.
//
// Returns ctx.Err() on cancellation; flock / I/O errors on
// path setup; nil on a clean tick (whether jobs ran or not).
func Tick(ctx context.Context, store *Store, opts TickOptions) (TickResult, error) {
	if store == nil {
		return TickResult{}, errors.New("routines: Tick: nil Store")
	}
	tp, err := resolveTickPaths(opts)
	if err != nil {
		return TickResult{}, err
	}

	// 1. Host-wide tick.lock LOCK_NB. Another tick (or a
	//    long-running Store mutation that uses the same lock,
	//    if we ever add that path) → skip silently. OS scheduler
	//    fires again in ~1 minute; the skip cost is one open()
	//    + flock syscall.
	tickLock, ok, err := TryLock(tp.tickLockPath)
	if err != nil {
		return TickResult{}, fmt.Errorf("routines: tick.lock: %w", err)
	}
	if !ok {
		return TickResult{Skipped: true}, nil
	}
	defer tickLock.Close()

	// 2-3. Load + filter due jobs under `now`.
	now := opts.now()
	jobs, err := store.List()
	if err != nil {
		return TickResult{}, err
	}
	due := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		if j.NextRun.IsZero() || !j.NextRun.After(now) {
			due = append(due, j)
		}
	}

	// 4. Per-job goroutine fan-out. NOTE: do NOT early-return on
	// len(due) == 0 — triggers/ inbox and the housekeeping GC
	// (steps 5-6) must run every tick regardless of cron-job
	// state. An empty WaitGroup.Wait() is a sub-microsecond no-op,
	// so falling through costs nothing on idle ticks.
	res := TickResult{Considered: len(jobs), Fired: len(due)}

	subFn := opts.subprocess()
	runTimeout := opts.runTimeout()
	notifier := opts.notifier()
	webhook := opts.Webhook

	var wg sync.WaitGroup
	for _, j := range due {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			runOne(ctx, store, tp.runsDir, tp.cronDir, j, now, runTimeout, subFn, notifier, webhook)
		}(j)
	}
	wg.Wait()

	// 5. Drain the triggers/ inbox (PRD §3.6 step 5). Runs
	// AFTER cron jobs so jobs always get priority on a busy
	// tick. Errors logged inline; one bad trigger doesn't stop
	// the rest, and the directory-listing error is the only
	// thing that bubbles up (rare — ENOENT degrades silently).
	triggersDir := filepath.Join(tp.cronDir, "triggers")
	if dispatched, terr := processTriggers(ctx, triggersDir, tp.runsDir, now, subFn, notifier, webhook); terr != nil {
		fmt.Fprintf(os.Stderr, "routines: triggers: %v\n", terr)
	} else {
		res.TriggersDispatched = dispatched
	}

	// 6. Housekeeping (G4 + G5): sweep old runs/ + .malformed
	// files under the still-held tick.lock so no concurrent tick
	// can race file deletion against file creation. Best-effort —
	// per-file errors are logged but never fail the tick.
	if removed, gerr := GCRuns(GCRunsOptions{Dir: tp.runsDir, Now: now}); gerr != nil {
		fmt.Fprintf(os.Stderr, "routines: gc runs: %v\n", gerr)
	} else {
		res.GCRemoved += removed
	}
	malformedDir := filepath.Join(triggersDir, ".malformed")
	if removed, gerr := GCMalformedTriggers(GCMalformedOptions{Dir: malformedDir, Now: now}); gerr != nil {
		fmt.Fprintf(os.Stderr, "routines: gc malformed: %v\n", gerr)
	} else {
		res.GCRemoved += removed
	}

	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, nil
}

// RunOne fires the named job immediately, regardless of its
// scheduled NextRun. Used by `seek cron run <name>` CLI to
// force a fire bypassing the schedule.
//
// Takes the per-job lock (runs/<name>.lock) — so a `cron run`
// can't double-fire concurrently with a scheduled tick of the
// same job — but does NOT take tick.lock. That means a
// scheduled `seek cron tick` invoked by OS scheduler can run
// in parallel with `seek cron run` for a DIFFERENT job; only
// the same-job collision is what the per-job lock guards
// against.
//
// Returns ErrJobNotFound if name isn't registered. Otherwise
// returns nil — the outcome of the run (completed/failed/
// killed) lives in the Store's LastStatus and the run record
// file. Errors surfaced from within runOne (lock acquisition,
// I/O) are logged to stderr by runOne itself; RunOne's nil
// return reflects "the orchestrator did its job", not "the
// subprocess succeeded".
func RunOne(parentCtx context.Context, store *Store, name string, opts TickOptions) error {
	if store == nil {
		return errors.New("routines: RunOne: nil Store")
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	job, err := store.Get(name)
	if err != nil {
		return err
	}
	tp, err := resolveTickPaths(opts)
	if err != nil {
		return err
	}
	runOne(parentCtx, store, tp.runsDir, tp.cronDir, job, opts.now(), opts.runTimeout(), opts.subprocess(), opts.notifier(), opts.Webhook)
	return nil
}

// runOne is the body of the per-job goroutine. Three locking
// outcomes:
//
//   - lock free → ran the subprocess, wrote a run record, called
//     Store.MarkRun.
//   - lock held → another tick is still running this job from a
//     prior fire; skip silently.
//   - lock acquire I/O error → log to stderr, skip; don't crash
//     the whole tick.
func runOne(parentCtx context.Context, store *Store, runsDir, cronDir string, job Job, ranAt time.Time, timeout time.Duration, subFn SubprocessFn, notify Notifier, webhook WebhookDispatcher) {
	jobLockPath := filepath.Join(runsDir, job.Name+".lock")
	jobLock, ok, err := TryLock(jobLockPath)
	if err != nil {
		// Lock infrastructure broken; don't kill the whole
		// tick — log via stderr and move on. The job stays at
		// its current NextRun; next tick will retry.
		fmt.Fprintf(os.Stderr, "routines: per-job lock %s: %v\n", job.Name, err)
		return
	}
	if !ok {
		// Prior fire still in flight; skip silently. The next
		// tick will retry if it's due again by then.
		return
	}
	defer jobLock.Close()

	// Per-run context: timeout + cancellable on parent.
	runCtx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	runID := NewRunID(ranAt)
	startedAt := ranAt

	// Build the subprocess BEFORE writing the run record header
	// — we want the header.command field to reflect the actual
	// argv that gets exec'd. If Subprocess returns an error
	// (e.g. os.Executable failed), no run file is written; we
	// still call MarkRun so the failure surfaces in
	// `seek cron list` as last_status=failed.
	cmd, subErr := subFn(runCtx, job, runID)
	if subErr != nil {
		errMsg := fmt.Sprintf("subprocess build failed: %v", subErr)
		_ = store.MarkRun(job.Name, runID, StatusFailed, errMsg, ranAt)
		return
	}

	rr, err := NewRecorder(runsDir, runID, job.Name, job.ProjectRoot,
		append([]string{cmd.Path}, cmd.Args[1:]...), job.Yolo, startedAt)
	if err != nil {
		// Failed to create the run record — log + mark failed.
		errMsg := fmt.Sprintf("create run record: %v", err)
		_ = store.MarkRun(job.Name, runID, StatusFailed, errMsg, ranAt)
		return
	}
	defer rr.Close()

	// Pipe stdout / stderr through goroutines that write events
	// to the run record. Buffered reader so each Read returns a
	// reasonable chunk; the recorder's mu serialises writes.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = rr.WriteFailed(-1, 0, "stdout pipe: "+err.Error())
		_ = store.MarkRun(job.Name, runID, StatusFailed, err.Error(), ranAt)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = rr.WriteFailed(-1, 0, "stderr pipe: "+err.Error())
		_ = store.MarkRun(job.Name, runID, StatusFailed, err.Error(), ranAt)
		return
	}

	if err := cmd.Start(); err != nil {
		_ = rr.WriteFailed(-1, 0, "start: "+err.Error())
		_ = store.MarkRun(job.Name, runID, StatusFailed, err.Error(), ranAt)
		return
	}

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

	// Classify outcome + dispatch OS notification per
	// job.Notify policy. Notifications fire AFTER MarkRun so
	// the store reflects the run before the user clicks/sees
	// the popup (no race where the popup arrives but `seek
	// cron list` still shows old status).
	var (
		terminalStatus string
		terminalErrMsg string
		terminalNote   string // body text fed to OS notification
	)
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		// Wait for grace period after the kernel SIGKILL'd via
		// CommandContext, then write killed. (DeadlineExceeded
		// triggers cancel() which sends SIGKILL; cmd.Wait
		// returns once the process exits.) The grace is
		// implicit here — cmd.Wait already returned, so we
		// just record.
		_ = rr.WriteKilled("timeout", dur)
		terminalStatus = StatusKilled
		terminalErrMsg = "timeout"
		terminalNote = fmt.Sprintf("killed after %s (timeout)", dur.Round(time.Second))
	case errors.Is(parentCtx.Err(), context.Canceled) && runCtx.Err() != nil:
		_ = rr.WriteKilled("canceled", dur)
		terminalStatus = StatusKilled
		terminalErrMsg = "canceled"
		terminalNote = "canceled mid-run"
	case waitErr == nil:
		exit := cmd.ProcessState.ExitCode()
		summaryMu.Lock()
		summary := strings.TrimRight(summaryBuf.String(), "\n")
		summaryMu.Unlock()
		_ = rr.WriteCompleted(exit, dur, summary)
		terminalStatus = StatusCompleted
		terminalNote = summary
		if terminalNote == "" {
			terminalNote = fmt.Sprintf("completed in %s", dur.Round(time.Second))
		}
	default:
		exit := -1
		if ps := cmd.ProcessState; ps != nil {
			exit = ps.ExitCode()
		}
		errMsg := waitErr.Error()
		_ = rr.WriteFailed(exit, dur, errMsg)
		terminalStatus = StatusFailed
		terminalErrMsg = errMsg
		terminalNote = errMsg
	}

	_ = store.MarkRun(job.Name, runID, terminalStatus, terminalErrMsg, ranAt)

	// Notification dispatch — best-effort. A notify failure
	// writes WARN to stderr but never undoes MarkRun. Per PRD
	// §3.8 "fallback (binary missing): write WARN to the run
	// record, don't fail the cron run itself" — we stick that
	// WARN on stderr too so launchd / systemd logs surface it.
	title := fmt.Sprintf("seek cron: %s (%s)", job.Name, terminalStatus)
	if notify != nil && shouldNotify(job, terminalStatus) {
		if err := notify(title, terminalNote); err != nil {
			fmt.Fprintf(os.Stderr, "routines: notify failed for %s: %v\n", job.Name, err)
		}
	}
	// Webhook push fires INDEPENDENT of Job.Notify (§D4): the desktop
	// popup and the remote channel are orthogonal. The dispatcher
	// filters by its own per-webhook events list. Best-effort — never
	// touches the already-written run record.
	if webhook != nil {
		webhook(parentCtx, "cron."+terminalStatus, title, terminalNote)
	}

	_ = cronDir // reserved for triggers/ dispatcher (M11.3 follow-up)
}

// drainStream reads chunks from r until EOF, calling onChunk
// for each non-empty chunk. Uses bufio so reads are buffered
// (default 4 KB) — small enough that "live" tailing of the
// run file shows progress, large enough to avoid syscall
// overhead per byte.
func drainStream(wg *sync.WaitGroup, r io.Reader, onChunk func(string)) {
	defer wg.Done()
	buf := bufio.NewReader(r)
	tmp := make([]byte, 4096)
	for {
		n, err := buf.Read(tmp)
		if n > 0 {
			onChunk(string(tmp[:n]))
		}
		if err != nil {
			return
		}
	}
}
