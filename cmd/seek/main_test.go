package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
	"github.com/whyiyhw/seek/pkg/mcp"
)

// detectGlamourStyle has three decision axes:
//  1. flag override ("dark"/"light" → return immediately)
//  2. SEEK_STYLE env var when flag is "auto"
//  3. terminal default (HasDarkBackground) when neither applies
//
// Axes 1 and 2 are pure logic and fully testable.
// Axis 3 depends on the real terminal — we verify it returns
// either "dark" or "light" without panicking.

func TestDetectGlamourStyle_FlagOverride(t *testing.T) {
	tests := []struct {
		name  string
		theme string
		want  string
	}{
		{"explicit dark", "dark", "dark"},
		{"explicit light", "light", "light"},
	}
	// Mixed-case values fall through to env/terminal detection,
	// so we don't assert a specific result — just verify no panic.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectGlamourStyle(tt.theme)
			if got != tt.want {
				t.Errorf("detectGlamourStyle(%q) = %q, want %q", tt.theme, got, tt.want)
			}
		})
	}
}

func TestDetectGlamourStyle_EnvOverride(t *testing.T) {
	// SEEK_STYLE must be honoured when flag is "auto".
	// We set the env, call the function, then restore.
	const envKey = "SEEK_STYLE"
	prev := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prev) })

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"env dark", "dark", "dark"},
		{"env light", "light", "light"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(envKey, tt.value)
			got := detectGlamourStyle("auto")
			if got != tt.want {
				t.Errorf("detectGlamourStyle(\"auto\") with SEEK_STYLE=%q = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDetectGlamourStyle_FlagBeatsEnv(t *testing.T) {
	// When flag is "dark" or "light", the env var must be ignored.
	const envKey = "SEEK_STYLE"
	prev := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prev) })
	os.Setenv(envKey, "light")

	got := detectGlamourStyle("dark")
	if got != "dark" {
		t.Errorf("detectGlamourStyle(\"dark\") with SEEK_STYLE=light = %q, want \"dark\"", got)
	}
}

func TestDetectGlamourStyle_Fallback(t *testing.T) {
	// No flag override, no env var → falls through to terminal
	// detection. We can't predict the result, but it must return
	// "dark" or "light" without error.
	const envKey = "SEEK_STYLE"
	prev := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prev) })
	os.Unsetenv(envKey)

	got := detectGlamourStyle("auto")
	if got != "dark" && got != "light" {
		t.Errorf("detectGlamourStyle(\"auto\") with no env = %q, want \"dark\" or \"light\"", got)
	}
}

// TestDetectGlamourStyle_NonTTYStdinSkipsQuery pins the CONIN$ hang
// fix: with a piped stdin (no tty), detection must short-circuit to
// "dark" WITHOUT issuing the OSC 11 query. Pre-fix, lipgloss v2's
// Windows path opened CONIN$, switched it to raw mode, and blocked in
// ReadConsole forever waiting for a reply no non-interactive console
// sends (the 2s CancelReader timeout did not fire on every console
// host) — a full `go test ./...` hung 10m at TestDetectGlamourStyle_
// Fallback exactly here. The pipe swap makes the non-tty condition
// true on every platform, so the test is a portable regression trap.
func TestDetectGlamourStyle_NonTTYStdinSkipsQuery(t *testing.T) {
	const envKey = "SEEK_STYLE"
	prevEnv := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prevEnv) })
	os.Unsetenv(envKey)

	prevStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = prevStdin
		r.Close()
		w.Close()
	})

	if got := detectGlamourStyle("auto"); got != "dark" {
		t.Errorf("detectGlamourStyle(\"auto\") with piped stdin = %q, want \"dark\" (query skipped)", got)
	}
}

