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
//   - Observers (PreTurn, PostTurn, PostToolUse, SessionStart, SessionEnd)
//     have no return value. The interface signature itself enforces that
//     they cannot alter the conversation, the request, or control flow.
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
