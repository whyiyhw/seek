package checkpointcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
)

// setupTempEnv redirects SEEK_HOME + SEEK_SESSIONS_DIR to a tempdir
// so the CLI commands operate in isolation.
func setupTempEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("SEEK_HOME", dir)
	t.Cleanup(func() { os.Unsetenv("SEEK_HOME") })
	os.Setenv("SEEK_SESSIONS_DIR", filepath.Join(dir, "sessions"))
	t.Cleanup(func() { os.Unsetenv("SEEK_SESSIONS_DIR") })
	return dir
}

func createSession(t *testing.T, cwd string) *session.Session {
	t.Helper()
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	s := session.New("test-model", cwd, "system", false, false)
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRun_Help(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := Run([]string{"help"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "seek checkpoint") {
		t.Errorf("help output missing header: %q", out.String())
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	err := Run([]string{"bogus"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected unknown-subcommand error, got %v", err)
	}
}

func TestCmdList_EmptySession(t *testing.T) {
	setupTempEnv(t)
	cwd := t.TempDir()
	s := createSession(t, cwd)
	var out, errOut bytes.Buffer
	if err := Run([]string{"list", "--session", s.ID}, &out, &errOut); err != nil {
		t.Fatalf("list: %v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "no git checkpoints") {
		t.Errorf("expected 'no git checkpoints' in output, got %q", out.String())
	}
}

func TestCmdList_JSON(t *testing.T) {
	setupTempEnv(t)
	cwd := t.TempDir()
	s := createSession(t, cwd)
	var out, errOut bytes.Buffer
	if err := Run([]string{"list", "--session", s.ID, "--json"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	// Empty list → empty output (zero JSONL lines), still valid.
	if strings.TrimSpace(out.String()) != "" {
		// If there were entries, each line must parse.
		for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			var v map[string]interface{}
			if err := json.Unmarshal([]byte(ln), &v); err != nil {
				t.Errorf("invalid JSONL line %q: %v", ln, err)
			}
		}
	}
}

func TestParseBefore(t *testing.T) {
	t.Run("date", func(t *testing.T) {
		_, err := parseBefore("2026-01-01")
		if err != nil {
			t.Errorf("YYYY-MM-DD should parse, got %v", err)
		}
	})
	t.Run("rfc3339", func(t *testing.T) {
		_, err := parseBefore("2026-01-01T12:00:00Z")
		if err != nil {
			t.Errorf("RFC3339 should parse, got %v", err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		_, err := parseBefore("not a date")
		if err == nil {
			t.Error("expected error for bad date")
		}
	})
	t.Run("empty", func(t *testing.T) {
		_, err := parseBefore("")
		if err == nil {
			t.Error("expected error for empty date")
		}
	})
}

func TestParseTurnArg(t *testing.T) {
	tests := []struct {
		in   string
		want int
		err  bool
	}{
		{"last", 0, false},
		{"latest", 0, false},
		{"-1", 0, false},
		{"1", 1, false},
		{"99", 99, false},
		{"0", 0, true},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tc := range tests {
		got, err := parseTurnArg(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseTurnArg(%q): expected err, got %d", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTurnArg(%q): unexpected err %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("parseTurnArg(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestCmdUndo_NoEvents verifies acceptance #2 partially: file undo
// works in any directory (no git required) — when nothing's been
// touched the command exits cleanly with "nothing to undo".
func TestCmdUndo_NoEvents(t *testing.T) {
	setupTempEnv(t)
	cwd := t.TempDir()
	s := createSession(t, cwd)
	var out, errOut bytes.Buffer
	if err := RunUndo([]string{"--session", s.ID}, &out, &errOut); err == nil {
		// Manager returns "no undoable event" error — CLI surfaces it as err.
		t.Errorf("expected undo on empty state to error, got success: %s", out.String())
	} else if !strings.Contains(err.Error(), "no undoable") {
		t.Errorf("expected 'no undoable' in error, got %v", err)
	}
}

// TestCmdRestore_NonGitRepo verifies acceptance #2: restore fails
// gracefully when there's no checkpoint index AND no git context.
func TestCmdRestore_NonGitRepo(t *testing.T) {
	setupTempEnv(t)
	cwd := t.TempDir()
	s := createSession(t, cwd)
	var out, errOut bytes.Buffer
	err := Run([]string{"restore", "1", "--session", s.ID}, &out, &errOut)
	if err == nil {
		t.Errorf("expected restore on empty state to error, got success: %s", out.String())
	}
}

// TestIntegration_RealGit exercises a real git working tree to
// validate acceptance #3 (HEAD untouched, history preserved). Skipped
// when `git` is unavailable so air-gapped CI doesn't fail.
func TestIntegration_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	setupTempEnv(t)
	cwd := t.TempDir()

	// Init a real git repo with one commit.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		// Suppress git's identity check on CI runners.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, stderr.String())
		}
	}
	run("init", "-q")
	run("config", "user.name", "test")
	run("config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")

	// Record HEAD before any checkpoint.
	headBefore := bytes.Buffer{}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = cwd
	cmd.Stdout = &headBefore
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Construct a session whose CWD is the repo so the CLI's
	// resolveSession picks the right project root.
	s := createSession(t, cwd)
	// resolveSession reads s.CWD; make sure it matches.
	if s.CWD != cwd {
		t.Fatalf("session CWD = %q, want %q", s.CWD, cwd)
	}

	// Simulate a checkpoint via the Manager. We can't trigger
	// permission.Check from a CLI test cleanly; the Manager's
	// MaybeCreateGit is enough.
	_, m, err := resolveSession(s.ID, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	// Dirty the working tree first so stash create has something
	// to capture.
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Snapshot via the real git binary.
	m.MaybeCreateGit(context.Background(),
		permission.Action{Kind: permission.KindWrite, Path: filepath.Join(cwd, "a.txt")})

	list, _ := m.ListGitCheckpoints()
	if len(list) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(list))
	}

	// Restore (note: working tree is currently "v2", restore brings
	// back the stash which IS "v2" — so use --force; the actual
	// invariant we care about is HEAD unchanged).
	//
	// Flags come BEFORE the positional turn arg because Go's
	// stdlib flag package stops parsing at the first non-flag
	// token. This matches `git`'s conventional layout.
	var out bytes.Buffer
	if err := Run([]string{"restore", "--session", s.ID, "--force", "1"}, &out, os.Stderr); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Verify HEAD didn't move.
	headAfter := bytes.Buffer{}
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = cwd
	cmd.Stdout = &headAfter
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if headBefore.String() != headAfter.String() {
		t.Errorf("HEAD moved: before=%q after=%q", headBefore.String(), headAfter.String())
	}
}

