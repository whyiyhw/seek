package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// singleTurnBackend returns a fake DeepSeek server that responds to one chat
// request with a text reply.
func singleTurnBackend(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"` + reply + `"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":5}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
}

// makeAgent builds a minimal agent wired to the given fake backend.
func makeAgent(t *testing.T, backendURL string) *agent.Agent {
	t.Helper()
	ag, err := agent.New(agent.Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(backendURL)),
		Model:  deepseek.ModelChat,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return ag
}

// serve runs the server with input from the given string and collects all
// output lines as parsed rpcResponse / rpcNotify objects (raw map[string]any).
func serve(t *testing.T, srv *Server, input string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	ctx := context.Background()
	if err := srv.Serve(ctx, strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var lines []map[string]any
	dec := json.NewDecoder(&out)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode output: %v", err)
		}
		lines = append(lines, m)
	}
	return lines
}

// TestServerInfo verifies that agent/info returns the configured model and yolo.
func TestServerInfo(t *testing.T) {
	// agent/info doesn't call the backend, so pass a dummy URL.
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fakeSrv.Close()

	srv := New(makeAgent(t, fakeSrv.URL), cache.New(), nil, nil, "deepseek-chat", false)
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":1,"method":"agent/info"}`+"\n")

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	got := lines[0]
	if got["id"] != float64(1) {
		t.Errorf("id = %v, want 1", got["id"])
	}
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %v", got["result"])
	}
	if result["model"] != "deepseek-chat" {
		t.Errorf("model = %v, want deepseek-chat", result["model"])
	}
	if result["yolo"] != false {
		t.Errorf("yolo = %v, want false", result["yolo"])
	}
	if result["version"] == "" {
		t.Error("version should be non-empty")
	}
}

// TestServerUnknownMethod verifies that an unknown method returns a -32601 error.
func TestServerUnknownMethod(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fakeSrv.Close()

	srv := New(makeAgent(t, fakeSrv.URL), cache.New(), nil, nil, "m", false)
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":2,"method":"no/such/method"}`+"\n")

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	errObj, ok := lines[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field, got: %v", lines[0])
	}
	if errObj["code"] != float64(codeMethodNotFound) {
		t.Errorf("code = %v, want %d", errObj["code"], codeMethodNotFound)
	}
}

// TestServerPromptEmptyText verifies that agent/prompt rejects an empty text.
func TestServerPromptEmptyText(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fakeSrv.Close()

	srv := New(makeAgent(t, fakeSrv.URL), cache.New(), nil, nil, "m", false)
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":3,"method":"agent/prompt","params":{"text":""}}`+"\n")

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	errObj, ok := lines[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field, got: %v", lines[0])
	}
	if errObj["code"] != float64(codeInvalidParams) {
		t.Errorf("code = %v, want %d", errObj["code"], codeInvalidParams)
	}
}

// TestServerSessionListEmpty verifies that session/list returns an empty
// array when no store is provided.
func TestServerSessionListEmpty(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fakeSrv.Close()

	srv := New(makeAgent(t, fakeSrv.URL), cache.New(), nil, nil, "m", false)
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":4,"method":"session/list"}`+"\n")

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	result, ok := lines[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got: %v", lines[0])
	}
	sessions, ok := result["sessions"].([]any)
	if !ok {
		t.Fatalf("expected sessions array, got: %v", result["sessions"])
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

// TestServerPromptStreaming verifies that agent/prompt emits turn_start,
// text_delta, turn_end notifications and a final result.
func TestServerPromptStreaming(t *testing.T) {
	backend := singleTurnBackend(t, "hello world")
	defer backend.Close()

	srv := New(makeAgent(t, backend.URL), cache.New(), nil, nil, "deepseek-chat", false)
	lines := serve(t, srv,
		`{"jsonrpc":"2.0","id":5,"method":"agent/prompt","params":{"text":"say hello"}}`+"\n",
	)

	// Expect at least: turn_start, text_delta(s), turn_end, final result.
	if len(lines) < 3 {
		t.Fatalf("expected ≥3 lines, got %d: %v", len(lines), lines)
	}

	// Last line must be the final result (has "id" field).
	last := lines[len(lines)-1]
	if last["id"] != float64(5) {
		t.Errorf("last line id = %v, want 5", last["id"])
	}
	result, ok := last["result"].(map[string]any)
	if !ok {
		t.Fatalf("last line missing result: %v", last)
	}
	if result["turns"] != float64(1) {
		t.Errorf("turns = %v, want 1", result["turns"])
	}

	// There should be at least one text_delta notification.
	var sawTextDelta bool
	for _, line := range lines[:len(lines)-1] {
		params, _ := line["params"].(map[string]any)
		if params["type"] == "text_delta" {
			sawTextDelta = true
		}
	}
	if !sawTextDelta {
		t.Error("expected at least one text_delta notification")
	}
}

// TestServerParseError verifies that malformed JSON returns a -32700 error.
func TestServerParseError(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fakeSrv.Close()

	srv := New(makeAgent(t, fakeSrv.URL), cache.New(), nil, nil, "m", false)
	lines := serve(t, srv, `not json`+"\n")

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	errObj, ok := lines[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field, got: %v", lines[0])
	}
	if errObj["code"] != float64(codeParseError) {
		t.Errorf("code = %v, want %d", errObj["code"], codeParseError)
	}
}

// TestServerMultipleRequests verifies that the server handles multiple
// sequential requests in one Serve call.
func TestServerMultipleRequests(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fakeSrv.Close()

	srv := New(makeAgent(t, fakeSrv.URL), cache.New(), nil, nil, "m", false)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"agent/info"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/list"}`,
		``,
	}, "\n")

	lines := serve(t, srv, input)
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(lines))
	}
	// Both should have result fields (no errors).
	for i, line := range lines {
		if line["error"] != nil {
			t.Errorf("line %d has unexpected error: %v", i, line["error"])
		}
		if line["result"] == nil {
			t.Errorf("line %d missing result", i)
		}
	}
}
