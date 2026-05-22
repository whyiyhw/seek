// Package llm defines the generic streaming interface for second-tier
// LLM providers (Anthropic, OpenAI, Gemini). DeepSeek is a first-class
// citizen with its own richer pkg/deepseek package and does NOT
// implement this interface — see PRD §4.1 for the rationale.
//
// pkg/agent routes between *deepseek.Client (first-class) and
// llm.Provider (second-tier) using a type switch. DeepSeek-specific
// features (cache stats, FIM, reasoner) are never exposed through this
// interface so the abstraction does not dilute them.
package llm

import (
	"context"
	"encoding/json"
)

// Provider is the interface every second-tier backend must satisfy.
// Implementations live in pkg/llm/provider/{anthropic,openai,gemini}
// and pkg/llm/compatible (OpenAI-compatible endpoints).
type Provider interface {
	// ChatStream starts a streaming chat request. The returned channel
	// is owned by the implementation; the caller drains it and the
	// channel is closed when the stream ends (normally or with error).
	// Cancelling ctx terminates the stream; the implementation closes
	// the channel and stops reading from the wire.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan Event, error)

	// Name returns a human-readable provider label used in TUI banners
	// and log messages (e.g. "Anthropic", "OpenAI", "Gemini").
	Name() string
}

// ChatRequest is the provider-agnostic request sent to ChatStream.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolDef
}

// Message is the canonical provider-agnostic message type.
// pkg/agent translates between this and deepseek.Message; no DeepSeek
// types cross the llm package boundary.
type Message struct {
	Role      string     // "system" | "user" | "assistant" | "tool"
	Content   string     // plain-text content (may be empty for tool-only turns)
	ToolCalls []ToolCall // set on assistant messages with function calls
	// ToolCallID and ToolName are set on role="tool" result messages.
	ToolCallID string
	ToolName   string
}

// ToolCall is a single function-call request issued by the assistant.
type ToolCall struct {
	ID        string // provider-assigned call ID
	Name      string // function name
	Arguments string // JSON-encoded argument object
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string
	Description string
	// Schema is a JSON Schema object ({"type":"object","properties":{...}}).
	Schema json.RawMessage
}

// ---------------------------------------------------------------------------
// Events — emitted by ChatStream on the returned channel.
// ---------------------------------------------------------------------------

// Event is the sum type for all streaming events a Provider can emit.
type Event interface{ llmEvent() }

// TextDelta carries an incremental piece of the assistant's text reply.
type TextDelta struct{ Delta string }

func (TextDelta) llmEvent() {}

// ToolCallDone is emitted once per tool call when the full call has been
// assembled (name + arguments complete). Providers that stream argument
// fragments assemble them internally before emitting this event.
type ToolCallDone struct {
	ID        string
	Name      string
	Arguments string // complete JSON
}

func (ToolCallDone) llmEvent() {}

// TurnDone signals the end of one assistant turn. FinishReason is a
// normalised string: "stop", "tool_calls", "length", or "error".
type TurnDone struct {
	FinishReason string
	InputTokens  int
	OutputTokens int
}

func (TurnDone) llmEvent() {}

// ErrorEvent wraps a fatal error from the provider. The channel is
// closed immediately after.
type ErrorEvent struct{ Err error }

func (ErrorEvent) llmEvent() {}
