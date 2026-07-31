package deepseek

import "encoding/json"

const (
	// V4 lineup (current — as of api-docs.deepseek.com/quick_start/pricing).
	// deepseek-v4-flash serves the latest GA build (DeepSeek-V4-Flash-0731
	// as of 2026-07-31); the ID is stable — DeepSeek rotates the version
	// behind it. Both support thinking mode as a request-level parameter
	// rather than as a separate model ID; both have a 1M context window.
	//
	// The legacy deepseek-chat / deepseek-reasoner aliases were removed
	// server-side on 2026-07-24 — old session files that still record
	// those names fall back to "unknown model" handling (no thinking,
	// budget/pricing defaults) rather than failing to load.
	ModelV4Flash = "deepseek-v4-flash"
	ModelV4Pro   = "deepseek-v4-pro"

	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`

	// ReasoningContent is populated by V4 thinking-mode responses.
	// It MUST be stripped before sending the message back to the API in a
	// subsequent request — DeepSeek rejects requests that include prior
	// reasoning_content fields.
	ReasoningContent string `json:"reasoning_content,omitempty"`

	// PredictedNext is the v4 柱 D "suggested reply" prediction generated
	// by a side-channel call after this assistant turn ended. It is
	// PURELY a session-persistence field — `PrepareForSend` strips it
	// before every API call so DeepSeek never sees the field. Stored
	// here (rather than as a sidecar) for proximity: each assistant
	// message carries the prediction it spawned, and the next user
	// message's match check can read it via a single-step lookback.
	// See PRD docs/prd/feature-suggested-reply.md §4.5.
	PredictedNext string `json:"predicted_next,omitempty"`
}

type ToolCall struct {
	// Index is set in streaming delta chunks so the consumer can merge
	// partial argument strings keyed by call index. Final/non-stream
	// responses omit it; omitempty keeps round-trips clean.
	Index    int          `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function ToolCallFunc `json:"function"`
}

type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`

	Tools          []Tool          `json:"tools,omitempty"`
	ToolChoice     any             `json:"tool_choice,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`

	StreamOptions *StreamOptions `json:"stream_options,omitempty"`

	// Thinking opts a V4-class model into reasoning mode. Replaces the
	// pre-V4 pattern of calling a separate "deepseek-reasoner" model
	// (a deprecated alias, removed 2026-07-24): just set
	// {"type":"enabled"} on a V4 chat request and you get
	// reasoning_content alongside content in the response.
	//
	// The reasoning_content from prior turns MUST be stripped before
	// resending history — same constraint as the old reasoner; see
	// StripReasoningContent.
	Thinking *ThinkingMode `json:"thinking,omitempty"`

	// ReasoningEffort tunes how hard the model thinks when Thinking is
	// enabled. DeepSeek V4 documents two levels: "high" and "max".
	// Higher = more reasoning tokens = higher quality / slower / more
	// expensive. Empty = model default. (The TUI's /effort command
	// only exposes "high"/"max" — the older OpenAI-style "low"/"medium"
	// values are not documented for V4 and may be silently ignored;
	// internal callers like the `think` tool also stick to high/max.)
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ThinkingMode controls V4's thinking parameter. As of 2026-01 the
// only fields documented are Type ∈ {"enabled", "disabled"}; the
// wire shape is an object rather than a bare string so DeepSeek can
// extend it without breaking clients.
type ThinkingMode struct {
	Type string `json:"type"` // "enabled" | "disabled"
}

// ShouldEnableThinking reports whether the given model name implies
// thinking-mode semantics and should therefore receive
// Thinking.Type="enabled" when the agent constructs a ChatRequest.
//
// Returns true for ModelV4Pro — V4-Pro is the high-end reasoning
// model; using it without thinking would waste the price premium.
//
// Returns false for ModelV4Flash (intentionally non-thinking by
// default) and for any unknown / custom model name — callers who
// want thinking on those must opt in explicitly via
// ChatRequest.Thinking.
func ShouldEnableThinking(model string) bool {
	switch model {
	case ModelV4Pro:
		return true
	}
	return false
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage captures token accounting plus DeepSeek's cache metadata.
//
// PromptCacheHitTokens / PromptCacheMissTokens are DeepSeek-specific fields
// surfaced via the prefix-cache feature. A high hit ratio means the request's
// prefix matched a previously-seen prefix and is billed at ~10× lower rate.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

// HitRatio returns the prefix-cache hit ratio for this request (0..1).
func (u Usage) HitRatio() float64 {
	total := u.PromptCacheHitTokens + u.PromptCacheMissTokens
	if total == 0 {
		return 0
	}
	return float64(u.PromptCacheHitTokens) / float64(total)
}

type APIError struct {
	StatusCode int    `json:"-"`
	Type       string `json:"type,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return "deepseek api error: " + e.Type + ": " + e.Message
	}
	return "deepseek api error: " + e.Message
}

type errorEnvelope struct {
	Error APIError `json:"error"`
}
