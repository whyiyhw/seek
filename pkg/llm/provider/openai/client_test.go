package openai

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

func sseBody(chunks []string) string {
	var b []byte
	for _, c := range chunks {
		b = append(b, []byte("data: "+c+"\n\n")...)
	}
	return string(b)
}

func sseServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody(chunks)))
	}))
}

func drain(ch <-chan llm.Event) []llm.Event {
	var out []llm.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func findTurnDone(t *testing.T, events []llm.Event) llm.TurnDone {
	t.Helper()
	for _, e := range events {
		if d, ok := e.(llm.TurnDone); ok {
			return d
		}
	}
	t.Fatal("no TurnDone event")
	return llm.TurnDone{}
}

// 1. Text-only stream, TurnDone has finish_reason=stop.
func TestChatStream_TextOnly(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":" world"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`[DONE]`,
	})
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(ch)

	var text string
	for _, e := range events {
		if d, ok := e.(llm.TextDelta); ok {
			text += d.Delta
		}
	}
	if text != "Hello world" {
		t.Errorf("text=%q want %q", text, "Hello world")
	}
	done := findTurnDone(t, events)
	if done.FinishReason != "stop" {
		t.Errorf("finish_reason=%q want stop", done.FinishReason)
	}
}

// 2. Single tool call assembled from streaming deltas.
func TestChatStream_ToolCall(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`,
		`[DONE]`,
	})
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(ch)

	var tcd *llm.ToolCallDone
	for _, e := range events {
		if d, ok := e.(llm.ToolCallDone); ok {
			d2 := d
			tcd = &d2
		}
	}
	if tcd == nil {
		t.Fatal("no ToolCallDone")
	}
	if tcd.ID != "c1" || tcd.Name != "get_weather" || tcd.Arguments != `{"location":"NYC"}` {
		t.Errorf("unexpected tool call: %+v", tcd)
	}
	if r := findTurnDone(t, events).FinishReason; r != "tool_calls" {
		t.Errorf("finish_reason=%q want tool_calls", r)
	}
}

// 3. Two tool calls in one turn, emitted in index order.
func TestChatStream_MultipleToolCalls(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"a","arguments":""}},{"index":1,"id":"c2","type":"function","function":{"name":"b","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}},{"index":1,"function":{"arguments":"{\"y\":2}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":10}}`,
		`[DONE]`,
	})
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var tcds []llm.ToolCallDone
	for _, e := range drain(ch) {
		if d, ok := e.(llm.ToolCallDone); ok {
			tcds = append(tcds, d)
		}
	}
	if len(tcds) != 2 {
		t.Fatalf("got %d ToolCallDone, want 2", len(tcds))
	}
	if tcds[0].ID != "c1" || tcds[0].Name != "a" || tcds[0].Arguments != `{"x":1}` {
		t.Errorf("call[0]=%+v", tcds[0])
	}
	if tcds[1].ID != "c2" || tcds[1].Name != "b" || tcds[1].Arguments != `{"y":2}` {
		t.Errorf("call[1]=%+v", tcds[1])
	}
}

// TestChatStream_SparseToolCallIndices: the `index` field is a stream
// cursor, NOT a promise that the set is contiguous 0..n-1. A gateway
// that coalesces or drops one delta (or renumbers after an internal
// retry) leaves a hole. The assembler used `if a == nil { break }`,
// which silently discarded every call AFTER the hole — nothing was
// emitted, so the turn looked like an empty completion and (pre
// c_lh6tlq) killed the session on the second-tier lanes.
func TestChatStream_SparseToolCallIndices(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"a","arguments":"{}"}}]},"finish_reason":null}]}`,
		// index 1 never arrives — the hole.
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c3","type":"function","function":{"name":"c","arguments":"{}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		`[DONE]`,
	})
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var tcds []llm.ToolCallDone
	for _, e := range drain(ch) {
		if d, ok := e.(llm.ToolCallDone); ok {
			tcds = append(tcds, d)
		}
	}
	if len(tcds) != 2 {
		t.Fatalf("got %d ToolCallDone, want 2 (a hole at index 1 must not drop index 2): %+v", len(tcds), tcds)
	}
	// Emitted in ascending index order, not map order.
	if tcds[0].ID != "c1" || tcds[1].ID != "c3" {
		t.Errorf("calls = %q,%q, want c1,c3 (ascending index order)", tcds[0].ID, tcds[1].ID)
	}
}

// 4. Cancelled context closes channel.
func TestChatStream_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := NewCompatible("k", srv.URL).ChatStream(ctx, llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	<-ch // consume the first event
	cancel()
	for range ch { // must eventually close
	}
}

// 5. Non-2xx status → error returned from ChatStream.
func TestChatStream_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	_, err := NewCompatible("bad", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

// 6. NewCompatible still returns Name()=="OpenAI".
func TestNewCompatible_Name(t *testing.T) {
	if n := NewCompatible("k", "http://x").Name(); n != "OpenAI" {
		t.Errorf("Name()=%q want OpenAI", n)
	}
}

// 7. Usage from final chunk is reflected in TurnDone token counts.
func TestChatStream_UsageInFinalChunk(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":7}}`,
		`[DONE]`,
	})
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	done := findTurnDone(t, drain(ch))
	if done.InputTokens != 42 || done.OutputTokens != 7 {
		t.Errorf("tokens=%d/%d want 42/7", done.InputTokens, done.OutputTokens)
	}
}

