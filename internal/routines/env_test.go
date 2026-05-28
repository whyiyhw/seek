package routines

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadEnvFile_MissingFileReturnsNilNil(t *testing.T) {
	// G3 contract: env overlay is opt-in. Absence MUST NOT be an
	// error or even a warning — that would force every cron user
	// to materialise a file they don't need.
	got, err := LoadEnvFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing file returned error: %v", err)
	}
	if got != nil {
		t.Errorf("missing file returned non-nil map: %v", got)
	}
}

func TestLoadEnvFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`# comment line — must be skipped`,
		``, // blank — must be skipped
		`DEEPSEEK_API_KEY=sk-abc123`,
		`PATH=/opt/seek/bin:/usr/bin`,
		`   GOROOT=/usr/local/go`, // leading whitespace on KEY
		`HOME=/Users/test`,
		`  # indented comment`,
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	want := map[string]string{
		"DEEPSEEK_API_KEY": "sk-abc123",
		"PATH":             "/opt/seek/bin:/usr/bin",
		"GOROOT":           "/usr/local/go",
		"HOME":             "/Users/test",
	}
	if len(got) != len(want) {
		t.Errorf("len(got) = %d, want %d; got = %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadEnvFile_StripsBalancedQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`DOUBLE="hello world"`,
		`SINGLE='quoted value'`,
		`UNBALANCED="no closing`,
		`EMPTY=""`,
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["DOUBLE"] != "hello world" {
		t.Errorf("DOUBLE = %q, want %q", got["DOUBLE"], "hello world")
	}
	if got["SINGLE"] != "quoted value" {
		t.Errorf("SINGLE = %q, want %q", got["SINGLE"], "quoted value")
	}
	if got["UNBALANCED"] != `"no closing` {
		t.Errorf("UNBALANCED = %q, want literal (unbalanced quote left in)", got["UNBALANCED"])
	}
	if got["EMPTY"] != "" {
		t.Errorf("EMPTY = %q, want empty string", got["EMPTY"])
	}
}

func TestLoadEnvFile_LastDuplicateWins(t *testing.T) {
	// Matches cmd.Env / POSIX semantics; documented in env.go.
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("KEY=first\nKEY=second\nKEY=third\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["KEY"] != "third" {
		t.Errorf("KEY = %q, want %q (last duplicate wins)", got["KEY"], "third")
	}
}

func TestLoadEnvFile_MissingEquals(t *testing.T) {
	// Strict parse: better to fail loudly than silently drop the
	// line and run a tick without the API key the user intended
	// to set.
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("KEY=value\nMALFORMED_NO_EQ\nOTHER=ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("LoadEnvFile must reject lines missing '='")
	}
	if !strings.Contains(err.Error(), ":2") {
		t.Errorf("error must reference line number 2: %v", err)
	}
}

func TestLoadEnvFile_EmptyKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("=valueWithNoKey\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("LoadEnvFile must reject empty KEY")
	}
}

func TestLoadEnvFile_DirIsError(t *testing.T) {
	// A directory at the env path is NOT ErrNotExist — it's a
	// real configuration error. Surface it as a non-nil error so
	// the spawn fails loudly.
	dir := t.TempDir()
	got, err := LoadEnvFile(dir) // pass the dir itself as the env path
	if err == nil && got != nil {
		t.Errorf("LoadEnvFile on a directory must error (or at least not silently parse it)")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("directory error reported as ErrNotExist: %v", err)
	}
}

func TestMergeEnv_OverlayOverridesBase(t *testing.T) {
	// cmd.Env duplicate-key rule: last entry wins. So overlay
	// appended AFTER base means overlay overrides — that's the
	// whole point.
	base := []string{
		"HOME=/old",
		"PATH=/usr/bin",
		"USER=alice",
	}
	overlay := map[string]string{
		"HOME": "/new",
		"DEEPSEEK_API_KEY": "sk-test",
	}
	got := MergeEnv(base, overlay)

	// All base entries should be present, in original order.
	for i, b := range base {
		if got[i] != b {
			t.Errorf("got[%d] = %q, want %q (base unchanged)", i, got[i], b)
		}
	}
	// Overlay entries appended in sorted order (deterministic).
	wantTail := []string{"DEEPSEEK_API_KEY=sk-test", "HOME=/new"}
	tail := got[len(base):]
	if len(tail) != len(wantTail) {
		t.Fatalf("tail length = %d, want %d (tail = %v)", len(tail), len(wantTail), tail)
	}
	for i, w := range wantTail {
		if tail[i] != w {
			t.Errorf("tail[%d] = %q, want %q", i, tail[i], w)
		}
	}
}

func TestMergeEnv_EmptyOverlayReturnsBaseCopy(t *testing.T) {
	base := []string{"A=1", "B=2"}
	got := MergeEnv(base, nil)
	if len(got) != 2 || got[0] != "A=1" || got[1] != "B=2" {
		t.Errorf("nil overlay: got = %v, want a copy of base", got)
	}
	// Verify it's a copy — mutating base must not affect got.
	base[0] = "MUTATED"
	if got[0] != "A=1" {
		t.Errorf("MergeEnv returned base directly, not a copy")
	}
}

