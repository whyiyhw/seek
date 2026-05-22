package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// StreamEventType discriminates the events emitted by ChatStream.
type StreamEventType string

const (
	// EventDelta carries an incremental token chunk in Delta.
	EventDelta StreamEventType = "delta"
	// EventReasoningDelta carries a CoT chunk from a V4 thinking-mode response.
	EventReasoningDelta StreamEventType = "reasoning_delta"
	// EventToolCallDelta carries a partial tool-call construction. Wired up
	// in a later milestone — included here so the event surface is stable.
	EventToolCallDelta StreamEventType = "tool_call_delta"
	// EventDone is the terminal event, carrying Usage (incl. cache stats)
	// and FinishReason.
	EventDone StreamEventType = "done"
)

type StreamEvent struct {
	Type StreamEventType

	Delta string // for EventDelta / EventReasoningDelta

	ToolCall *ToolCall // for EventToolCallDelta

	FinishReason string // for EventDone
	Usage        Usage  // for EventDone
}

// streamChunk is the wire shape of one SSE data: {...} line.
type streamChunk struct {
	ID      string         `json:"id"`
	Choices []streamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type streamDelta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ChatStream issues a streaming chat completion and returns a channel of
// StreamEvent. The channel is closed when the stream terminates. The caller
// must drain the channel — cancelling ctx is the way to abort early.
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamEvent, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil request")
	}
	r := *req
	r.Stream = true
	if r.StreamOptions == nil {
		r.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	resp, err := c.do(ctx, endpointChat, &r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, parseAPIError(resp.StatusCode, body)
	}

	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		var (
			lastFinish string
			lastUsage  Usage
		)

		sc := bufio.NewScanner(resp.Body)
		// Allow lines up to 1 MiB to handle very large tool-call argument
		// chunks without truncation.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			// SSE format: each event is `data: <payload>` lines, separated
			// by blank lines. DeepSeek sends one chunk per `data:` line.
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) == 0 {
				continue
			}
			if string(payload) == "[DONE]" {
				break
			}

			var chunk streamChunk
			if err := json.Unmarshal(payload, &chunk); err != nil {
				out <- StreamEvent{
					Type:  EventDone,
					Usage: lastUsage,
					// Surface decode failure via a sentinel finish reason
					// for now — proper error event lives in pkg/agent layer.
					FinishReason: "decode_error:" + truncate(err.Error(), 80),
				}
				return
			}

			if chunk.Usage != nil {
				lastUsage = *chunk.Usage
			}

			for _, ch := range chunk.Choices {
				if ch.Delta.ReasoningContent != "" {
					out <- StreamEvent{
						Type:  EventReasoningDelta,
						Delta: ch.Delta.ReasoningContent,
					}
				}
				if ch.Delta.Content != "" {
					out <- StreamEvent{
						Type:  EventDelta,
						Delta: ch.Delta.Content,
					}
				}
				for i := range ch.Delta.ToolCalls {
					tc := ch.Delta.ToolCalls[i]
					out <- StreamEvent{
						Type:     EventToolCallDelta,
						ToolCall: &tc,
					}
				}
				if ch.FinishReason != "" {
					lastFinish = ch.FinishReason
				}
			}
		}

		if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
			out <- StreamEvent{
				Type:         EventDone,
				Usage:        lastUsage,
				FinishReason: "stream_error:" + truncate(err.Error(), 80),
			}
			return
		}

		out <- StreamEvent{
			Type:         EventDone,
			Usage:        lastUsage,
			FinishReason: lastFinish,
		}
	}()

	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// stripReasoningContent returns a copy of msgs with ReasoningContent cleared.
// DeepSeek's reasoner API rejects messages that retain a previous turn's
// reasoning_content field; callers MUST normalise history before resending.
//
// Exported for use by pkg/agent when it stitches reasoner output into a
// subsequent chat call.
func StripReasoningContent(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].ReasoningContent = ""
	}
	return out
}

// formatPercent is a tiny helper used by cmd/seek; lives here so external
// callers don't reinvent it.
func FormatHitRatio(u Usage) string {
	if u.PromptCacheHitTokens+u.PromptCacheMissTokens == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", u.HitRatio()*100)
}
