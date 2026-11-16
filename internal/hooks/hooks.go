// Package hooks defines lifecycle interception points for the seek agent.
//
// Two kinds of hooks, separated at the type level so prefix-cache safety is
// a compile-time property rather than a convention:
//
//   - Decorators (PrePromptHook, PreToolUseHook) run synchronously in
//     registration order and may mutate the request. Their output MUST be
//     deterministic — DeepSeek's prefix cache keys on the exact byte
//     sequence of the prompt history, so a non-deterministic decorator
//     silently destroys cache hit rate.
//
//   - Observers (PreTurn, PostTurn, PostToolUse, SessionStart,
//     SessionEnd, plus the P3 points SubagentStart, SubagentStop, Stop,
//     PreCompact, Notification) have no return value. The interface
//     signature itself enforces that they cannot alter the
//     conversation, the request, or control flow.
//
// A single struct may implement multiple interfaces; Registry.Register
// dispatches it into every slot it satisfies. The v1 memory subsystem
// uses this to plug PrePrompt + PreToolUse + PostTurn + SessionEnd in one
// place without touching pkg/agent.
package hooks

import (
	"context"
	"encoding/json"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ----- Decorator hooks -----

// PrePromptHook fires once per Prompt() call, before the user message is
// appended to history. Use it to inject memory, expand slash commands, or
// rewrite the user text. Output must be deterministic across runs given
// the same input — see package doc.
type PrePromptHook interface {
	OnPrePrompt(ctx context.Context, in PrePromptIn) (PrePromptOut, error)
}

// PrePromptIn carries the original user text and a read-only snapshot of
// history. Mutating History is undefined behaviour — Registry passes a
// defensive copy but hooks should not rely on that.
type PrePromptIn struct {
	UserText string
	History  []deepseek.Message
}

// PrePromptOut returns the modifications the hook wants applied. Both
// fields are optional.
//
// UserText: empty string means "no change" — the working text from the
// previous hook in the chain (or the original) is preserved. Hooks that
// genuinely want to clear the user text should set a single space; the
// "" convention is what makes the common no-op case ergonomic.
//
// Prepend: user-role messages inserted BEFORE the user turn. By
// convention wrap injected content in <context>...</context> tags so the
// model can tell context from instructions. Prepend stays user-role on
// purpose — keeping injected bytes in the cache-volatile (this-turn) region
// avoids rewriting the system prompt, which is the most cache-sensitive
// part of the history.
type PrePromptOut struct {
	UserText string
	Prepend  []deepseek.Message
}

// PreToolUseHook fires before each tool invocation. Use it for permission
// gates, audit logging, or redacting secrets out of args. The first hook
// to return a non-empty Deny short-circuits the chain.
type PreToolUseHook interface {
	OnPreToolUse(ctx context.Context, in PreToolUseIn) (PreToolUseOut, error)
}

type PreToolUseIn struct {
	CallID string
	Name   string
	Args   json.RawMessage
}

// PreToolUseOut: at most one of Deny / Args should be set.
//
// Deny, non-empty, vetoes the call. The text becomes the tool result fed
// back to the LLM (mirrors permission.ErrDenied — the model sees the
// refusal and can ask the user, retry differently, or move on).
//
// Args, non-nil, replaces the args passed to the tool. This field exists
// for one purpose only: redacting secrets out of args before execution
// (e.g., scrubbing tokens from bash commands). General-purpose args
// rewriting is intentionally not supported — that's middleware territory
// and we don't want it; tools should validate their own args.
type PreToolUseOut struct {
	Deny string
	Args json.RawMessage
}

// ----- Observer events -----

type PreTurnEvent struct {
	Index   int
	History []deepseek.Message
}

type PostTurnEvent struct {
	Index     int
	Usage     deepseek.Usage
	ToolCalls int
	Finish    string
}

type PostToolUseEvent struct {
	CallID string
	Name   string
	Args   json.RawMessage
	Result string
	Err    error
}

type SessionStartEvent struct {
	ID      string
	Model   string
	CWD     string
	Resumed bool
}

type SessionEndEvent struct {
	ID        string
	Turns     int
	ToolCalls int
	Usage     deepseek.Usage
}

// ----- P3 observer events (feature-orchestration §6 D15) -----
//
// Five observer-only points. None of them has ANY control-flow effect:
// the shell-hook implementations treat timeout / non-zero exit as
// log-and-continue (the pre_tool deny surface stays the ONLY deny
// point), and the interface signatures carry no return value that
// could touch prompt bytes (feature-hooks-injection §3.3).

// SubagentStartEvent fires after the subagent Manager emits its
// `started` event (sync and background spawns alike).
type SubagentStartEvent struct {
	SID  string // sub-sid, unique per run
	Type string // agent type slug ("" = built-in general)
	Desc string // one-line task description from the spawn args
}

// SubagentStopEvent fires when a subagent reaches any terminal state
// (completed / failed / killed / orphaned).
type SubagentStopEvent struct {
	SID    string
	Type   string
	Desc   string
	Status string // completed | failed | killed | orphaned
	Turns  int    // LLM turns the subagent took
	Tokens int64  // prompt+completion total for the run
}

// StopEvent fires once per Prompt() call after the whole reply settles
// — final answer, max turns, ctx cancel, or fatal error, whichever
// ended it. Distinct granularity from PostTurn: PostTurn fires per
// model turn (a Prompt may contain several), Stop fires once per
// round.
type StopEvent struct {
	Turns  int    // total model turns in this Prompt() call
	Finish string // Prompt-level outcome (wiring chooses vocabulary)
}

// PreCompactEvent fires before /compact rewrites history (before the
// LLM summarisation call, not after).
type PreCompactEvent struct {
	Trigger string // "manual" today; reserved for future auto triggers
}

// NotificationEvent fires when something surfaces outside the TUI's
// text plane: an OS notification (pillar H Notifier) or an ask_user
// picker popping up.
type NotificationEvent struct {
	Kind  string // e.g. "os" | "ask_user"
	Title string
}

// ----- Observer interfaces -----

type PreTurnObserver interface {
	OnPreTurn(ctx context.Context, ev PreTurnEvent)
}

type PostTurnObserver interface {
	OnPostTurn(ctx context.Context, ev PostTurnEvent)
}

type PostToolUseObserver interface {
	OnPostToolUse(ctx context.Context, ev PostToolUseEvent)
}

type SessionStartObserver interface {
	OnSessionStart(ctx context.Context, ev SessionStartEvent)
}

type SessionEndObserver interface {
	OnSessionEnd(ctx context.Context, ev SessionEndEvent)
}

type SubagentStartObserver interface {
	OnSubagentStart(ctx context.Context, ev SubagentStartEvent)
}

type SubagentStopObserver interface {
	OnSubagentStop(ctx context.Context, ev SubagentStopEvent)
}

type StopObserver interface {
	OnStop(ctx context.Context, ev StopEvent)
}

type PreCompactObserver interface {
	OnPreCompact(ctx context.Context, ev PreCompactEvent)
}

type NotificationObserver interface {
	OnNotification(ctx context.Context, ev NotificationEvent)
}
