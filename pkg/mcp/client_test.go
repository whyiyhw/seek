package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// runFakeServer simulates a minimal MCP server.
// It reads one JSON-RPC request at a time (line-delimited) and writes
// a canned response. Notifications (id == 0) are silently ignored.
func runFakeServer(t *testing.T, r io.Reader, w io.Writer, done chan<- struct{}) {
	t.Helper()
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r)
		enc := json.NewEncoder(w)
		for scanner.Scan() {
			var req request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				t.Logf("fake server: decode request: %v", err)
				continue
			}
			// Notifications have no id — skip.
			if req.ID == 0 {
				continue
			}
			var result any
			switch req.Method {
			case "initialize":
				result = InitializeResult{
					ProtocolVersion: ProtocolVersion,
					Capabilities:    serverCapabilities{},
					ServerInfo:      ServerInfo{Name: "test-server", Version: "0.1"},
				}
			case "tools/list":
				result = listToolsResult{
					Tools: []ToolDef{
						{
							Name:        "echo",
							Description: "Echo the input back",
							InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
						},
					},
				}
			case "tools/call":
				var p callToolParams
				if err := json.Unmarshal(req.Params, &p); err != nil {
					t.Logf("fake server: decode call params: %v", err)
				}
				msg, _ := p.Arguments["msg"].(string)
				result = CallToolResult{
					Content: []ContentBlock{{Type: "text", Text: "echo: " + msg}},
				}
			default:
				result = map[string]string{}
			}
			raw, _ := json.Marshal(result)
			if err := enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: raw}); err != nil {
				t.Logf("fake server: write response: %v", err)
				return
			}
		}
	}()
}

// newPipedPair returns a Client connected to a fake server via in-process pipes.
func newPipedPair(t *testing.T) (*Client, chan struct{}) {
	t.Helper()
	// client writes to serverIn; server reads from serverIn.
	serverInR, serverInW := io.Pipe()
	// server writes to serverOut; client reads from serverOut.
	serverOutR, serverOutW := io.Pipe()

	done := make(chan struct{})
	runFakeServer(t, serverInR, serverOutW, done)

	client := NewClient(serverOutR, serverInW)

	t.Cleanup(func() {
		serverInW.Close()
		serverOutW.Close()
		<-done
	})
	return client, done
}

func TestClient_Initialize(t *testing.T) {
	client, _ := newPipedPair(t)
	ctx := context.Background()

	result, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.ServerInfo.Name != "test-server" {
		t.Errorf("ServerInfo.Name = %q, want %q", result.ServerInfo.Name, "test-server")
	}
	if result.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", result.ProtocolVersion, ProtocolVersion)
	}
}

func TestClient_ListTools(t *testing.T) {
	client, _ := newPipedPair(t)
	ctx := context.Background()

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "echo")
	}
}

func TestClient_CallTool(t *testing.T) {
	client, _ := newPipedPair(t)
	ctx := context.Background()

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	result, err := client.CallTool(ctx, "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	got := result.TextContent()
	if got != "echo: hello" {
		t.Errorf("TextContent = %q, want %q", got, "echo: hello")
	}
}

func TestClient_CallTool_NilArgs(t *testing.T) {
	client, _ := newPipedPair(t)
	ctx := context.Background()

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	result, err := client.CallTool(ctx, "echo", nil)
	if err != nil {
		t.Fatalf("CallTool(nil args): %v", err)
	}
	// msg not provided — server returns "echo: "
	if !strings.HasPrefix(result.TextContent(), "echo:") {
		t.Errorf("unexpected result: %q", result.TextContent())
	}
}

func TestClient_ContextCancelled(t *testing.T) {
	client, _ := newPipedPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := client.Initialize(ctx)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

func TestClient_TextContent_MultiBlock(t *testing.T) {
	r := &CallToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "line1"},
			{Type: "image", Text: "ignored"},
			{Type: "text", Text: "line2"},
		},
	}
	got := r.TextContent()
	want := "line1\nline2"
	if got != want {
		t.Errorf("TextContent = %q, want %q", got, want)
	}
}

func TestClient_TextContent_Empty(t *testing.T) {
	r := &CallToolResult{}
	if got := r.TextContent(); got != "" {
		t.Errorf("TextContent on empty = %q, want empty", got)
	}
}

// ---------- failure-path tests ----------

// newErrorServer creates a client whose fake server returns an RPC error
// for every tools/call request and a normal response for everything else.
func newErrorServer(t *testing.T) *Client {
	t.Helper()
	serverInR, serverInW := io.Pipe()
	serverOutR, serverOutW := io.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(serverInR)
		enc := json.NewEncoder(serverOutW)
		for scanner.Scan() {
			var req request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.ID == 0 {
				continue
			}
			switch req.Method {
			case "initialize":
				raw, _ := json.Marshal(InitializeResult{
					ProtocolVersion: ProtocolVersion,
					ServerInfo:      ServerInfo{Name: "err-server", Version: "0.1"},
				})
				enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			case "tools/list":
				raw, _ := json.Marshal(listToolsResult{Tools: []ToolDef{}})
				enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			default:
				enc.Encode(response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &rpcError{Code: -32603, Message: "internal error"},
				})
			}
		}
	}()

	c := NewClient(serverOutR, serverInW)
	t.Cleanup(func() {
		serverInW.Close()
		serverOutW.Close()
		<-done
	})
	return c
}

func TestClient_RPCError(t *testing.T) {
	client := newErrorServer(t)
	ctx := context.Background()

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.CallTool(ctx, "any", nil)
	if err == nil {
		t.Fatal("expected error from server RPC error response, got nil")
	}
	if !strings.Contains(err.Error(), "internal error") {
		t.Errorf("error message should contain server text, got: %v", err)
	}
}

func TestClient_MalformedResponse(t *testing.T) {
	// Directly plumb a reader that produces invalid JSON after the
	// initialize exchange, so the decoder returns an error.
	serverInR, serverInW := io.Pipe()
	serverOutR, serverOutW := io.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(serverInR)
		enc := json.NewEncoder(serverOutW)
		first := true
		for scanner.Scan() {
			var req request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.ID == 0 {
				continue
			}
			if first {
				// Respond to initialize normally.
				first = false
				raw, _ := json.Marshal(InitializeResult{
					ProtocolVersion: ProtocolVersion,
					ServerInfo:      ServerInfo{Name: "broken", Version: "0.1"},
				})
				enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: raw})
				continue
			}
			// Second request → send garbage.
			serverOutW.Write([]byte("not valid json\n"))
		}
	}()

	client := NewClient(serverOutR, serverInW)
	t.Cleanup(func() {
		serverInW.Close()
		serverOutW.Close()
		<-done
	})

	ctx := context.Background()
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := client.ListTools(ctx)
	if err == nil {
		t.Fatal("expected error decoding malformed response, got nil")
	}
}

func TestClient_Concurrent(t *testing.T) {
	// Multiple goroutines calling the same client must not race.
	// Calls are serialised by the mutex; the fake server handles them
	// one at a time.
	client, _ := newPipedPair(t)
	ctx := context.Background()

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	const workers = 5
	errc := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			result, err := client.CallTool(ctx, "echo", map[string]any{"msg": "hi"})
			if err != nil {
				errc <- err
				return
			}
			if result.TextContent() != "echo: hi" {
				errc <- fmt.Errorf("unexpected result: %q", result.TextContent())
				return
			}
			errc <- nil
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errc; err != nil {
			t.Errorf("worker error: %v", err)
		}
	}
}