// TestChatStream_UsageCachedTokens pins the GLM-lane cache mapping:
// prompt_tokens_details.cached_tokens rides the final usage chunk and
// must surface on TurnDone.CachedInputTokens so runTurnLLM can split
// the prompt into hit/miss buckets. A usage block without the detail
// object yields 0 (no cache information).
func TestChatStream_UsageCachedTokens(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":60}}}`,
		`[DONE]`,
	})
	defer srv.Close()
	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(ch)
	d := findTurnDone(t, events)
	if d.InputTokens != 100 || d.OutputTokens != 7 {
		t.Fatalf("token counts = %d/%d, want 100/7", d.InputTokens, d.OutputTokens)
	}
	if d.CachedInputTokens != 60 {
		t.Errorf("CachedInputTokens = %d, want 60", d.CachedInputTokens)
	}

	// No prompt_tokens_details → 0, never a guess.
	srv2 := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":3}}`,
		`[DONE]`,
	})
	defer srv2.Close()
	ch2, err := NewCompatible("k", srv2.URL).ChatStream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	d2 := findTurnDone(t, drain(ch2))
	if d2.CachedInputTokens != 0 {
		t.Errorf("CachedInputTokens without detail object = %d, want 0", d2.CachedInputTokens)
	}
}

// recordingServer captures the headers of every request it serves, so
// the identification tests below can assert on the real wire shape.
func recordingServer(t *testing.T) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// TestChatStream_IdentifiesAsSeek pins the OpenCode Go lane's client
// contract, first half: every request carries a non-empty User-Agent
// that names seek (a generic or absent UA degrades Go-plan requests to
// per-token billing; the openai client is where all OpenAI-compatible
// lanes — openai / glm / compatible / opencode — send from, so the
// header rides them all).
func TestChatStream_IdentifiesAsSeek(t *testing.T) {
	srv, reqs := recordingServer(t)
	if _, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(*reqs))
	}
	ua := (*reqs)[0].Header.Get("User-Agent")
	if ua == "" {
		t.Error("User-Agent is empty — the OpenCode Go lane requires a self-identifying UA")
	}
	if !strings.Contains(ua, "seek") {
		t.Errorf("User-Agent = %q, want it to contain \"seek\"", ua)
	}
}

