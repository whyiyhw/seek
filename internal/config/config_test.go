package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withSeekHome points $SEEK_HOME at a fresh temp dir for the test's
// duration so each test gets a clean config file location.
func withSeekHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, had := os.LookupEnv("SEEK_HOME")
	_ = os.Setenv("SEEK_HOME", dir)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEEK_HOME", prev)
		} else {
			_ = os.Unsetenv("SEEK_HOME")
		}
	})
	return dir
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	// Pre-condition: no config file under SEEK_HOME.
	withSeekHome(t)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file should be ok, got %v", err)
	}
	if got.DefaultProvider != "" || len(got.Providers) != 0 {
		t.Errorf("expected zero Config, got %+v", got)
	}
}

func TestSave_LoadRoundTrip(t *testing.T) {
	withSeekHome(t)
	cfg := Config{
		DefaultProvider: "deepseek",
		Providers: map[string]ProviderConfig{
			"deepseek":  {APIKey: "sk-deepseek-xxx"},
			"anthropic": {APIKey: "sk-ant-yyy"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DefaultProvider != "deepseek" {
		t.Errorf("DefaultProvider lost: %q", got.DefaultProvider)
	}
	if got.Providers["deepseek"].APIKey != "sk-deepseek-xxx" {
		t.Errorf("deepseek key lost: %+v", got.Providers["deepseek"])
	}
	if got.Providers["anthropic"].APIKey != "sk-ant-yyy" {
		t.Errorf("anthropic key lost: %+v", got.Providers["anthropic"])
	}
}

func TestSave_File0600(t *testing.T) {
	// API keys must not be world-readable. POSIX-only test (Windows
	// doesn't have the same mode semantics; on Windows os.WriteFile
	// returns 0o666 & ~umask regardless of the mode argument, so the
	// guarantee there is "ACL-default", which is also user-only).
	if isWindows() {
		t.Skip("0600 semantics don't apply on Windows")
	}
	withSeekHome(t)
	if err := Save(Config{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path, _ := Path()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("config file perms = %o, want 0600", mode)
	}
}

func TestKeyFor_EnvBeatsConfig(t *testing.T) {
	// env var must win — CI / secret managers / temporary overrides
	// rely on that precedence.
	withSeekHome(t)
	cfg := Config{Providers: map[string]ProviderConfig{
		"deepseek": {APIKey: "from-config"},
	}}
	_ = os.Setenv("DEEPSEEK_API_KEY", "from-env")
	t.Cleanup(func() { _ = os.Unsetenv("DEEPSEEK_API_KEY") })

	if got := KeyFor(cfg, "deepseek"); got != "from-env" {
		t.Errorf("env should win, got %q", got)
	}
}

func TestKeyFor_FallsBackToConfig(t *testing.T) {
	withSeekHome(t)
	_ = os.Unsetenv("DEEPSEEK_API_KEY")
	cfg := Config{Providers: map[string]ProviderConfig{
		"deepseek": {APIKey: "from-config"},
	}}
	if got := KeyFor(cfg, "deepseek"); got != "from-config" {
		t.Errorf("config fallback failed, got %q", got)
	}
}

func TestKeyFor_NotFound(t *testing.T) {
	withSeekHome(t)
	_ = os.Unsetenv("DEEPSEEK_API_KEY")
	if got := KeyFor(Config{}, "deepseek"); got != "" {
		t.Errorf("expected empty for missing key, got %q", got)
	}
}

func TestKeyFor_UnknownProviderOnlyChecksConfig(t *testing.T) {
	// "Compatible" / custom names don't have a canonical env var, so
	// KeyFor should not blow up — just consult the config.
	withSeekHome(t)
	cfg := Config{Providers: map[string]ProviderConfig{
		"my-custom": {APIKey: "abc"},
	}}
	if got := KeyFor(cfg, "my-custom"); got != "abc" {
		t.Errorf("custom-provider lookup failed: %q", got)
	}
}

func TestSetKey_CreatesMap(t *testing.T) {
	// nil Providers map should not panic — SetKey allocates.
	cfg := Config{}
	SetKey(&cfg, "deepseek", "sk-xxx")
	if cfg.Providers["deepseek"].APIKey != "sk-xxx" {
		t.Errorf("SetKey didn't persist: %+v", cfg.Providers)
	}
}

func TestPath_UnderSeekHome(t *testing.T) {
	dir := withSeekHome(t)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, Filename)
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestReadLimits_DefaultsAndOverrides(t *testing.T) {
	var c Config
	if got := c.ReadMaxLimit(); got != 200 {
		t.Errorf("default ReadMaxLimit = %d, want 200", got)
	}
	if got := c.ReadWholeReadBytes(); got != 32*1024 {
		t.Errorf("default ReadWholeReadBytes = %d, want %d", got, 32*1024)
	}

	c.Read = &ReadConfig{MaxLimit: 500, WholeReadBytes: 4096}
	if got := c.ReadMaxLimit(); got != 500 {
		t.Errorf("ReadMaxLimit = %d, want 500", got)
	}
	if got := c.ReadWholeReadBytes(); got != 4096 {
		t.Errorf("ReadWholeReadBytes = %d, want 4096", got)
	}

	// Zero values must fall back to defaults, not clamp to zero.
	c.Read = &ReadConfig{}
	if got := c.ReadMaxLimit(); got != 200 {
		t.Errorf("zero ReadMaxLimit = %d, want 200", got)
	}
}

// isWindows is split out so the perm-check test reads cleanly. We
// avoid runtime.GOOS at the call site to keep the test body
// platform-agnostic in shape.
func isWindows() bool {
	return os.PathSeparator == '\\'
}
