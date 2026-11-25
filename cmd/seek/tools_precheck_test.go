package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/mcpconfig"
)

func boolPtr(b bool) *bool { return &b }

// ---------------------------------------------------------------------------
// checkToolsWhitelistPreMCP — the three-way decision (built-in / provably
// impossible / deferred to MCP), table-driven.
//
// The table is the CONTRACT the fork-order optimisation rests on: case 3
// (deferral) is the only reason precheck can be as aggressive as it is
// without ever rejecting an invocation the post-load check would accept.
// A change to mcptool.loadOne's naming or mcpconfig.ServerEntry.UsePrefix
// must show up here first, or a legitimate `-tools semble__search` starts
// dying at flag-parse time.
// ---------------------------------------------------------------------------

func TestCheckToolsWhitelistPreMCP_Table(t *testing.T) {

	// Two configs, because the MCP side of the decision is entirely a
	// function of mcp.json: prefixed servers (default) vs a prefix-less
	// escape-hatch server that may register ANY bare name.
	prefixed := mcpconfig.Config{MCPServers: map[string]mcpconfig.ServerEntry{
		"butler": {Command: "/bin/true"},
		"semble": {Command: "/bin/true"},
	}}
	bare := mcpconfig.Config{MCPServers: map[string]mcpconfig.ServerEntry{
		"butler": {Command: "/bin/true"},
		"loose":  {Command: "/bin/true", Prefix: boolPtr(false)},
	}}

	tests := []struct {
		name    string
		flagVal string
		cfg     mcpconfig.Config
		wantErr bool
		// wantStderr, when set, must appear verbatim in stderr — a usage
		// rejection is only useful if it names the offender and the
		// alternatives (the exit-2 contract CI reads).
		wantStderr []string
	}{
		// --- flag-value hygiene: nothing to validate -------------------
		{"empty flag defers to full registry", "", prefixed, false, nil},
		{"comma-only is empty", ",,", prefixed, false, nil},

		// --- 1. built-in, already registered here ----------------------
		{"registered built-in passes", "read", prefixed, false, nil},
		{"several registered built-ins pass", "read,edit,grep", prefixed, false, nil},

		// --- 1b. built-in that registers AFTER the MCP block -----------
		{"post-MCP built-in passes", "plan", prefixed, false, nil},
		{"mixed post-MCP built-ins pass", "plan,propose,agent,memory_recall", prefixed, false, nil},

		// --- 2. provably impossible: no MCP server could supply it -----
		{"unknown with no MCP config", "nosuch", mcpconfig.Config{}, true,
			[]string{"unknown tool(s): nosuch", "available tools:"}},
		{"unknown with a prefixed-only roster", "nosuch", prefixed, true,
			[]string{"unknown tool(s): nosuch"}},
		{"unknown alongside a valid name", "read,nosuch", prefixed, true,
			[]string{"unknown tool(s): nosuch"}},
		{"prefixed name whose server is absent", "ghost__search", prefixed, true,
			[]string{"unknown tool(s): ghost__search"}},
		{"prefixed name is only valid when its server exists", "butler__x", prefixed, false, nil},

		// --- 3. deferred: only ListTools can answer -------------------
		{"configured server prefix defers", "butler__echo", prefixed, false, nil},
		{"second configured server prefix defers", "semble__search", prefixed, false, nil},
		{"prefix-less server defers ANY bare name", "echo", bare, false, nil},
		// A prefix-less server registers names verbatim, so it may own
		// "ghost__search" too — the double underscore is not evidence of a
		// server that does not exist.
		{"prefix-less server defers even a prefixed-looking name", "ghost__search", bare, false, nil},
		{"bare name is unknown when every server prefixes", "echo", prefixed, true,
			[]string{"unknown tool(s): echo"}},

		// --- zero-tool sentinel (in flight on card c_lh6tlq) ----------
		// `none` is not a tool name. Deferring it (rather than rejecting)
		// is what keeps this branch merge-safe in EITHER order: today it
		// reaches applyToolsWhitelist and exits 2 there; once the sentinel
		// card lands, the same deferral lets it through to a cleared
		// registry instead of failing at flag-parse time.
		{"zero-tool sentinel is deferred", "none", prefixed, false, nil},
		// The sentinel card decides what `none,read` means (a usage error
		// there, not an unknown name), so this check defers the whole
		// value whenever the sentinel is present — including on a roster
		// where "none" would otherwise look like an unknowable bare name.
		{"sentinel mixed with a name is still deferred", "none,read", prefixed, false, nil},
		{"sentinel mixed with an unknown is still deferred", "none,nosuch", prefixed, false, nil},
	}

	// Deliberately NOT t.Parallel: the stderr assertions below go through
	// captureStderr, which swaps the process-global os.Stderr for a pipe.
	// Two parallel subtests would read each other's output.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := newWhitelistTestRegistry()
			before := reg.Names()

			var err error
			stderr := captureStderr(t, func() {
				err = checkToolsWhitelistPreMCP(tc.flagVal, reg, tc.cfg)
			})

			if tc.wantErr {
				if !errors.Is(err, errToolsUsage) {
					t.Fatalf("checkToolsWhitelistPreMCP(%q) err = %v, want errToolsUsage", tc.flagVal, err)
				}
				for _, want := range tc.wantStderr {
					if !strings.Contains(stderr, want) {
						t.Errorf("stderr = %q, want it to contain %q", stderr, want)
					}
				}
			} else if err != nil {
				t.Fatalf("checkToolsWhitelistPreMCP(%q) err = %v, want nil (defer to the post-load check)", tc.flagVal, err)
			}

			// The precheck is a pure predicate: it must never narrow the
			// registry, no matter which branch it took.
			if got := reg.Names(); !reflect.DeepEqual(got, before) {
				t.Errorf("registry mutated by the precheck: %v, want %v", got, before)
			}
		})
	}
}

