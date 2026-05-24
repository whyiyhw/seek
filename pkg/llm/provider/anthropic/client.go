// Package anthropic implements the pkg/llm.Provider interface for the
// Anthropic Messages API (claude-3-* family). It uses stdlib only — no
// external dependencies.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/whyiyhw/seek/pkg/llm"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	endpointMessages = "/v1/messages"
	anthropicVersion = "2023-06-01"
)

// Client is the Anthropic provider. Create with New or newWithBase (tests).
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New creates a Client using the production Anthropic endpoint.
func New(apiKey string) *Client {
	return newWithBase(apiKey, defaultBaseURL)
}

// newWithBase creates a Client with an overridable base URL — used by tests.
func newWithBase(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Name implements llm.Provider.
func (c *Client) Name() string { return "Anthropic" }

// ---------------------------------------------------------------------------
// Wire types — Anthropic Messages API request/response shapes
// ---------------------------------------------------------------------------

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ---------------------------------------------------------------------------
// Message conversion helpers
// ---------------------------------------------------------------------------

// buildRequest converts an llm.ChatRequest into an anthropicRequest.
// Rules:
//   - If the first message has role "system", it is extracted into the
//     top-level system field (Anthropic does not accept system in messages[]).
//   - Consecutive tool-result messages are grouped into a single user message
//     with a []contentBlock payload (Anthropic requirement).
//   - Assistant messages with tool calls become content-block arrays.
func buildRequest(req llm.ChatRequest) anthropicRequest {
	ar := anthropicRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}

	msgs := req.Messages
	if len(msgs) > 0 && msgs[0].Role == "system" {
		ar.System = msgs[0].Content
		msgs = msgs[1:]
	}

	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}

	i := 0
	for i < len(msgs) {
		m := msgs[i]

		// Group consecutive tool-result messages into one user message.
		if m.Role == "tool" {
			var blocks []contentBlock
			for i < len(msgs) && msgs[i].Role == "tool" {
				blocks = append(blocks, contentBlock{
					Type:      "tool_result",
					ToolUseID: msgs[i].ToolCallID,
					Content:   msgs[i].Content,
				})
				i++
			}
			ar.Messages = append(ar.Messages, anthropicMessage{
				Role:    "user",
				Content: blocks,
			})
			continue
		}

		// Assistant message with tool calls → content-block array.
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var blocks []contentBlock
			if m.Content != "" {
				blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var raw json.RawMessage
				if tc.Arguments != "" {
					raw = json.RawMessage(tc.Arguments)
				} else {
					raw = json.RawMessage("{}")
				}
				blocks = append(blocks, contentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: raw,
				})
			}
			ar.Messages = append(ar.Messages, anthropicMessage{
				Role:    "assistant",
				Content: blocks,
			})
			i++
			continue
		}

		// Plain user or assistant message.
		ar.Messages = append(ar.Messages, anthropicMessage{
			Role:    m.Role,
			Content: m.Content,
		})
		i++
	}

	return ar
}

// ---------------------------------------------------------------------------
// SSE parsing types
// ---------------------------------------------------------------------------

type sseEvent struct {
	Type string `json:"type"`

	// message_start
	Message *struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message,omitempty"`

	// content_block_start / content_block_stop
	Index        int `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`

	// content_block_delta
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
		StopReason  string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`

	// message_delta usage
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// toolState tracks an in-progress tool_use block.
type toolState struct {
	id   string
	name string
	args strings.Builder
}

// ---------------------------------------------------------------------------
// ChatStream
// ---------------------------------------------------------------------------

// ChatStream implements llm.Provider. It opens an SSE stream, parses events,
// and forwards them to the returned channel. The channel is always closed,
// even on error paths. Cancelling ctx aborts the stream cleanly.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	ar := buildRequest(req)
	body, err := json.Marshal(ar)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+endpointMessages, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(errBody))
	}

	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		var (
			inputTokens  int
			outputTokens int
			finishReason string

			// index → toolState for concurrent blocks (Anthropic sends one at a
			// time in practice, but the spec allows out-of-order by index)
			tools = map[int]*toolState{}
		)

		send := func(e llm.Event) bool {
			select {
			case out <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}

		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for sc.Scan() {
			// Check context before processing each line.
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := sc.Bytes()
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) == 0 {
				continue
			}

			var ev sseEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				send(llm.ErrorEvent{Err: fmt.Errorf("anthropic: decode SSE: %w", err)})
				return
			}

			switch ev.Type {
			case "message_start":
				if ev.Message != nil {
					inputTokens = ev.Message.Usage.InputTokens
					outputTokens = ev.Message.Usage.OutputTokens
				}

			case "content_block_start":
				if ev.ContentBlock == nil {
					continue
				}
				if ev.ContentBlock.Type == "tool_use" {
					tools[ev.Index] = &toolState{
						id:   ev.ContentBlock.ID,
						name: ev.ContentBlock.Name,
					}
				}

			case "content_block_delta":
				if ev.Delta == nil {
					continue
				}
				switch ev.Delta.Type {
				case "text_delta":
					if !send(llm.TextDelta{Delta: ev.Delta.Text}) {
						return
					}
				case "input_json_delta":
					if ts, ok := tools[ev.Index]; ok {
						ts.args.WriteString(ev.Delta.PartialJSON)
					}
				}

			case "content_block_stop":
				if ts, ok := tools[ev.Index]; ok {
					if !send(llm.ToolCallDone{
						ID:        ts.id,
						Name:      ts.name,
						Arguments: ts.args.String(),
					}) {
						return
					}
					delete(tools, ev.Index)
				}

			case "message_delta":
				if ev.Delta != nil && ev.Delta.StopReason != "" {
					finishReason = ev.Delta.StopReason
				}
				if ev.Usage != nil {
					outputTokens = ev.Usage.OutputTokens
				}

			case "message_stop":
				send(llm.TurnDone{
					FinishReason: finishReason,
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
				})
				return
			}
		}

		// Scanner finished without message_stop (context cancel or network error).
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			send(llm.ErrorEvent{Err: fmt.Errorf("anthropic: stream read: %w", err)})
		}
	}()

	return out, nil
}
