// Package mcpconfig parses ~/.config/seek/mcp.json — the MCP server
// configuration file whose format is compatible with Claude Code and
// Cursor so users can migrate their server lists without changes.
package mcpconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// Load reads the config from the default platform path.
// Returns an empty Config (not an error) when the file does not exist —
// having no MCP servers configured is a normal, valid state.
func Load() (Config, error) {
	path, err := defaultPath()
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

// DefaultPath returns the platform-specific config path.
func DefaultPath() (string, error) {
	return defaultPath()
}

func defaultPath() (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			return "", fmt.Errorf("mcpconfig: %%APPDATA%% is not set")
		}
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("mcpconfig: home dir: %w", err)
		}
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg != "" {
			base = xdg
		} else {
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "seek", "mcp.json"), nil
}