// A deferral must never be silent about WHY it deferred: the two cases
// are different contracts (prefix match vs prefix-less wildcard), and a
// future tightening of either one has to be a deliberate edit here.
func TestCheckToolsWhitelistPreMCP_DeferralReasons(t *testing.T) {
	flat := mcpconfig.Config{MCPServers: map[string]mcpconfig.ServerEntry{
		"butler": {Command: "/bin/true"},
	}}
	reg := newWhitelistTestRegistry()

	// Known prefix → defer.
	if err := checkToolsWhitelistPreMCP("butler__anything", reg, flat); err != nil {
		t.Fatalf("prefix-matched name must defer, got %v", err)
	}
	// Same name, no such server → reject (the prefix is the whole reason
	// the first case deferred).
	if err := checkToolsWhitelistPreMCP("butler__anything", reg, mcpconfig.Config{}); !errors.Is(err, errToolsUsage) {
		t.Fatalf("name without a matching server must reject, got %v", err)
	}
}

// One unreadable mcp.json yields a zero Config — no servers can load, so
// no name may defer on MCP's account (the old behaviour skipped loading
// entirely when the config was broken).
func TestCheckToolsWhitelistPreMCP_BrokenConfigDefersNothing(t *testing.T) {
	reg := newWhitelistTestRegistry()

	var err error
	stderr := captureStderr(t, func() {
		err = checkToolsWhitelistPreMCP("butler__echo", reg, mcpconfig.Config{})
	})
	if !errors.Is(err, errToolsUsage) {
		t.Fatalf("err = %v, want errToolsUsage with no servers configured", err)
	}
	if !strings.Contains(stderr, "butler__echo") {
		t.Errorf("stderr = %q, want the offender named", stderr)
	}
}

// ---------------------------------------------------------------------------
// E2E — the compiled binary with a REAL mcp.json whose single server is
// this test binary in MCP-stub mode (the acp_mcp_test.go fixture). The
// stub writes its pid on startup, so "did a child fork?" is a fact on
// disk, not an inference from log lines.
// ---------------------------------------------------------------------------

func writeMCPConfig(t *testing.T, dir string, servers map[string]mcpconfig.ServerEntry) {
	t.Helper()
	data, err := json.Marshal(mcpconfig.Config{MCPServers: servers})
	if err != nil {
		t.Fatalf("marshal mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
}

// stubServerRoster configures one stub MCP server whose child pid lands
// in pidFile. `prefix` is the mcp.json escape hatch (nil = the default
// "<server>__<tool>" registration).
func stubServerRoster(pidFile string, prefix *bool) map[string]mcpconfig.ServerEntry {
	return map[string]mcpconfig.ServerEntry{
		"butler": {
			Command: os.Args[0],
			Args:    []string{"-acp-mcp-stub"},
			Env:     map[string]string{"ACP_MCP_STUB_PIDFILE": pidFile},
			Prefix:  prefix,
		},
	}
}

// Acceptance ① (judge 前置④): `-tools nosuch` must exit 2 WITHOUT the
// configured MCP server spawning. Before the precheck the child forked
// first and the flag died after — the exact cost this card removes.
func TestToolsPrecheckE2E_UnknownNameSpawnsNoMCPServer(t *testing.T) {
	workDir := t.TempDir()
	pidFile := filepath.Join(workDir, "mcp-child.pid")
	writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))

	b := newE2EBackend(t)
	stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "nosuch")
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	// The fork is the cost: a pid file on disk means a child really ran.
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("the MCP server spawned — the precheck must reject before any fork")
	}
	if !strings.Contains(stderr, "unknown tool(s): nosuch") {
		t.Errorf("stderr misses the offender:\n%s", stderr)
	}
	if !strings.Contains(stderr, "available tools:") {
		t.Errorf("stderr misses the available list:\n%s", stderr)
	}
	if strings.Contains(stderr, "mcp: loaded") {
		t.Errorf("an MCP load happened despite the invalid whitelist:\n%s", stderr)
	}
	if n := b.requestCount(); n != 0 {
		t.Errorf("backend saw %d requests, want 0 (no prompt before the flag dies)", n)
	}
}

