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

func TestKeyFor_GLMEnvVar(t *testing.T) {
	// glm has a canonical env var (GLM_API_KEY) that beats the config
	// file, same precedence shape as the other named providers.
	withSeekHome(t)
	_ = os.Unsetenv("GLM_API_KEY")
	cfg := Config{Providers: map[string]ProviderConfig{
		"glm": {APIKey: "from-config"},
	}}
	if got := KeyFor(cfg, "glm"); got != "from-config" {
		t.Errorf("glm config fallback failed, got %q", got)
	}
	_ = os.Setenv("GLM_API_KEY", "from-env")
	t.Cleanup(func() { _ = os.Unsetenv("GLM_API_KEY") })
	if got := KeyFor(cfg, "glm"); got != "from-env" {
		t.Errorf("glm env should win, got %q", got)
	}
}

// TestBaseURLFor_GLMPrecedence: endpoint overrides follow the same
// env > config precedence as keys. This is the Coding Plan escape hatch:
// a coding-plan key must be pointed at /api/coding/paas/v4 or it bills
// the recharge balance / fails with 1113.
func TestBaseURLFor_GLMPrecedence(t *testing.T) {
	withSeekHome(t)
	_ = os.Unsetenv("GLM_BASE_URL")
	cfg := Config{Providers: map[string]ProviderConfig{
		"glm": {APIKey: "k", BaseURL: "https://from-config.example/v4/chat/completions"},
	}}
	if got := BaseURLFor(cfg, "glm"); got != "https://from-config.example/v4/chat/completions" {
		t.Errorf("config fallback failed, got %q", got)
	}
	_ = os.Setenv("GLM_BASE_URL", "https://from-env.example/v4/chat/completions")
	t.Cleanup(func() { _ = os.Unsetenv("GLM_BASE_URL") })
	if got := BaseURLFor(cfg, "glm"); got != "https://from-env.example/v4/chat/completions" {
		t.Errorf("glm env override should win, got %q", got)
	}
}

func TestBaseURLFor_NoOverride(t *testing.T) {
	// Empty means "no override": the lane's compiled default applies.
	// A key-only provider block must not read as a URL, and unknown
	// providers have no canonical env var to consult.
	withSeekHome(t)
	_ = os.Unsetenv("GLM_BASE_URL")
	if got := BaseURLFor(Config{}, "glm"); got != "" {
		t.Errorf("missing override = %q, want empty", got)
	}
	cfg := Config{Providers: map[string]ProviderConfig{"glm": {APIKey: "k"}}}
	if got := BaseURLFor(cfg, "glm"); got != "" {
		t.Errorf("key-only block = %q, want empty", got)
	}
	if got := BaseURLFor(Config{}, "my-custom"); got != "" {
		t.Errorf("unknown provider = %q, want empty", got)
	}
}

