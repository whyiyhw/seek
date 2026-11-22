package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The projection half of the MCP passthrough: what session/new's
// mcpServers array becomes once seek decides what it can launch. Nothing
// here spawns a process — the wire shape and the launch parameters are
// the whole contract (cmd/seek owns connecting).

func TestStdioServers_ProjectsStdioAndSkipsTheRest(t *testing.T) {
	roster := []MCPServer{
		{Name: "butler", Type: "stdio", Command: "/usr/local/bin/butler-mcp",
			Args: []string{"--card", "c_1"}, Env: []MCPEnvVar{{Name: "BUTLER_TOKEN", Value: "t"}}},
		{Name: "noprefix", Command: "/bin/echo"},             // stdio historically ships no discriminant
		{Name: "remote", Type: "http", Command: "/bin/true"}, // not launchable by pkg/mcp
		{Name: "sse", Type: "sse"},                           // ditto
		{Name: "bridge", Type: "acp"},                        // ditto
		{Name: "", Type: "stdio", Command: "/bin/true"},      // no name ⇒ no usable tool prefix
		{Name: "nocmd", Type: "stdio"},                       // nothing to exec
		{Name: "emptyenv", Type: "stdio", Command: "/bin/true", Env: []MCPEnvVar{{Name: "", Value: "x"}}},
	}

	got, skipped := StdioServers(roster)

	if len(got) != 3 {
		t.Fatalf("launchable = %d (%+v), want 3", len(got), got)
	}
	if got[0].Name != "butler" || got[0].Command != "/usr/local/bin/butler-mcp" ||
		len(got[0].Args) != 2 || got[0].Args[1] != "c_1" {
		t.Fatalf("first server = %+v", got[0])
	}
	// Env is flattened to the "KEY=VALUE" shape pkg/mcp's ServerConfig
	// takes — cmd/seek stays a pass-through.
	if len(got[0].Env) != 1 || got[0].Env[0] != "BUTLER_TOKEN=t" {
		t.Fatalf("env = %v", got[0].Env)
	}
	// Declaration order is the launch order (a JSON array is ordered; no
	// map is involved), so identical rosters launch identically.
	if got[1].Name != "noprefix" || got[2].Name != "emptyenv" {
		t.Fatalf("order = %q, %q", got[1].Name, got[2].Name)
	}
	if len(got[2].Env) != 0 {
		t.Fatalf("nameless env entry must be dropped: %v", got[2].Env)
	}

	// Every skip is explained, and the unsupported types are named.
	joined := strings.Join(skipped, "\n")
	for _, want := range []string{"remote", "http", "sse", "bridge", "#5", "nocmd"} {
		if !strings.Contains(joined, want) {
			t.Errorf("skipped reasons miss %q:\n%s", want, joined)
		}
	}
	if len(skipped) != 5 {
		t.Errorf("skipped = %d, want 5:\n%s", len(skipped), joined)
	}
}

func TestStdioServers_EmptyRosterIsNil(t *testing.T) {
	got, skipped := StdioServers(nil)
	if got != nil || skipped != nil {
		t.Fatalf("empty roster = %+v %+v, want nil nil", got, skipped)
	}
}

// ToolPrefix is the card's `mcp.<server>.<tool>` shape, in the only
// spelling provider APIs accept: dots are not in
// ^[a-zA-Z0-9_-]{1,64}$ and 400 the very first request.
func TestStdioServer_ToolPrefixIsProviderSafe(t *testing.T) {
	cases := map[string]string{
		"butler":                "mcp__butler__",
		"butler.card":           "mcp__butler_card__",
		"my tools/v2":           "mcp__my_tools_v2__",
		"already_safe-1":        "mcp__already_safe-1__",
		"工具":                    "mcp______", // folded to underscores, never empty
		strings.Repeat("x", 80): "mcp__" + strings.Repeat("x", maxNameSegment) + "__",
	}
	for name, want := range cases {
		got := StdioServer{Name: name}.ToolPrefix()
		if got != want {
			t.Errorf("ToolPrefix(%q) = %q, want %q", name, got, want)
		}
		if len(got)+10 > 64 {
			t.Errorf("prefix %q leaves no room for a real tool name under the 64-char ceiling", got)
		}
	}
}