// TestChatStream_SessionHeaderStableWithinConversation pins the
// contract's second half: the same conversation's requests carry the
// same x-opencode-session value (stable per-session routing / prompt-
// cache affinity), a different conversation's carry a different one,
// and requests without a SessionID (deepseek lane, --no-save, older
// callers) send NO header — byte-identical to the pre-opencode wire
// shape, so the header can never surprise a strict gateway.
func TestChatStream_SessionHeaderStableWithinConversation(t *testing.T) {
	srv, reqs := recordingServer(t)
	c := NewCompatible("k", srv.URL)

	const convA1, convA2 = "20261117-100000-aaaa", "20261117-100000-aaaa" // two turns, one conversation
	const convB = "20261117-100001-bbbb"                                  // a second conversation
	for _, sid := range []string{convA1, convA2, convB, ""} {
		req := llm.ChatRequest{SessionID: sid}
		if _, err := c.ChatStream(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if len(*reqs) != 4 {
		t.Fatalf("server saw %d requests, want 4", len(*reqs))
	}

	h := func(i int) string { return (*reqs)[i].Header.Get("x-opencode-session") }
	if h(0) == "" || h(0) != h(1) {
		t.Errorf("same conversation across turns: header %q vs %q, want equal non-empty", h(0), h(1))
	}
	if h(0) == h(2) {
		t.Errorf("different conversations must differ: both %q", h(0))
	}
	if h(2) != convB {
		t.Errorf("header = %q, want the caller's session id %q verbatim", h(2), convB)
	}
	if h(3) != "" {
		t.Errorf("empty SessionID must send no header, got %q", h(3))
	}
}

// TestChatStream_ImagesArrayForm: an image-bearing message must reach the
// wire as array-form content — one text block plus one image_url block
// per attachment — while a text-only message keeps the plain-string form
// byte-for-byte (the OpenCode Go lane and every OpenAI-compatible lane
// depend on both halves).
func TestChatStream_ImagesArrayForm(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "sys"},
			{
				Role:    "user",
				Content: "what is this",
				Images: []llm.Image{
					{URL: "data:image/png;base64,QUJD"},
					{URL: "https://example.com/x.jpg"},
				},
			},
			{Role: "user", Content: "plain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(ch)

	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v (%s)", err, body)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %s", len(req.Messages), body)
	}
	// Text-only messages keep the string form.
	if string(req.Messages[0].Content) != `"sys"` {
		t.Errorf("system content = %s, want a plain string", req.Messages[0].Content)
	}
	if string(req.Messages[2].Content) != `"plain"` {
		t.Errorf("text-only user content = %s, want a plain string", req.Messages[2].Content)
	}
	// Image-bearing message: array form, text first.
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(req.Messages[1].Content, &parts); err != nil {
		t.Fatalf("image content is not an array: %v (%s)", err, req.Messages[1].Content)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3: %s", len(parts), req.Messages[1].Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "what is this" {
		t.Errorf("part 0 = %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Errorf("part 1 = %+v", parts[1])
	}
	if parts[2].Type != "image_url" || parts[2].ImageURL.URL != "https://example.com/x.jpg" {
		t.Errorf("part 2 = %+v", parts[2])
	}
}

// TestChatStream_ImagesOnlyNoText: an image with empty text must not emit
// an empty text block (some gateways reject it) — just the image part.
func TestChatStream_ImagesOnlyNoText(t *testing.T) {
	msgs := convertMessages([]llm.Message{{
		Role:   "user",
		Images: []llm.Image{{URL: "data:image/png;base64,QQ"}},
	}})
	parts, ok := msgs[0].Content.([]openAIContentPart)
	if !ok || len(parts) != 1 || parts[0].Type != "image_url" {
		t.Fatalf("content = %#v", msgs[0].Content)
	}
	// An all-empty image list degrades to the plain string, never an
	// empty array (a 400 on strict gateways).
	msgs = convertMessages([]llm.Message{{Role: "user", Content: "hi", Images: []llm.Image{{URL: ""}}}})
	if s, ok := msgs[0].Content.(string); !ok || s != "hi" {
		t.Fatalf("empty-URL content = %#v, want the plain string", msgs[0].Content)
	}
}

// TestChatStream_FilesArrayForm: a document attachment reaches the wire
// as a type:"file" content part — {"file":{"filename":…,"file_data":
// "data:application/pdf;base64,…"}} per OpenAI's file-inputs guide
// (PDF only) — while text-only messages keep the plain-string content
// byte-for-byte. The compatible adapter (GLM lane) shares this
// encoding; a lane that lacks the part type fails loudly at the
// gateway, which is the modelcap gate's job to prevent upstream.
func TestChatStream_FilesArrayForm(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role:    "user",
				Content: "summarize this",
				Images:  []llm.Image{{URL: "data:image/png;base64,QUJD"}},
				Files: []llm.File{
					{Filename: "report.pdf", URL: "data:application/pdf;base64,SUQ="},
					{Filename: "empty.pdf", URL: ""}, // skipped, never an empty part
				},
			},
			{Role: "user", Content: "plain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(ch)

	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v (%s)", err, body)
	}
	// Text-only message keeps the string form (prefix-cache guard).
	if string(req.Messages[1].Content) != `"plain"` {
		t.Errorf("text-only content = %s, want a plain string", req.Messages[1].Content)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
		File struct {
			Filename string `json:"filename"`
			FileData string `json:"file_data"`
		} `json:"file"`
	}
	if err := json.Unmarshal(req.Messages[0].Content, &parts); err != nil {
		t.Fatalf("file content is not an array: %v (%s)", err, req.Messages[0].Content)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3: %s", len(parts), req.Messages[0].Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "summarize this" {
		t.Errorf("part 0 = %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Errorf("part 1 = %+v", parts[1])
	}
	if parts[2].Type != "file" || parts[2].File.Filename != "report.pdf" ||
		parts[2].File.FileData != "data:application/pdf;base64,SUQ=" {
		t.Errorf("part 2 = %+v", parts[2])
	}
}

// TestChatStream_FilesOnlyNoText: a file with empty text emits just the
// file part; an all-unusable list degrades to the plain string (the
// shared never-empty-array rule).
func TestChatStream_FilesOnlyNoText(t *testing.T) {
	msgs := convertMessages([]llm.Message{{
		Role:  "user",
		Files: []llm.File{{Filename: "r.pdf", URL: "data:application/pdf;base64,SUQ="}},
	}})
	parts, ok := msgs[0].Content.([]openAIContentPart)
	if !ok || len(parts) != 1 || parts[0].Type != "file" || parts[0].File == nil {
		t.Fatalf("content = %#v", msgs[0].Content)
	}
	msgs = convertMessages([]llm.Message{{Role: "user", Content: "hi", Files: []llm.File{{Filename: "r.pdf"}}}})
	if s, ok := msgs[0].Content.(string); !ok || s != "hi" {
		t.Fatalf("empty-URL content = %#v, want the plain string", msgs[0].Content)
	}
}

// TestChatStream_TextOnlyWireBytes pins the exact request body of a
// text-only conversation — the prefix-cache contract. Adding the Files
// field must not have moved a single byte here: these goldens were
// captured from this branch's base (which predates the file-passthrough
// change) and the adapters' output was diffed against it byte-for-byte.
// Any drift means the whole prompt prefix misses cache on every lane,
// which costs ~10× the tokens of a hit.
func TestChatStream_TextOnlyWireBytes(t *testing.T) {
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
			want: `{"model":"m","messages":[{"role":"system","content":"sys"},` +
				`{"role":"user","content":"hi"}],"max_tokens":128,"stream":true,` +
				`"stream_options":{"include_usage":true}}`,
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
			want: `{"model":"m","messages":[{"role":"system","content":"sys"},` +
				`{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function",` +
				`"function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},` +
				`{"role":"tool","content":"ok","tool_call_id":"c1","name":"read"},` +
				`{"role":"user","content":"again"}],"max_tokens":128,` +
				`"tools":[{"type":"function","function":{"name":"read","description":"r",` +
				`"parameters":{"type":"object"}}}],"stream":true,` +
				`"stream_options":{"include_usage":true}}`,
		},
	}
	for _, c := range cases {
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		}))
		ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), c.req)
		if err != nil {
			srv.Close()
			t.Fatalf("%s: %v", c.name, err)
		}
		drain(ch)
		srv.Close()
		if string(body) != c.want {
			t.Errorf("%s: wire bytes drifted\n got %s\nwant %s", c.name, body, c.want)
		}
	}
}

