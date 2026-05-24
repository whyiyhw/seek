package deepseek

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSSE serves a canned SSE stream so we can exercise the parser without a
// real DeepSeek key. The payload mimics the wire format DeepSeek emits.
func fakeSSE(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing bearer auth: %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
}

func TestChatStream_ParsesDeltasAndUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		``,
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		``,
		`data: {"id":"1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":2}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	srv := fakeSSE(t, body)
	defer srv.Close()

	c := New(WithAPIKey("test"), WithBaseURL(srv.URL))
	ch, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelChat,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var (
		got    string
		usage  Usage
		finish string
	)
	for ev := range ch {
		switch ev.Type {
		case EventDelta:
			got += ev.Delta
		case EventDone:
			usage = ev.Usage
			finish = ev.FinishReason
		}
	}
	if got != "Hello" {
		t.Errorf("delta text = %q, want %q", got, "Hello")
	}
	if finish != "stop" {
		t.Errorf("finish = %q, want stop", finish)
	}
	if usage.PromptCacheHitTokens != 8 || usage.PromptCacheMissTokens != 2 {
		t.Errorf("cache tokens = (%d, %d), want (8, 2)",
			usage.PromptCacheHitTokens, usage.PromptCacheMissTokens)
	}
	if got, want := usage.HitRatio(), 0.8; got != want {
		t.Errorf("hit ratio = %v, want %v", got, want)
	}
}

func TestChatStream_ReasoningDelta(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"Let me think..."}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"42"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	srv := fakeSSE(t, body)
	defer srv.Close()

	c := New(WithAPIKey("test"), WithBaseURL(srv.URL))
	ch, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelReasoner,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var reasoning, content string
	for ev := range ch {
		switch ev.Type {
		case EventReasoningDelta:
			reasoning += ev.Delta
		case EventDelta:
			content += ev.Delta
		}
	}
	if reasoning != "Let me think..." {
		t.Errorf("reasoning = %q", reasoning)
	}
	if content != "42" {
		t.Errorf("content = %q", content)
	}
}

func TestStripReasoningContent(t *testing.T) {
	// V4 thinking-mode contract (api-docs.deepseek.com/guides/thinking_mode):
	//  - assistant w/o tool_calls → reasoning_content can be stripped
	//  - assistant w/  tool_calls → reasoning_content MUST be preserved
	//    (replaying without it returns 400 from the API)
	//  - user / tool / system → reasoning_content N/A, no behaviour change
	in := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, Content: "a", ReasoningContent: "because..."},
		{Role: RoleAssistant, ReasoningContent: "planning a tool call",
			ToolCalls: []ToolCall{{ID: "c1", Type: "function",
				Function: ToolCallFunc{Name: "read", Arguments: `{"path":"x"}`}}}},
		{Role: RoleTool, Content: "tool result", ToolCallID: "c1"},
		{Role: RoleAssistant, Content: "final", ReasoningContent: "wrap-up CoT"},
	}
	out := StripReasoningContent(in)

	// [1] assistant w/o tool_calls: stripped
	if out[1].ReasoningContent != "" {
		t.Errorf("[1] assistant w/o tool_calls: ReasoningContent should be stripped, got %q", out[1].ReasoningContent)
	}
	// [2] assistant w/ tool_calls: KEPT (this is the load-bearing case)
	if out[2].ReasoningContent != "planning a tool call" {
		t.Errorf("[2] assistant w/ tool_calls: ReasoningContent MUST be preserved per V4 API contract, got %q", out[2].ReasoningContent)
	}
	// [4] another assistant w/o tool_calls: stripped
	if out[4].ReasoningContent != "" {
		t.Errorf("[4] final assistant w/o tool_calls: ReasoningContent should be stripped, got %q", out[4].ReasoningContent)
	}

	// Original must be untouched (function returns a copy, never mutates input).
	if in[1].ReasoningContent != "because..." || in[2].ReasoningContent != "planning a tool call" || in[4].ReasoningContent != "wrap-up CoT" {
		t.Errorf("input was mutated: %+v", in)
	}
}

func TestShouldEnableThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{ModelV4Pro, true},
		{ModelReasoner, true},
		{ModelV4Flash, false},
		{ModelChat, false},
		{"", false},
		{"some-future-custom-model", false},
	}
	for _, c := range cases {
		if got := ShouldEnableThinking(c.model); got != c.want {
			t.Errorf("ShouldEnableThinking(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

func TestUsage_HitRatio(t *testing.T) {
	cases := []struct {
		hit, miss int
		want      float64
	}{
		{0, 0, 0},
		{100, 0, 1.0},
		{0, 100, 0.0},
		{75, 25, 0.75},
	}
	for _, c := range cases {
		u := Usage{PromptCacheHitTokens: c.hit, PromptCacheMissTokens: c.miss}
		if got := u.HitRatio(); got != c.want {
			t.Errorf("HitRatio(%d,%d) = %v, want %v", c.hit, c.miss, got, c.want)
		}
	}
}

func TestChat_NonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"1","object":"chat.completion","created":1,"model":"deepseek-chat",
			"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6,"prompt_cache_hit_tokens":3,"prompt_cache_miss_tokens":2}
		}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("test"), WithBaseURL(srv.URL))
	resp, err := c.Chat(context.Background(), &ChatRequest{
		Model:    ModelChat,
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := resp.Choices[0].Message.Content; got != "pong" {
		t.Errorf("content = %q, want pong", got)
	}
	if resp.Usage.PromptCacheHitTokens != 3 {
		t.Errorf("cache hit tokens = %d, want 3", resp.Usage.PromptCacheHitTokens)
	}
}

func TestChat_MissingKey(t *testing.T) {
	c := New() // no key
	_, err := c.Chat(context.Background(), &ChatRequest{Model: ModelChat})
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("expected missing-key error, got %v", err)
	}
}

// validStream is a canned SSE body that produces exactly one delta
// "ok" and finishes with reason="stop". Used as the "happy" payload by
// the retry tests below.
const validStreamBody = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func drainStream(t *testing.T, ch <-chan StreamEvent) (text, finish string) {
	t.Helper()
	for ev := range ch {
		switch ev.Type {
		case EventDelta:
			text += ev.Delta
		case EventDone:
			finish = ev.FinishReason
		}
	}
	return text, finish
}

// TestChatStream_RetryOn500 verifies that an HTTP 500 from DeepSeek is
// transparently retried once. This is the canonical case the retry was
// added for — an `internal_error: Internal Server Error` from the
// initial request would otherwise bubble up as a hard failure to the
// agent layer and force the user to manually re-send.
func TestChatStream_RetryOn500(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"internal_error","message":"Internal Server Error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, validStreamBody)
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	ch, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelChat,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	text, finish := drainStream(t, ch)
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
	if finish != "stop" {
		t.Errorf("finish = %q, want stop", finish)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want 2 (first=500, second=200)", got)
	}
}

// TestChatStream_RetryOnEmptyBody covers the second retry trigger: the
// HTTP response is 200 but the SSE body closes without producing any
// delta. This is what happens when DeepSeek's upstream closes the
// connection mid-thinking — the stream is technically clean, but the
// caller would otherwise see an empty assistant turn (which then trips
// the agent's empty-response guard).
func TestChatStream_RetryOnEmptyBody(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			// Empty body — emulates a stream that closes before any
			// data: line is sent.
			return
		}
		_, _ = io.WriteString(w, validStreamBody)
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	ch, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelChat,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	text, finish := drainStream(t, ch)
	if text != "ok" {
		t.Errorf("text = %q, want %q (retry should have succeeded)", text, "ok")
	}
	if finish != "stop" {
		t.Errorf("finish = %q, want stop", finish)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want 2", got)
	}
}

// TestChatStream_RetryBudgetCapped pins the "one retry, not infinite"
// invariant. If the upstream is genuinely down, surface the error
// rather than burning tokens (and the user's time) on an infinite loop.
func TestChatStream_RetryBudgetCapped(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"type":"internal_error","message":"boom"}}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelChat,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("expected error after retry exhausted")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should preserve original message, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want exactly 2 (initial + one retry)", got)
	}
}

// TestChatStream_NoRetryAfterEmit is the load-bearing safety property:
// once any delta has reached the caller, the partial state is committed
// to whatever rendered it (TUI viewport, session history, accumulating
// tool_call args). A retry at this point would duplicate or contradict
// content the user has already seen.
//
// The fixture emits a valid delta first, then a malformed JSON chunk.
// With retry, the test would observe two emits of "ok"; without, it
// observes one emit plus a decode_error finish reason — matching the
// pre-retry behaviour relied on by agent_test.go:TestAgent_DecodeError
// MidStream_DropsTurn.
func TestChatStream_NoRetryAfterEmit(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Malformed chunk: terminates the stream with a decode error
		// without ever sending [DONE]. After emit, this must NOT retry.
		_, _ = io.WriteString(w, "data: {not json\n\n")
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	ch, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelChat,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	text, finish := drainStream(t, ch)
	if text != "ok" {
		t.Errorf("text = %q, want %q (retry would have duplicated content)", text, "ok")
	}
	if !strings.HasPrefix(finish, "decode_error:") {
		t.Errorf("finish = %q, want decode_error:... (sentinel relied on by agent layer)", finish)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want exactly 1 (must not retry post-emit)", got)
	}
}

// TestChatStream_4xxNoRetry: 4xx codes are configuration / auth / quota
// errors, not transients. Retrying just delays the inevitable. Pin
// behaviour so a future change to retry policy doesn't accidentally
// widen the trigger.
func TestChatStream_4xxNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.ChatStream(context.Background(), &ChatRequest{Model: ModelChat})
	if err == nil {
		t.Fatalf("expected auth error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (no retry on 4xx)", got)
	}
}

// TestChatStream_CtxCancelDuringBackoff verifies that a cancellation
// arriving while we're sleeping between attempts returns cleanly
// instead of pressing on with a doomed retry.
func TestChatStream_CtxCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"type":"internal_error","message":"x"}}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first request lands, while openChatStream
	// is waiting out its backoff before retrying.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.ChatStream(ctx, &ChatRequest{Model: ModelChat})
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}
