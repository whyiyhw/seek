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

// DefaultPath returns the path where mcp.json is read from. Kept as a
// thin wrapper around paths.MCPConfig for callers (tests, --help text)
// that already used this symbol.
func DefaultPath() (string, error) {
	return paths.MCPConfig()
}
