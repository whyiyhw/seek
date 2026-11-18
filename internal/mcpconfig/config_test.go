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

func TestLoadFrom_PrefixField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	content := `{
		"mcpServers": {
			"default1": {"command": "a"},
			"explicit-true": {"command": "b", "prefix": true},
			"opt-out": {"command": "c", "prefix": false}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	cases := []struct {
		server string
		prefix *bool
		want   bool // expected UsePrefix
	}{
		{"default1", nil, true},
		{"explicit-true", boolPtr(true), true},
		{"opt-out", boolPtr(false), false},
	}
	for _, c := range cases {
		e, ok := cfg.MCPServers[c.server]
		if !ok {
			t.Fatalf("MCPServers[%q] missing", c.server)
		}
		if (e.Prefix == nil) != (c.prefix == nil) {
			t.Errorf("%s: Prefix = %v, want %v", c.server, e.Prefix, c.prefix)
			continue
		}
		if c.prefix != nil && *e.Prefix != *c.prefix {
			t.Errorf("%s: *Prefix = %v, want %v", c.server, *e.Prefix, *c.prefix)
		}
		if got := e.UsePrefix(); got != c.want {
			t.Errorf("%s: UsePrefix() = %v, want %v", c.server, got, c.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
