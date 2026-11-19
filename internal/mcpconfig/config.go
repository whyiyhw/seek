// Package mcpconfig parses ~/.seek/mcp.json — the MCP server
// configuration file. The format is compatible with Claude Code and
// Cursor so users can copy their server lists across tools without
// rewriting them; only the file location is seek-specific now.
package mcpconfig

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/whyiyhw/seek/internal/paths"
)

// ServerEntry is one entry in the mcpServers map.
type ServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	// Prefix opts out of the default "<server>__<tool>" registration:
	// an explicit false restores the server's bare tool names. Absent
	// or true keeps the prefixed form (the default). Pointer so the
	// absent case is distinguishable from an explicit false. Escape
	// hatch only — not advertised.
	Prefix *bool `json:"prefix,omitempty"`
	// Hint is optional free-form guidance (最高优先) describing what
	// this server is for and when to reach for its tools. When set, it
	// replaces seek's own per-server copy in the system-prompt MCP
	// section verbatim — the user knows their server better than seek
	// does (docs/prd/feature-mcp-client.md §3.2, tier 1 of 3).
	Hint string `json:"hint,omitempty"`
}

// UsePrefix reports whether this server's tools should register under
// the "<server>__<tool>" form (the default). False only when the
// config says "prefix": false explicitly.
func (e ServerEntry) UsePrefix() bool {
	return e.Prefix == nil || *e.Prefix
}

// Config is the top-level structure of mcp.json.
type Config struct {
	MCPServers map[string]ServerEntry `json:"mcpServers"`
	// MCPPrompt disables the system-prompt MCP section when explicitly
	// set to false. Tools stay registered — only the prompt guidance is
	// suppressed (the A2 "tool exists, no guidance" eval arm, §5.1).
	// nil (absent) means enabled; a pointer distinguishes the two.
	MCPPrompt *bool `json:"mcp_prompt,omitempty"`
}

// PromptEnabled reports whether the system-prompt MCP section should be
// assembled. Default true; only an explicit "mcp_prompt": false in
// mcp.json turns it off.
func (c Config) PromptEnabled() bool {
	return c.MCPPrompt == nil || *c.MCPPrompt
}

// Load reads the config from the default path (~/.seek/mcp.json, or
// $SEEK_HOME/mcp.json if SEEK_HOME is set).
// Returns an empty Config (not an error) when the file does not exist —
// having no MCP servers configured is a normal, valid state.
func Load() (Config, error) {
	path, err := paths.MCPConfig()
	if err != nil {
		return Config{}, err
	}
	return LoadFrom(path)
}

// LoadFrom reads config from a specific file path. Returns an empty
// Config if the file does not exist.
func LoadFrom(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("mcpconfig: read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("mcpconfig: parse %s: %w", path, err)
	}
	return cfg, nil
}
