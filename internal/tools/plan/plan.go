// Package plan implements the `plan` tool: a structured task list that
// tracks execution progress against a plan the user has approved via
// `propose`. The tool exposes start / complete / skip actions on
// 1-based step indices; mutations are reflected to the TUI through a
// Sink (typically wired to agent.EmitEvent in cmd/seek).
//
// State model. The tool holds an authoritative in-memory list of steps
// (each {Text, Status}) plus a currentIdx pointing at the
// in_progress step (-1 = none). Seed replaces the list wholesale —
// called by the propose Sink on user approval. Each Execute mutates
// one slot and emits a snapshot through the Sink. The Sink call
// happens AFTER the mutex is released to avoid re-entrancy deadlocks
// if a future Sink implementation calls back into Snapshot.
//
// Concurrency. Execute is called from the agent's single Prompt
// goroutine, but Snapshot is read by TUI tests on a separate goroutine
// and the resume path (Phase B) seeds from a non-agent goroutine.
// Hence the mutex.
//
// State persistence. The tool itself does NOT write to disk. PlanState
// is reconstructed on session resume by scanning the transcript for
// the last `[plan: approved]` result and forward-applying every plan
// tool call that followed it (see Phase B). Keeping state derivable
// from the message log avoids a second source of truth and dodges
// session schema migrations (CLAUDE.md: "Session format is JSONL …").
//
// See docs/prd/feature-plan-mode.md for the full plan-mode workflow.
package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/whyiyhw/seek/internal/tools"
)

const toolName = "plan"

const description = "Track execution progress against the plan approved by propose. After the user approves a plan, call plan(action=\"start\", index=N) before beginning step N, then plan(action=\"complete\", index=N) when the step is done. The TUI renders a live task list reflecting these calls and the session resume path replays them, so progress survives restarts. Use action=\"skip\" only for steps the user explicitly waved off; prefer re-propose for larger scope changes. Index is 1-based. Calling before propose is approved (no active plan) returns an error."

