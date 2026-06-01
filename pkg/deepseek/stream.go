package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Retry policy for transient DeepSeek stream failures (HTTP 5xx,
// transport errors, or an SSE body that completes without emitting any
// data). One retry catches the common upstream-blip case; more would
// just amplify cost when the model genuinely produced nothing. Safe
// because retry only fires before any event has reached the caller,
// and the re-sent body is byte-identical so prefix-cache stays hot.
const (
	maxStreamRetries   = 1
	streamRetryBackoff = 500 * time.Millisecond
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
//
// Retry: transient failures (HTTP 5xx, transport errors, an SSE body that
// closes without producing any data) trigger ONE silent retry with a fixed
// 500ms backoff. Retry only fires while emittedAny == false — once any
// delta has reached the caller, the partial state is committed and a
// stream interruption surfaces normally (as decode_error: / stream_error:
// finish reasons, matching pre-retry behaviour).
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamEvent, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil request")
	}
	r := *req
	r.Stream = true
	if r.StreamOptions == nil {
		r.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	resp, err := c.openChatStream(ctx, &r)
	if err != nil {
		return nil, err
	}

	out := make(chan StreamEvent, 16)
	go c.pumpChatStream(ctx, &r, resp, out)
	return out, nil
}

// openChatStream issues the streaming request, returning the response
// once a 2xx is observed. Transport errors, 5xx, and 429 (Too Many
// Requests) are retried once with a fixed backoff; other 4xx is fatal
// (those are configuration problems, not transients).
func (c *Client) openChatStream(ctx context.Context, req *ChatRequest) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxStreamRetries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, streamRetryBackoff); err != nil {
				return nil, err
			}
		}
		resp, err := c.do(ctx, endpointChat, req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		apiErr := parseAPIError(resp.StatusCode, body)
		if resp.StatusCode/100 == 5 || resp.StatusCode == 429 {
			lastErr = apiErr
			continue
		}
		return nil, apiErr
	}
	return nil, lastErr
}

// pumpChatStream owns the response body and drives one or more
// readChatStream passes. If the first pass produces no events at all
// (transport break, malformed first chunk, or a clean [DONE] with no
// deltas) we re-open the request once — the same conditions that cause
// upstream 5xx also produce SSE bodies that close before producing data,
// and a one-shot retry resolves both without surfacing them to the agent
// layer (which would otherwise hit the empty-response guard).
func (c *Client) pumpChatStream(ctx context.Context, req *ChatRequest, initial *http.Response, out chan<- StreamEvent) {
	defer close(out)

	resp := initial
	for attempt := 0; attempt <= maxStreamRetries; attempt++ {
		result := c.readChatStream(resp.Body, out)

		canRetry := !result.emittedAny &&
			attempt < maxStreamRetries &&
			ctx.Err() == nil &&
			(result.streamErr != nil || result.isEmpty())

		if !canRetry {
			emitTerminalDone(out, result, nil)
			return
		}

		if err := sleepCtx(ctx, streamRetryBackoff); err != nil {
			emitTerminalDone(out, result, nil)
			return
		}
		newResp, err := c.openChatStream(ctx, req)
		if err != nil {
			emitTerminalDone(out, result, err)
			return
		}
		resp = newResp
	}
}

// streamReadResult collects the outcome of a single SSE read pass.
// emittedAny is the load-bearing field: once true, the caller's UI/state
// has committed to the partial stream and retry is no longer safe.
type streamReadResult struct {
	emittedAny   bool
	lastUsage    Usage
	finishReason string
	streamErr    error // populated on decode failure or transport read error
}

// isEmpty reports whether the stream completed cleanly but produced
// nothing the caller could act on. This is the "model said nothing"
// case that the agent layer's empty-response guard exists to catch —
// we retry here so callers don't see it on the first attempt.
func (r streamReadResult) isEmpty() bool {
	return r.streamErr == nil && !r.emittedAny && r.finishReason == ""
}

