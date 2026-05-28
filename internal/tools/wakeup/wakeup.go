// Package wakeup implements the `schedule_wakeup` LLM tool —
// the model's surface over internal/routines for one-shot
// future-time wake-ups. Mirrors the thin-wrapper pattern of
// internal/tools/agent: schema + Execute here, all heavy
// lifting in internal/routines.Store + tick engine.
//
// Use cases:
//
//   - "check that CI build in 5 minutes" → schedule_wakeup(
//     delay_seconds=300, prompt="check CI status of build #N")
//   - "follow up on this PR in an hour" → schedule_wakeup(
//     delay_seconds=3600, prompt="check if PR #M got reviewed")
//   - "remind me about this thing tomorrow" → schedule_wakeup(
//     delay_seconds=86400, prompt="remind me to ...")
//
// The wakeup runs as a one-shot cron job (max_runs=1, auto-
// deleted after firing). It executes via `seek -p '<prompt>'`
// subprocess — the wakeup itself doesn't preserve any context
// from the calling session; the prompt MUST be self-contained.
//
// PRD docs/prd/feature-routines.md §3.7.
package wakeup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/whyiyhw/seek/internal/routines"
	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "delay_seconds": {
      "type": "integer",
      "minimum": 60,
      "maximum": 86400,
      "description": "Seconds from now until the wakeup fires. Minimum 60 (1 minute); maximum 86400 (24 hours). Sub-minute wake-ups not supported — schedule.@every floor."
    },
    "prompt": {
      "type": "string",
      "description": "The user prompt fed to the wakeup-run subprocess. The wakeup runs in a FRESH seek with no memory of this conversation — write a self-contained briefing including any context the wakeup needs."
    }
  },
  "required": ["delay_seconds", "prompt"],
  "additionalProperties": false
}`)

const description = `Schedule a one-shot wakeup: register a cron job that fires <delay_seconds> seconds from now (60s–86400s = 1min–24h), runs the supplied <prompt> via a fresh ` + "`seek -p`" + ` subprocess, then auto-deletes itself. Use for "check this CI in 5 minutes" / "follow up on this PR in an hour" / "remind me tomorrow" patterns where you want to act on something later without blocking the current turn. The wakeup sees NO context from this conversation beyond the prompt — write it self-contained. Result is wire-format ` + "`[schedule: waking at <iso8601>]`" + ` so subsequent turns can reference it.`

// Args is the strict-decode target. JSON tags must match
// schemaBytes exactly (UnmarshalStrict rejects mismatches).
type Args struct {
	DelaySeconds int    `json:"delay_seconds"`
	Prompt       string `json:"prompt"`
}

// Length caps enforced in Execute beyond what JSON Schema
// declares: schema's `delay_seconds: minimum/maximum` aren't
// honoured by UnmarshalStrict (just DisallowUnknownFields),
// so Execute re-validates.
const (
	minDelaySeconds = 60
	maxDelaySeconds = 86400
	maxPromptBytes  = 32 * 1024
)

// Tool is the LLM-facing wrapper. Constructed with New(store);
// the store mutates ~/.seek/cron/jobs.jsonl (atomic rewrite).
// nil store means "host program didn't wire cron" — tool isn't
// registered at all, so reaching New(nil) is a programmer
// error.
type Tool struct {
	store *routines.Store

	// now is the time source for delay computation. Tests
	// inject a fixed clock so wakeup IDs / next_run_at are
	// reproducible.
	now func() time.Time
}

// New constructs the tool. store must be non-nil; nil signals
// "host couldn't open the cron registry" (e.g. permission
// error on ~/.seek/) — in that case the host should branch on
// Store availability and SKIP registration. Reaching New(nil)
// at runtime is a programmer error.
func New(store *routines.Store) *Tool {
	if store == nil {
		panic("wakeup: New called with nil Store — host did not wire internal/routines")
	}
	return &Tool{store: store, now: defaultNow}
}

func defaultNow() time.Time { return time.Now().UTC() }

// WithNow overrides the time source. TESTS ONLY — exposed as a
// method so tests can construct a Tool then pin the clock for
// deterministic wakeup IDs / next_run_at values.
func (t *Tool) WithNow(fn func() time.Time) *Tool {
	t.now = fn
	return t
}

func (*Tool) Name() string            { return "schedule_wakeup" }
func (*Tool) Description() string     { return description }
func (*Tool) Schema() json.RawMessage { return schemaBytes }

// ReadOnly implements tools.ReadOnlyTool. Marking schedule_wakeup
// as read-only is the SAME semantic stretch as agent tool's
// ReadOnly: the underlying operation MUTATES disk state
// (jobs.jsonl gets a new entry), but the marker is consumed by
// pkg/agent.allReadOnly() for concurrent dispatch — not by
// permission gate. Store.Create is concurrent-safe (sync.Mutex
// + atomic rewrite), so two parallel schedule_wakeup calls in
// the same turn are safe.
//
// Without this marker, batched `[schedule_wakeup, schedule_wakeup,
// schedule_wakeup]` calls would serialise at the agent loop —
// defeating the use case "let me set up three different follow-
// ups at once".
func (*Tool) ReadOnly() bool { return true }

// Execute validates args, builds the cron job, persists it.
// Returns wire-format wakeup confirmation as the tool result;
// failure paths return `[schedule: failed reason=<X>]` so the
// LLM reads a structured signal rather than a wrapped Go err.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("schedule_wakeup", raw, &a,
		"delay_seconds", "prompt"); err != nil {
		return "", err
	}
	if a.Prompt == "" {
		return "", tools.MissingField("schedule_wakeup", "prompt", raw,
			"delay_seconds", "prompt")
	}
	if a.DelaySeconds == 0 {
		return "", tools.MissingField("schedule_wakeup", "delay_seconds", raw,
			"delay_seconds", "prompt")
	}
	if a.DelaySeconds < minDelaySeconds {
		return failWire("delay_too_short",
			fmt.Sprintf("delay_seconds=%d below minimum %d (Schedule.@every floor)", a.DelaySeconds, minDelaySeconds)), nil
	}
	if a.DelaySeconds > maxDelaySeconds {
		return failWire("delay_too_long",
			fmt.Sprintf("delay_seconds=%d above maximum %d (24h); split into earlier wakeup that re-schedules", a.DelaySeconds, maxDelaySeconds)), nil
	}
	if len(a.Prompt) > maxPromptBytes {
		return failWire("prompt_too_long",
			fmt.Sprintf("prompt exceeds %d bytes; trim or split", maxPromptBytes)), nil
	}

	now := t.now()
	delay := time.Duration(a.DelaySeconds) * time.Second
	// Schedule string: minimum is 60s; Go's time.Duration
	// formats sub-hour durations as "<min>m<sec>s" so the
	// resulting @every is human-readable for `seek cron list`.
	schedRaw := "@every " + delay.String()
	sched, err := routines.ParseSchedule(schedRaw)
	if err != nil {
		// Shouldn't hit — minimums above guarantee parseable.
		// But surfacing the parse err is more useful than a
		// silent shrug if a future Schedule change breaks the
		// invariant.
		return failWire("schedule_build_error", err.Error()), nil
	}

	name := autoName(now)
	job := routines.Job{
		Name:     name,
		Schedule: sched,
		Prompt:   a.Prompt,
		// NextRun pinned to now+delay so the wakeup fires at
		// EXACTLY the requested moment (not now+delay rounded
		// to the next @every boundary). Schedule.Next is only
		// consulted on the next fire, which never happens
		// because MaxRuns=1 → auto-delete.
		NextRun:    now.Add(delay),
		MaxRuns:    1,
		Yolo:       true,
		Notify:     routines.NotifyAlways,
		LastStatus: routines.StatusScheduled,
	}
	if err := t.store.Create(job, routines.CreateOptions{}); err != nil {
		return failWire("store_error", err.Error()), nil
	}

	when := job.NextRun.Format(time.RFC3339)
	return fmt.Sprintf("[schedule: waking at %s] (job %s)", when, name), nil
}

// autoName returns "wakeup-<timestamp>-<6hex>" — same shape as
// routines.NewRunID so visual inspection of `seek cron list`
// immediately reveals wakeup-spawned entries. The wakeup-
// prefix distinguishes from user-created cron jobs (which get
// either user-supplied names or "cron-<id>" defaults).
func autoName(now time.Time) string {
	id := routines.NewRunID(now)
	return "wakeup-" + id
}

// failWire formats a wire-format failure string per
// feature-routines.md §3.7. The "[schedule: failed reason=<X>]"
// prefix is byte-stable contract (matches plan-mode + subagent
// conventions); the trailing hint is display-only.
func failWire(reason, hint string) string {
	return fmt.Sprintf("[schedule: failed reason=%s] %s", reason, hint)
}

// Compile-time interface assertions: Tool satisfies both
// tools.Tool AND tools.ReadOnlyTool. ReadOnly's parallel-
// dispatch property would silently regress if the method got
// dropped in a future refactor; the assertion forces a build
// break instead of a runtime degradation.
var (
	_ tools.Tool         = (*Tool)(nil)
	_ tools.ReadOnlyTool = (*Tool)(nil)
)
