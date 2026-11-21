// MCP passthrough for the ACP lane (backlog ⑦): the client puts its own
// MCP roster on session/new's `mcpServers` array, and seek connects to
// those servers for the lifetime of the session.
//
// This file owns the WIRE SHAPE and the projection onto launch
// parameters — nothing is spawned here. The four steps the card asks for
// split cleanly across the stack:
//
//	声明 (declared)  this file: decode + validate + project
//	起连 (connect)    cmd/seek: mcptool.LoadServers, the same client stack
//	                 mcp.json already uses (pkg/mcp stdio, handshake,
//	                 tools/list) — no second implementation
//	进工具面 (surface) cmd/seek: registry.Add, names "mcp__<server>__<tool>"
//	随会话清理 (cleanup) cmd/seek: runACP defers acpBackend.closeMCP, so the
//	                 children die with the transport (stdin EOF / ctx cancel)
//	                 — one step earlier than the process, which is what
//	                 makes session death reap them
//
// Only the stdio variant is launchable: pkg/mcp speaks JSON-RPC over a
// child process's stdin/stdout, so http/sse/acp entries are skipped with
// a stderr note rather than half-honoured (the same posture LoadSession's
// doc comment records for the pre-passthrough days).
package acp

import (
	"fmt"
	"strings"
)

// MCPServer is one entry of session/new's mcpServers array (ACP schema
// `McpServer`). The schema is a union discriminated by `type`; the
// fields of all variants are flattened here because only four of them
// matter to seek and a strict per-variant decode would fail on the
// clients that omit the discriminant entirely (seek's own reference SDK
// types McpServerStdio without a `type` requirement).
type MCPServer struct {
	// Type is "stdio" (or "" — the stdio variant historically carried no
	// discriminant on the wire), "http", "sse", or "acp".
	Type string `json:"type,omitempty"`
	// Name is the human-readable server name; it becomes the middle
	// segment of every tool name (see StdioServer.ToolPrefix).
	Name string `json:"name"`
	// Command is the server executable's absolute path (ACP requires
	// absolute; seek passes it to exec verbatim and lets a missing
	// binary fail loudly at spawn).
	Command string `json:"command,omitempty"`
	// Args are the server's command-line arguments.
	Args []string `json:"args,omitempty"`
	// Env are extra variables for the child's environment. seek scrubs
	// the parent environment first (internal/childenv), so this is the
	// ONLY way an injected server sees a credential.
	Env []MCPEnvVar `json:"env,omitempty"`
}

// MCPEnvVar is one {name,value} pair of an McpServer's env array.
type MCPEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// StdioServer is one launchable server: the declaration projected onto
// the only transport pkg/mcp speaks.
type StdioServer struct {
	Name    string
	Command string
	Args    []string
	// Env is already in "KEY=VALUE" form — the shape pkg/mcp's
	// ServerConfig takes, so cmd/seek stays a pass-through.
	Env []string
}

// ToolPrefix is what every tool of this server is registered under:
// "mcp__<server>__".
//
// The card's shorthand for this shape is `mcp.<server>.<tool>`; the
// literal dots are not usable — DeepSeek/OpenAI/Anthropic all constrain
// `tools[].function.name` to ^[a-zA-Z0-9_-]{1,64}$, and a dotted name
// 400s the very first request. Double underscores are the house prefix
// (mcp.json's own tools are `<server>__<tool>`) plus dsh's three-segment
// `mcp__<server>__<tool>` form, which is what makes the injected
// namespace distinguishable from the user's mcp.json roster. Pitfall:
// docs/pitfalls/mcp-bridge-tool-naming/.
func (s StdioServer) ToolPrefix() string { return "mcp__" + sanitizeSegment(s.Name) + "__" }

// maxNameSegment bounds the server segment so "mcp__<server>__" stays
// short enough to leave room for real tool names under the provider's
// 64-character function-name ceiling (mcptool clamps the total; this
// keeps the clamp from eating the tool name for ordinary rosters).
const maxNameSegment = 32

// sanitizeSegment folds a server name into the provider-safe alphabet
// [A-Za-z0-9_-]. Server names are human labels ("butler", "my.tools/v2")
// and the dot would 400 the first request; folding is lossy but the
// name was never an identifier, and the MCPServer.Name survives verbatim
// in logs and in the skipped-reason text.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > maxNameSegment {
		out = out[:maxNameSegment]
	}
	if out == "" {
		out = "server"
	}
	return out
}

// StdioServers projects a declared roster onto the launchable subset.
// Every entry that cannot be launched is returned in skipped as a
// human-readable reason — session/new must still succeed: a broken
// third-party declaration is the user's config problem, not a reason to
// brick the editor session (same posture as mcp.json's non-fatal load).
//
// Order follows the declaration (a JSON array is ordered); no map is
// involved, so two identical rosters always produce byte-identical
// launch order.
func StdioServers(roster []MCPServer) (out []StdioServer, skipped []string) {
	for i, s := range roster {
		switch s.Type {
		case "", "stdio":
		default:
			skipped = append(skipped, fmt.Sprintf("%s: type %q is not supported (stdio only)", entryName(s, i), s.Type))
			continue
		}
		if strings.TrimSpace(s.Name) == "" {
			skipped = append(skipped, fmt.Sprintf("#%d: missing name", i))
			continue
		}
		if strings.TrimSpace(s.Command) == "" {
			skipped = append(skipped, fmt.Sprintf("%s: missing command", s.Name))
			continue
		}
		srv := StdioServer{Name: s.Name, Command: s.Command, Args: s.Args}
		for _, e := range s.Env {
			if e.Name == "" {
				continue // unusable child-env entry; same rule as childenv.Scrub
			}
			srv.Env = append(srv.Env, e.Name+"="+e.Value)
		}
		out = append(out, srv)
	}
	return out, skipped
}

func entryName(s MCPServer, i int) string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("#%d", i)
}
