package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFrom_Missing(t *testing.T) {
	cfg, err := LoadFrom("/tmp/seek_test_does_not_exist_mcp.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("expected empty MCPServers for missing file, got %v", cfg.MCPServers)
	}
}

func TestLoadFrom_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	content := `{
		"mcpServers": {
			"fs": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
				"env": {"DEBUG": "1"}
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("len(MCPServers) = %d, want 1", len(cfg.MCPServers))
	}
	e, ok := cfg.MCPServers["fs"]
	if !ok {
		t.Fatal("MCPServers[\"fs\"] missing")
	}
	if e.Command != "npx" {
		t.Errorf("Command = %q, want %q", e.Command, "npx")
	}
	if len(e.Args) != 3 {
		t.Errorf("len(Args) = %d, want 3", len(e.Args))
	}
	if e.Env["DEBUG"] != "1" {
		t.Errorf("Env[DEBUG] = %q, want %q", e.Env["DEBUG"], "1")
	}
}

func TestLoadFrom_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestLoadFrom_HintField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	content := `{
		"mcpServers": {
			"my-server": {
				"command": "npx",
				"args": ["-y", "whatever"],
				"hint": "Use for X, not for Y"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.MCPServers["my-server"].Hint; got != "Use for X, not for Y" {
		t.Errorf("Hint = %q, want %q", got, "Use for X, not for Y")
	}
	// Entries without hint parse to "" (zero value), not an error.
	if got := cfg.MCPServers["missing"]; got.Command != "" || got.Hint != "" {
		t.Errorf("unexpected entry for absent server: %+v", got)
	}
}

func TestPromptEnabled(t *testing.T) {
	dir := t.TempDir()

	write := func(content string) Config {
		path := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFrom(path)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		return cfg
	}

	if cfg := write(`{"mcpServers": {}}`); !cfg.PromptEnabled() {
		t.Error("absent mcp_prompt should default to enabled")
	}
	if cfg := write(`{"mcpServers": {}, "mcp_prompt": true}`); !cfg.PromptEnabled() {
		t.Error(`mcp_prompt: true should be enabled`)
	}
	if cfg := write(`{"mcpServers": {}, "mcp_prompt": false}`); cfg.PromptEnabled() {
		t.Error(`mcp_prompt: false should disable the prompt section (A2 eval arm)`)
	}
}
