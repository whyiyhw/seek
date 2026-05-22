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
