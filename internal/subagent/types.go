// Package subagent is the orchestrator for v5 柱 G subagents — child
// LLM contexts spawned by the parent agent via the `agent` tool.
//
// The package is intentionally split from the `agent` tool itself
// (which lives at internal/tools/agent, landing in a follow-up
// commit): Manager + Runner here, tool schema + Execute there. This
// mirrors the bash / memory / propose pattern where the orchestrating
// state machine and the tool surface are separate.
//
// See docs/prd/feature-subagent.md for the full design — in
// particular §3 (data model + signatures) and §5 (integration with
// permission / cache / hooks / plan-mode).
//
// Lifecycle in one paragraph: the parent agent calls the `agent` tool,
// which calls Manager.Spawn. Manager spawns a child permission.Policy
// via Policy.Spawn (monotonic-only restriction), creates a child
// cache.Tracker which the parent AdoptChild's so costs roll up,
// composes the child's system prompt via sysprompt.ComposeSubagent,
// emits a `started` event to the project subagents.jsonl index, then
// runs the injected Runner with the child Policy/Tracker/system
// prompt. On Runner return (success, failure, cancellation, kill)
// Manager emits the terminal event and returns a Result whose
// Summary field is the wire-format string the `agent` tool returns
// as its tool result.
package subagent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Type is the discriminator over the three built-in subagent
// templates. Wire-format string (used in subagents.jsonl events and
// the LLM-side `agent` tool schema's `subagent_type` enum) is the
// underlying string value, so additions must extend the schema enum
// in lock-step. See templates.go for each Type's tool subset,
// permission Restriction, and system-prompt Extra clause.
type Type string

const (
	// TypeGeneralPurpose is the default subagent type — inherits the
	// parent's tool registry minus `agent` (no nested spawn) and
	// `ask_user` (disambiguation UX not in v5). Inherits parent
	// Preference + Workflow.
	TypeGeneralPurpose Type = "general-purpose"

	// TypeExplore is the research-only subagent type — read /
	// grep / list_dir / git / webfetch / think tool subset, forced
	// into WorkflowPlanAnalyze regardless of parent. Used for
	// parallel investigation that must not mutate the workspace.
	TypeExplore Type = "explore"

	// TypePlan is the plan-analyze subagent type — full tool subset
	// (minus agent / ask_user) but forced into WorkflowPlanAnalyze
	// so the subagent can propose changes via the propose tool
	// without executing them. Useful for parallel planning of
	// independent slices of a larger change.
	TypePlan Type = "plan"
)

// IsValid reports whether t is one of the three built-in templates.
// Used by Manager.Spawn to reject unknown types before doing any
// work; the LLM-side `agent` tool schema's enum is the first line of
// defense but this guards against future programmer error too.
func (t Type) IsValid() bool {
	switch t {
	case TypeGeneralPurpose, TypeExplore, TypePlan:
		return true
	}
	return false
}

// Status is the lifecycle state of a subagent, derived by folding
// the subagents.jsonl event stream per docs/prd/feature-subagent.md
// §3.4. A subagent has exactly one terminal status (Completed /
// Failed / Killed / Orphaned / Promoted) — once written, no further
// events are emitted for that sub_sid.
type Status string

const (
	// StatusActive — `started` event written, no terminal event yet.
	// Folded from "this sub_sid has only a started event".
	StatusActive Status = "active"

	// StatusCompleted — runner returned a non-empty summary; tokens
	// are recorded in the `completed` event. Terminal.
	StatusCompleted Status = "completed"

	// StatusFailed — runner returned an error other than ctx
	// cancellation by an explicit Kill, OR the spawn was rejected
	// before the runner started (too_many_subagents, invalid args,
	// Policy.Spawn rejection). The Reason field on the `failed`
	// event carries the specific cause. Terminal.
	StatusFailed Status = "failed"

	// StatusKilled — Manager.Kill was called for this sub_sid;
	// runner's ctx was cancelled and it returned a ctx.Canceled
	// error. Distinguished from StatusFailed reason=canceled
	// (parent's turn cancelled out from under us) so the UI can
	// show "killed by user" vs "interrupted by parent". Terminal.
	StatusKilled Status = "killed"

	// StatusOrphaned — seek startup scan found a `started` event
	// with no terminal counterpart, meaning the seek process that
	// owned this subagent crashed before it could write a final
	// event. Auto-emitted on first startup that encounters the
	// dangling started. Terminal.
	StatusOrphaned Status = "orphaned"

	// StatusPromoted — `seek subagent resume <sub-sid>` was used to
	// turn this subagent's transcript into a new top-level session.
	// The promoted event closes out the parent → subagent
	// relationship while leaving the transcript intact for the new
	// root session to consume. Terminal.
	StatusPromoted Status = "promoted"
)