// schemaBytes is a package-level []byte so the wire bytes stay stable
// across turns (CLAUDE.md: identical schema bytes = DeepSeek prefix
// cache hits).
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["start", "complete", "skip"],
      "description": "What to do with the step. 'start' marks it in_progress (the previous in_progress step is reset to pending). 'complete' marks it done. 'skip' marks it skipped — prefer re-propose for bigger scope changes."
    },
    "index": {
      "type": "integer",
      "minimum": 1,
      "description": "1-based step index to act on. Must be within the active plan's step count."
    }
  },
  "required": ["action", "index"],
  "additionalProperties": false
}`)

// Status is the lifecycle phase of one step. The string values are
// load-bearing: agent.PlanStep.Status carries them verbatim across the
// event boundary, and the TUI's renderer switches on them.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusSkipped    Status = "skipped"
)

// Step is one row of the plan.
type Step struct {
	Text   string
	Status Status
}

// Sink observes PlanState mutations. Implementations typically forward
// to agent.EmitEvent(PlanStepUpdated{...}) so the TUI re-renders.
// snapshot is a defensive copy — Sink implementations may retain it
// without further synchronisation. currentIdx is the 0-based index of
// the in_progress step, or -1 when no step is active.
//
// Nil sinks are tolerated by Tool (no events emitted) so the tool can
// be unit-tested without a wiring harness.
type Sink interface {
	StepChanged(snapshot []Step, currentIdx int)
}

// Tool is the plan tool implementation.
type Tool struct {
	mu         sync.Mutex
	steps      []Step
	currentIdx int
	sink       Sink
}

// New constructs the tool. sink may be nil — production wires a non-nil
// adapter that emits agent events; tests pass nil to assert pure state
// behaviour or a recording sink to verify call ordering.
func New(sink Sink) *Tool {
	return &Tool{sink: sink, currentIdx: -1}
}

// Name implements tools.Tool.
func (*Tool) Name() string { return toolName }

// Description implements tools.Tool.
func (*Tool) Description() string { return description }

// Schema implements tools.Tool.
func (*Tool) Schema() json.RawMessage { return schemaBytes }

// Seed replaces the active plan with the given step texts, all
// pending, and clears any in-progress marker. Called by the propose
// Sink when the user approves a plan; also by the Phase B resume
// reconstruction. Empty stepTexts clears state (same as Clear).
func (t *Tool) Seed(stepTexts []string) {
	t.mu.Lock()
	if len(stepTexts) == 0 {
		t.steps = nil
	} else {
		t.steps = make([]Step, len(stepTexts))
		for i, txt := range stepTexts {
			t.steps[i] = Step{Text: txt, Status: StatusPending}
		}
	}
	t.currentIdx = -1
	snap := snapshot(t.steps)
	cur := t.currentIdx
	t.mu.Unlock()

	if t.sink != nil {
		t.sink.StepChanged(snap, cur)
	}
}

// Clear drops the active plan. Called when propose returns Cancelled
// or /plan toggles off entirely.
func (t *Tool) Clear() {
	t.Seed(nil)
}

// Snapshot returns a defensive copy of the current step list and the
// in-progress index. Safe for concurrent callers.
func (t *Tool) Snapshot() ([]Step, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return snapshot(t.steps), t.currentIdx
}

// Restore replaces the tool's state with the given steps and current
// index — used by the resume path to rehydrate after
// ReconstructFromTranscript. Unlike Seed (which forces every step to
// pending), Restore preserves per-step Status verbatim. The sink IS
// notified so the TUI re-renders.
//
// currentIdx must satisfy -1 <= currentIdx < len(steps); out-of-range
// values are clamped to -1 (defensive: a corrupted transcript should
// not panic). Passing nil or empty steps is equivalent to Clear.
func (t *Tool) Restore(steps []Step, currentIdx int) {
	t.mu.Lock()
	if len(steps) == 0 {
		t.steps = nil
		t.currentIdx = -1
	} else {
		t.steps = snapshot(steps)
		if currentIdx < -1 || currentIdx >= len(t.steps) {
			currentIdx = -1
		}
		t.currentIdx = currentIdx
	}
	snap := snapshot(t.steps)
	cur := t.currentIdx
	t.mu.Unlock()

	if t.sink != nil {
		t.sink.StepChanged(snap, cur)
	}
}

type args struct {
	Action string `json:"action"`
	Index  int    `json:"index"`
}

// Execute implements tools.Tool.
func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict(toolName, raw, &a, "action", "index"); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Action) == "" {
		return "", tools.MissingField(toolName, "action", raw, "action", "index")
	}
	if a.Index < 1 {
		return "", fmt.Errorf("%s: index must be 1-based (got %d)", toolName, a.Index)
	}

	t.mu.Lock()

	if len(t.steps) == 0 {
		t.mu.Unlock()
		return "", errors.New(toolName + ": no active plan — call propose() first and have the user approve it before tracking progress")
	}
	if a.Index > len(t.steps) {
		n := len(t.steps)
		t.mu.Unlock()
		return "", fmt.Errorf("%s: index %d out of range (plan has %d step(s))", toolName, a.Index, n)
	}

	idx := a.Index - 1
	var result string
	switch a.Action {
	case "start":
		prev := t.currentIdx
		t.steps[idx].Status = StatusInProgress
		t.currentIdx = idx
		result = fmt.Sprintf("[plan: step %d started] %s", a.Index, t.steps[idx].Text)
		if prev >= 0 && prev != idx && t.steps[prev].Status == StatusInProgress {
			t.steps[prev].Status = StatusPending
			result += fmt.Sprintf("\nNote: step %d was still in_progress and has been reset to pending. Call plan(action=\"complete\", index=%d) before moving on next time.", prev+1, prev+1)
		}
	case "complete":
		t.steps[idx].Status = StatusCompleted
		if t.currentIdx == idx {
			t.currentIdx = -1
		}
		done := 0
		for _, s := range t.steps {
			if s.Status == StatusCompleted {
				done++
			}
		}
		result = fmt.Sprintf("[plan: step %d complete, %d/%d done]", a.Index, done, len(t.steps))
		if done == len(t.steps) {
			result += "\nAll steps complete. Say so explicitly to the user (\"Completed: …. Anything else?\") so they have a clean exit ramp."
		}
	case "skip":
		t.steps[idx].Status = StatusSkipped
		if t.currentIdx == idx {
			t.currentIdx = -1
		}
		result = fmt.Sprintf("[plan: step %d skipped]", a.Index)
	default:
		t.mu.Unlock()
		return "", fmt.Errorf("%s: unknown action %q (valid: start, complete, skip)", toolName, a.Action)
	}

	snap := snapshot(t.steps)
	cur := t.currentIdx
	t.mu.Unlock()

	if t.sink != nil {
		t.sink.StepChanged(snap, cur)
	}
	return result, nil
}

func snapshot(s []Step) []Step {
	if len(s) == 0 {
		return nil
	}
	out := make([]Step, len(s))
	copy(out, s)
	return out
}
