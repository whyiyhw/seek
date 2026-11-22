package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/tools"
)

// stubMCPMain is the child half of the passthrough tests: this same test
// binary, re-entered with -acp-mcp-stub, speaks MCP over stdio exactly
// like a third-party server would. Using the test binary (rather than a
// checked-in fake script) keeps the fixture in-process-source-only and
// portable to Windows CI.
//
// Env: ACP_MCP_STUB_PIDFILE — where to record this process's pid, so the
// parent can prove the child is gone after teardown.
func stubMCPMain() {
	if pf := os.Getenv("ACP_MCP_STUB_PIDFILE"); pf != "" {
		_ = os.WriteFile(pf, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // notification (notifications/initialized)
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "acp-mcp-stub", "version": "0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{
					"name":        "echo",
					"description": "echo back the card id",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"card": map[string]any{"type": "string"}}},
				},
				{
					"name":        "card.read",
					"description": "a dotted tool name — legal in MCP, must be registered verbatim",
					"inputSchema": map[string]any{"type": "object"},
				},
			}}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			card, _ := p.Arguments["card"].(string)
			result = map[string]any{"content": []map[string]any{
				{"type": "text", "text": "stub:" + p.Name + ":" + card},
			}}
		default:
			result = map[string]any{}
		}
		raw, _ := json.Marshal(result)
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw)})
	}
}

// stubRoster points one StdioServer at the test binary's stub mode.
func stubRoster(t *testing.T, name string) (acp.StdioServer, string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	return acp.StdioServer{
		Name:    name,
		Command: os.Args[0],
		Args:    []string{"-acp-mcp-stub"},
		Env:     []string{"ACP_MCP_STUB_PIDFILE=" + pidFile},
	}, pidFile
}

func waitForPid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && len(b) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stub never wrote %s", pidFile)
	return 0
}

// processAlive reports whether pid is still running, plus whether the
// platform can answer at all. Windows cannot: os.Process.Signal supports
// only Kill there, so a liveness probe has no portable spelling (CI runs
// Windows) — known=false means "don't assert on the pid", which is why
// every caller guards on it, and why the POSIX half of the same assertion
// also lives in scripts/acp-smoke-test.sh (kill -0).
func processAlive(pid int) (alive, known bool) {
	if runtime.GOOS == "windows" {
		return false, false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false, true
	}
	return p.Signal(syscall.Signal(0)) == nil, true
}