// buildProvider("glm", ...) wiring: label, default model, key resolution,
// and the --base-url override path. SEEK_HOME is pointed at a temp dir so
// the test never reads the developer's real ~/.seek/config.json.
func TestBuildProvider_GLM(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())

	t.Run("env key, default base url", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "k")
		provider, dsClient, label, modelDefault, err := buildProvider("glm", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if dsClient != nil {
			t.Errorf("glm must not build a deepseek client")
		}
		if provider == nil || provider.Name() != "GLM" {
			t.Errorf("provider label = %v, want GLM", provider)
		}
		if label != "GLM" {
			t.Errorf("provLabel = %q, want \"GLM\"", label)
		}
		if modelDefault != "glm-5.3" {
			t.Errorf("modelDefault = %q, want glm-5.3", modelDefault)
		}
	})

	t.Run("config-file key", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "")
		home := t.TempDir()
		t.Setenv("SEEK_HOME", home)
		cfgJSON := `{"providers": {"glm": {"api_key": "from-config"}}}`
		if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		provider, _, label, modelDefault, err := buildProvider("glm", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if provider == nil || label != "GLM" || modelDefault != "glm-5.3" {
			t.Errorf("glm from config-file key: provider=%v label=%q model=%q", provider, label, modelDefault)
		}
	})

	t.Run("no key errors", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "")
		t.Setenv("SEEK_HOME", t.TempDir())
		_, _, _, _, err := buildProvider("glm", "", "")
		if err == nil || !strings.Contains(err.Error(), "no GLM API key") {
			t.Errorf("expected no-key error, got %v", err)
		}
	})

	t.Run("base-url override accepted", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "k")
		provider, _, _, _, err := buildProvider("glm", "http://localhost:9/v1/chat/completions", "")
		if err != nil {
			t.Fatalf("buildProvider with --base-url: %v", err)
		}
		if provider == nil {
			t.Errorf("nil provider with --base-url override")
		}
	})
}

// buildProvider("opencode", ...) wiring, mirroring the GLM paradigm:
// label, lane default model (id per opencode.ai/docs/console/models,
// 待核 against /v1/models with a real key), key resolution via
// OPENCODE_API_KEY → providers.opencode, and the --base-url override.
func TestBuildProvider_OpenCode(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())

	t.Run("env key, default label and model", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "k")
		provider, dsClient, label, modelDefault, err := buildProvider("opencode", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if dsClient != nil {
			t.Errorf("opencode must not build a deepseek client")
		}
		if provider == nil || provider.Name() != "OpenCode" {
			t.Errorf("provider label = %v, want OpenCode", provider)
		}
		if label != "OpenCode" {
			t.Errorf("provLabel = %q, want \"OpenCode\"", label)
		}
		if modelDefault != "deepseek-v4.1-flash" {
			t.Errorf("modelDefault = %q, want deepseek-v4.1-flash", modelDefault)
		}
	})

	t.Run("config-file key", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "")
		home := t.TempDir()
		t.Setenv("SEEK_HOME", home)
		cfgJSON := `{"providers": {"opencode": {"api_key": "from-config"}}}`
		if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		provider, _, label, modelDefault, err := buildProvider("opencode", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if provider == nil || label != "OpenCode" || modelDefault != "deepseek-v4.1-flash" {
			t.Errorf("opencode from config-file key: provider=%v label=%q model=%q", provider, label, modelDefault)
		}
	})

	t.Run("no key errors", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "")
		t.Setenv("SEEK_HOME", t.TempDir())
		_, _, _, _, err := buildProvider("opencode", "", "")
		if err == nil || !strings.Contains(err.Error(), "no OpenCode API key") {
			t.Errorf("expected no-key error, got %v", err)
		}
	})

	t.Run("base-url override accepted", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "k")
		provider, _, _, _, err := buildProvider("opencode", "http://localhost:9/v1/chat/completions", "")
		if err != nil {
			t.Fatalf("buildProvider with --base-url: %v", err)
		}
		if provider == nil {
			t.Errorf("nil provider with --base-url override")
		}
	})
}

// cronAutoTickEnabled has two independent off-switches: --no-cron-tick
// (explicit opt-out) and the ACP headless lane (always off — its processes
// are short-lived spawns where a detached tick goroutine is a half-fired
// cron job risk, PRD feature-paseo-provider.md §2.3). The default case
// (interactive TUI, no flag) MUST stay on: startup auto-tick is what makes
// routines fire "when seek IS open" without an OS scheduler.
func TestCronAutoTickEnabled(t *testing.T) {
	tests := []struct {
		name       string
		noCronTick bool
		acpLane    bool
		want       bool
	}{
		{"interactive default: tick fires", false, false, true},
		{"--no-cron-tick opts out", true, false, false},
		{"acp headless lane skips", false, true, false},
		{"acp lane with flag (flag inexpressible there): still off", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronAutoTickEnabled(tt.noCronTick, tt.acpLane); got != tt.want {
				t.Errorf("cronAutoTickEnabled(noCronTick=%v, acpLane=%v) = %v, want %v",
					tt.noCronTick, tt.acpLane, got, tt.want)
			}
		})
	}
}

