package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withEnv temporarily sets (or unsets, when value=="") an environment
// variable for the duration of a test, restoring the prior value via
// t.Cleanup so parallel test failures don't leak state.
func withEnv(t *testing.T, key, value string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if value == "" {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, value)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestHome_DefaultsToDotSeek(t *testing.T) {
	withEnv(t, envHome, "") // ensure no override
	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+".seek") {
		t.Errorf("expected path ending in /.seek, got %q", got)
	}
}

func TestHome_RespectsSEEK_HOME(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)
	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if got != override {
		t.Errorf("Home() = %q, want %q", got, override)
	}
}

func TestSubdirs_ComposeUnderHome(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)

	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"Sessions", Sessions, filepath.Join(override, "sessions")},
		{"MCPConfig", MCPConfig, filepath.Join(override, "mcp.json")},
		{"UserSkills", UserSkills, filepath.Join(override, "skills")},
		{"UserHooksToml", UserHooksToml, filepath.Join(override, "hooks.toml")},
		{"TrustedProjectsJSON", TrustedProjectsJSON, filepath.Join(override, "trusted-projects.json")},
		{"HooksAuditLog", HooksAuditLog, filepath.Join(override, "hooks-audit.jsonl")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			if err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestProjectID_Deterministic(t *testing.T) {
	a := ProjectID("/Users/whyiyhw/code/github/seek")
	b := ProjectID("/Users/whyiyhw/code/github/seek")
	if a != b {
		t.Errorf("ProjectID should be deterministic, got %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("ProjectID should be 16 hex chars, got %d (%q)", len(a), a)
	}
	for _, c := range a {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("ProjectID contains non-hex char %q in %q", c, a)
		}
	}
}

func TestProjectID_DifferentPathsDiffer(t *testing.T) {
	a := ProjectID("/Users/x/projectA")
	b := ProjectID("/Users/x/projectB")
	if a == b {
		t.Errorf("ProjectID collided on different paths: both = %q", a)
	}
}

func TestProjectDir_ComposesUnderProjects(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)
	got, err := ProjectDir("/abs/path/to/project")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(override, "projects", ProjectID("/abs/path/to/project"))
	if got != expected {
		t.Errorf("ProjectDir = %q, want %q", got, expected)
	}
}

func TestProjectPlans_ComposesUnderProjectDir(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)
	got, err := ProjectPlans("/abs/path/to/project")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(override, "projects", ProjectID("/abs/path/to/project"), "plans")
	if got != expected {
		t.Errorf("ProjectPlans = %q, want %q", got, expected)
	}
}

func TestSessionCheckpointDir_ComposesUnderProjectSessions(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)
	got, err := SessionCheckpointDir("/abs/path/to/project", "sid123")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(override, "projects",
		ProjectID("/abs/path/to/project"), "sessions", "sid123")
	if got != want {
		t.Errorf("SessionCheckpointDir = %q, want %q", got, want)
	}
}

func TestSessionCheckpointDir_RequiresSessionID(t *testing.T) {
	_, err := SessionCheckpointDir("/abs/path", "")
	if err == nil {
		t.Fatal("expected error for empty session id, got nil")
	}
}

func TestSubagentsIndex_ComposesUnderProjectDir(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)
	got, err := SubagentsIndex("/abs/path/to/project")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(override, "projects",
		ProjectID("/abs/path/to/project"), "subagents.jsonl")
	if got != want {
		t.Errorf("SubagentsIndex = %q, want %q", got, want)
	}
}

func TestSubagentSessionDir_ComposesUnderProjectSessions(t *testing.T) {
	override := t.TempDir()
	withEnv(t, envHome, override)
	got, err := SubagentSessionDir("/abs/path/to/project", "sid123", "sub456")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(override, "projects",
		ProjectID("/abs/path/to/project"), "sessions", "sid123", "subagents", "sub456")
	if got != want {
		t.Errorf("SubagentSessionDir = %q, want %q", got, want)
	}
}

func TestSubagentSessionDir_RequiresBothIDs(t *testing.T) {
	if _, err := SubagentSessionDir("/abs", "", "sub"); err == nil {
		t.Error("expected error for empty parent sid")
	}
	if _, err := SubagentSessionDir("/abs", "sid", ""); err == nil {
		t.Error("expected error for empty subagent sid")
	}
}

func TestProjectHooksToml_ComposesUnderSeekDir(t *testing.T) {
	got := ProjectHooksToml("/abs/path/to/project")
	want := filepath.Join("/abs/path/to/project", ".seek", "hooks.toml")
	if got != want {
		t.Errorf("ProjectHooksToml = %q, want %q", got, want)
	}
}

func TestHome_IgnoresXDG(t *testing.T) {
	// Pre-v1.0 versions read $XDG_CONFIG_HOME. Pin the new behaviour
	// so a future "let's support XDG again" change has to consciously
	// revisit this test.
	withEnv(t, envHome, "")
	withEnv(t, "XDG_CONFIG_HOME", "/this/should/be/ignored")
	got, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if strings.Contains(got, "ignored") {
		t.Errorf("XDG_CONFIG_HOME leaked into Home(): %q", got)
	}
}
