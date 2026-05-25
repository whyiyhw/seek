package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

// requireGit skips the test if the git binary is missing — CI runners
// without git installed shouldn't fail this suite.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
}

// initRepo creates a temporary git repo with two commits so log/diff/
// status all have something to display. Returns the abs path.
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	prevWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "test@test")
	run("git", "config", "user.name", "Test User")
	run("git", "config", "commit.gpgsign", "false")
	run("git", "checkout", "-b", "main")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "a.txt")
	run("git", "commit", "-m", "first commit")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "a.txt")
	run("git", "commit", "-m", "second commit")

	return dir
}

func TestExecute_LogAllowed(t *testing.T) {
	initRepo(t)
	out, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "log",
		"args": ["--oneline"]
	}`))
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	// Two commits → at least two non-empty lines.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Errorf("expected ≥2 log lines, got %d: %q", len(lines), out)
	}
	for _, want := range []string{"first commit", "second commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q: %q", want, out)
		}
	}
}

func TestExecute_DiffAllowed(t *testing.T) {
	initRepo(t)
	out, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "diff",
		"args": ["HEAD~1", "HEAD"]
	}`))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(out, "+world") {
		t.Errorf("diff should show the added line, got: %q", out)
	}
}

func TestExecute_StatusAllowed(t *testing.T) {
	initRepo(t)
	// Make a working-tree change so status has something to report.
	if err := os.WriteFile("a.txt", []byte("hello\nworld\nchange\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "status",
		"args": ["--short"]
	}`))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("status should mention a.txt, got: %q", out)
	}
}

func TestExecute_RejectsMutatingSubcommand(t *testing.T) {
	requireGit(t)
	cases := []string{"push", "commit", "reset", "checkout", "rebase", "merge", "clean", "fetch", "pull", "clone", "rm", "mv", "add"}
	for _, sub := range cases {
		t.Run(sub, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"subcommand": sub})
			_, err := Tool{}.Execute(context.Background(), raw)
			if err == nil {
				t.Fatalf("subcommand %q must be rejected", sub)
			}
			if !strings.Contains(err.Error(), "whitelist") {
				t.Errorf("error should mention whitelist, got: %v", err)
			}
		})
	}
}

func TestExecute_RejectsBlockedArgs(t *testing.T) {
	initRepo(t)
	cases := []struct {
		name string
		args []string
	}{
		// -c lets the caller override config; well-known footgun.
		{"-c override", []string{"-c", "core.sshCommand=evil", "log"}},
		// --exec / --upload-pack invoke remote programs.
		{"--exec", []string{"--exec=/tmp/x", "log"}},
		{"--upload-pack=", []string{"--upload-pack=/tmp/x", "log"}},
		// --git-dir / --work-tree redirect git away from cwd.
		{"--git-dir", []string{"--git-dir", "/etc"}},
		// -C changes directory mid-command.
		{"-C", []string{"-C", "/etc"}},
		// --delete / -d / -D / --force / --prune are belt-and-
		// suspenders for unforeseen subcommand mutations.
		{"-d", []string{"-d", "main"}},
		{"--force", []string{"--force"}},
		// --output writes to disk.
		{"--output=", []string{"--output=/tmp/x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"subcommand": "log",
				"args":       tc.args,
			}
			raw, _ := json.Marshal(payload)
			_, err := Tool{}.Execute(context.Background(), raw)
			if err == nil {
				t.Fatalf("args %v must be rejected", tc.args)
			}
			if !strings.Contains(err.Error(), "not allowed") {
				t.Errorf("error should say 'not allowed', got: %v", err)
			}
		})
	}
}

func TestExecute_OutputCapped(t *testing.T) {
	initRepo(t)
	// Make a 700-line file so `git show HEAD:<file>` produces enough
	// output to exercise the hard cap.
	bigDir := initRepo(t) // fresh repo for this case
	bigPath := filepath.Join(bigDir, "big.txt")
	var sb strings.Builder
	for i := 0; i < 700; i++ {
		sb.WriteString("line\n")
	}
	if err := os.WriteFile(bigPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = bigDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "add", "big.txt")
	run("git", "commit", "-m", "big file")

	// max_lines=1000 should be clamped silently to 500.
	out, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "show",
		"args": ["HEAD:big.txt"],
		"max_lines": 1000
	}`))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker when output exceeds cap, got tail: %q", tail(out, 200))
	}
	// Line count of the body itself (excluding the truncation line)
	// must be ≤ hardLineCap.
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if got := len(lines); got > hardLineCap+1 { // +1 for the truncation marker line
		t.Errorf("output should be ≤ %d lines + truncation marker, got %d", hardLineCap, got)
	}
}