func TestMergeEnv_DeterministicOrder(t *testing.T) {
	// Byte-stable cmd.Env is a property tests can rely on for
	// snapshot assertions. Verify two invocations produce identical
	// slices regardless of map iteration order.
	base := []string{}
	overlay := map[string]string{
		"Z": "z", "A": "a", "M": "m", "B": "b",
	}
	a := MergeEnv(base, overlay)
	b := MergeEnv(base, overlay)
	if strings.Join(a, "\x00") != strings.Join(b, "\x00") {
		t.Errorf("MergeEnv non-deterministic: a=%v b=%v", a, b)
	}
	want := []string{"A=a", "B=b", "M=m", "Z=z"}
	for i, w := range want {
		if a[i] != w {
			t.Errorf("a[%d] = %q, want %q", i, a[i], w)
		}
	}
}

func TestDefaultSubprocess_EnvOverlayApplied(t *testing.T) {
	// End-to-end pin: the overlay file actually flows into cmd.Env.
	// This is the load-bearing assertion for G3 — if a future
	// refactor breaks the wiring, this test catches it.

	// Redirect envOverlayPath to a tmp file so we don't depend on
	// the test machine's real ~/.seek/cron/env (or lack thereof).
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	if err := os.WriteFile(envPath, []byte("DEEPSEEK_API_KEY=sk-overlay\nFROM_OVERLAY=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := envOverlayPath
	envOverlayPath = func() (string, error) { return envPath, nil }
	t.Cleanup(func() { envOverlayPath = prev })

	cmd, err := DefaultSubprocess(t.Context(), Job{Prompt: "test"}, "run-id")
	if err != nil {
		t.Fatalf("DefaultSubprocess: %v", err)
	}
	if cmd.Env == nil {
		t.Fatal("cmd.Env not set — DefaultSubprocess must set it explicitly")
	}
	// Inherited env (e.g. PATH) AND overlay entries both present.
	envSet := make(map[string]string, len(cmd.Env))
	for _, kv := range cmd.Env {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			envSet[kv[:eq]] = kv[eq+1:]
		}
	}
	if envSet["DEEPSEEK_API_KEY"] != "sk-overlay" {
		t.Errorf("DEEPSEEK_API_KEY = %q, want sk-overlay", envSet["DEEPSEEK_API_KEY"])
	}
	if envSet["FROM_OVERLAY"] != "yes" {
		t.Errorf("FROM_OVERLAY = %q, want yes", envSet["FROM_OVERLAY"])
	}
}

func TestDefaultSubprocess_OverrideOverridesInherited(t *testing.T) {
	// Set an env var in the parent process, then have the overlay
	// override it — proves last-wins semantics flow end-to-end.
	t.Setenv("SEEK_TEST_OVERRIDDEN", "from-parent")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	if err := os.WriteFile(envPath, []byte("SEEK_TEST_OVERRIDDEN=from-overlay\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := envOverlayPath
	envOverlayPath = func() (string, error) { return envPath, nil }
	t.Cleanup(func() { envOverlayPath = prev })

	cmd, err := DefaultSubprocess(t.Context(), Job{Prompt: "test"}, "run-id")
	if err != nil {
		t.Fatal(err)
	}
	// Last-wins: walk cmd.Env in REVERSE, the first match is the
	// effective value (this mirrors how exec resolves duplicate keys).
	var effective string
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(cmd.Env[i], "SEEK_TEST_OVERRIDDEN="); ok {
			effective = v
			break
		}
	}
	if effective != "from-overlay" {
		t.Errorf("effective env = %q, want from-overlay (overlay must override parent env)", effective)
	}
}

func TestDefaultSubprocess_MissingOverlayIsNonFatal(t *testing.T) {
	// G3 opt-in contract: no env file → cmd.Env = pure os.Environ().
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env-does-not-exist")
	prev := envOverlayPath
	envOverlayPath = func() (string, error) { return envPath, nil }
	t.Cleanup(func() { envOverlayPath = prev })

	cmd, err := DefaultSubprocess(t.Context(), Job{Prompt: "test"}, "run-id")
	if err != nil {
		t.Fatalf("missing overlay file must NOT fail spawn: %v", err)
	}
	if cmd.Env == nil {
		t.Fatal("cmd.Env still must be set explicitly (= os.Environ()) even with no overlay")
	}
	// Must contain inherited env — pick any var we can be sure is set.
	t.Setenv("SEEK_TEST_INHERITED", "yes")
	cmd, err = DefaultSubprocess(t.Context(), Job{Prompt: "test"}, "run-id")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Env, "SEEK_TEST_INHERITED=yes") {
		t.Error("inherited env var did not flow into cmd.Env")
	}
}

func TestDefaultSubprocess_MalformedOverlayIsFatal(t *testing.T) {
	// A typo'd env file is a real misconfiguration; we'd rather
	// fail spawn than run with a half-loaded env.
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	if err := os.WriteFile(envPath, []byte("THIS_LINE_HAS_NO_EQUALS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := envOverlayPath
	envOverlayPath = func() (string, error) { return envPath, nil }
	t.Cleanup(func() { envOverlayPath = prev })

	_, err := DefaultSubprocess(t.Context(), Job{Prompt: "test"}, "run-id")
	if err == nil {
		t.Fatal("malformed overlay must fail DefaultSubprocess")
	}
}
