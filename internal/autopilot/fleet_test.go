package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo makes a throwaway git repo with one commit so a worktree-style
// dir has a HEAD to commit on top of.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "a@b.c"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := git(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(ctx, dir, "add", "-A"); err != nil {
		t.Fatalf("seed add: %v: %s", err, out)
	}
	if out, err := git(ctx, dir, "commit", "-q", "-m", "seed"); err != nil {
		t.Fatalf("seed commit: %v: %s", err, out)
	}
	return dir
}

func TestCommitWorktree_CommitsChanges(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "out.txt"), []byte("autopilot wrote this"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha := commitWorktree(context.Background(), dir, "Append a line")
	if sha == "" {
		t.Fatal("expected a non-empty short SHA after committing a change")
	}
	// The message is prefixed and the change is actually recorded.
	log, err := git(context.Background(), dir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(log); got != "autopilot: Append a line" {
		t.Fatalf("commit subject = %q", got)
	}
	// Tree is clean afterwards (everything was committed).
	st, _ := git(context.Background(), dir, "status", "--porcelain")
	if strings.TrimSpace(st) != "" {
		t.Fatalf("worktree should be clean after commit, got: %q", st)
	}
}

func TestCommitWorktree_NothingToCommit(t *testing.T) {
	dir := initRepo(t)
	// No changes since the seed commit → no new commit, empty SHA, no error.
	if sha := commitWorktree(context.Background(), dir, "noop"); sha != "" {
		t.Fatalf("expected empty SHA when nothing changed, got %q", sha)
	}
}

func TestCommitWorktree_NotAGitDir(t *testing.T) {
	// best-effort: a non-git dir yields empty SHA, never a panic/error.
	if sha := commitWorktree(context.Background(), t.TempDir(), "x"); sha != "" {
		t.Fatalf("non-git dir should yield empty SHA, got %q", sha)
	}
	if sha := commitWorktree(context.Background(), "", "x"); sha != "" {
		t.Fatalf("empty dir should yield empty SHA, got %q", sha)
	}
}
