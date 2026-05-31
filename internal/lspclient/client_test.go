package lspclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

// --- mock LSP server scaffolding ------------------------------------

// newTestClient wires a Client to an in-process mock server over two
// pipes. serve runs the mock: it reads framed client→server messages and
// writes framed server→client ones. out is a *io.PipeWriter so a mock can
// Close() it to simulate a crash.
func newTestClient(t *testing.T, serve func(in *bufio.Reader, out *io.PipeWriter)) *Client {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	c := newClient(s2cR, c2sW)
	go serve(bufio.NewReader(c2sR), s2cW)
	t.Cleanup(func() {
		_ = c.Close()
		_ = c2sW.Close()
		_ = s2cW.Close()
		_ = c2sR.Close()
		_ = s2cR.Close()
	})
	return c
}

func mockReadReq(in *bufio.Reader) (incoming, bool) {
	body, err := readMessage(in)
	if err != nil {
		return incoming{}, false
	}
	var m incoming
	if err := json.Unmarshal(body, &m); err != nil {
		return incoming{}, false
	}
	return m, true
}

func mockResult(out io.Writer, id *json.RawMessage, result any) {
	r, _ := json.Marshal(result)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(r)})
	_ = writeMessage(out, body)
}

func mockNotify(out io.Writer, method string, params any) {
	p, _ := json.Marshal(params)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": json.RawMessage(p)})
	_ = writeMessage(out, body)
}

func mockServerRequest(out io.Writer, id int64, method string) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage("{}")})
	_ = writeMessage(out, body)
}

// serveStd handles initialize + references; notifications (initialized,
// didOpen) are read and ignored.
func serveStd(refs []Location) func(*bufio.Reader, *io.PipeWriter) {
	return func(in *bufio.Reader, out *io.PipeWriter) {
		for {
			m, ok := mockReadReq(in)
			if !ok {
				return
			}
			switch m.Method {
			case "initialize":
				mockResult(out, m.ID, map[string]any{"capabilities": map[string]any{}})
			case "shutdown":
				mockResult(out, m.ID, nil)
			case "textDocument/references":
				mockResult(out, m.ID, refs)
				// "initialized" / "exit" / "textDocument/didOpen": notifications, ignored
			}
		}
	}
}

// --- tests ----------------------------------------------------------

func TestClient_InitHandshake(t *testing.T) {
	c := newTestClient(t, serveStd(nil))
	if err := c.Initialize(context.Background(), "file:///proj"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

func TestClient_References_RoundTrip(t *testing.T) {
	want := []Location{{
		URI:   "file:///a.go",
		Range: Range{Start: Position{Line: 10, Character: 4}, End: Position{Line: 10, Character: 7}},
	}}
	c := newTestClient(t, serveStd(want))
	ctx := context.Background()
	if err := c.Initialize(ctx, "file:///proj"); err != nil {
		t.Fatal(err)
	}
	if err := c.DidOpen(ctx, "file:///a.go", "go", "package a\n"); err != nil {
		t.Fatal(err)
	}
	got, err := c.References(ctx, "file:///a.go", Position{Line: 10, Character: 4}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("References = %+v, want %+v", got, want)
	}
}

func TestClient_References_NullResult(t *testing.T) {
	// gopls returns null (not []) when there are no references.
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		for {
			m, ok := mockReadReq(in)
			if !ok {
				return
			}
			if m.Method == "textDocument/references" {
				_ = writeMessage(out, []byte(`{"jsonrpc":"2.0","id":`+string(*m.ID)+`,"result":null}`))
			}
		}
	}
	c := newTestClient(t, serve)
	got, err := c.References(context.Background(), "file:///a.go", Position{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("null result should decode to nil, got %+v", got)
	}
}

// A server notification arriving before the response must not corrupt the
// call — the read loop drops it and still delivers the response.
func TestClient_NotificationDropped(t *testing.T) {
	want := []Location{{URI: "file:///a.go"}}
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		for {
			m, ok := mockReadReq(in)
			if !ok {
				return
			}
			if m.Method == "textDocument/references" {
				mockNotify(out, "window/logMessage", map[string]any{"type": 3, "message": "indexing…"})
				mockNotify(out, "$/progress", map[string]any{"token": "x"})
				mockResult(out, m.ID, want)
			}
		}
	}
	c := newTestClient(t, serve)
	got, err := c.References(context.Background(), "file:///a.go", Position{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v (notifications must not corrupt the call)", got, want)
	}
}

// Two concurrent calls; the mock answers in REVERSE order. Each call must
// still receive its own response — that's the id-demux working.
func TestClient_RequestCorrelation(t *testing.T) {
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		m1, ok := mockReadReq(in)
		if !ok {
			return
		}
		m2, ok := mockReadReq(in)
		if !ok {
			return
		}
		// reverse-order responses
		mockResult(out, m2.ID, "r-"+m2.Method)
		mockResult(out, m1.ID, "r-"+m1.Method)
	}
	c := newTestClient(t, serve)

	var mu sync.Mutex
	results := map[string]string{}
	var wg sync.WaitGroup
	for _, method := range []string{"foo", "bar"} {
		wg.Add(1)
		go func(method string) {
			defer wg.Done()
			raw, err := c.Call(context.Background(), method, map[string]any{})
			if err != nil {
				t.Errorf("%s: %v", method, err)
				return
			}
			var s string
			_ = json.Unmarshal(raw, &s)
			mu.Lock()
			results[method] = s
			mu.Unlock()
		}(method)
	}
	wg.Wait()
	if results["foo"] != "r-foo" || results["bar"] != "r-bar" {
		t.Fatalf("correlation failed: %v", results)
	}
}

func TestClient_Timeout(t *testing.T) {
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		for {
			if _, ok := mockReadReq(in); !ok { // read but never respond
				return
			}
		}
	}
	c := newTestClient(t, serve)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Call(ctx, "textDocument/references", map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestClient_CtxCancel(t *testing.T) {
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		for {
			if _, ok := mockReadReq(in); !ok {
				return
			}
		}
	}
	c := newTestClient(t, serve)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := c.Call(ctx, "m", map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
}

// Server dies mid-flight: the in-flight call and all subsequent calls must
// fail (not hang).
func TestClient_ServerCrash(t *testing.T) {
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		_, _ = mockReadReq(in) // read the call, then crash
		_ = out.Close()
	}
	c := newTestClient(t, serve)
	if _, err := c.Call(context.Background(), "m", map[string]any{}); err == nil {
		t.Fatal("in-flight call should fail when server crashes")
	}
	if _, err := c.Call(context.Background(), "m2", map[string]any{}); err == nil {
		t.Fatal("call after crash should fail fast, not hang")
	}
}

// A server→client request (e.g. client/registerCapability) must get a
// reply, or the server can block. We answer MethodNotFound.
func TestClient_ServerRequestGetsReply(t *testing.T) {
	gotReply := make(chan incoming, 1)
	serve := func(in *bufio.Reader, out *io.PipeWriter) {
		mockServerRequest(out, 999, "client/registerCapability")
		m, ok := mockReadReq(in)
		if ok {
			gotReply <- m
		}
	}
	newTestClient(t, serve) // read loop auto-replies; no Call needed
	select {
	case reply := <-gotReply:
		if reply.Error == nil {
			t.Fatalf("expected error reply to server request, got %+v", reply)
		}
		if reply.Error.Code != -32601 {
			t.Fatalf("error code = %d, want -32601 (MethodNotFound)", reply.Error.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client never replied to the server→client request (would hang a real server)")
	}
}
