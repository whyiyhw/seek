// Package mcptool bridges MCP tool definitions (from a running MCP
// server) into the seek tools.Tool interface so they can be registered
// in a tools.Registry and called by the agent exactly like built-in tools.
package mcptool

import (
	"context"
	"encoding/json"
	"fmt"
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
// the tool will be registered — normally def.Name, but the caller may
// prefix it (e.g. "server__tool") to resolve conflicts with built-in tools.
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
}

// LoadResult holds the Bridges and per-server Errors from LoadServers.
type LoadResult struct {
	Bridges []*Bridge
	Errors  []error
}

// LoadServers starts each MCP server, runs the initialize handshake,
// lists tools, and returns Bridge instances ready for registration.
//
// Each server's error is collected independently — one broken server
// does not block the others. existingNames is the set of tool names
// already in the seek registry; conflicting MCP tool names are
// prefixed with "<serverName>__" to avoid registry panics.
func LoadServers(ctx context.Context, servers map[string]ServerConfig, existingNames map[string]bool) LoadResult {
	var result LoadResult
	for serverName, cfg := range servers {
		bridges, err := loadOne(ctx, serverName, cfg, existingNames)
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

func loadOne(ctx context.Context, serverName string, cfg ServerConfig, existing map[string]bool) ([]*Bridge, error) {
	client, err := mcp.StartServer(ctx, mcp.ServerConfig{
		Command: cfg.Command,
		Args:    cfg.Args,
		Env:     cfg.Env,
	})
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
		effectName := def.Name
		if existing[effectName] {
			effectName = serverName + "__" + def.Name
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
