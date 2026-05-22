package mcptool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/mcp"
)

// fakeMsg is used to decode incoming JSON-RPC messages without importing
// unexported pkg/mcp types.
type fakeMsg struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// runBridgeServer starts a goroutine acting as an MCP server whose
// tools/call behavior is controlled by the callFn callback.
//   - callFn receives the tool name and arguments and returns the
//     CallToolResult to encode, or an error (which becomes isError=true).
func runBridgeServer(
	t *testing.T,
	r io.Reader,
	w io.Writer,
	tools []mcp.ToolDef,
	callFn func(name string, args map[string]any) mcp.CallToolResult,
) {
	t.Helper()
	go func() {
		scanner := bufio.NewScanner(r)
		enc := json.NewEncoder(w)
		for scanner.Scan() {
			var msg fakeMsg
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			if msg.ID == 0 { // notification
				continue
			}
			switch msg.Method {
			case "initialize":
				raw, _ := json.Marshal(mcp.InitializeResult{
					ProtocolVersion: mcp.ProtocolVersion,
					ServerInfo:      mcp.ServerInfo{Name: "bridge-test", Version: "0"},
				})
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": json.RawMessage(raw)})
			case "tools/list":
				raw, _ := json.Marshal(map[string]any{"tools": tools})
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": json.RawMessage(raw)})
			case "tools/call":
				var p struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				json.Unmarshal(msg.Params, &p)
				result := callFn(p.Name, p.Arguments)
				raw, _ := json.Marshal(result)
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": json.RawMessage(raw)})
			}
		}
	}()
}

// newBridgeClient creates an mcp.Client backed by a fake server, and
// returns a Bridge wrapping the named tool def. The fake server's
// tools/call behavior is controlled by callFn.
func newBridgeClient(
	t *testing.T,
	def mcp.ToolDef,
	effectName string,
	callFn func(name string, args map[string]any) mcp.CallToolResult,
) *Bridge {
	t.Helper()
	srvInR, srvInW := io.Pipe()
	srvOutR, srvOutW := io.Pipe()

	runBridgeServer(t, srvInR, srvOutW, []mcp.ToolDef{def}, callFn)

	client := mcp.NewClient(srvOutR, srvInW)
	t.Cleanup(func() {
		srvInW.Close()
		srvOutW.Close()
	})

	ctx := context.Background()
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return New(client, "test-server", def, effectName)
}

// ---------- Bridge.Execute tests ----------

func TestBridge_Execute_HappyPath(t *testing.T) {
	def := mcp.ToolDef{
		Name:        "greet",
		Description: "says hello",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}
	bridge := newBridgeClient(t, def, "greet", func(_ string, args map[string]any) mcp.CallToolResult {
		name, _ := args["name"].(string)
		return mcp.CallToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "hello " + name}},
		}
	})

	got, err := bridge.Execute(context.Background(), json.RawMessage(`{"name":"world"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestBridge_Execute_BadJSON(t *testing.T) {
	def := mcp.ToolDef{
		Name:        "greet",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	bridge := newBridgeClient(t, def, "greet", func(_ string, _ map[string]any) mcp.CallToolResult {
		return mcp.CallToolResult{}
	})

	_, err := bridge.Execute(context.Background(), json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("expected error for bad JSON args, got nil")
	}
	if !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("error should mention bad arguments: %v", err)
	}
}

func TestBridge_Execute_IsError(t *testing.T) {
	def := mcp.ToolDef{
		Name:        "fail",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	bridge := newBridgeClient(t, def, "fail", func(_ string, _ map[string]any) mcp.CallToolResult {
		return mcp.CallToolResult{
			IsError: true,
			Content: []mcp.ContentBlock{{Type: "text", Text: "something went wrong"}},
		}
	})

	_, err := bridge.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for IsError=true result, got nil")
	}
	if !strings.Contains(err.Error(), "tool error") {
		t.Errorf("error should say tool error: %v", err)
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error should include server message: %v", err)
	}
}

func TestBridge_Execute_EmptyResponse(t *testing.T) {
	def := mcp.ToolDef{
		Name:        "silent",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	bridge := newBridgeClient(t, def, "silent", func(_ string, _ map[string]any) mcp.CallToolResult {
		return mcp.CallToolResult{Content: []mcp.ContentBlock{}} // no text
	})

	got, err := bridge.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "empty response") {
		t.Errorf("expected empty-response notice, got %q", got)
	}
}

func TestBridge_Execute_ContextCancelled(t *testing.T) {
	def := mcp.ToolDef{
		Name:        "slow",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	bridge := newBridgeClient(t, def, "slow", func(_ string, _ map[string]any) mcp.CallToolResult {
		return mcp.CallToolResult{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := bridge.Execute(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

func TestBridge_NameDescriptionSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	def := mcp.ToolDef{Name: "tool", Description: "does things", InputSchema: schema}
	bridge := New(nil, "srv", def, "srv__tool")

	if bridge.Name() != "srv__tool" {
		t.Errorf("Name = %q, want %q", bridge.Name(), "srv__tool")
	}
	if bridge.Description() != "does things" {
		t.Errorf("Description = %q, want %q", bridge.Description(), "does things")
	}
	if string(bridge.Schema()) != string(schema) {
		t.Errorf("Schema = %s, want %s", bridge.Schema(), schema)
	}
}

// ---------- helper function tests ----------

func TestEnvMapToSlice_Nil(t *testing.T) {
	if got := EnvMapToSlice(nil); got != nil {
		t.Errorf("EnvMapToSlice(nil) = %v, want nil", got)
	}
}

func TestEnvMapToSlice_Values(t *testing.T) {
	m := map[string]string{"FOO": "bar", "BAZ": "qux"}
	got := EnvMapToSlice(m)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	set := make(map[string]bool)
	for _, s := range got {
		set[s] = true
	}
	if !set["FOO=bar"] || !set["BAZ=qux"] {
		t.Errorf("missing entries: %v", got)
	}
}

func TestFormatErrors_Empty(t *testing.T) {
	if got := FormatErrors(nil); got != "" {
		t.Errorf("FormatErrors(nil) = %q, want empty", got)
	}
}

func TestFormatErrors_Multiple(t *testing.T) {
	errs := []error{
		fmt.Errorf("err one"),
		fmt.Errorf("err two"),
	}
	got := FormatErrors(errs)
	if !strings.Contains(got, "err one") || !strings.Contains(got, "err two") {
		t.Errorf("missing errors in output: %q", got)
	}
}
