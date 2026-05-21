package think

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// reasonerServer mimics deepseek-reasoner's non-stream response, including
// the reasoning_content field on the assistant message.
func reasonerServer(t *testing.T, wantSystem string, reasoning, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != deepseek.ModelReasoner {
			t.Errorf("model = %q, want reasoner", body.Model)
		}
		if len(body.Tools) != 0 {
			t.Errorf("reasoner call should not include tools, got %d", len(body.Tools))
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
			"model":   deepseek.ModelReasoner,
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
	srv := reasonerServer(t, "step-by-step reasoner", "step 1 ... step 2 ...", "do X then Y")
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
	srv := reasonerServer(t, "code-review reasoner", "looks fine", "no issues")
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

func TestThink_TruncatesLongReasoning(t *testing.T) {
	long := strings.Repeat("a", reasoningCap+500)
	srv := reasonerServer(t, "", long, "short")
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
