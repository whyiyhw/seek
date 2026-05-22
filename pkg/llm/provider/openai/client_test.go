package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	<-ch   // consume the first event
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