// readChatStream parses one SSE body. It does NOT close out (the caller
// owns the channel lifetime) and only emits data events — terminal
// EventDone is emitted by the caller after retry policy has been
// applied.
func (c *Client) readChatStream(body io.ReadCloser, out chan<- StreamEvent) streamReadResult {
	defer body.Close()

	var result streamReadResult
	sc := bufio.NewScanner(body)
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
			result.streamErr = fmt.Errorf("decode: %w", err)
			return result
		}

		if chunk.Usage != nil {
			result.lastUsage = *chunk.Usage
		}

		for _, ch := range chunk.Choices {
			if ch.Delta.ReasoningContent != "" {
				out <- StreamEvent{Type: EventReasoningDelta, Delta: ch.Delta.ReasoningContent}
				result.emittedAny = true
			}
			if ch.Delta.Content != "" {
				out <- StreamEvent{Type: EventDelta, Delta: ch.Delta.Content}
				result.emittedAny = true
			}
			for i := range ch.Delta.ToolCalls {
				tc := ch.Delta.ToolCalls[i]
				out <- StreamEvent{Type: EventToolCallDelta, ToolCall: &tc}
				result.emittedAny = true
			}
			if ch.FinishReason != "" {
				result.finishReason = ch.FinishReason
			}
		}
	}

	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		result.streamErr = err
	}
	return result
}

// emitTerminalDone synthesises the final EventDone. The finish_reason
// sentinels ("decode_error:..." / "stream_error:...") match the pre-
// retry wire shape so callers (e.g. agent_test.go:TestAgent_DecodeError
// MidStream_DropsTurn) continue to recognise them.
func emitTerminalDone(out chan<- StreamEvent, result streamReadResult, openErr error) {
	finish := result.finishReason
	switch {
	case openErr != nil:
		finish = "stream_error:" + truncate(openErr.Error(), 80)
	case result.streamErr != nil:
		msg := result.streamErr.Error()
		if rest, ok := strings.CutPrefix(msg, "decode: "); ok {
			finish = "decode_error:" + truncate(rest, 80)
		} else {
			finish = "stream_error:" + truncate(msg, 80)
		}
	}
	out <- StreamEvent{
		Type:         EventDone,
		Usage:        result.lastUsage,
		FinishReason: finish,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// StripReasoningContent returns a copy of msgs with ReasoningContent
// cleared on assistant messages WHERE SAFE per the V4 thinking-mode
// contract (api-docs.deepseek.com/guides/thinking_mode):
//
//   - assistant message had tool_calls → reasoning_content MUST be
//     preserved and replayed on every subsequent turn, otherwise the
//     API returns 400 ("reasoning_content in the thinking mode must
//     be passed back to the API"). The rationale, per DeepSeek, is
//     that the model's choice to call a tool is causally tied to its
//     CoT for that turn; replaying the conversation without it changes
//     what the model "knows about itself".
//
//   - assistant message had no tool_calls → reasoning_content can be
//     dropped freely. The next turn re-derives reasoning from scratch;
//     keeping the old trace just inflates prompt tokens.
//
// NOTE: this contract is the OPPOSITE of the pre-V4 deepseek-reasoner
// model, which rejected any request that retained prior
// reasoning_content. The pitfall is documented in docs/pitfalls.md.
//
// As of v4 柱 D, this function also clears PredictedNext on every
// message — that field is a session-persistence-only artifact (see
// pkg/deepseek/types.go), and DeepSeek doesn't accept it.
//
// Callers should always run history through this function before
// resending — the conditional logic lives here so individual call
// sites don't need to track which assistant turns had tool_calls.
func StripReasoningContent(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		// PredictedNext is always stripped — it's a side-channel UX
		// hint that lives in the session JSONL but never crosses the
		// API boundary.
		out[i].PredictedNext = ""
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			// Required by the API — keep reasoning_content intact.
			continue
		}
		out[i].ReasoningContent = ""
	}
	return out
}

// FormatHitRatio formats a Usage as a human-readable percentage string
// (e.g. "73.2%"). Returns "n/a" when there are no cache-accounted tokens.
// Lives in deepseek so external callers don't reinvent it.
func FormatHitRatio(u Usage) string {
	if u.PromptCacheHitTokens+u.PromptCacheMissTokens == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", u.HitRatio()*100)
}
