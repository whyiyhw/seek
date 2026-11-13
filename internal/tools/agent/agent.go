// Package agent implements the `agent` tool — the LLM-facing surface
// over internal/subagent. It is intentionally thin: the heavy lifting
// (Policy.Spawn / Tracker.AdoptChild / system-prompt assembly / event
// indexing / Runner orchestration) lives in internal/subagent. This
// package is just argument parse + dispatch + wire-format pass-through.
//
// Split rationale (mirrors bash / memory / propose): tool schema +
// Execute lives next to the LLM contract; state machine + persistence
// lives where it can be unit-tested without the LLM in the loop. The
// schema bytes are package-level so the Wire() output stays byte-
// identical across turns and process restarts — load-bearing for
// DeepSeek's prefix cache.
//
// See docs/prd/feature-subagent.md for the full design; this file
// is the thinnest possible Go reflection of §3.1 (LLM schema) +
// §3.2 (wire-format result).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/worktree"
)

// schemaBytes is the package-level JSON schema for the `agent` tool.
// Byte-stable across processes (load-bearing for prefix cache —
// PRD v0 §4.8.1). Do NOT regenerate at call time, do NOT inject
// runtime data.
//
// Mirrors docs/prd/feature-subagent.md §3.1 verbatim with one
// deviation: `maxLength` is omitted from the `description` field
// because tools.UnmarshalStrict doesn't enforce JSON Schema length
// constraints (only DisallowUnknownFields). Length is enforced in
// Execute via the explicit guards documented in the PRD as
// "Execute-side strict, schema-side advisory".
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "Short (3-5 word) description of the task. Shown in /agents UI."
    },
    "prompt": {
      "type": "string",
      "description": "The full task for the subagent. The subagent sees NOTHING else from the parent — write a self-contained briefing including any context the subagent needs."
    },
    "subagent_type": {
      "type": "string",
      "enum": ["general-purpose", "explore", "plan"],
      "description": "general-purpose: full tools (minus agent/ask_user), parent's model, inherits parent permissions. explore: read-only subset (read/grep/list_dir/git/webfetch/think) forced into plan-analyze workflow regardless of parent — output is bulleted findings; use for parallel research. plan: same read-only subset as explore, forced into plan-analyze workflow — output is a numbered, structured action plan the parent (or a human reviewer) can execute step by step."
    },
    "isolation": {
      "type": "string",
      "enum": ["none", "worktree"],
      "description": "none (default): subagent works in the parent's cwd. worktree: creates a temporary git worktree so the subagent works on an isolated copy; useful for parallel implementation attempts. Auto-cleaned if no changes. (worktree not yet implemented — use \"none\" until v0.6.0 ships M11.1.)"
    },
    "session_id": {
      "type": "string",
      "description": "CONTINUE a prior subagent instead of spawning fresh: pass its sub-sid — the full id, or the short value from a previous result's \"— sub-sid:\" footer (uniquely resolved). The prior run's conversation is restored as context and your prompt is delivered as the next instruction — write only the follow-up, do not repeat the original briefing. The prior run must have finished; a still-running, promoted, transcript-less (failed/killed/orphaned) or unknown id returns a clear error. The continuation gets a NEW sub-sid (footer shows resumed-from); quote that new id for further continuations. When set, subagent_type is inherited from the original run and ignored here."
    }
  },
  "required": ["description", "prompt"],
  "additionalProperties": false
}`)

const description = `Launch a subagent with isolated context to perform a focused task, or (pass session_id) continue a prior subagent with its context restored and a follow-up instruction. The subagent returns ONE final summary string; it has no access to your conversation history or memory beyond the prompt you provide (or, for continuations, its own prior transcript). Use for: parallel research across multiple independent paths (spawn N explore subagents simultaneously), long-context work that would bloat the main conversation (e.g. "read these 50 files and summarise patterns"), role-specialised passes (plan-analyze proposal that you'll review), iterative follow-ups on a finished subagent's work (session_id — e.g. "now audit the edge cases in what you found"). Costs roll up into your session's total automatically. NOT for: short factual lookups (just grep), tasks needing the user's confirmation mid-run (subagents can't ask the user), or sequencing where one step depends on the previous (subagents run independently). Max 3 concurrent; over the cap returns a failure result and you can retry after one completes.`

// Args is the strict-decode target. JSON tags must match schemaBytes
// field names exactly (DisallowUnknownFields rejects mismatches).
type Args struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type,omitempty"`
	Isolation    string `json:"isolation,omitempty"`
	// SessionID, when non-empty, switches Execute from fresh-spawn to
	// continuation: Manager.Continue restores the prior run's context
	// and delivers Prompt as the follow-up. See schemaBytes.
	SessionID string `json:"session_id,omitempty"`
}

// Length caps enforced in Execute. Schema's `description` field
// includes a "3-5 word" hint but JSON Schema's maxLength isn't
// enforced by our UnmarshalStrict path; these constants cover the
// pathology where the model produces a runaway description.
//
// Numbers come from docs/prd/feature-subagent.md §3.1 ("description
// > 120 chars → 截断 + hint; prompt > 32 KB → 失败 prompt_too_long").
const (
	maxDescriptionLen = 120
	maxPromptBytes    = 32 * 1024
)

// Tool is the LLM-facing wrapper around subagent.Manager.
//
// wtMgr is the v5 柱 G M11.1 worktree integration — optional
// (nullable for non-git environments). When set + the model
// passes isolation="worktree", Execute calls wtMgr.Create
// BEFORE Spawn and wtMgr.Cleanup(if_dirty=keep) AFTER, threading
// the worktree's Path through subagent.SpawnArgs.WorktreePath so
// the child Policy + tool execution lands in the worktree dir.
// When wtMgr is nil, isolation="worktree" returns a clean wire-
// format failure pointing the user at "this needs a git repo".
type Tool struct {
	mgr   *subagent.Manager
	wtMgr *worktree.Manager
}

// New constructs the tool. manager must be non-nil; a nil
// manager means "the host program didn't wire subagents" — in
// that case the tool isn't registered in the parent Registry at
// all, so reaching New(nil, _) is a programmer error.
//
// wtMgr is OPTIONAL — pass nil when seek runs outside a git
// working tree. Execute then refuses isolation="worktree"
// requests with a structured failure but still serves the
// general-purpose / explore / plan paths.
func New(manager *subagent.Manager, wtMgr *worktree.Manager) *Tool {
	if manager == nil {
		panic("agent: New called with nil Manager — host program did not wire internal/subagent")
	}
	return &Tool{mgr: manager, wtMgr: wtMgr}
}

func (*Tool) Name() string            { return "agent" }
func (*Tool) Description() string     { return description }
func (*Tool) Schema() json.RawMessage { return schemaBytes }

// ReadOnly implements tools.ReadOnlyTool. Marking `agent` as
// read-only is a deliberate semantic stretch — a subagent CAN
// mutate the filesystem if its template includes write/edit/bash
// (general-purpose does). But the `tools.ReadOnlyTool` marker is
// consumed by pkg/agent.readOnlyCall() to route a tool call onto
// the concurrent side of the partitioned dispatch, NOT by the
// permission gate (which uses the separate Action.ReadOnly flag
// set inside individual tools like bash).
//
// Dispatch-level concurrency is safe for `agent` because:
//
//   - Manager.Spawn is concurrent-safe (tested: parallel spawn N
//     with -race in subagent/manager_test.go).
//   - The subagents.jsonl index serialises appends under
//     indexLock + O_APPEND atomicity.
//   - Each spawn allocates fresh child Tracker / Policy / pkg/
//     agent.Agent; no shared mutable state between siblings.
//   - The MaxConcurrent gate caps simultaneous in-flights so a
//     runaway model can't fork-bomb the orchestrator.
//
// Without this marker, parallel `agent` tool calls in the same
// turn (e.g. "spawn 3 explore subagents to research X / Y / Z")
// would serialise at the agent loop, defeating the entire point of
// subagents. With it, the user sees max(t1, t2, t3) wall time
// instead of sum.
//
// Side note: mixing agent + a non-read-only tool in the same batch
// (e.g. [agent, bash]) overlaps the two — agent runs concurrently
// while bash takes the sequential stream. The model issuing one
// batch declares the calls independent, and non-read-only tools
// still keep their relative order among themselves.
func (*Tool) ReadOnly() bool { return true }

// Execute parses args and dispatches to Manager.Spawn. The Result's
// Summary field is ALWAYS a well-formed wire-format string (success
// path = "[agent: completed] ..." / any failure = "[agent: failed
// reason=...] ...") so the LLM can read the prefix to decide next
// action. Execute returns (Summary, nil) for every Manager outcome
// — including subagent failure — so the agent loop feeds the
// structured result back without wrapping it in an error frame
// that would lose the wire-format signal.
//
// The error return is reserved for argument-parsing failures
// (strict unmarshal, missing required field, runtime length
// violations) — those need to surface via the agent loop's
// ErrUnknownTool / strict-unmarshal handling path so the model sees
// a clear "fix your arguments" hint rather than a wire-format
// failure that looks like a subagent crash.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("agent", raw, &a,
		"description", "prompt", "subagent_type", "isolation", "session_id"); err != nil {
		return "", err
	}
	// Fresh spawns require an explicit description (it names a NEW
	// task in /agents). Continuations may omit it — the session_id
	// already identifies the lineage, so a default label suffices.
	if a.Description == "" && a.SessionID == "" {
		return "", tools.MissingField("agent", "description", raw,
			"description", "prompt", "subagent_type", "isolation", "session_id")
	}
	if a.Prompt == "" {
		return "", tools.MissingField("agent", "prompt", raw,
			"description", "prompt", "subagent_type", "isolation", "session_id")
	}

	// Runtime length enforcement (PRD §3.1 "Execute-side strict").
	// Truncate description with an inline hint so the /agents panel
	// stays readable; reject oversize prompts outright because a
	// silent truncation could lose load-bearing context.
	if a.Description == "" && a.SessionID != "" {
		// Continuation default label — short by construction, still
		// run through the cap below for uniformity.
		a.Description = "continue " + a.SessionID
	}
	if len(a.Description) > maxDescriptionLen {
		a.Description = a.Description[:maxDescriptionLen] + "…(truncated)"
	}
	if len(a.Prompt) > maxPromptBytes {
		// Wire-format failure rather than tool error — same outcome
		// as if Manager rejected: the LLM gets a structured signal
		// it can react to.
		return failNoSpawn("prompt_too_long",
			"prompt exceeds 32 KB; split the task into smaller subagent calls"), nil
	}

	// Resolve isolation. "none" / "" is the standard path
	// (spawn under parent cwd); "worktree" creates a seek-managed
	// worktree BEFORE spawn so the child runs in an isolated git
	// checkout. M11.1 Phase 2 implementation per PRD §3.8.
	// Applies to continuations identically (a continuation runs
	// wherever the caller isolates it — the prior run's worktree
	// was cleaned up when that run finished).
	var (
		worktreePath   string
		worktreeBranch string
	)
	switch a.Isolation {
	case "", "none":
		// happy path; worktreePath stays empty, child uses
		// parent's cwd.
	case "worktree":
		if t.wtMgr == nil {
			return failNoSpawn("spawn_error",
				"isolation=\"worktree\" requires a git working tree (Manager not wired — non-git project?); use isolation=\"none\" or omit"), nil
		}
		wt, err := t.wtMgr.Create(ctx, "", "")
		if err != nil {
			return failNoSpawn("spawn_error",
				"worktree create: "+err.Error()), nil
		}
		worktreePath = wt.Path
		worktreeBranch = wt.Branch
	default:
		return failNoSpawn("spawn_error",
			"isolation must be one of: none, worktree (got "+a.Isolation+")"), nil
	}

	// Dispatch. Fresh spawn vs continuation (P1-b): session_id
	// switches the entry point. Both return a Result whose Summary
	// is the wire-format string handed back unchanged; continuations
	// additionally carry PriorMessages seeded from the prior run's
	// transcript inside Manager.Continue.
	var res subagent.Result
	if a.SessionID != "" {
		res = t.mgr.Continue(ctx, subagent.ContinueArgs{
			FromSubSid:   a.SessionID,
			Description:  a.Description,
			Prompt:       a.Prompt,
			WorktreePath: worktreePath,
			// ParentTurn is 0 — same M11.0 limitation as the spawn
			// path below.
			ParentTurn: 0,
		})
	} else {
		// subagent_type defaults to general-purpose when omitted,
		// matching the PRD schema description. Manager.Spawn will
		// reject any other invalid value at its own validation
		// step; we let it speak. (On the continuation branch the
		// type is inherited from the prior run — the field is
		// ignored.)
		st := subagent.Type(a.SubagentType)
		if st == "" {
			st = subagent.TypeGeneralPurpose
		}
		res = t.mgr.Spawn(ctx, subagent.SpawnArgs{
			Description:  a.Description,
			Prompt:       a.Prompt,
			Type:         st,
			WorktreePath: worktreePath,
			// ParentTurn is 0 in M11.0 — the parent turn counter
			// isn't threaded through the tool interface yet. The
			// index event still records the spawn timing via the
			// timestamp, and the /agents panel doesn't depend on
			// parent_turn for MVP. Will thread through Sink in a
			// follow-up if the UI needs it.
			ParentTurn: 0,
		})
	}

	res = t.settleWorktree(res, worktreePath, worktreeBranch)
	return res.Summary, nil
}

