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
// sends the given wantModel, thinking={type:enabled}, reasoning_effort=high.
func thinkingServer(t *testing.T, wantModel, wantSystem string, reasoning, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != wantModel {
			t.Errorf("model = %q, want %q", body.Model, wantModel)
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
	srv := thinkingServer(t, deepseek.ModelV4Flash, "step-by-step reasoner", "step 1 ... step 2 ...", "do X then Y")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "Plan a refactor"})

	out, err := New(c, func() string { return deepseek.ModelV4Flash }, nil).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"reasoning ---", "step 1", "answer ---", "do X then Y", "usage:", deepseek.ModelV4Flash} {
		if !strings.Contains(out, frag) {
			t.Errorf("output missing %q: %s", frag, out)
		}
	}
}

func TestThink_ReflectUsesReviewSystem(t *testing.T) {
	srv := thinkingServer(t, deepseek.ModelV4Flash, "code-review reasoner", "looks fine", "no issues")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "Review my diff", Reflect: true, Context: "some code"})

	_, err := New(c, func() string { return deepseek.ModelV4Flash }, nil).Execute(context.Background(), args)
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
	if _, err := New(c, func() string { return deepseek.ModelV4Flash }, nil).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
}

func TestThink_MissingTask(t *testing.T) {
	c := deepseek.New(deepseek.WithAPIKey("t"))
	_, err := New(c, nil, nil).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "task is required") {
		t.Errorf("err = %v", err)
	}
}

// thinkingSSE serves a deepseek-style SSE stream that interleaves
// reasoning_content and content deltas, then a final usage chunk and
// [DONE]. Mirrors what a real V4 model with thinking=enabled emits.
func thinkingSSE(t *testing.T, wantModel string, reasoningDeltas, contentDeltas []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != wantModel {
			t.Errorf("model = %q, want %q", body.Model, wantModel)
		}
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

// collectingPusher returns a push callback that records every delta
// it sees, plus the slices the recorder writes into. Order is
// preserved across calls.
func collectingPusher() (push func(tools.StreamDelta) error, reasoning, content *[]string) {
	var r, c []string
	push = func(d tools.StreamDelta) error {
		if d.Reasoning {
			r = append(r, d.Delta)
		} else {
			c = append(c, d.Delta)
		}
		return nil
	}
	return push, &r, &c
}

func TestThink_ExecuteStream_RoutesDeltasByKind(t *testing.T) {
	srv := thinkingSSE(t, deepseek.ModelV4Flash,
		[]string{"step 1...", " step 2..."},
		[]string{"do X", " then Y"},
	)
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "Plan a refactor"})

	push, reasoning, content := collectingPusher()
	out, err := New(c, func() string { return deepseek.ModelV4Flash }, nil).ExecuteStream(context.Background(), args, push)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(*reasoning, ""); got != "step 1... step 2..." {
		t.Errorf("reasoning stream = %q", got)
	}
	if got := strings.Join(*content, ""); got != "do X then Y" {
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

func TestThink_ExecuteStream_PropagatesPushError(t *testing.T) {
	// A push callback that returns ctx.Canceled on the first delta
	// must cause ExecuteStream to return that error immediately,
	// without waiting for the underlying stream to finish. This is
	// exactly the Esc-interrupt path: the agent's push fails fast,
	// the tool propagates, dispatchTool moves on.
	//
	// Server keeps emitting deltas indefinitely; if the push-error
	// propagation works the test returns in milliseconds, otherwise
	// it would hang until the httptest server is torn down.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."}}]}`+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "x"})

	wantErr := context.Canceled
	push := func(_ tools.StreamDelta) error { return wantErr }

	_, err := New(c, func() string { return deepseek.ModelV4Flash }, nil).ExecuteStream(context.Background(), args, push)
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestThink_TruncatesLongReasoning(t *testing.T) {
	long := strings.Repeat("a", reasoningCap+500)
	srv := thinkingServer(t, deepseek.ModelV4Flash, "", long, "short")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "x"})
	out, err := New(c, func() string { return deepseek.ModelV4Flash }, nil).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker: ...%s", out[len(out)-200:])
	}
}

// TestThink_BumpEffort is a direct unit test for the one-level-up rule.
// Keeping it as a plain function test (no HTTP server) means a future
// change to the rule must touch this file — a deliberate alarm bell.
func TestThink_BumpEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "high"},
		{"off", "high"}, // off should not reach this function but be defensive
		{"high", "max"},
		{"max", "max"},
		{"garbage", "high"}, // unknown values fall to the safe default
	}
	for _, c := range cases {
		if got := bumpEffort(c.in); got != c.want {
			t.Errorf("bumpEffort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestThink_EffortFromSession verifies that the effortFunc closure
// flows into the request body. The session-level "high" must become
// the think-call "max" — the whole point of the linkage is that
// invoking think always escalates relative to the surrounding chat.
func TestThink_EffortFromSession(t *testing.T) {
	cases := []struct {
		sessionEffort string
		wantThink     string
	}{
		{"", "high"},
		{"high", "max"},
		{"max", "max"},
	}
	for _, tc := range cases {
		t.Run("session="+tc.sessionEffort, func(t *testing.T) {
			var captured string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body deepseek.ChatRequest
				_ = json.NewDecoder(r.Body).Decode(&body)
				captured = body.ReasoningEffort
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"r"},"finish_reason":"stop"}],"usage":{}}`)
			}))
			defer srv.Close()

			client := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
			args, _ := json.Marshal(Args{Task: "x"})
			sess := tc.sessionEffort
			_, err := New(client, func() string { return deepseek.ModelV4Flash },
				func() string { return sess }).Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if captured != tc.wantThink {
				t.Errorf("session=%q → think effort=%q, want %q",
					tc.sessionEffort, captured, tc.wantThink)
			}
		})
	}
}

func TestThink_UsesCurrentModel(t *testing.T) {
	// When the modelFunc returns V4-Pro, the think tool must send
	// V4-Pro in the request body and reflect it in the output header.
	srv := thinkingServer(t, deepseek.ModelV4Pro, "step-by-step reasoner", "deep reasoning", "pro answer")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Task: "complex analysis"})

	out, err := New(c, func() string { return deepseek.ModelV4Pro }, nil).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, deepseek.ModelV4Pro) {
		t.Errorf("output should mention %q: %s", deepseek.ModelV4Pro, out)
	}
	if !strings.Contains(out, "pro answer") {
		t.Errorf("output missing answer: %s", out)
	}
}
