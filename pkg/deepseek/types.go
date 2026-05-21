package deepseek

import "encoding/json"

const (
	ModelChat     = "deepseek-chat"
	ModelReasoner = "deepseek-reasoner"

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

	// ReasoningContent is populated by deepseek-reasoner responses.
	// It MUST be stripped before sending the message back to the API in a
	// subsequent request — DeepSeek rejects requests that include prior
	// reasoning_content fields.
	ReasoningContent string `json:"reasoning_content,omitempty"`
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