// settleWorktree runs the post-run worktree cleanup (PRD §3.8 "子结束
// 后自动 exit_worktree (if_dirty: keep)") and returns res with any
// cleanup note appended to Summary. Three outcomes:
//
//   - Cleanup succeeds + status="cleaned" (no dirty files)
//     → worktree auto-removed; nothing appended to summary
//     (PRD: "无改动时不追加（自动 cleaned）").
//   - Cleanup succeeds + status="kept" + changes > 0 →
//     worktree stays on disk; append the path/branch/changes
//     line so the LLM (and the user reading the transcript)
//     knows where to find the work.
//   - Cleanup fails → don't lose the subagent's success;
//     append a "(worktree cleanup error: ...)" line for
//     diagnostics but keep the original Summary intact.
//
// No-op when worktreePath is empty. We deliberately use
// context.Background() (not the caller's ctx) so a cancelled parent
// turn doesn't strand the worktree mid-cleanup. The cleanup is
// short — git status + maybe git worktree remove — bounded latency.
func (t *Tool) settleWorktree(res subagent.Result, worktreePath, worktreeBranch string) subagent.Result {
	if worktreePath == "" {
		return res
	}
	cleanupRes, cleanupErr := t.wtMgr.Cleanup(context.Background(), worktreePath, "keep")
	switch {
	case cleanupErr != nil:
		res.Summary += fmt.Sprintf("\n— worktree cleanup error: %v (path=%s branch=%s)",
			cleanupErr, worktreePath, worktreeBranch)
	case cleanupRes.Status == "kept" && cleanupRes.Changes > 0:
		res.Summary += fmt.Sprintf("\n— worktree: %s (branch %s, %d changes)",
			cleanupRes.Path, cleanupRes.Branch, cleanupRes.Changes)
	}
	// status="cleaned" → no append (the silent happy path).
	return res
}

// failNoSpawn formats a wire-format failure for paths that reject
// before reaching Manager.Spawn (length caps, unknown isolation
// value). Reuses subagent.FormatFailed so the prefix bytes stay
// identical to Manager-emitted failures — parsers shouldn't care
// where the failure originated.
//
// SubSid is empty for these paths: nothing was allocated, no event
// was emitted, no on-disk state exists. Wire format handles empty
// sub-sid gracefully (footer reads "sub-sid: " with empty value).
func failNoSpawn(reason, hint string) string {
	return subagent.FormatFailed("", reason, hint)
}

// Sentinel error returned when New is called with nil manager but
// the caller wants to recover rather than panic. Not currently
// used; kept as a hook for future test seams.
var errNilManager = errors.New("agent: nil Manager")

// Compile-time assertions: Tool satisfies the tools.Tool contract
// AND the tools.ReadOnlyTool extension. These are free — they
// catch interface drift at build time. If a refactor accidentally
// drops the ReadOnly method, the second assertion fails and the
// parallel-dispatch property is recovered at compile rather than
// silently degrading at runtime.
var (
	_ tools.Tool         = (*Tool)(nil)
	_ tools.ReadOnlyTool = (*Tool)(nil)
)