// mcpSectionFromBridges is the MCP-D3 wiring boundary: bridges from
// LoadServers plus the parsed mcp.json go in, the sysprompt MCPSection
// bytes come out. The copy bytes themselves are locked by
// internal/tools/mcptool's own suite; what's proven here is the three
// seams that suite cannot see — (1) an mcp.json "hint" reaches the
// section and beats the built-in semble copy, (2) an explicit
// "mcp_prompt": false yields an empty section (tools register before
// this call, so only the guidance disappears — the A2 eval arm), and
// (3) server blocks are byte-stable regardless of bridge order while
// each server's tools keep bridge registration order.
func TestMCPSectionFromBridges(t *testing.T) {
	bridges := func() []*mcptool.Bridge {
		return []*mcptool.Bridge{
			mcptool.New(nil, "semble", mcp.ToolDef{
				Name:        "search",
				Description: "Search code in a repo using hybrid BM25 + semantic retrieval.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"},"query":{"type":"string"}},"required":["repo","query"]}`),
			}, "semble__search"),
			mcptool.New(nil, "semble", mcp.ToolDef{
				Name:        "find_related",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"line":{"type":"integer"}},"required":["file","line"]}`),
			}, "semble__find_related"),
			mcptool.New(nil, "zeta", mcp.ToolDef{Name: "search"}, "zeta__search"),
		}
	}
	noPromptConfig := func() mcpconfig.Config {
		servers := map[string]mcpconfig.ServerEntry{"semble": {}, "zeta": {}}
		return mcpconfig.Config{MCPServers: servers}
	}

	t.Run("no hint: built-in semble copy, tools in registration order", func(t *testing.T) {
		got := mcpSectionFromBridges(bridges(), noPromptConfig())
		if !strings.Contains(got, "- semble (semantic code search, local & indexed):") {
			t.Errorf("built-in semble copy missing:\n%s", got)
		}
		if !strings.Contains(got, "  Tools: semble__search(repo, query), semble__find_related(file, line).") {
			t.Errorf("tools line wrong or not in registration order:\n%s", got)
		}
		// zeta is unknown to seek and has no hint → tier-3 listing, and
		// dictionary order puts semble before zeta.
		if !strings.Contains(got, "- zeta (external MCP server):") {
			t.Errorf("tier-3 listing missing for zeta:\n%s", got)
		}
		if si, zi := strings.Index(got, "- semble"), strings.Index(got, "- zeta"); !(si >= 0 && si < zi) {
			t.Errorf("servers not in dictionary order (semble@%d zeta@%d):\n%s", si, zi, got)
		}
	})

	t.Run("hint from mcp.json overrides built-in copy", func(t *testing.T) {
		cfg := noPromptConfig()
		entry := cfg.MCPServers["semble"]
		entry.Hint = "Prefer semble for intent queries\nnot for exact symbols"
		cfg.MCPServers["semble"] = entry

		got := mcpSectionFromBridges(bridges(), cfg)
		if !strings.Contains(got, "- semble:\n") {
			t.Errorf("hinted server block missing:\n%s", got)
		}
		if !strings.Contains(got, "  Prefer semble for intent queries\n  not for exact symbols\n") {
			t.Errorf("hint not rendered verbatim:\n%s", got)
		}
		if strings.Contains(got, "semantic code search") {
			t.Errorf("built-in copy leaked through a tier-1 hint:\n%s", got)
		}
	})

	t.Run("mcp_prompt false yields empty section", func(t *testing.T) {
		cfg := noPromptConfig()
		off := false
		cfg.MCPPrompt = &off
		if got := mcpSectionFromBridges(bridges(), cfg); got != "" {
			t.Errorf("mcp_prompt:false must suppress the section, got:\n%s", got)
		}
		// Explicit true is the same as absent: the section is assembled.
		on := true
		cfg.MCPPrompt = &on
		if got := mcpSectionFromBridges(bridges(), cfg); got == "" {
			t.Error("mcp_prompt:true must keep the section")
		}
	})

	t.Run("bridge order does not change bytes", func(t *testing.T) {
		in := bridges()
		want := mcpSectionFromBridges(in, noPromptConfig())
		// Shuffle across servers only: within one server, registration
		// order is part of the contract (asserted above).
		shuffled := []*mcptool.Bridge{in[2], in[0], in[1]}
		if got := mcpSectionFromBridges(shuffled, noPromptConfig()); got != want {
			t.Errorf("bridge order leaked into the section:\n--- want ---\n%s\n--- got ---\n%s", want, got)
		}
	})

	t.Run("empty bridges yield empty section", func(t *testing.T) {
		if got := mcpSectionFromBridges(nil, noPromptConfig()); got != "" {
			t.Errorf("mcpSectionFromBridges(nil) = %q, want empty", got)
		}
		if got := mcpSectionFromBridges([]*mcptool.Bridge{}, noPromptConfig()); got != "" {
			t.Errorf("mcpSectionFromBridges(empty) = %q, want empty", got)
		}
	})
}