// TestProviderConfig_BaseURLRoundTrip: base_url is a hand-edited field —
// it must survive Save/Load, and an api_key-only block (every config
// written before this field existed) must still load clean.
func TestProviderConfig_BaseURLRoundTrip(t *testing.T) {
	withSeekHome(t)
	const wantURL = "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"
	cfg := Config{Providers: map[string]ProviderConfig{
		"glm": {APIKey: "k", BaseURL: wantURL},
	}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Providers["glm"].BaseURL != wantURL || got.Providers["glm"].APIKey != "k" {
		t.Errorf("glm block round-trip: %+v", got.Providers["glm"])
	}

	dir := withSeekHome(t)
	pre := `{"providers": {"glm": {"api_key": "old"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := Load()
	if err != nil {
		t.Fatalf("pre-base_url config must load clean: %v", err)
	}
	if old.Providers["glm"].BaseURL != "" || old.Providers["glm"].APIKey != "old" {
		t.Errorf("pre-base_url block parsed wrong: %+v", old.Providers["glm"])
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

// TestLoad_LegacyOCRSectionIgnored: the ocr section was removed with
// pillar Q's decommission (feature-vision M-V.0). Configs written by
// older builds still carry it; the plain json.Unmarshal loader must
// ignore it silently rather than erroring the startup path.
func TestLoad_LegacyOCRSectionIgnored(t *testing.T) {
	dir := withSeekHome(t)
	legacy := `{"providers": {"deepseek": {"api_key": "sk-x"}}, "ocr": {"enabled": true, "command": "/usr/bin/tesseract", "timeout_seconds": 15}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("legacy ocr section must not break Load: %v", err)
	}
	if cfg.Providers["deepseek"].APIKey != "sk-x" {
		t.Errorf("adjacent fields must still parse: %+v", cfg)
	}
}

// TestLoad_NoServeSection_Compat: every config written before the
// remote control plane exists has no `serve` key. Loading one must not
// error and must read as "all defaults" (nil section, yolo off) —
// top-level backward compat is the M1b acceptance bar.
func TestLoad_NoServeSection_Compat(t *testing.T) {
	dir := withSeekHome(t)
	old := `{"default_provider": "deepseek", "providers": {"deepseek": {"api_key": "sk-x"}}, "language": "zh"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("old config without serve section must load clean: %v", err)
	}
	if cfg.Serve != nil {
		t.Errorf("absent serve section must load as nil, got %+v", cfg.Serve)
	}
	if cfg.ServeYolo() {
		t.Error("no serve section must mean yolo off (D-C5 safe default)")
	}
}

// TestServeConfig_RoundTrip: a hand-edited serve section survives
// Save/Load byte-faithfully — this file is user-writable, so the
// section must round-trip without the wizard's help.
func TestServeConfig_RoundTrip(t *testing.T) {
	withSeekHome(t)
	cfg := Config{
		Serve: &ServeConfig{
			Projects:       []string{"/Users/me/code/seek", "/Users/me/code/butler"},
			ProjectRoots:   []string{"/srv/code"}, // c_heffj3: picker reach widens via this line
			MaxConcurrency: 8,
			Yolo:           true,
			WebAddr:        "100.80.25.74:8790",
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Serve == nil {
		t.Fatal("serve section lost in round-trip")
	}
	if len(got.Serve.Projects) != 2 || got.Serve.Projects[0] != "/Users/me/code/seek" {
		t.Errorf("projects round-trip: %+v", got.Serve.Projects)
	}
	if got.Serve.MaxConcurrency != 8 {
		t.Errorf("max_concurrency round-trip: %d", got.Serve.MaxConcurrency)
	}
	if !got.ServeYolo() {
		t.Error("yolo round-trip")
	}
	if got.Serve.WebAddr != "100.80.25.74:8790" {
		t.Errorf("web_addr round-trip: %q", got.Serve.WebAddr)
	}
	if len(got.Serve.ProjectRoots) != 1 || got.Serve.ProjectRoots[0] != "/srv/code" {
		t.Errorf("project_roots round-trip: %+v", got.Serve.ProjectRoots)
	}
}

// TestServeYolo_DefaultOff: the exposure switch defaults OFF even when
// the section exists but leaves yolo unset (D-C5).
func TestServeYolo_DefaultOff(t *testing.T) {
	if (Config{Serve: &ServeConfig{MaxConcurrency: 4}}).ServeYolo() {
		t.Error("yolo must default off")
	}
}

// TestAgentsConfig_RoundTrip: the `agents` section (P2-c default
// routing, feature-orchestration.md D6/D14) survives save/load and
// stays absent for configs written before the section existed.
func TestAgentsConfig_RoundTrip(t *testing.T) {
	withSeekHome(t)
	cfg := Config{
		Agents: map[string]AgentConfig{
			"explore": {Provider: "glm", Model: "glm-5.3", Effort: "low", MaxTurns: 60},
			"plan":    {Provider: "glm"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got.Agents["explore"]
	if !ok {
		t.Fatal("agents section lost in round-trip")
	}
	if e.Provider != "glm" || e.Model != "glm-5.3" || e.Effort != "low" || e.MaxTurns != 60 {
		t.Errorf("explore entry round-trip: %+v", e)
	}
	if p := got.Agents["plan"]; p.Provider != "glm" || p.Model != "" {
		t.Errorf("plan entry round-trip: %+v", p)
	}
}

// TestSafetyCheckLevel_OnlyHighOptsIn pins the SC1 mapping
// (feature-deepseek-app-api.md §5): the config key is a string so future
// tiers can join, but today exactly one token — "high" — enables the
// model safety check. Anything else reads as off; a typo must never
// forward a half-supported value to the API.
func TestSafetyCheckLevel_OnlyHighOptsIn(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"high", "HIGH", " High ", "\thigh\n"} {
		if got := (Config{SafetyCheck: s}).SafetyCheckLevel(); got != "high" {
			t.Errorf("SafetyCheckLevel(%q) = %q, want %q", s, got, "high")
		}
	}
	for _, s := range []string{"", "low", "medium", "max", "off", "true", "1", "high,low", "high risk"} {
		if got := (Config{SafetyCheck: s}).SafetyCheckLevel(); got != "" {
			t.Errorf("SafetyCheckLevel(%q) = %q, want off", s, got)
		}
	}
}

// TestSafetyCheckConfig_RoundTrip: the hand-editable key survives
// Save/Load, and a config written before SC1 (no key) loads as "off" —
// top-level backward compat is the file-format contract.
func TestSafetyCheckConfig_RoundTrip(t *testing.T) {
	withSeekHome(t)
	if err := Save(Config{SafetyCheck: "high"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SafetyCheck != "high" || got.SafetyCheckLevel() != "high" {
		t.Errorf("safety_check round-trip: field=%q level=%q", got.SafetyCheck, got.SafetyCheckLevel())
	}

	dir := withSeekHome(t)
	pre := `{"default_provider": "deepseek", "providers": {"deepseek": {"api_key": "sk-x"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := Load()
	if err != nil {
		t.Fatalf("pre-SC1 config must load clean: %v", err)
	}
	if old.SafetyCheckLevel() != "" {
		t.Errorf("absent safety_check must mean off, got %q", old.SafetyCheckLevel())
	}
}

// TestAgentsConfig_AbsentSectionCompat: a pre-P2-c config (no agents
// key) loads with a nil map — unknown top-level keys never error, and
// nil is the "built-ins only" signal cmd/seek branches on.
func TestAgentsConfig_AbsentSectionCompat(t *testing.T) {
	withSeekHome(t)
	cfg := Config{Language: "zh"} // no Agents
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Agents != nil {
		t.Errorf("Agents = %v, want nil for a config without the section", got.Agents)
	}
}

// ---- auto_compact ----

// TestAutoCompact_AbsentSectionIsOff: a config without the section
// (every config written before the feature) must resolve to 0 — off —
// for any model. Auto-compaction rewrites the conversation, so the
// default has to be "no".
func TestAutoCompact_AbsentSectionIsOff(t *testing.T) {
	withSeekHome(t)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if n := got.AutoCompactThreshold("deepseek-flash"); n != 0 {
		t.Errorf("absent section: threshold = %d, want 0 (off)", n)
	}
}

// TestAutoCompact_ThresholdResolution pins the two ways of saying "when"
// plus the floor: at_tokens is model-independent and wins, at_fraction
// scales the model's window, and anything under the 1k floor is off.
func TestAutoCompact_ThresholdResolution(t *testing.T) {
	t.Setenv("SEEK_AUTO_COMPACT", "") // empty = "nothing here", file's setting stands

	cases := []struct {
		name  string
		cfg   AutoCompactConfig
		model string
		want  int
	}{
		{"tokens", AutoCompactConfig{AtTokens: 350_000}, "deepseek-flash", 350_000},
		{"fraction of 1M", AutoCompactConfig{AtFraction: 0.35}, "deepseek-flash", 350_000},
		{"tokens win", AutoCompactConfig{AtTokens: 350_000, AtFraction: 0.5}, "deepseek-flash", 350_000},
		{"fraction follows window", AutoCompactConfig{AtFraction: 0.35}, "gpt-4o", 44_800},
		{"both zero = off", AutoCompactConfig{}, "deepseek-flash", 0},
		{"below the floor = off", AutoCompactConfig{AtTokens: 500}, "deepseek-flash", 0},
	}
	for _, c := range cases {
		cfg := Config{AutoCompact: &c.cfg}
		if got := cfg.AutoCompactThreshold(c.model); got != c.want {
			t.Errorf("%s: threshold = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestAutoCompact_EnvOverride: $SEEK_AUTO_COMPACT beats the file for one
// launch — a dot is a fraction, a plain integer is an absolute count, and
// "off" / "0" disables. An unparsable value leaves the file's setting
// standing rather than guessing.
func TestAutoCompact_EnvOverride(t *testing.T) {
	cfg := Config{AutoCompact: &AutoCompactConfig{AtTokens: 200_000}}

	cases := []struct {
		env   string
		model string
		want  int
	}{
		{"0.35", "deepseek-flash", 350_000},
		{"350000", "deepseek-flash", 350_000},
		{"off", "deepseek-flash", 0},
		{"0", "deepseek-flash", 0},
		{"not-a-number", "deepseek-flash", 200_000}, // file's setting stands
	}
	for _, c := range cases {
		t.Setenv("SEEK_AUTO_COMPACT", c.env)
		if got := cfg.AutoCompactThreshold(c.model); got != c.want {
			t.Errorf("SEEK_AUTO_COMPACT=%q: threshold = %d, want %d", c.env, got, c.want)
		}
	}
}

// TestAutoCompact_RoundTrip: the section survives Save → Load, and an
// older binary ignoring the key is covered by the omitempty contract
// (absent section loads as nil, tested above).
func TestAutoCompact_RoundTrip(t *testing.T) {
	withSeekHome(t)
	cfg := Config{AutoCompact: &AutoCompactConfig{AtTokens: 350_000, AtFraction: 0.35}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoCompact == nil {
		t.Fatal("auto_compact section lost on round trip")
	}
	if got.AutoCompact.AtTokens != 350_000 || got.AutoCompact.AtFraction != 0.35 {
		t.Errorf("auto_compact = %+v, want {350000 0.35}", *got.AutoCompact)
	}
}