// Acceptance ①: a declared roster is connected through the SAME stack
// mcp.json uses, and its tools land in the live registry under
// mcp__<server>__<tool> — which is what the model sees in the tools array.
func TestACPMCP_DeclaredRosterReachesTheToolSurface(t *testing.T) {
	srv, pidFile := stubRoster(t, "butler")

	reg := tools.New()
	b := &acpBackend{reg: reg}
	b.connectMCP([]acp.MCPServer{{
		Name:    srv.Name,
		Type:    "stdio",
		Command: srv.Command,
		Args:    srv.Args,
		Env:     []acp.MCPEnvVar{{Name: "ACP_MCP_STUB_PIDFILE", Value: strings.TrimPrefix(srv.Env[0], "ACP_MCP_STUB_PIDFILE=")}},
	}})
	if b.mcp == nil {
		t.Fatal("declared roster connected nothing")
	}
	t.Cleanup(func() { _ = b.closeMCP() })

	pid := waitForPid(t, pidFile)

	names := reg.Names()
	want := []string{"mcp__butler__card_read", "mcp__butler__echo"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("registry names = %v, want %v", names, want)
	}
	// The wire list (what the model is offered) carries the same names —
	// that is the "进会话工具面" half of the acceptance.
	var wire []string
	for _, tl := range reg.Wire() {
		wire = append(wire, tl.Function.Name)
	}
	if strings.Join(wire, ",") != strings.Join(want, ",") {
		t.Fatalf("wire tools = %v, want %v", wire, want)
	}

	// And the bridge really talks to the child: one call round-trips.
	out, err := reg.Dispatch(context.Background(), "mcp__butler__echo", json.RawMessage(`{"card":"c_oyt6ft"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out != "stub:echo:c_oyt6ft" {
		t.Fatalf("tool output = %q", out)
	}
	if alive, known := processAlive(pid); known && !alive {
		t.Fatal("child process died mid-session")
	}
}

// Acceptance ②: no mcpServers ⇒ nothing at all happens. No connection
// record, no registry mutation — the pre-passthrough behaviour, byte for
// byte. (No goroutine assertion here: pkg/mcp spawns none of its own, so
// a process-wide count would be measuring the rest of the suite.)
func TestACPMCP_NoRosterIsAStrictNoOp(t *testing.T) {
	reg := tools.New()
	b := &acpBackend{reg: reg}
	b.connectMCP(nil)
	b.connectMCP([]acp.MCPServer{})

	if b.mcp != nil {
		t.Fatalf("empty roster produced a connection: %+v", b.mcp)
	}
	if got := reg.Names(); len(got) != 0 {
		t.Fatalf("registry mutated: %v", got)
	}
}

// Acceptance ③: session end reaps the children. This is the leak the card
// is about — the child process outliving the session — so the assertion
// is on the pid, not just on a closed flag.
func TestACPMCP_CloseReapsChildren(t *testing.T) {
	srv, pidFile := stubRoster(t, "butler")
	reg := tools.New()
	b := &acpBackend{reg: reg}
	b.connectMCP([]acp.MCPServer{{Name: srv.Name, Command: srv.Command, Args: srv.Args,
		Env: []acp.MCPEnvVar{{Name: "ACP_MCP_STUB_PIDFILE", Value: strings.TrimPrefix(srv.Env[0], "ACP_MCP_STUB_PIDFILE=")}}}})
	pid := waitForPid(t, pidFile)
	if alive, known := processAlive(pid); known && !alive {
		t.Fatal("child exited before teardown")
	}

	if err := b.closeMCP(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if b.mcp != nil {
		t.Fatal("close must forget the roster")
	}
	if !waitProcessGone(pid, 10*time.Second) {
		t.Fatalf("child pid %d still alive after close — leaked", pid)
	}
	// Idempotent: a second close (runACP's defer plus an explicit one)
	// must not error or re-reap.
	if err := b.closeMCP(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// waitProcessGone polls for the pid to disappear. A platform that cannot
// answer (Windows) reports "gone" so the assertion degrades to the
// goroutine check rather than failing for lack of a probe.
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive, known := processAlive(pid)
		if !known || !alive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A repeated session/new with the identical roster must not spawn a
// second copy (paseo re-anchors by re-calling session/new), and a
// genuinely different roster is refused rather than half-applied.
func TestACPMCP_RepeatedDeclarationIsIdempotent(t *testing.T) {
	srv, pidFile := stubRoster(t, "butler")
	decl := []acp.MCPServer{{Name: srv.Name, Command: srv.Command, Args: srv.Args,
		Env: []acp.MCPEnvVar{{Name: "ACP_MCP_STUB_PIDFILE", Value: strings.TrimPrefix(srv.Env[0], "ACP_MCP_STUB_PIDFILE=")}}}}

	reg := tools.New()
	b := &acpBackend{reg: reg}
	b.connectMCP(decl)
	t.Cleanup(func() { _ = b.closeMCP() })
	first := b.mcp
	pid := waitForPid(t, pidFile)

	b.connectMCP(decl)
	if b.mcp != first {
		t.Fatal("identical roster reconnected (second child spawned)")
	}
	if got := reg.Names(); len(got) != 2 {
		t.Fatalf("registry names = %v, want the same two tools", got)
	}

	other := []acp.MCPServer{{Name: "other", Command: srv.Command, Args: srv.Args}}
	b.connectMCP(other)
	if b.mcp != first {
		t.Fatal("a different roster must be refused, not swapped (one process = one session)")
	}
	if alive, known := processAlive(pid); known && !alive {
		t.Fatal("refused roster change must not tear down the live roster")
	}
}

// A declaration that cannot launch (missing binary / unsupported transport)
// is non-fatal: session/new still succeeds, the session just has no extra
// tools. Losing the editor session over a third-party config error would be
// far worse than the missing tools.
func TestACPMCP_BrokenDeclarationIsNonFatal(t *testing.T) {
	reg := tools.New()
	b := &acpBackend{reg: reg}
	b.connectMCP([]acp.MCPServer{
		{Name: "remote", Type: "http", Command: "/bin/true"},
		{Name: "missing", Type: "stdio", Command: filepath.Join(t.TempDir(), "does-not-exist")},
	})
	t.Cleanup(func() { _ = b.closeMCP() })
	if got := reg.Names(); len(got) != 0 {
		t.Fatalf("broken roster registered %v", got)
	}
	if b.mcp == nil {
		t.Fatal("the roster record must still exist so a repeated declaration is idempotent")
	}
}

// startACPMCP must not panic on a registry collision — client-declared
// names are data, and tools.Registry.Add panics on duplicates.
func TestACPMCP_NameCollisionIsSkippedNotPanicked(t *testing.T) {
	srv, _ := stubRoster(t, "butler")
	reg := tools.New()
	// Pre-occupy one of the names the stub would register.
	reg.Add(fakeNamedTool("mcp__butler__echo"))
	b := &acpBackend{reg: reg}
	b.connectMCP([]acp.MCPServer{{Name: srv.Name, Command: srv.Command, Args: srv.Args}})
	t.Cleanup(func() { _ = b.closeMCP() })

	names := reg.Names()
	if len(names) != 2 || names[0] != "mcp__butler__card_read" || names[1] != "mcp__butler__echo" {
		t.Fatalf("names = %v (the colliding one keeps its original owner)", names)
	}
	// Dispatch resolves to the pre-existing tool, not to the skipped bridge.
	if _, err := reg.Dispatch(context.Background(), "mcp__butler__echo", json.RawMessage(`{}`)); err == nil {
		t.Fatal("dispatch should reach the pre-existing tool, whose Execute errors")
	}
}

// A roster arriving after the tool list froze is refused, not silently
// connected: its tools could never be offered to the model, and spawning
// children nothing can call is the worse failure.
func TestACPMCP_RosterAfterWireIsRefused(t *testing.T) {
	srv, pidFile := stubRoster(t, "butler")
	reg := tools.New()
	reg.Wire() // the first turn already ran

	b := &acpBackend{reg: reg}
	b.connectMCP([]acp.MCPServer{{Name: srv.Name, Command: srv.Command, Args: srv.Args,
		Env: []acp.MCPEnvVar{{Name: "ACP_MCP_STUB_PIDFILE", Value: strings.TrimPrefix(srv.Env[0], "ACP_MCP_STUB_PIDFILE=")}}}})
	if b.mcp != nil {
		t.Fatalf("a roster after the wire freeze must not connect: %+v", b.mcp)
	}
	if got := reg.Names(); len(got) != 0 {
		t.Fatalf("registry names = %v, want none", got)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("no child may be spawned for a roster that cannot reach the model")
	}
}

// Acceptance ③ (leaktest): repeated session/new → session-end cycles must
// not accumulate anything — neither child processes nor goroutines.
//
// pkg/mcp starts no goroutines of its own (its calls are synchronous), so
// the only goroutine a connect/close pair touches is os/exec's ctx
// watchdog, which ends with the child. A warmed-up baseline keeps the
// one-time runtime/exec noise out of the count; the poll absorbs the
// scheduler, the same caliber as the web SSE leaktests.
func TestACPMCP_ConnectCloseCyclesDoNotLeak(t *testing.T) {
	srv, pidFile := stubRoster(t, "butler")
	decl := []acp.MCPServer{{Name: srv.Name, Command: srv.Command, Args: srv.Args,
		Env: []acp.MCPEnvVar{{Name: "ACP_MCP_STUB_PIDFILE", Value: strings.TrimPrefix(srv.Env[0], "ACP_MCP_STUB_PIDFILE=")}}}}

	// Each cycle asserts BOTH halves of "no leak": the child pid disappears
	// on close (process half) and — see the goroutine poll below — nothing
	// accumulates in the runtime. The registry is deliberately NOT asserted
	// empty after close: closeMCP only reaps the children, it does not
	// unregister tools, because one process is one session and the registry
	// dies with it; each cycle builds a fresh registry anyway, so cross-cycle
	// accumulation would show up in the pid/goroutine checks, not here.
	cycle := func() {
		_ = os.Remove(pidFile) // a stale pid from the previous cycle must not pass for this one's
		reg := tools.New()
		b := &acpBackend{reg: reg}
		b.connectMCP(decl)
		pid := waitForPid(t, pidFile)
		if err := b.closeMCP(); err != nil {
			t.Fatalf("cycle close: %v", err)
		}
		if !waitProcessGone(pid, 10*time.Second) {
			t.Fatalf("cycle: child pid %d survived close — leaked", pid)
		}
	}

	// Warm-up: absorbs first-use runtime/exec allocation.
	cycle()
	baseline := runtime.NumGoroutine()

	for range 3 {
		cycle()
	}

	// Goroutines settle back to the warmed-up baseline.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if n := runtime.NumGoroutine(); n <= baseline {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("goroutines %d → %d after 3 connect/close cycles — leaked", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type namedTool struct{ name string }

func (n *namedTool) Name() string            { return n.name }
func (n *namedTool) Description() string     { return "pre-existing" }
func (n *namedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (n *namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("pre-existing tool")
}

func fakeNamedTool(name string) *namedTool { return &namedTool{name: name} }