// IsTerminal reports whether s is one of the four terminal states.
// Used by the folder in index.go and the orphan-recovery scan.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusKilled, StatusOrphaned, StatusPromoted:
		return true
	}
	return false
}

// Tokens is the per-subagent token usage rolled up at terminal time.
// Mirrors the relevant fields of deepseek.Usage in narrower form
// (we only need the three buckets users actually look at; the full
// Usage stays inside the child cache.Tracker).
//
// Cost is intentionally NOT stored here. cost is recomputed from
// parent.Tracker.CumulativeCost() (which transitively includes the
// child Tracker via AdoptChild) at render time. Persisting cost
// would couple the on-disk schema to the pricing table version —
// see docs/prd/feature-subagent.md §3.4 "Cost 不入索引".
type Tokens struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	CacheHit   int `json:"cache_hit"`
}

// Subagent is the in-memory snapshot of one spawn, surfaced to the
// `/agents` panel + `seek subagent list` after folding the index
// events. Mutable fields (Status, EndedAt, Tokens, Reason) reflect
// the latest event for this sub_sid; immutable ones (SubSid,
// ParentSid, ParentTurn, Type, Description, WorktreePath, StartedAt)
// come from the original `started` event.
type Subagent struct {
	SubSid       string    `json:"sub_sid"`
	ParentSid    string    `json:"parent_sid"`
	ParentTurn   int       `json:"parent_turn"`
	Type         Type      `json:"type"`
	Description  string    `json:"description"`
	WorktreePath string    `json:"worktree_path,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// `omitzero` (Go 1.24+) is the correct elision for time.Time
	// and nested structs — `omitempty` is a no-op on those because
	// the zero value still serialises as a full type instance.
	EndedAt time.Time `json:"ended_at,omitzero"`
	Status  Status    `json:"status"`
	Tokens  Tokens    `json:"tokens,omitzero"`
	// Reason is set when Status == StatusFailed; carries the
	// failure reason enum value from the wire-format spec (canceled
	// / timeout / spawn_error / hooks_denied / max_turns_exceeded /
	// prompt_too_long / too_many_subagents).
	Reason string `json:"reason,omitempty"`
}

// Result is what Manager.Spawn returns. Summary is ALWAYS a
// well-formed wire-format string per docs/prd/feature-subagent.md
// §3.2 — success path produces "[agent: completed] ..." and any
// failure path produces "[agent: failed reason=...] ...". The
// caller (the `agent` tool's Execute) returns Summary verbatim as
// the tool result with no further wrapping.
//
// Status mirrors the index event so the caller can decide whether
// to do anything post-hoc (almost always: just return Summary).
type Result struct {
	SubSid  string
	Summary string
	Status  Status
	Tokens  Tokens
}

// newSubSid returns a fresh subagent ID in the same shape as
// session.generateID — sortable timestamp + 6 random hex chars
// ("20260121-103045-a1b2c3"). Independent ID space from session
// IDs per docs/prd/feature-subagent.md §3.3; collisions across
// the two spaces are harmless because each lives in its own
// directory tree.
//
// On entropy exhaustion falls back to nanosecond-precision hex so
// IDs never collide from a zero-valued random buffer (same fallback
// the session generator uses).
func newSubSid(now time.Time) string {
	var rnd [3]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("%s-%06x",
			now.Format("20060102-150405"),
			now.Nanosecond()/1000)
	}
	return fmt.Sprintf("%s-%s",
		now.Format("20060102-150405"),
		hex.EncodeToString(rnd[:]))
}
