package titlesug

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// titleServer serves one canned chat completion and records how many
// requests arrived, so "at most one call" behaviour can be pinned.
func titleServer(t *testing.T, replies ...string) (*deepseek.Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		var req deepseek.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Thinking == nil || req.Thinking.Type != "disabled" {
			t.Errorf("thinking must be disabled, got %+v", req.Thinking)
		}
		if req.Model != deepseek.ModelV41Flash {
			t.Errorf("model = %q, want %q", req.Model, deepseek.ModelV41Flash)
		}
		if req.MaxTokens != maxOutputTokens {
			t.Errorf("max_tokens = %d, want %d", req.MaxTokens, maxOutputTokens)
		}
		n := int(hits.Add(1)) - 1
		content := ""
		if n < len(replies) {
			content = replies[n]
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
	}))
	t.Cleanup(srv.Close)
	return deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), &hits
}

func openingHistory() []deepseek.Message {
	return []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "帮我看看 WebSocket 重连为什么失败"},
		{Role: deepseek.RoleAssistant, Content: "先看一下你的重连逻辑"},
	}
}

func TestGenerate_HappyPath(t *testing.T) {
	client, hits := titleServer(t, "修复 WebSocket 重连")
	got := Generate(context.Background(), client, openingHistory())
	if got != "修复 WebSocket 重连" {
		t.Fatalf("title = %q", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestGenerate_Cleaning(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"quoted", `"修复 WebSocket 重连"`, "修复 WebSocket 重连"},
		{"cjk quotes", "「修复重连」", "修复重连"},
		{"book quotes", "《修复重连》", "修复重连"},
		{"multiline", "第一行标题\n后续解释废话", "第一行标题"},
		{"leading blank", "\n\n  带空格的标题  \n", "带空格的标题"},
		{"empty", "  \n  ", ""},
		{"long runes", strings.Repeat("宽", session.TitleMaxRunes+20), strings.Repeat("宽", session.TitleMaxRunes) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := titleServer(t, tc.raw)
			if got := Generate(context.Background(), client, openingHistory()); got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

// Cancellation / transport failure must collapse to "" with no title
// written by the caller (tested here at the generator boundary).
func TestGenerate_ContextCanceled(t *testing.T) {
	client, _ := titleServer(t, "too late")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Generate(ctx, client, openingHistory()); got != "" {
		t.Fatalf("canceled Generate = %q, want empty", got)
	}
}

func TestGenerate_APIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	defer srv.Close()
	client := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	if got := Generate(context.Background(), client, openingHistory()); got != "" {
		t.Fatalf("failed Generate = %q, want empty", got)
	}
}

func TestGenerate_NilClientAndEmptyHistory(t *testing.T) {
	if got := Generate(context.Background(), nil, openingHistory()); got != "" {
		t.Fatalf("nil client = %q", got)
	}
	client, hits := titleServer(t, "unused")
	if got := Generate(context.Background(), client, nil); got != "" {
		t.Fatalf("empty history = %q", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}
}

func TestOpeningTranscript_BoundedAndStripsSideChannels(t *testing.T) {
	history := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "system prompt must not leak"},
		{Role: deepseek.RoleUser, Content: "帮我改 bug"},
		{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
			{ID: "c1", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"/very/long/path"}`}},
		}, Content: "dispatching"},
		{Role: deepseek.RoleTool, ToolCallID: "c1", Content: "tool output"},
		{Role: deepseek.RoleAssistant, Content: "done", PredictedNext: "thanks", ReasoningContent: "hidden reasoning"},
	}
	got := openingTranscript(history)
	if strings.Contains(got, "system prompt") || strings.Contains(got, "dispatching") ||
		strings.Contains(got, "tool output") || strings.Contains(got, "hidden reasoning") ||
		strings.Contains(got, "thanks") {
		t.Fatalf("side channels leaked into naming prompt: %q", got)
	}
	if !strings.Contains(got, "User: 帮我改 bug") || !strings.Contains(got, "Assistant: done") {
		t.Fatalf("opening = %q", got)
	}

	// A single huge user message must be truncated without splitting a
	// multi-byte rune (invalid UTF-8 would 400 the naming request).
	huge := []deepseek.Message{{Role: deepseek.RoleUser, Content: strings.Repeat("宽", 20000)}}
	out := openingTranscript(huge)
	if len(out) > maxInputBytes+len("User: ") || !utf8.ValidString(out) {
		t.Fatalf("oversize opening: bytes=%d valid=%v", len(out), utf8.ValidString(out))
	}
}

// Compile-time guard: GenerateTimeout must be a sane budget, not zero.
func TestGenerateTimeout_Positive(t *testing.T) {
	if GenerateTimeout <= 0 || GenerateTimeout > 30*time.Second {
		t.Fatalf("GenerateTimeout = %s", GenerateTimeout)
	}
}
