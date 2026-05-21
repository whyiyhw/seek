package deepseek

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	in := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, Content: "a", ReasoningContent: "because..."},
	}
	out := StripReasoningContent(in)
	if out[1].ReasoningContent != "" {
		t.Errorf("ReasoningContent not stripped")
	}
	// Original must be untouched (function returns a copy).
	if in[1].ReasoningContent != "because..." {
		t.Errorf("input was mutated")
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
