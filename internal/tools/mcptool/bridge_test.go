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

// ---------- LoadServers tests ----------

// fakeConnect returns a connectFn serving in-process fake MCP servers
// keyed by cfg.Command: a Command present in toolsByCommand serves that
// tool list; any other Command fails to start (simulating a broken
// server). Pipe cleanup is tied to t.
func fakeConnect(t *testing.T, toolsByCommand map[string][]mcp.ToolDef) connectFn {
	t.Helper()
	return func(_ context.Context, cfg ServerConfig) (*mcp.Client, error) {
		defs, ok := toolsByCommand[cfg.Command]
		if !ok {
			return nil, fmt.Errorf("start %q: command not found", cfg.Command)
		}
		srvInR, srvInW := io.Pipe()
		srvOutR, srvOutW := io.Pipe()
		runBridgeServer(t, srvInR, srvOutW, defs, func(_ string, _ map[string]any) mcp.CallToolResult {
			return mcp.CallToolResult{}
		})
		t.Cleanup(func() {
			srvInW.Close()
			srvOutW.Close()
		})
		return mcp.NewClient(srvOutR, srvInW), nil
	}
}

// rosterOf renders a LoadResult as the byte-exact registration roster:
// tool names in registration order, then error strings. This is the
// "what two startups must agree on byte-for-byte" contract.
func rosterOf(lr LoadResult) string {
	var b strings.Builder
	for _, br := range lr.Bridges {
		b.WriteString(br.Name())
		b.WriteString("\n")
	}
	for _, e := range lr.Errors {
		b.WriteString(e.Error())
		b.WriteString("\n")
	}
	return b.String()
}

// freshExisting copies an initial name set so each run starts from the
// same state (LoadServers mutates the map it is given).
func freshExisting(initial map[string]bool) map[string]bool {
	m := make(map[string]bool, len(initial))
	for k, v := range initial {
		m[k] = v
	}
	return m
}

