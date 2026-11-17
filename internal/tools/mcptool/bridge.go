// Package mcptool bridges MCP tool definitions (from a running MCP
// server) into the seek tools.Tool interface so they can be registered
// in a tools.Registry and called by the agent exactly like built-in tools.
package mcptool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/pkg/mcp"
)

// Bridge wraps one MCP tool as a seek Tool.
type Bridge struct {
	client     *mcp.Client
	serverName string
	effectName string // may be prefixed to avoid registry conflicts
	def        mcp.ToolDef
}

// New wraps one MCP tool definition. effectName is the name under which
// the tool will be registered — by convention "<server>__<tool>" (e.g.
// semble__search); loadOne computes it, see the naming policy there.
func New(client *mcp.Client, serverName string, def mcp.ToolDef, effectName string) *Bridge {
	return &Bridge{
		client:     client,
		serverName: serverName,
		effectName: effectName,
		def:        def,
	}
}

func (b *Bridge) Name() string            { return b.effectName }
func (b *Bridge) Description() string     { return b.def.Description }
func (b *Bridge) Schema() json.RawMessage { return b.def.InputSchema }

func (b *Bridge) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("mcptool %s: bad arguments: %v", b.effectName, err)
	}
	result, err := b.client.CallTool(ctx, b.def.Name, args)
	if err != nil {
		return "", fmt.Errorf("mcptool %s: %w", b.effectName, err)
	}
	text := result.TextContent()
	if result.IsError {
		return "", fmt.Errorf("mcptool %s: tool error: %s", b.effectName, text)
	}
	if text == "" {
		return fmt.Sprintf("mcptool %s: (empty response)", b.effectName), nil
	}
	return text, nil
}

// ServerConfig is the per-server configuration passed to LoadServers.
// It mirrors mcpconfig.ServerEntry to avoid an import cycle between
// this package and mcpconfig.
type ServerConfig struct {
	Command string
	Args    []string
	// Env holds additional "KEY=VALUE" pairs to merge on top of the
	// inherited environment. nil means no additions.
	Env []string
	// NoPrefix opts this server out of the default "<server>__<tool>"
	// registration name, keeping the server's bare tool names instead.
	// It is the escape hatch behind mcp.json's "prefix": false. If a
	// bare name collides with something already registered, the tool
	// falls back to the prefixed form — the bare name is simply not
	// available. Zero value (false) = prefix on, the default.
	NoPrefix bool
}

// LoadResult holds the Bridges and per-server Errors from LoadServers.
type LoadResult struct {
	Bridges []*Bridge
	Errors  []error
}

// LoadServers starts each MCP server, runs the initialize handshake,
// lists tools, and returns Bridge instances ready for registration.
//
// Servers are visited in lexicographic order of their names so the
// same config produces a byte-identical registration roster (names
// and order) on every startup — map iteration order never leaks in.
// Each server's error is collected independently — one broken server
// does not block the others. existingNames is the set of tool names
// already in the seek registry.
func LoadServers(ctx context.Context, servers map[string]ServerConfig, existingNames map[string]bool) LoadResult {
	return loadServers(ctx, servers, existingNames, func(ctx context.Context, cfg ServerConfig) (*mcp.Client, error) {
		return mcp.StartServer(ctx, mcp.ServerConfig{
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
		})
	})
}

// connectFn launches one MCP server subprocess and returns a client.
// A seam so tests can substitute in-process fake servers.
type connectFn func(ctx context.Context, cfg ServerConfig) (*mcp.Client, error)

func loadServers(ctx context.Context, servers map[string]ServerConfig, existingNames map[string]bool, connect connectFn) LoadResult {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var result LoadResult
	for _, serverName := range names {
		bridges, err := loadOne(ctx, serverName, servers[serverName], existingNames, connect)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("mcp server %q: %w", serverName, err))
			continue
		}
		for _, b := range bridges {
			existingNames[b.effectName] = true
		}
		result.Bridges = append(result.Bridges, bridges...)
	}
	return result
}

func loadOne(ctx context.Context, serverName string, cfg ServerConfig, existing map[string]bool, connect connectFn) ([]*Bridge, error) {
	client, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if _, err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	defs, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("list tools: %w", err)
	}

	bridges := make([]*Bridge, 0, len(defs))
	for _, def := range defs {
		// Default: register as "<server>__<tool>" (e.g. semble__search).
		// The prefix carries ownership in the name and makes cross-server
		// collisions impossible. NoPrefix (mcp.json's "prefix": false
		// escape hatch) keeps the bare name when it is free, falling
		// back to the prefixed form when something already holds it.
		effectName := serverName + "__" + def.Name
		if cfg.NoPrefix && !existing[def.Name] {
			effectName = def.Name
		}
		bridges = append(bridges, New(client, serverName, def, effectName))
	}
	return bridges, nil
}

// EnvMapToSlice converts a map[string]string into "KEY=VALUE" pairs
// suitable for appending to an os.Environ() slice.
func EnvMapToSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// FormatErrors joins a slice of errors into a multi-line string for
// stderr startup output.
func FormatErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	lines := make([]string, 0, len(errs))
	for _, e := range errs {
		lines = append(lines, "  "+e.Error())
	}
	return strings.Join(lines, "\n")
}
