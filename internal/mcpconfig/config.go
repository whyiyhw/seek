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
