package main

import (
	"os"
	"strings"
	"testing"
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
