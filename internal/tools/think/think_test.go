package think

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// thinkingServer mimics a V4 chat response with thinking enabled —
// reasoning_content alongside content. Verifies that the think tool
// sends Model=V4-Flash, thinking={type:enabled}, reasoning_effort=high.
func thinkingServer(t *testing.T, wantSystem string, reasoning, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != deepseek.ModelV4Flash {
			t.Errorf("model = %q, want V4-Flash", body.Model)
		}
		if body.Thinking == nil || body.Thinking.Type != "enabled" {
			t.Errorf("expected Thinking.Type=enabled, got %+v", body.Thinking)
		}
		if body.ReasoningEffort != "high" {
			t.Errorf("expected reasoning_effort=high, got %q", body.ReasoningEffort)
		}
		if wantSystem != "" {
			if len(body.Messages) == 0 || body.Messages[0].Role != deepseek.RoleSystem || !strings.Contains(body.Messages[0].Content, wantSystem) {
				t.Errorf("missing system fragment %q; messages=%+v", wantSystem, body.Messages)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":      "x",
			"object":  "chat.completion",
			"created": 1,
			"model":   deepseek.ModelV4Flash,
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":              "assistant",
					"content":           answer,
					"reasoning_content": reasoning,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":            50,
				"completion_tokens":        30,
				"total_tokens":             80,
				"prompt_cache_hit_tokens":  10,
				"prompt_cache_miss_tokens": 40,
			},
		}
		buf, _ := json.Marshal(resp)
		_, _ = w.Write(buf)
		_ = io.EOF
	}))
}

func TestThink_HappyPath(t *testing.T) {
	srv := thinkingServer(t, "step-by-step reasoner", "step 1 ... step 2 ...", "do X then Y")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "Plan a refactor"})

	out, err := New(c).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"reasoning ---", "step 1", "answer ---", "do X then Y", "usage:"} {
		if !strings.Contains(out, frag) {
			t.Errorf("output missing %q: %s", frag, out)
		}
	}
}

func TestThink_ReflectUsesReviewSystem(t *testing.T) {
	srv := thinkingServer(t, "code-review reasoner", "looks fine", "no issues")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "Review my diff", Reflect: true, Context: "some code"})

	_, err := New(c).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
}

func TestThink_ContextIsPasted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Find the user message and check for context.
		var user string
		for _, m := range body.Messages {
			if m.Role == deepseek.RoleUser {
				user = m.Content
			}
		}
		if !strings.Contains(user, "MAGIC_TOKEN_4242") {
			t.Errorf("context not pasted into user message: %q", user)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"r"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "evaluate", Context: "MAGIC_TOKEN_4242"})
	if _, err := New(c).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
}

func TestThink_MissingTask(t *testing.T) {
	c := deepseek.New(deepseek.WithAPIKey("t"))
	_, err := New(c).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "task is required") {
		t.Errorf("err = %v", err)
	}
}

// thinkingSSE serves a deepseek-style SSE stream that interleaves
// reasoning_content and content deltas, then a final usage chunk and
// [DONE]. Mirrors what a real V4-Flash with thinking=enabled emits.
func thinkingSSE(t *testing.T, reasoningDeltas, contentDeltas []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			t.Errorf("ExecuteStream did not set Stream=true on the request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		for _, d := range reasoningDeltas {
			b, _ := json.Marshal(d)
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":`+string(b)+`}}]}`+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		for _, d := range contentDeltas {
			b, _ := json.Marshal(d)
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":`+string(b)+`}}]}`+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":30,"total_tokens":80,"prompt_cache_hit_tokens":10,"prompt_cache_miss_tokens":40}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// drain collects every delta off the channel until it closes, returning
// them split by their Reasoning flag. Both slices preserve order.
func drainDeltas(ch <-chan tools.StreamDelta) (reasoning, content []string) {
	for d := range ch {
		if d.Reasoning {
			reasoning = append(reasoning, d.Delta)
		} else {
			content = append(content, d.Delta)
		}
	}
	return
}

func TestThink_ExecuteStream_RoutesDeltasByKind(t *testing.T) {
	srv := thinkingSSE(t,
		[]string{"step 1...", " step 2..."},
		[]string{"do X", " then Y"},
	)
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "Plan a refactor"})

	deltas := make(chan tools.StreamDelta, 16)
	var (
		reasoning []string
		content   []string
		drained   = make(chan struct{})
	)
	go func() {
		defer close(drained)
		reasoning, content = drainDeltas(deltas)
	}()

	out, err := New(c).ExecuteStream(context.Background(), args, deltas)
	if err != nil {
		t.Fatal(err)
	}
	close(deltas)
	<-drained

	if got := strings.Join(reasoning, ""); got != "step 1... step 2..." {
		t.Errorf("reasoning stream = %q", got)
	}
	if got := strings.Join(content, ""); got != "do X then Y" {
		t.Errorf("content stream = %q", got)
	}
	// Final return string must still match the formatResult shape so
	// downstream behaviour (history persistence, follow-up chat turn)
	// is identical to the non-streaming path.
	for _, frag := range []string{"step 1...", "do X then Y", "reasoning ---", "answer ---", "usage:"} {
		if !strings.Contains(out, frag) {
			t.Errorf("return string missing %q:\n%s", frag, out)
		}
	}
}

func TestThink_ExecuteStream_RespectsCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."}}]}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "x"})

	ctx, cancel := context.WithCancel(context.Background())
	deltas := make(chan tools.StreamDelta, 16)
	go func() {
		for range deltas {
		}
	}()

	go func() {
		// Cancel shortly after kick-off — the server holds the
		// connection open, so without the cancel the test would
		// stall until httptest tears down.
		<-time.After(50 * time.Millisecond)
		cancel()
	}()

	_, err := New(c).ExecuteStream(ctx, args, deltas)
	if err == nil {
		t.Errorf("expected ctx.Canceled, got nil")
	}
	close(deltas)
}

func TestThink_TruncatesLongReasoning(t *testing.T) {
	long := strings.Repeat("a", reasoningCap+500)
	srv := thinkingServer(t, "", long, "short")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "x"})
	out, err := New(c).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker: ...%s", out[len(out)-200:])
	}
}
