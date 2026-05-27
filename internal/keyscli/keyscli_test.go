package keyscli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withHomeOverride points paths.Home() at a temp dir for the
// duration of the test. Mirrors the pattern used by paths_test.go.
func withHomeOverride(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)
	return dir
}

func TestRun_NoArgs_PrintsHelp_ReturnsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	err := Run(nil, &out, &errOut)
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("help text missing, got %q", errOut.String())
	}
}

func TestRun_UnknownSubcommand_ReturnsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	err := Run([]string{"banana"}, &out, &errOut)
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(errOut.String(), "unknown subcommand") {
		t.Errorf("unknown-subcommand error missing, got %q", errOut.String())
	}
}

func TestRun_Actions_TextOutput(t *testing.T) {
	withHomeOverride(t)
	var out, errOut bytes.Buffer
	if err := Run([]string{"actions"}, &out, &errOut); err != nil {
		t.Fatalf("err = %v, want nil; errOut=%q", err, errOut.String())
	}
	for _, want := range []string{"submit", "interrupt", "cycle-mode", "clear-screen", "Default", "Description", "keybindings.toml"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("actions output missing %q; full output=%q", want, out.String())
		}
	}
}

func TestRun_Actions_JSON(t *testing.T) {
	withHomeOverride(t)
	var out, errOut bytes.Buffer
	if err := Run([]string{"actions", "--json"}, &out, &errOut); err != nil {
		t.Fatalf("err = %v, want nil; errOut=%q", err, errOut.String())
	}
	s := strings.TrimSpace(out.String())
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		t.Errorf("output should be a JSON array, got %q", s)
	}
	if !strings.Contains(s, `"submit"`) {
		t.Errorf("JSON should mention submit action, got %q", s)
	}
}

func TestRun_List_DefaultsWhenNoFile(t *testing.T) {
	withHomeOverride(t)
	var out, errOut bytes.Buffer
	if err := Run([]string{"list"}, &out, &errOut); err != nil {
		t.Fatalf("err = %v, want nil; errOut=%q", err, errOut.String())
	}
	for _, want := range []string{"submit", "default", "enter"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output missing %q; full output=%q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "user") {
		t.Errorf("no file → no 'user' source rows; got %q", out.String())
	}
}

func TestRun_List_ShowsUserOverride(t *testing.T) {
	home := withHomeOverride(t)
	tomlPath := filepath.Join(home, "keybindings.toml")
	if err := os.WriteFile(tomlPath, []byte(`[bindings]
clear-screen = "ctrl+x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run([]string{"list"}, &out, &errOut); err != nil {
		t.Fatalf("err = %v, want nil; errOut=%q", err, errOut.String())
	}
	if !strings.Contains(out.String(), "ctrl+x") {
		t.Errorf("list should show user override key ctrl+x; full output=%q", out.String())
	}
	if !strings.Contains(out.String(), "user") {
		t.Errorf("list should label rebound row as user; full output=%q", out.String())
	}
}

func TestRun_Check_GoodFile_ReturnsNil(t *testing.T) {
	home := withHomeOverride(t)
	tomlPath := filepath.Join(home, "keybindings.toml")
	if err := os.WriteFile(tomlPath, []byte(`[bindings]
clear-screen = "ctrl+x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run([]string{"check"}, &out, &errOut); err != nil {
		t.Errorf("err = %v, want nil; errOut=%q", err, errOut.String())
	}
	if !strings.Contains(out.String(), "ok:") {
		t.Errorf("good file should print 'ok:', got %q", out.String())
	}
}

func TestRun_Check_BadFile_ReturnsUsageError(t *testing.T) {
	home := withHomeOverride(t)
	tomlPath := filepath.Join(home, "keybindings.toml")
	if err := os.WriteFile(tomlPath, []byte(`[bindings]
typoaction = "ctrl+x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := Run([]string{"check"}, &out, &errOut)
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage (validation error); errOut=%q", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "unknown action") {
		t.Errorf("errOut should describe the validation issue, got %q", errOut.String())
	}
}

func TestRun_Check_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(tomlPath, []byte(`[bindings]
clear-screen = "ctrl+x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run([]string{"check", tomlPath}, &out, &errOut); err != nil {
		t.Errorf("err = %v, want nil; errOut=%q", err, errOut.String())
	}
	if !strings.Contains(out.String(), tomlPath) {
		t.Errorf("ok message should include the explicit path; got %q", out.String())
	}
}