// TestChatStream_TemperatureWire pins the temperature knob's two states
// on the OpenAI-compatible wire (card c_trrsn7):
//
//   - nil → the key is ABSENT. Byte-identity matters more than the field
//     itself: every existing caller streams this body on every turn, and
//     an added `"temperature":null` would miss the prefix cache (~10×
//     input price) for users who never asked for the knob.
//   - explicit 0 → the key is PRESENT with 0. This is the value the judge
//     lane pins, and the one a non-pointer field would lose to omitempty.
func TestChatStream_TemperatureWire(t *testing.T) {
	zero := 0.0
	half := 0.5
	for _, c := range []struct {
		name string
		req  llm.ChatRequest
		want string
	}{
		{
			name: "absent → no key",
			req:  llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: "user", Content: "hi"}}},
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,` +
				`"stream_options":{"include_usage":true}}`,
		},
		{
			name: "explicit zero → key present",
			req: llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: "user", Content: "hi"}},
				Temperature: &zero},
			// Temperature is the LAST field in openAIRequest, so a
			// non-nil value appends the key and never reorders the
			// existing body — the cache contract for every caller that
			// does not set the knob.
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,` +
				`"stream_options":{"include_usage":true},"temperature":0}`,
		},
		{
			name: "0.5 → key present",
			req: llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: "user", Content: "hi"}},
				Temperature: &half},
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,` +
				`"stream_options":{"include_usage":true},"temperature":0.5}`,
		},
	} {
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		}))
		ch, err := NewCompatible("k", srv.URL).ChatStream(context.Background(), c.req)
		if err != nil {
			srv.Close()
			t.Fatalf("%s: %v", c.name, err)
		}
		drain(ch)
		srv.Close()
		if string(body) != c.want {
			t.Errorf("%s: wire bytes drifted\n got %s\nwant %s", c.name, body, c.want)
		}
	}
}
