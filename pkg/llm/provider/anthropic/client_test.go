package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/llm"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sseServer starts a fake Anthropic SSE server that writes the given lines
// (plain strings, not yet prefixed with "data:") to each request.
// Lines that start with "data:" are sent as-is; others are wrapped.
func sseServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement http.Flusher")
			return
		}
		for _, l := range lines {
			if !strings.HasPrefix(l, "data:") {
				l = "data: " + l
			}
			fmt.Fprintln(w, l)
			fl.Flush()
		}
	}))
}

// drain collects all events from the channel with a generous timeout to avoid
// hanging the test suite on a broken implementation.
func drain(t *testing.T, ch <-chan llm.Event) []llm.Event {
	t.Helper()
	var out []llm.Event
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timer.C:
			t.Fatal("timed out waiting for channel to close")
			return out
		}
	}
}

func newClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := newWithBase("test-key", srv.URL)
	return c
}

// ---------------------------------------------------------------------------
// Canned SSE payloads
// ---------------------------------------------------------------------------

func jsonLine(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var (
	msgStart = jsonLine(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})

	textBlockStart = jsonLine(map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
		},
	})

	textDelta = func(text string) string {
		return jsonLine(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		})
	}

	textBlockStop = jsonLine(map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})

	msgDelta = jsonLine(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": 5},
	})

	msgStop = jsonLine(map[string]any{"type": "message_stop"})

	toolBlockStart = func(index int, id, name string) string {
		return jsonLine(map[string]any{
			"type":  "content_block_start",
			"index": index,
			"content_block": map[string]any{
				"type": "tool_use",
				"id":   id,
				"name": name,
			},
		})
	}

	toolArgDelta = func(index int, partial string) string {
		return jsonLine(map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
		})
	}

	toolBlockStop = func(index int) string {
		return jsonLine(map[string]any{"type": "content_block_stop", "index": index})
	}

	msgDeltaTool = jsonLine(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "tool_use"},
		"usage": map[string]any{"output_tokens": 12},
	})
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestName(t *testing.T) {
	c := New("key")
	if got := c.Name(); got != "Anthropic" {
		t.Errorf("Name() = %q, want %q", got, "Anthropic")
	}
}

func TestChatStream_TextOnly(t *testing.T) {
	lines := []string{
		msgStart,
		textBlockStart,
		textDelta("Hello, "),
		textDelta("world!"),
		textBlockStop,
		msgDelta,
		msgStop,
	}
	srv := sseServer(t, lines)
	defer srv.Close()

	c := newClient(t, srv)
	ch, err := c.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := drain(t, ch)

	var deltas []string
	var done *llm.TurnDone
	for _, e := range events {
		switch v := e.(type) {
		case llm.TextDelta:
			deltas = append(deltas, v.Delta)
		case llm.TurnDone:
			d := v
			done = &d
		case llm.ErrorEvent:
			t.Fatalf("unexpected error event: %v", v.Err)
		}
	}

	text := strings.Join(deltas, "")
	if text != "Hello, world!" {
		t.Errorf("text = %q, want %q", text, "Hello, world!")
	}
	if done == nil {
		t.Fatal("TurnDone event missing")
	}
	if done.FinishReason != "end_turn" {
		t.Errorf("finish reason = %q, want end_turn", done.FinishReason)
	}
	if done.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", done.InputTokens)
	}
	if done.OutputTokens != 5 {
		t.Errorf("output tokens = %d, want 5", done.OutputTokens)
	}
}