func TestLoadServers_PrefixPolicy(t *testing.T) {
	toolsByCommand := map[string][]mcp.ToolDef{
		"ok:semble": {
			{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "find_related", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		"ok:legacy": {
			{Name: "free_tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, // collides with built-in
		},
	}
	servers := map[string]ServerConfig{
		"semble": {Command: "ok:semble"},
		"legacy": {Command: "ok:legacy", NoPrefix: true},
	}
	// "read" simulates an already-registered built-in tool.
	existing := freshExisting(map[string]bool{"read": true})

	lr := loadServers(context.Background(), servers, existing, fakeConnect(t, toolsByCommand))

	if len(lr.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", lr.Errors)
	}
	// Lexicographic server order (legacy < semble); within a server the
	// server's own tool order. Default = "<server>__<tool>"; NoPrefix
	// keeps bare names except when taken ("read" → "legacy__read").
	want := "free_tool\nlegacy__read\nsemble__search\nsemble__find_related\n"
	if got := rosterOf(lr); got != want {
		t.Errorf("roster:\ngot  %q\nwant %q", got, want)
	}
}

// TestLoadServers_DeterministicRoster is the D1 acceptance lock: two
// loads of the same config must produce a byte-identical roster —
// registration names, their order, and the error list.
func TestLoadServers_DeterministicRoster(t *testing.T) {
	toolsByCommand := map[string][]mcp.ToolDef{
		"ok:alpha": {
			{Name: "shared", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "unique_a", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		"ok:mid": {
			{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, // collides with built-in
			{Name: "free_bare", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		"ok:zeta": {
			{Name: "shared", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "unique_z", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		// "boom" is deliberately absent → connect fails for that server.
	}
	newServers := func() map[string]ServerConfig {
		return map[string]ServerConfig{
			"zeta":   {Command: "ok:zeta"},
			"alpha":  {Command: "ok:alpha"},
			"mid":    {Command: "ok:mid", NoPrefix: true},
			"broken": {Command: "boom"},
		}
	}
	initialExisting := map[string]bool{"read": true}

	run := func() string {
		lr := loadServers(context.Background(), newServers(), freshExisting(initialExisting), fakeConnect(t, toolsByCommand))
		return rosterOf(lr)
	}

	first, second := run(), run()
	if first != second {
		t.Errorf("same config, different rosters:\nfirst  %q\nsecond %q", first, second)
	}
	// And the roster is exactly this: servers in lexicographic name
	// order (alpha, broken, mid, zeta; broken contributes only an
	// error line, listed after the bridges), mid's bare "read" falls
	// back to the prefixed form because "read" is already registered.
	want := "alpha__shared\nalpha__unique_a\nmid__read\nfree_bare\nzeta__shared\nzeta__unique_z\n" +
		`mcp server "broken": start "boom": command not found` + "\n"
	if first != want {
		t.Errorf("roster:\ngot  %q\nwant %q", first, want)
	}
}

func TestLoadServers_Empty(t *testing.T) {
	lr := loadServers(context.Background(), nil, nil, fakeConnect(t, nil))
	if len(lr.Bridges) != 0 || len(lr.Errors) != 0 {
		t.Errorf("empty config should be a no-op, got %+v", lr)
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

// ---------- NamePrefix / safeToolName (ACP passthrough) ----------

// TestLoadServers_NamePrefix pins the ACP lane's namespace: the same
// roster registered under "mcp__<server>__" gets the three-segment form,
// with the server name AND the tool name folded into the provider
// alphabet (MCP permits dots; ^[a-zA-Z0-9_-]{1,64}$ does not, and a
// rejected name kills the session's first request).
func TestLoadServers_NamePrefix(t *testing.T) {
	toolsByCommand := map[string][]mcp.ToolDef{
		"ok:butler": {
			{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "card.read", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	servers := map[string]ServerConfig{
		"butler.card": {Command: "ok:butler", NamePrefix: "mcp__butler_card__"},
	}
	lr := loadServers(context.Background(), servers, freshExisting(nil), fakeConnect(t, toolsByCommand))
	if len(lr.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", lr.Errors)
	}
	want := "mcp__butler_card__echo\nmcp__butler_card__card_read\n"
	if got := rosterOf(lr); got != want {
		t.Errorf("roster:\ngot  %q\nwant %q", got, want)
	}
}

// The default path must stay byte-identical: mcp.json rosters are a
// shipped contract, so a dotted tool name registered the default way
// keeps its dot (whatever the provider does with it is not this change's
// business).
func TestLoadServers_DefaultPrefixIsUnfolded(t *testing.T) {
	toolsByCommand := map[string][]mcp.ToolDef{
		"ok:x": {{Name: "card.read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	lr := loadServers(context.Background(), map[string]ServerConfig{"x": {Command: "ok:x"}},
		freshExisting(nil), fakeConnect(t, toolsByCommand))
	if got, want := rosterOf(lr), "x__card.read\n"; got != want {
		t.Errorf("default roster = %q, want %q (unchanged bytes)", got, want)
	}
}

func TestSafeToolName(t *testing.T) {
	cases := map[string]string{
		"mcp__srv__ok":          "mcp__srv__ok",
		"mcp__srv__card.read":   "mcp__srv__card_read",
		"mcp__my.tools__a/b c":  "mcp__my_tools__a_b_c",
		"工具":                    "__",
		strings.Repeat("a", 70): strings.Repeat("a", maxToolName),
	}
	for in, want := range cases {
		if got := safeToolName(in); got != want {
			t.Errorf("safeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

// LoadResult.Close must reap the clients it reports and keep going after
// a failure — one wedged server cannot leave its neighbours running.
func TestLoadResult_CloseReapsEveryClientAndIsIdempotentOnZero(t *testing.T) {
	// Clients built over pipes have no subprocess: Close is a no-op that
	// must not panic, which is the shape every fake in this suite uses.
	c1 := mcp.NewClient(strings.NewReader(""), io.Discard)
	c2 := mcp.NewClient(strings.NewReader(""), io.Discard)
	lr := LoadResult{Clients: []*mcp.Client{c1, nil, c2}}
	if err := lr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := (LoadResult{}).Close(); err != nil {
		t.Fatalf("empty Close: %v", err)
	}
}
