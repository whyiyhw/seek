// ACP MCP passthrough (backlog ⑦, card c_oyt6ft): the roster a client
// puts on session/new's `mcpServers` becomes live tools in the session.
//
// The four steps the card names, and where each one lives:
//
//	声明 (declared)   internal/acp/mcp.go — decode + validate + project
//	起连 (connect)    here: mcptool.LoadServers, i.e. the SAME client
//	                  stack mcp.json uses (pkg/mcp stdio transport,
//	                  initialize handshake, tools/list) — no second
//	                  implementation, no second config format
//	进工具面 (surface) here: reg.Add under "mcp__<server>__<tool>", so an
//	                  editor-supplied server is distinguishable from the
//	                  user's own mcp.json roster in the tools array
//	随会话清理 (cleanup) here: acpMCP.Close (runACP defers it; Server.Serve
//	                  returns on stdin EOF / ctx cancel), which closes every
//	                  client — and a client is the only handle on a spawned
//	                  child process
//
// Lifetime is deliberately NARROWER than the mcp.json path: those servers
// live until process exit because the process is the session, but here
// the roster belongs to a client that can hang up, so the children go
// with the transport. That is the whole point of returning Clients from
// LoadServers.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
)

// acpMCP is one connected passthrough roster.
type acpMCP struct {
	result  mcptool.LoadResult
	servers []acp.StdioServer
	tools   []string // registered names, sorted, for logging only
	closed  bool
}

// connectMCP honours session/new's mcpServers declaration.
//
// Absent/empty is a strict no-op — no child process, no registry change,
// no stderr line — which is what keeps the pre-passthrough behaviour
// byte-identical for every client that does not send the array (acceptance
// ②).
//
// The declaration is NOT allowed to change the system prompt: the MCPSection
// is composed once at startup and frozen (prefix-cache discipline), so an
// injected server shows up in the tools array but not in the prompt prose.
// That asymmetry is deliberate and matches the pitfall about the prompt
// describing the startup landscape rather than mirroring each request.
//
// Connecting is SYNCHRONOUS, in the ACP read loop — the same shape
// session/load already has (startup work on a request that the client
// needs an answer to). A server that never answers `initialize` therefore
// stalls the lane until it is killed, exactly as a wedged mcp.json server
// stalls seek's startup; pkg/mcp's per-call ctx check cannot interrupt a
// blocked pipe read, so a deadline here would be theatre. The dispatch
// layer already rejects a roster while a turn is in flight, so no running
// prompt can be starved.
func (b *acpBackend) connectMCP(decl []acp.MCPServer) {
	if len(decl) == 0 {
		return
	}
	servers, skipped := acp.StdioServers(decl)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "acp: mcp: skipped %s\n", s)
	}
	if len(servers) == 0 {
		return
	}
	if b.mcp != nil {
		// One process = one session = one roster, the same MVP rule the
		// rest of the lane follows. paseo re-sends the identical array
		// when it re-anchors (no-op, and a silent one); a genuinely
		// different array is refused LOUDLY rather than half-applied:
		// the registry cannot forget the first roster's tools, and a
		// bridge whose client was reaped would fail at call time.
		if sameRoster(b.mcp.servers, servers) {
			return
		}
		fmt.Fprintf(os.Stderr, "acp: mcp: roster change ignored — this process already serves %d connected server(s) (one process = one session; restart seek acp for a new roster)\n", len(b.mcp.servers))
		return
	}
	// The tools array freezes on the session's first turn
	// (tools.Registry.Wire). A roster arriving after that point could
	// register tools the model is never offered — refuse it loudly rather
	// than spawn children nothing can call. The freeze state is read from
	// the registry itself, not tracked in parallel.
	if b.reg != nil && b.reg.Wired() {
		fmt.Fprintln(os.Stderr, "acp: mcp: roster ignored — this session's first turn already froze its tool list, so the declared servers could not reach the model (restart seek acp to get them)")
		return
	}
	// The children are bound to the transport's own lifecycle ctx: if the
	// process is signalled or the lane tears down without reaching the
	// defer, ctx cancellation still KILLS every child (exec.CommandContext),
	// so nothing keeps running against the project. Reaping (cmd.Wait) is
	// still Close's job — a cancelled child the process never waits for is
	// a zombie until seek exits, not a live orphan. Close is the normal
	// path; this is the belt to its braces.
	b.mcp = startACPMCP(b.lifecycleCtx(), b.reg, servers)
}