func TestChatStream_ToolCall(t *testing.T) {
	lines := []string{
		msgStart,
		toolBlockStart(0, "tool_abc123", "read_file"),
		toolArgDelta(0, `{"path":`),
		toolArgDelta(0, `"/tmp/foo"}`),
		toolBlockStop(0),
		msgDeltaTool,
		msgStop,
	}
	srv := sseServer(t, lines)
	defer srv.Close()

	c := newClient(t, srv)
	ch, err := c.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []llm.Message{{Role: "user", Content: "read a file"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := drain(t, ch)

	var toolDone *llm.ToolCallDone
	var turnDone *llm.TurnDone
	for _, e := range events {
		switch v := e.(type) {
		case llm.ToolCallDone:
			d := v
			toolDone = &d
		case llm.TurnDone:
			d := v
			turnDone = &d
		case llm.ErrorEvent:
			t.Fatalf("unexpected error event: %v", v.Err)
		}
	}

	if toolDone == nil {
		t.Fatal("ToolCallDone event missing")
	}
	if toolDone.ID != "tool_abc123" {
		t.Errorf("tool ID = %q, want tool_abc123", toolDone.ID)
	}
	if toolDone.Name != "read_file" {
		t.Errorf("tool name = %q, want read_file", toolDone.Name)
	}
	want := `{"path":"/tmp/foo"}`
	if toolDone.Arguments != want {
		t.Errorf("tool args = %q, want %q", toolDone.Arguments, want)
	}
	if turnDone == nil {
		t.Fatal("TurnDone missing")
	}
	if turnDone.FinishReason != "tool_use" {
		t.Errorf("finish reason = %q, want tool_use", turnDone.FinishReason)
	}
}

func TestChatStream_MultipleTools(t *testing.T) {
	lines := []string{
		msgStart,
		toolBlockStart(0, "id_1", "tool_alpha"),
		toolArgDelta(0, `{"x":1}`),
		toolBlockStop(0),
		toolBlockStart(1, "id_2", "tool_beta"),
		toolArgDelta(1, `{"y":2}`),
		toolBlockStop(1),
		msgDeltaTool,
		msgStop,
	}
	srv := sseServer(t, lines)
	defer srv.Close()

	c := newClient(t, srv)
	ch, err := c.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []llm.Message{{Role: "user", Content: "do two things"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := drain(t, ch)

	var tools []llm.ToolCallDone
	for _, e := range events {
		if v, ok := e.(llm.ToolCallDone); ok {
			tools = append(tools, v)
		}
		if v, ok := e.(llm.ErrorEvent); ok {
			t.Fatalf("unexpected error event: %v", v.Err)
		}
	}

	if len(tools) != 2 {
		t.Fatalf("got %d ToolCallDone events, want 2", len(tools))
	}
	if tools[0].ID != "id_1" || tools[0].Name != "tool_alpha" {
		t.Errorf("tool[0] = {%s %s}, want {id_1 tool_alpha}", tools[0].ID, tools[0].Name)
	}
	if tools[1].ID != "id_2" || tools[1].Name != "tool_beta" {
		t.Errorf("tool[1] = {%s %s}, want {id_2 tool_beta}", tools[1].ID, tools[1].Name)
	}
}

func TestChatStream_ContextCancelled(t *testing.T) {
	// Slow server: blocks indefinitely after first event.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprintln(w, "data: "+msgStart)
		fl.Flush()
		// Block until client disconnects.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := newClient(t, srv)
	ch, err := c.ChatStream(ctx, llm.ChatRequest{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	// Cancel after the first event arrives or after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Channel must close cleanly (no panic, no hang).
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed — success
			}
		case <-timer.C:
			t.Fatal("channel did not close after context cancellation")
		}
	}
}

func TestChatStream_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintln(w, `{"error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`)
	}))
	defer srv.Close()

	c := newClient(t, srv)
	ch, err := c.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})

	// A non-2xx response must surface as an error from ChatStream itself (no
	// channel), because the HTTP response is available synchronously.
	if err == nil {
		// Drain channel in case implementation chose to send ErrorEvent.
		if ch != nil {
			events := drain(t, ch)
			for _, e := range events {
				if ee, ok := e.(llm.ErrorEvent); ok {
					_ = ee
					return // acceptable: error delivered via channel
				}
			}
		}
		t.Fatal("expected error for 429 status, got nil")
	}

	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error %q should mention status 429", err.Error())
	}
}
