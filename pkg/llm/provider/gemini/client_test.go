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
			cp := e
			done = &cp
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
			cp := e
			tool = &cp
		case llm.TurnDone:
			cp := e
			done = &cp
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

// TestBuildRequest_Images: a user message with attachments emits one
// inlineData part per image after the text part; a text-only message
// keeps exactly the single text part it always had.
func TestBuildRequest_Images(t *testing.T) {
	body, err := buildRequest(llm.ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []llm.Message{
			{Role: "system", Content: "sys"},
			{
				Role:    "user",
				Content: "what is this",
				Images: []llm.Image{
					{URL: "data:image/png;base64,QUJD"},
					{URL: "https://example.com/x.jpg"}, // not inline-able → skipped
				},
			},
			{Role: "user", Content: "plain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text       string `json:"text"`
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(req.Contents) != 2 {
		t.Fatalf("contents = %d, want 2 (system rides systemInstruction)", len(req.Contents))
	}
	parts := req.Contents[0].Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %+v, want text + 1 inlineData", parts)
	}
	if parts[0].Text != "what is this" {
		t.Errorf("part 0 = %+v", parts[0])
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != "QUJD" {
		t.Errorf("part 1 = %+v", parts[1])
	}
	// Text-only message keeps the single text part, no inlineData keys.
	if len(req.Contents[1].Parts) != 1 || req.Contents[1].Parts[0].Text != "plain" ||
		req.Contents[1].Parts[0].InlineData != nil {
		t.Errorf("text-only parts = %+v", req.Contents[1].Parts)
	}
}

// TestBuildRequest_ImagesUnusableDegrades: no usable data URL → the
// message keeps its single text part.
func TestBuildRequest_ImagesUnusableDegrades(t *testing.T) {
	parts := userParts(llm.Message{Role: "user", Content: "hi", Images: []llm.Image{{URL: "https://x/y.jpg"}}})
	if len(parts) != 1 || parts[0].Text != "hi" || parts[0].InlineData != nil {
		t.Fatalf("parts = %+v", parts)
	}
}

// TestBuildRequest_Files: a document attachment rides as one more
// inlineData part — {"inlineData":{"mimeType":"application/pdf",
// "data":"…"}}, Gemini's document-understanding shape (inline PDFs,
// same part type as images). Text-only messages keep the single text
// part byte-for-byte.
func TestBuildRequest_Files(t *testing.T) {
	body, err := buildRequest(llm.ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []llm.Message{
			{
				Role:    "user",
				Content: "summarize",
				Files:   []llm.File{{Filename: "report.pdf", URL: "data:application/pdf;base64,SUQ="}},
			},
			{Role: "user", Content: "plain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text       string `json:"text"`
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	parts := req.Contents[0].Parts
	if len(parts) != 2 || parts[0].Text != "summarize" {
		t.Fatalf("parts = %+v, want text + inlineData", parts)
	}
	if parts[1].InlineData == nil ||
		parts[1].InlineData.MimeType != "application/pdf" ||
		parts[1].InlineData.Data != "SUQ=" {
		t.Fatalf("file part = %+v", parts[1])
	}
	// Text-only message keeps the single text part, no inlineData keys.
	if len(req.Contents[1].Parts) != 1 || req.Contents[1].Parts[0].Text != "plain" ||
		req.Contents[1].Parts[0].InlineData != nil {
		t.Errorf("text-only parts = %+v", req.Contents[1].Parts)
	}
}

// TestBuildRequest_TextOnlyWireBytes pins the exact JSON body of a
// text-only conversation — the prefix-cache contract. The goldens were
// captured from this branch's base (before the file-passthrough change)
// and the adapter's output was diffed against it byte-for-byte: adding
// llm.Message.Files must not move a byte on messages that carry no files.
func TestBuildRequest_TextOnlyWireBytes(t *testing.T) {
	cases := []struct {
		name string
		req  llm.ChatRequest
		want string
	}{
		{
			name: "plain two-message",
			req: llm.ChatRequest{
				Model:     "m",
				MaxTokens: 128,
				Messages: []llm.Message{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "hi"},
				},
			},
			want: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],` +
				`"systemInstruction":{"parts":[{"text":"sys"}]},` +
				`"generationConfig":{"maxOutputTokens":128}}`,
		},
		{
			name: "tool-call history",
			req: llm.ChatRequest{
				Model:     "m",
				MaxTokens: 128,
				Messages: []llm.Message{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "hi"},
					{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"a"}`}}},
					{Role: "tool", ToolCallID: "c1", ToolName: "read", Content: "ok"},
					{Role: "user", Content: "again"},
				},
				Tools: []llm.ToolDef{{Name: "read", Description: "r", Schema: []byte(`{"type":"object"}`)}},
			},
			want: `{"contents":[{"role":"user","parts":[{"text":"hi"}]},` +
				`{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]},` +
				`{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"content":"ok"}}}]},` +
				`{"role":"user","parts":[{"text":"again"}]}],` +
				`"tools":[{"functionDeclarations":[{"name":"read","description":"r","parameters":{"type":"object"}}]}],` +
				`"systemInstruction":{"parts":[{"text":"sys"}]},` +
				`"generationConfig":{"maxOutputTokens":128}}`,
		},
	}
	for _, c := range cases {
		b, err := buildRequest(c.req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(b) != c.want {
			t.Errorf("%s: wire bytes drifted\n got %s\nwant %s", c.name, b, c.want)
		}
	}
}
