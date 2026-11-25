// Package llm defines the generic streaming interface for second-tier
// LLM providers (Anthropic, OpenAI, Gemini). DeepSeek is a first-class
// citizen with its own richer pkg/deepseek package and does NOT
// implement this interface — see PRD §4.1 for the rationale.
//
// pkg/agent routes between *deepseek.Client (first-class) and
// llm.Provider (second-tier) using a type switch. DeepSeek-specific
// features (cache stats, reasoner) are never exposed through this
// interface so the abstraction does not dilute them.
package llm

import (
	"context"
	"encoding/json"
	"strings"
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
	Model     string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int // completion token cap; 0 = provider default

	// Temperature, when non-nil, is the sampling temperature for this
	// request. nil (the default) sends NO temperature key — a request
	// without it must stay byte-identical to its pre-knob shape, so the
	// prefix cache keeps hitting. A pointer rather than a float because
	// explicit 0 and "absent" are different wire states (0 is the
	// determinism knob the judge lane pins).
	//
	// Range validation is the caller's (cmd/seek accepts 0..2); each
	// adapter forwards the number verbatim, so a lane with a narrower
	// range (Anthropic: 0..1) answers with its own 400 rather than a
	// silently clamped value.
	Temperature *float64

	// SessionID, when non-empty, identifies the conversation this
	// request belongs to. The openai client sends it as an
	// x-opencode-session header — the OpenCode Go lane's client
	// contract (stable per-session value for routing optimisation and
	// prompt-cache affinity; missing it plus a generic User-Agent
	// degrades Go-plan requests to per-token billing). Providers
	// without such a contract (anthropic / gemini, and the first-class
	// deepseek client) ignore the field.
	SessionID string
}

// Message is the canonical provider-agnostic message type.
// pkg/agent translates between this and deepseek.Message; no DeepSeek
// types cross the llm package boundary.
type Message struct {
	Role      string     // "system" | "user" | "assistant" | "tool"
	Content   string     // plain-text content (may be empty for tool-only turns)
	ToolCalls []ToolCall // set on assistant messages with function calls
	// Images are the user message's image attachments, already
	// materialised as data: URLs (the caller resolves asset references
	// at send time, exactly as the DeepSeek path does). Each adapter
	// re-encodes them into its own wire shape; a message with no Images
	// keeps the plain-string `content` it always had. Legal on user
	// messages only — providers 400 on images anywhere else.
	Images []Image
	// Files are the user message's document attachments (PDF), the
	// sibling of Images: already materialised as data: URLs, one per
	// document, re-encoded by each adapter into its own wire shape. A
	// message with no Files (and no Images) keeps the plain-string
	// `content` it always had — the prefix-cache contract. Legal on user
	// messages only, same constraint as Images.
	Files []File
	// ToolCallID and ToolName are set on role="tool" result messages.
	ToolCallID string
	ToolName   string
}

// Image is one image attachment in its sendable form: a data: URL
// ("data:image/png;base64,…") or, for providers that accept them, an
// https URL.
type Image struct {
	URL string
}

// File is one document attachment in its sendable form: a data: URL
// ("data:application/pdf;base64,…") carrying the document's true media
// type. Filename is the original name (it rides the wire where the
// format has a slot) and is display-only elsewhere.
type File struct {
	Filename string
	URL      string
}

// The document media types the second-tier wire formats accept, per
// each lane's official docs (checked 2026-11-19):
//
//   - OpenAI Chat Completions: the `file` content part takes
//     PDF only — "for other file types, read the file in your
//     application and send its text" (developers.openai.com/api/docs/
//     guides/file-inputs).
//   - Anthropic Messages: the `document` block's base64 source accepts
//     media_type application/pdf only (platform.claude.com/docs/en/
//     build-with-claude/pdf-support).
//   - Gemini generateContent: document understanding reads
//     inline/application/pdf (aistudio.google.com/docs/document-
//     processing).
//
// application/pdf is the intersection, hence the whole set. Word/PPT
// ride ONLY on the DeepSeek lane (its Files API parses them
// server-side); on second-tier lanes they degrade to the in-band note.
var fileMediaTypes = map[string]bool{
	"application/pdf": true,
}

// SendableFileMediaType reports whether media type is one the
// second-tier transports can encode. The submit-time document router
// (internal/fileroute) asks this before attaching, so a .docx never
// gains an "attached" marker it can't ride on.
func SendableFileMediaType(mediaType string) bool {
	return fileMediaTypes[mediaType]
}

// Sendable reports whether File can ride a second-tier wire: a base64
// data: URL whose media type the transports accept.
func (f File) Sendable() bool {
	const prefix = "data:"
	if !strings.HasPrefix(f.URL, prefix) {
		return false
	}
	rest := f.URL[len(prefix):]
	comma := strings.Index(rest, ",")
	if comma < 0 || !strings.HasSuffix(rest[:comma], ";base64") {
		return false
	}
	return SendableFileMediaType(strings.TrimSuffix(rest[:comma], ";base64"))
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
//
// CachedInputTokens is the portion of InputTokens the provider served
// from its server-side prompt cache, when the wire format reports it
// (OpenAI-compatible prompt_tokens_details.cached_tokens; filled by the
// GLM lane). Providers without cache reporting leave it 0, meaning
// "no cache information" — the agent prices the full input at the miss
// rate, which is the conservative (never under-billing) reading.
type TurnDone struct {
	FinishReason      string
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
}

func (TurnDone) llmEvent() {}

// ErrorEvent wraps a fatal error from the provider. The channel is
// closed immediately after.
type ErrorEvent struct{ Err error }

func (ErrorEvent) llmEvent() {}