// lifecycleCtx is the ctx new MCP children are bound to: the ACP lane's
// run ctx when the backend was built by runACP, Background otherwise
// (tests). Never nil.
func (b *acpBackend) lifecycleCtx() context.Context {
	if b.runCtx != nil {
		return b.runCtx
	}
	return context.Background()
}

// closeMCP tears down the live roster (child processes included) and
// forgets it. Called on replacement and from runACP's deferred cleanup —
// the session-end half of acceptance ③.
func (b *acpBackend) closeMCP() error {
	if b.mcp == nil {
		return nil
	}
	err := b.mcp.Close()
	b.mcp = nil
	return err
}

// startACPMCP connects every launchable entry of the declared roster and
// registers its tools. Errors never fail the session: a third-party
// server that will not start is a config problem, and session/new must
// still give the client a working conversation (same non-fatal posture
// as mcp.json's startup load).
func startACPMCP(ctx context.Context, reg *tools.Registry, roster []acp.StdioServer) *acpMCP {
	m := &acpMCP{servers: roster}
	if reg == nil || len(roster) == 0 {
		return m
	}
	cfg := make(map[string]mcptool.ServerConfig, len(roster))
	for _, s := range roster {
		if _, dup := cfg[s.Name]; dup {
			fmt.Fprintf(os.Stderr, "acp: mcp server %q declared twice — keeping the first\n", s.Name)
			continue
		}
		cfg[s.Name] = mcptool.ServerConfig{
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
			// The three-segment house prefix (dsh's, and the shape that
			// keeps injected servers distinguishable from mcp.json's
			// "<server>__<tool>"). ToolPrefix sanitises the name into the
			// provider-safe alphabet; a raw dot would 400 the first request.
			NamePrefix: s.ToolPrefix(),
		}
	}

	existing := make(map[string]bool, len(reg.Names()))
	for _, n := range reg.Names() {
		existing[n] = true
	}

	m.result = mcptool.LoadServers(ctx, cfg, existing)
	for _, e := range m.result.Errors {
		fmt.Fprintf(os.Stderr, "acp: %v\n", e)
	}

	registered := 0
	for _, b := range m.result.Bridges {
		// A collision here would panic inside reg.Add (duplicate names are
		// programming errors there). Client-declared names are DATA, not
		// our own code, so a clash is a skipped tool plus a note instead.
		if reg.Lookup(b.Name()) != nil {
			fmt.Fprintf(os.Stderr, "acp: mcp tool %q skipped: name already registered\n", b.Name())
			continue
		}
		reg.Add(b)
		m.tools = append(m.tools, b.Name())
		registered++
	}
	sort.Strings(m.tools)
	if registered > 0 {
		// Names are logged, not just counted: they ARE the session's tool
		// surface, and the operator (or the lane smoke) needs to see the
		// form the model will call — "mcp__<server>__<tool>".
		fmt.Fprintf(os.Stderr, "acp: mcp: registered %d tool(s) from %d server(s): %s\n",
			registered, len(m.result.Clients), strings.Join(m.tools, " "))
	}
	return m
}

// Close terminates every child process the roster spawned. Idempotent —
// runACP defers it, and an explicit close (a future session-end hook)
// must not double-reap.
func (m *acpMCP) Close() error {
	if m == nil || m.closed {
		return nil
	}
	m.closed = true
	if len(m.result.Clients) == 0 {
		return nil
	}
	err := m.result.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "acp: mcp close: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "acp: mcp closed %d server(s)\n", len(m.result.Clients))
	return err
}

// sameRoster reports whether two declarations would launch the same set
// of servers. Used to make a repeated session/new idempotent: paseo
// re-sends the same roster when it re-anchors, and spawning a second
// copy of every server for the same session would be a leak with a
// friendly name.
func sameRoster(a, b []acp.StdioServer) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(s acp.StdioServer) string {
		return s.Name + "\x00" + s.Command + "\x00" + strings.Join(s.Args, "\x00") + "\x00" + strings.Join(s.Env, "\x00")
	}
	as := make([]string, len(a))
	bs := make([]string, len(b))
	for i := range a {
		as[i], bs[i] = key(a[i]), key(b[i])
	}
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