// Acceptance ② + the deferral half of ③: with the SAME mcp.json,
//   - `-tools read` still starts (a valid built-in never blocks the
//     normal startup path, MCP servers still load, the whitelist still
//     narrows the wire), and
//   - `-tools butler__echo` (unknown to the built-in registry, but the
//     configured server's prefix) is deferred to the post-load check and
//     validates against what the server actually listed.
func TestToolsPrecheckE2E_ValidAndDeferredNamesStillLoad(t *testing.T) {
	t.Run("built-in tool does not regress", func(t *testing.T) {
		workDir := t.TempDir()
		pidFile := filepath.Join(workDir, "mcp-child.pid")
		writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))

		b := newE2EBackend(t)
		stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "read")
		if code != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		// The accepted whitelist must not have suppressed MCP loading —
		// that would change "load then filter" into "filter then maybe
		// load", a different product.
		waitForPid(t, pidFile)
		if !strings.Contains(stderr, "mcp: loaded") {
			t.Errorf("stderr misses the MCP load line:\n%s", stderr)
		}
		if got := toolNamesFromBody(t, b.firstBody(t)); !reflect.DeepEqual(got, []string{"read"}) {
			t.Errorf("wire tools = %v, want exactly [read]", got)
		}
	})

	t.Run("mcp-prefixed name validates after load", func(t *testing.T) {
		workDir := t.TempDir()
		pidFile := filepath.Join(workDir, "mcp-child.pid")
		writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))

		b := newE2EBackend(t)
		stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "butler__echo")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (the prefix defers to the post-load check)\nstdout:\n%s\nstderr:\n%s",
				code, stdout, stderr)
		}
		waitForPid(t, pidFile)
		if got := toolNamesFromBody(t, b.firstBody(t)); !reflect.DeepEqual(got, []string{"butler__echo"}) {
			t.Errorf("wire tools = %v, want exactly [butler__echo]", got)
		}
	})

	// The other half of the same conflict: a prefixed name the server did
	// NOT list is unknowable before ListTools — so it MUST fork, and it
	// must then die in the post-load check with the same exit 2. This is
	// the documented residue of the fork-order optimisation, pinned so a
	// future "no fork ever" refactor has to argue with a test.
	t.Run("unlisted prefixed name dies only after load", func(t *testing.T) {
		workDir := t.TempDir()
		pidFile := filepath.Join(workDir, "mcp-child.pid")
		writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))

		b := newE2EBackend(t)
		stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "butler__no_such_tool")
		if code != 2 {
			t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		waitForPid(t, pidFile) // deferred ⇒ the child was spawned on purpose
		if !strings.Contains(stderr, "butler__no_such_tool") {
			t.Errorf("stderr misses the offender:\n%s", stderr)
		}
		if n := b.requestCount(); n != 0 {
			t.Errorf("backend saw %d requests, want 0", n)
		}
	})

	// A prefix-less server may register ANY bare name, so nothing bare is
	// provably impossible while one is configured — the precheck must
	// defer even a name no current tool uses.
	t.Run("prefix-less server defers bare names", func(t *testing.T) {
		workDir := t.TempDir()
		pidFile := filepath.Join(workDir, "mcp-child.pid")
		writeMCPConfig(t, workDir, stubServerRoster(pidFile, boolPtr(false)))

		b := newE2EBackend(t)
		stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "echo")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (a prefix-less server owns bare names)\nstdout:\n%s\nstderr:\n%s",
				code, stdout, stderr)
		}
		waitForPid(t, pidFile)
		if got := toolNamesFromBody(t, b.firstBody(t)); !reflect.DeepEqual(got, []string{"echo"}) {
			t.Errorf("wire tools = %v, want exactly [echo]", got)
		}
	})
}

// The precheck must not become a second opinion on the flag's own syntax:
// an empty/absent value still means the full registry and still forks
// MCP servers (the no-flag lane is the untouched baseline).
func TestToolsPrecheckE2E_AbsentFlagStillLoads(t *testing.T) {
	workDir := t.TempDir()
	pidFile := filepath.Join(workDir, "mcp-child.pid")
	writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))

	b := newE2EBackend(t)
	stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	waitForPid(t, pidFile)

	names := toolNamesFromBody(t, b.firstBody(t))
	if len(names) <= 5 {
		t.Errorf("default registry carries %d tools (≤5) — expected the full set: %v", len(names), names)
	}
	contained := false
	for _, n := range names {
		if n == "butler__echo" {
			contained = true
		}
	}
	if !contained {
		t.Errorf("wire tools = %v, want it to carry butler__echo", names)
	}
}
