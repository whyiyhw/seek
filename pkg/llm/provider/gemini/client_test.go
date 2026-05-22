package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/llm"
)

// sseServer returns a test server that replies with a canned SSE body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
}

func drain(ch <-chan llm.Event) []llm.Event {
	var evs []llm.Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestName(t *testing.T) {
	if got := New("k").Name(); got != "Gemini" {
		t.Errorf("Name() = %q, want Gemini", got)
	}
}

func TestChatStream_TextOnly(t *testing.T) {
	body := "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}` + "\n\n" +
		"data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}` + "\n\n"

	srv := sseServer(t, body)
	defer srv.Close()

	ch, err := newWithBase("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{
		Model: "gemini-pro", Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var text string
	var done *llm.TurnDone
	for _, ev := range drain(ch) {
		switch e := ev.(type) {
		case llm.TextDelta:
			text += e.Delta
		case llm.TurnDone:
			cp := e; done = &cp
		case llm.ErrorEvent:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}

	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}
	if done == nil {
		t.Fatal("no TurnDone")
	}
	if done.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", done.FinishReason)
	}
	if done.InputTokens != 10 || done.OutputTokens != 5 {
		t.Errorf("tokens = (%d, %d), want (10, 5)", done.InputTokens, done.OutputTokens)
	}
}

func TestChatStream_FunctionCall(t *testing.T) {
	body := "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"/foo"}}}]},"finishReason":"STOP"}]}` + "\n\n"

	srv := sseServer(t, body)
	defer srv.Close()

	ch, err := newWithBase("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{
		Model: "gemini-pro", Messages: []llm.Message{{Role: "user", Content: "read file"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var tool *llm.ToolCallDone
	var done *llm.TurnDone
	for _, ev := range drain(ch) {
		switch e := ev.(type) {
		case llm.ToolCallDone:
			cp := e; tool = &cp
		case llm.TurnDone:
			cp := e; done = &cp
		case llm.ErrorEvent:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}

	if tool == nil {
		t.Fatal("no ToolCallDone")
	}
	if tool.ID != "gemini_0" {
		t.Errorf("ID = %q, want gemini_0", tool.ID)
	}
	if tool.Name != "read" {
		t.Errorf("Name = %q, want read", tool.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tool.Arguments), &args); err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if args["path"] != "/foo" {
		t.Errorf("args[path] = %v, want /foo", args["path"])
	}
	if done == nil {
		t.Fatal("no TurnDone")
	}
	if done.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", done.FinishReason)
	}
}

func TestChatStream_SystemPromptExtracted(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: "+`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	defer srv.Close()

	ch, err := newWithBase("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{
		Model: "gemini-pro",
		Messages: []llm.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}

	var gr geminiRequest
	if err := json.Unmarshal(captured, &gr); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if gr.SystemInstruction == nil {
		t.Fatal("systemInstruction is nil")
	}
	if len(gr.SystemInstruction.Parts) == 0 || gr.SystemInstruction.Parts[0].Text != "You are helpful." {
		t.Errorf("systemInstruction text = %q, want %q",
			gr.SystemInstruction.Parts[0].Text, "You are helpful.")
	}
	for _, c := range gr.Contents {
		for _, p := range c.Parts {
			if p.Text == "You are helpful." {
				t.Error("system message leaked into contents")
			}
		}
	}
}

func TestChatStream_ContextCancelled(t *testing.T) {
	connected := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(connected)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := newWithBase("k", srv.URL).ChatStream(ctx, llm.ChatRequest{
		Model: "gemini-pro", Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	<-connected
	cancel()
	for range ch { // must close, not hang
	}
}

func TestChatStream_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"API key invalid"}}`)
	}))
	defer srv.Close()

	_, err := newWithBase("bad", srv.URL).ChatStream(context.Background(), llm.ChatRequest{
		Model: "gemini-pro", Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want mention of 403", err.Error())
	}
}