// The wire decode is what the client actually sends: an array of
// variant objects, stdio without a `type`, env as {name,value} pairs.
func TestNewSessionParams_DecodesMCPServers(t *testing.T) {
	raw := `{"cwd":"/p","mcpServers":[
		{"name":"butler","command":"/abs/butler-mcp","args":["--x"],"env":[{"name":"K","value":"v"}]},
		{"name":"remote","type":"http","url":"https://x","headers":[{"name":"A","value":"B"}]}
	]}`
	var p NewSessionParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Cwd != "/p" || len(p.MCPServers) != 2 {
		t.Fatalf("params = %+v", p)
	}
	if p.MCPServers[0].Type != "" || p.MCPServers[0].Command != "/abs/butler-mcp" ||
		len(p.MCPServers[0].Env) != 1 || p.MCPServers[0].Env[0].Name != "K" {
		t.Fatalf("stdio entry = %+v", p.MCPServers[0])
	}
	// Unknown variant fields are simply not decoded — the skipped reason
	// names the type, which is all the operator needs.
	if p.MCPServers[1].Type != "http" {
		t.Fatalf("http entry = %+v", p.MCPServers[1])
	}
}

// A roster-bearing session/new registers tools into a registry a running
// turn reads, so dispatch rejects it mid-turn; a roster-less one keeps
// its historical tolerance (acceptance ②: no behaviour change at all for
// clients that never send mcpServers).
func TestServer_NewSessionWithMCPRosterIsRejectedMidTurn(t *testing.T) {
	started := make(chan struct{})
	calls := 0
	b := fakeBackend{
		prompt: func(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
			close(started)
			<-ctx.Done()
			return PromptResult{}, ctx.Err()
		},
		newSession: func(p NewSessionParams) (NewSessionResult, error) {
			calls++
			return NewSessionResult{SessionID: "sess-1"}, nil
		},
	}
	enc, dec := startServer(t, b)
	send(t, enc, 40, "session/prompt", PromptParams{SessionID: "s"})
	<-started

	send(t, enc, 41, "session/new", NewSessionParams{
		Cwd:        "/p",
		MCPServers: []MCPServer{{Name: "butler", Command: "/abs/butler-mcp"}},
	})
	var e rpcError
	if err := json.Unmarshal(readMsg(t, dec)["error"], &e); err != nil {
		t.Fatalf("expected a rejection: %v", err)
	}
	if e.Code != -32000 || !strings.Contains(e.Message, "in flight") {
		t.Fatalf("rejection = %+v", e)
	}

	// Roster-less session/new is NOT rejected — the pre-passthrough lane
	// re-anchors mid-turn exactly as it always did.
	send(t, enc, 42, "session/new", NewSessionParams{Cwd: "/p"})
	var res NewSessionResult
	if err := json.Unmarshal(readMsg(t, dec)["result"], &res); err != nil {
		t.Fatalf("roster-less session/new must still answer: %v", err)
	}
	if res.SessionID != "sess-1" || calls != 1 {
		t.Fatalf("res = %+v, backend calls = %d", res, calls)
	}

	send(t, enc, -1, "session/cancel", map[string]string{"sessionId": "s"})
	var pr PromptResult
	json.Unmarshal(readMsg(t, dec)["result"], &pr)
	if pr.StopReason != "cancelled" {
		t.Fatalf("stop reason = %q", pr.StopReason)
	}
}

// The array reaches the backend verbatim: dispatch must not swallow it.
func TestServer_NewSessionPassesRosterToBackend(t *testing.T) {
	var got NewSessionParams
	b := fakeBackend{newSession: func(p NewSessionParams) (NewSessionResult, error) {
		got = p
		return NewSessionResult{SessionID: "sess-1"}, nil
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 43, "session/new", NewSessionParams{
		Cwd:        "/p",
		MCPServers: []MCPServer{{Name: "butler", Command: "/abs/butler-mcp", Args: []string{"a"}}},
	})
	_ = readMsg(t, dec)
	if len(got.MCPServers) != 1 || got.MCPServers[0].Name != "butler" || got.MCPServers[0].Args[0] != "a" {
		t.Fatalf("backend saw %+v", got)
	}
}
