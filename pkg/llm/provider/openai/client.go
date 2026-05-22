// Package openai implements pkg/llm.Provider for the OpenAI Chat Completions
// API and any compatible endpoint.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/whyiyhw/seek/pkg/llm"
)

const defaultBaseURL = "https://api.openai.com/v1/chat/completions"

// Client is a streaming OpenAI Chat Completions client satisfying llm.Provider.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New returns a Client pointed at the official OpenAI endpoint.
func New(apiKey string) *Client {
	return &Client{apiKey: apiKey, baseURL: defaultBaseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

// NewCompatible returns a Client pointed at an OpenAI-compatible endpoint.
func NewCompatible(apiKey, baseURL string) *Client {
	return &Client{apiKey: apiKey, baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Name satisfies llm.Provider.
func (c *Client) Name() string { return "OpenAI" }

// --- wire types ---

type openAIRequest struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Tools         []openAITool    `json:"tools,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAIToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function openAIToolCallFn  `json:"function"`
}

type openAIToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string          `json:"type"`
	Function openAIToolFn    `json:"function"`
}

type openAIToolFn struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *streamUsage   `json:"usage,omitempty"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Content   *string          `json:"content"`
	ToolCalls []tcDeltaChunk   `json:"tool_calls,omitempty"`
}

type tcDeltaChunk struct {
	Index    int         `json:"index"`
	ID       string      `json:"id,omitempty"`
	Function *tcFnChunk  `json:"function,omitempty"`
}

type tcFnChunk struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type streamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// toolAccum accumulates one streaming tool call by index.
type toolAccum struct {
	id   string
	name string
	args bytes.Buffer
}

// --- conversion helpers ---

func convertMessages(msgs []llm.Message) []openAIMessage {
	out := make([]openAIMessage, len(msgs))
	for i, m := range msgs {
		om := openAIMessage{Role: m.Role}
		switch m.Role {
		case "tool":
			om.Content, om.ToolCallID, om.Name = m.Content, m.ToolCallID, m.ToolName
		case "assistant":
			if len(m.ToolCalls) > 0 {
				tc := make([]openAIToolCall, len(m.ToolCalls))
				for j, t := range m.ToolCalls {
					tc[j] = openAIToolCall{ID: t.ID, Type: "function", Function: openAIToolCallFn{Name: t.Name, Arguments: t.Arguments}}
				}
				om.ToolCalls = tc
			} else {
				om.Content = m.Content
			}
		default:
			om.Content = m.Content
		}
		out[i] = om
	}
	return out
}

func convertTools(tools []llm.ToolDef) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, len(tools))
	for i, t := range tools {
		out[i] = openAITool{Type: "function", Function: openAIToolFn{Name: t.Name, Description: t.Description, Parameters: t.Schema}}
	}
	return out
}

// --- ChatStream ---

// ChatStream implements llm.Provider. The returned channel is always closed.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	buf, err := json.Marshal(openAIRequest{
		Model: req.Model, Messages: convertMessages(req.Messages),
		Tools: convertTools(req.Tools), Stream: true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, raw)
	}

	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		streamBody(ctx, resp.Body, out)
	}()
	return out, nil
}

func streamBody(ctx context.Context, r io.Reader, out chan<- llm.Event) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	accum := map[int]*toolAccum{}
	var lastFinish string
	var inputTokens, outputTokens int

	for sc.Scan() {
		select {
		case <-ctx.Done():
			out <- llm.ErrorEvent{Err: ctx.Err()}
			return
		default:
		}
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || string(payload) == "[DONE]" {
			if string(payload) == "[DONE]" {
				break
			}
			continue
		}

		var chunk streamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			out <- llm.ErrorEvent{Err: fmt.Errorf("openai: decode chunk: %w", err)}
			return
		}
		if chunk.Usage != nil {
			inputTokens, outputTokens = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != nil && *ch.Delta.Content != "" {
				out <- llm.TextDelta{Delta: *ch.Delta.Content}
			}
			for _, tc := range ch.Delta.ToolCalls {
				a := accum[tc.Index]
				if a == nil {
					a = &toolAccum{}
					accum[tc.Index] = a
				}
				if tc.ID != "" {
					a.id = tc.ID
				}
				if tc.Function != nil {
					if tc.Function.Name != "" {
						a.name = tc.Function.Name
					}
					a.args.WriteString(tc.Function.Arguments)
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				lastFinish = *ch.FinishReason
			}
		}
	}

	for i := 0; i < len(accum); i++ {
		a := accum[i]
		if a == nil {
			break
		}
		out <- llm.ToolCallDone{ID: a.id, Name: a.name, Arguments: a.args.String()}
	}
	out <- llm.TurnDone{FinishReason: lastFinish, InputTokens: inputTokens, OutputTokens: outputTokens}
}