func TestExecute_DefaultLineCapIs100(t *testing.T) {
	initRepo(t)
	// Same setup as cap test but smaller — 200 lines, no max_lines
	// passed → should clamp to defaultLineCap=100.
	cwd, _ := os.Getwd()
	bigPath := filepath.Join(cwd, "small.txt")
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("x\n")
	}
	if err := os.WriteFile(bigPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "add", "small.txt")
	run("git", "commit", "-m", "small file")

	out, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "show",
		"args": ["HEAD:small.txt"]
	}`))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("200-line file with default cap=100 should truncate; got tail: %q", tail(out, 200))
	}
}

func TestExecute_RejectsUnknownSubcommand(t *testing.T) {
	_, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "nope"
	}`))
	if err == nil {
		t.Fatal("unknown subcommand must be rejected")
	}
	if !strings.Contains(err.Error(), "whitelist") {
		t.Errorf("error should mention the whitelist, got: %v", err)
	}
}

func TestExecute_MissingSubcommand(t *testing.T) {
	_, err := Tool{}.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("missing subcommand must error")
	}
}

func TestExecute_PlanModeAllowsGit(t *testing.T) {
	// permission.KindGit must pass Check in plan mode. The git
	// tool itself doesn't consult the policy directly (the
	// subcommand whitelist is the safety boundary), but plan mode
	// is the case the whole tool exists for — verify the policy
	// would say yes if asked.
	p, err := permission.New("/", permission.ModePlan)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	if err := p.Check(permission.Action{Kind: permission.KindGit}); err != nil {
		t.Errorf("plan mode must allow KindGit, got: %v", err)
	}
}

func TestExecute_NonGitDirectoryError(t *testing.T) {
	requireGit(t)
	// Chdir to an empty tempdir (no .git) and verify we get a
	// clear error, not a stack trace or hung command.
	dir := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	_, err := Tool{}.Execute(context.Background(), json.RawMessage(`{
		"subcommand": "log"
	}`))
	if err == nil {
		t.Fatal("expected error in non-git directory")
	}
	// Don't pin the exact message — git's wording varies by version
	// — but the error must reach the caller, not be swallowed.
	if errors.Is(err, context.Canceled) {
		t.Errorf("non-git error must not be reported as cancellation: %v", err)
	}
}

func TestValidateArgs_AllowsNormalFlags(t *testing.T) {
	// Defence-in-depth: normal flag-like args (--oneline, --stat,
	// --no-color, -n, --name-only, paths) must NOT be rejected.
	// Regression guard against an over-eager blocker.
	cases := [][]string{
		{"--oneline"},
		{"-n", "20"},
		{"--stat"},
		{"--no-color"},
		{"--graph"},
		{"--name-only"},
		{"HEAD~5..HEAD"},
		{"main...feature"},
		{"--", "path/to/file.go"},
		{"--format=%H %s"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := validateArgs(args); err != nil {
				t.Errorf("validateArgs(%v) should pass, got: %v", args, err)
			}
		})
	}
}

func TestWhitelist_NetworkReadException(t *testing.T) {
	// ls-remote is the one network-touching subcommand we accept.
	// This test pins the exception so a future cleanup pass doesn't
	// remove it without thinking — if you want it gone, delete this
	// test AND the entry in allowedSubcommands together, on purpose.
	if !allowedSubcommands["ls-remote"] {
		t.Error("ls-remote must remain in the whitelist as the documented network-read exception")
	}
	// The other genuinely-network ops must stay OUT.
	for _, sub := range []string{"fetch", "pull", "clone", "push"} {
		if allowedSubcommands[sub] {
			t.Errorf("%s must NOT be in the whitelist — it writes to .git/ or mutates remote state", sub)
		}
	}
}

func TestReadOnly_True(t *testing.T) {
	// ReadOnly() marker enables concurrent dispatch in the agent.
	// Regression guard so the marker is never accidentally dropped.
	if !(Tool{}).ReadOnly() {
		t.Error("git tool must be marked ReadOnly() so the agent can batch it concurrently with other read-only tools")
	}
}

// tail returns the last n bytes of s for compact assertion messages.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
