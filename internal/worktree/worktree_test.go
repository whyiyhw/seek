package worktree

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGit is a recording stub that lets tests script git command
// responses by invocation order. Each call pops the next response
// off the queue; running off the end fails the test with the args
// that surprised it. Args of each call are kept in `calls` so
// assertions can verify the exact sequence + flags the manager
// emitted.
type fakeGit struct {
	mu    sync.Mutex
	queue []fakeGitResp
	calls [][]string
}

type fakeGitResp struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeGit) push(stdout, stderr string, err error) *fakeGit {
	f.queue = append(f.queue, fakeGitResp{stdout, stderr, err})
	return f
}

func (f *fakeGit) run(t *testing.T) GitRunner {
	t.Helper()
	return func(ctx context.Context, cwd string, args ...string) (string, string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, args)
		if len(f.queue) == 0 {
			t.Fatalf("fakeGit: no queued response for call: git %v", args)
		}
		resp := f.queue[0]
		f.queue = f.queue[1:]
		return resp.stdout, resp.stderr, resp.err
	}
}

// argsAt returns the args of the Nth git call (0-indexed) for
// inline assertions like assertContains(args, "worktree", "add").
func (f *fakeGit) argsAt(t *testing.T, idx int) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if idx >= len(f.calls) {
		t.Fatalf("fakeGit: only %d call(s) recorded, want index %d", len(f.calls), idx)
	}
	return f.calls[idx]
}

// withTestHome redirects ~/.seek to a tempdir so the worktree
// manager's homeProjectDir lookup lands in test-controlled space.
func withTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	return home
}

func newTestManager(t *testing.T, fg *fakeGit) *Manager {
	t.Helper()
	withTestHome(t)
	root := t.TempDir()
	mgr, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr.runGit = fg.run(t)
	return mgr
}

// TestCreate_HappyPath: rev-parse → worktree add → update-ref;
// returned Worktree carries the right fields; active map gets a
// new entry.
func TestCreate_HappyPath(t *testing.T) {
	fg := (&fakeGit{}).
		push("abc123\n", "", nil). // rev-parse HEAD
		push("", "", nil).          // worktree add
		push("", "", nil)           // update-ref
	mgr := newTestManager(t, fg)

	wt, err := mgr.Create(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if wt.ID == "" || !strings.Contains(wt.ID, "-") {
		t.Errorf("worktree ID malformed: %q", wt.ID)
	}
	if wt.Base != "abc123" {
		t.Errorf("Base = %q, want abc123", wt.Base)
	}
	if !strings.HasPrefix(wt.Branch, "seek/wt/") {
		t.Errorf("auto-generated branch should be under seek/wt/, got %q", wt.Branch)
	}
	if !strings.Contains(wt.Path, "worktrees") {
		t.Errorf("worktree Path should be under ~/.seek/projects/.../worktrees/, got %q", wt.Path)
	}

	// Verify git invocations: 1st is rev-parse, 2nd is worktree
	// add with -b <branch> <path> <sha>, 3rd is update-ref
	// refs/seek/worktrees/<id>.
	args := fg.argsAt(t, 1)
	if args[0] != "worktree" || args[1] != "add" || args[2] != "-b" {
		t.Errorf("expected `worktree add -b ...`, got %v", args)
	}
	args = fg.argsAt(t, 2)
	if args[0] != "update-ref" || !strings.HasPrefix(args[1], "refs/seek/worktrees/") {
		t.Errorf("expected update-ref refs/seek/worktrees/..., got %v", args)
	}

	// Active map gained one entry.
	if got := len(mgr.List()); got != 1 {
		t.Errorf("List() len = %d, want 1", got)
	}
}

// TestCreate_CustomBranchAndBase: the caller-supplied branch and
// base flow through to the git invocation verbatim.
func TestCreate_CustomBranchAndBase(t *testing.T) {
	fg := (&fakeGit{}).
		push("def456\n", "", nil).
		push("", "", nil).
		push("", "", nil)
	mgr := newTestManager(t, fg)

	wt, err := mgr.Create(context.Background(), "feat/explore", "main")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "feat/explore" {
		t.Errorf("Branch = %q, want feat/explore", wt.Branch)
	}
	// rev-parse should have asked for "main".
	first := fg.argsAt(t, 0)
	if first[len(first)-1] != "main" {
		t.Errorf("rev-parse target = %q, want main", first[len(first)-1])
	}
}

// TestCreate_RevParseFailureSurfaced: bad base name yields a
// clean error including git stderr.
func TestCreate_RevParseFailureSurfaced(t *testing.T) {
	fg := (&fakeGit{}).push("", "fatal: ambiguous argument 'bogus'", errors.New("exit status 128"))
	mgr := newTestManager(t, fg)

	_, err := mgr.Create(context.Background(), "", "bogus")
	if err == nil {
		t.Fatal("expected error for bad base")
	}
	if !strings.Contains(err.Error(), "ambiguous argument 'bogus'") {
		t.Errorf("error should include git stderr: %v", err)
	}
	// active map untouched.
	if got := len(mgr.List()); got != 0 {
		t.Errorf("List() len = %d after failed Create, want 0", got)
	}
}

// TestStatus_Clean: empty porcelain output → 0 dirty files.
func TestStatus_Clean(t *testing.T) {
	fg := (&fakeGit{}).push("", "", nil)
	mgr := newTestManager(t, fg)

	n, err := mgr.Status(context.Background(), "/some/path")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("Status clean = %d, want 0", n)
	}
}

// TestStatus_DirtyCountsLines: porcelain output has one line per
// changed file.
func TestStatus_DirtyCountsLines(t *testing.T) {
	porcelain := " M file1.go\n?? new.txt\n D removed.go\n"
	fg := (&fakeGit{}).push(porcelain, "", nil)
	mgr := newTestManager(t, fg)

	n, err := mgr.Status(context.Background(), "/p")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("Status dirty = %d, want 3", n)
	}
}

// TestCleanup_CleanPath: zero dirty → "cleaned", git worktree
// remove --force + update-ref -d called.
func TestCleanup_CleanPath(t *testing.T) {
	fg := (&fakeGit{}).
		push("abc\n", "", nil). // Create: rev-parse
		push("", "", nil).      // Create: worktree add
		push("", "", nil).      // Create: update-ref
		push("", "", nil).      // Cleanup: status (clean)
		push("", "", nil).      // Cleanup: worktree remove
		push("", "", nil)       // Cleanup: update-ref -d
	mgr := newTestManager(t, fg)

	wt, err := mgr.Create(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr.Cleanup(context.Background(), wt.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "cleaned" {
		t.Errorf("Status = %q, want cleaned", res.Status)
	}
	if res.Changes != 0 {
		t.Errorf("Changes = %d, want 0 on clean path", res.Changes)
	}
	// active map drained.
	if got := len(mgr.List()); got != 0 {
		t.Errorf("active not drained after clean Cleanup: %d", got)
	}
}

// TestCleanup_DirtyKeep: dirty + keep → leaves worktree in place,
// returns "kept" with changes count; no remove / no stash invoked.
func TestCleanup_DirtyKeep(t *testing.T) {
	fg := (&fakeGit{}).
		push("abc\n", "", nil).
		push("", "", nil).
		push("", "", nil).
		push(" M x.go\n?? y.go\n", "", nil) // status: 2 dirty files
	mgr := newTestManager(t, fg)

	wt, _ := mgr.Create(context.Background(), "", "")
	res, err := mgr.Cleanup(context.Background(), wt.Path, "keep")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "kept" || res.Changes != 2 {
		t.Errorf("kept result = %+v, want status=kept changes=2", res)
	}
	// Cleanup MUST drain active even on keep (PRD: seek's
	// bookkeeping has moved on; the user owns the dir).
	if got := len(mgr.List()); got != 0 {
		t.Errorf("active not drained on keep path: %d", got)
	}
}

// TestCleanup_DirtyDiscard: dirty + discard → stash create →
// update-ref refs/seek/discarded/<ts> → reset --hard → clean -fd →
// worktree remove → ref delete. Result has StashRef.
func TestCleanup_DirtyDiscard(t *testing.T) {
	fg := (&fakeGit{}).
		push("abc\n", "", nil).
		push("", "", nil).
		push("", "", nil).
		push(" M x.go\n", "", nil).       // status: 1 dirty
		push("stashsha\n", "", nil).      // stash create
		push("", "", nil).                // update-ref discarded
		push("", "", nil).                // reset --hard
		push("", "", nil).                // clean -fd
		push("", "", nil).                // worktree remove
		push("", "", nil)                 // update-ref -d
	mgr := newTestManager(t, fg)

	wt, _ := mgr.Create(context.Background(), "", "")
	res, err := mgr.Cleanup(context.Background(), wt.Path, "discard")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "discarded" || res.Changes != 1 {
		t.Errorf("discard result = %+v, want status=discarded changes=1", res)
	}
	if !strings.HasPrefix(res.StashRef, "refs/seek/discarded/") {
		t.Errorf("StashRef should be under refs/seek/discarded/, got %q", res.StashRef)
	}

	// Verify the rescue stash happened BEFORE reset (it has to —
	// otherwise discard would nuke the content before saving it).
	stashIdx := -1
	resetIdx := -1
	for i, args := range fg.calls {
		if len(args) >= 2 && args[0] == "stash" && args[1] == "create" {
			stashIdx = i
		}
		if len(args) >= 2 && args[0] == "reset" && args[1] == "--hard" {
			resetIdx = i
		}
	}
	if stashIdx < 0 || resetIdx < 0 || stashIdx > resetIdx {
		t.Errorf("rescue stash must precede reset --hard; stashIdx=%d resetIdx=%d", stashIdx, resetIdx)
	}
}

// TestCleanup_DiscardStashFailureAborts: if the rescue stash
// fails, Cleanup MUST abort BEFORE the reset/remove so the dirty
// content is preserved on disk. The user can recover by inspecting
// the worktree directory.
func TestCleanup_DiscardStashFailureAborts(t *testing.T) {
	fg := (&fakeGit{}).
		push("abc\n", "", nil).
		push("", "", nil).
		push("", "", nil).
		push(" M x.go\n", "", nil).
		push("", "stash failed", errors.New("exit 1"))
	mgr := newTestManager(t, fg)

	wt, _ := mgr.Create(context.Background(), "", "")
	_, err := mgr.Cleanup(context.Background(), wt.Path, "discard")
	if err == nil {
		t.Fatal("expected error when rescue stash fails")
	}
	if !strings.Contains(err.Error(), "rescue stash") {
		t.Errorf("error message should mention rescue stash: %v", err)
	}
}

// TestCleanup_IfDirtyInvalid: anything besides ""|"keep"|"discard"
// errors at the parameter gate.
func TestCleanup_IfDirtyInvalid(t *testing.T) {
	fg := (&fakeGit{}).push("abc\n", "", nil).push("", "", nil).push("", "", nil)
	mgr := newTestManager(t, fg)

	wt, _ := mgr.Create(context.Background(), "", "")
	_, err := mgr.Cleanup(context.Background(), wt.Path, "delete")
	if err == nil {
		t.Fatal("expected error for ifDirty=delete")
	}
}

// TestMapToProject_InsideWorktree: a path under an active
// worktree is rewritten to its project-root equivalent.
func TestMapToProject_InsideWorktree(t *testing.T) {
	fg := (&fakeGit{}).push("abc\n", "", nil).push("", "", nil).push("", "", nil)
	mgr := newTestManager(t, fg)

	wt, _ := mgr.Create(context.Background(), "", "")
	wtFile := filepath.Join(wt.Path, "docs", "prd", "x.md")
	mapped := mgr.MapToProject(wtFile)
	want := filepath.Join(mgr.root, "docs", "prd", "x.md")
	if mapped != want {
		t.Errorf("MapToProject = %q, want %q", mapped, want)
	}
}

// TestMapToProject_OutsideWorktree: paths NOT under any active
// worktree pass through unchanged.
func TestMapToProject_OutsideWorktree(t *testing.T) {
	fg := (&fakeGit{}).push("abc\n", "", nil).push("", "", nil).push("", "", nil)
	mgr := newTestManager(t, fg)
	_, _ = mgr.Create(context.Background(), "", "")

	outside := "/tmp/unrelated/file.go"
	if got := mgr.MapToProject(outside); got != outside {
		t.Errorf("path outside worktree should pass through; got %q", got)
	}
}

// TestPruneDiscarded_OnlyRemovesOld: refs older than threshold
// get update-ref -d; recent ones stay.
func TestPruneDiscarded_OnlyRemovesOld(t *testing.T) {
	old := time.Now().UTC().Add(-72 * time.Hour).Format("20060102-150405")
	recent := time.Now().UTC().Add(-1 * time.Hour).Format("20060102-150405")
	refsOut := "refs/seek/discarded/" + old + "\nrefs/seek/discarded/" + recent + "\n"

	fg := (&fakeGit{}).
		push(refsOut, "", nil). // for-each-ref
		push("", "", nil)        // update-ref -d (only for old)
	mgr := newTestManager(t, fg)

	n, err := mgr.PruneDiscarded(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("PruneDiscarded count = %d, want 1", n)
	}
	// Verify the update-ref call targeted the OLD ref name.
	deleteCall := fg.argsAt(t, 1)
	if deleteCall[0] != "update-ref" || deleteCall[1] != "-d" {
		t.Errorf("expected `update-ref -d`, got %v", deleteCall)
	}
	if !strings.Contains(deleteCall[2], old) {
		t.Errorf("update-ref should target old ref %q, got %v", old, deleteCall)
	}
}

// TestPruneDiscarded_SkipsMalformedRefName: a ref name that
// doesn't match the YYYYMMDD-HHMMSS shape is left alone. This
// matters because a future schema bump or manual ref pollution
// shouldn't crash GC.
func TestPruneDiscarded_SkipsMalformedRefName(t *testing.T) {
	refsOut := "refs/seek/discarded/not-a-timestamp\nrefs/seek/discarded/20200101-000000\n"
	fg := (&fakeGit{}).
		push(refsOut, "", nil).
		push("", "", nil) // delete only for the valid old one
	mgr := newTestManager(t, fg)

	n, err := mgr.PruneDiscarded(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 prune (skip malformed); got %d", n)
	}
}

// TestPruneDiscarded_ZeroDurationPrunesEverything: olderThan=0
// means "anything strictly before now" → all parseable refs go.
func TestPruneDiscarded_ZeroDurationPrunesEverything(t *testing.T) {
	now := time.Now().UTC()
	a := now.Add(-1 * time.Hour).Format("20060102-150405")
	b := now.Add(-2 * time.Hour).Format("20060102-150405")
	refsOut := "refs/seek/discarded/" + a + "\nrefs/seek/discarded/" + b + "\n"

	fg := (&fakeGit{}).
		push(refsOut, "", nil).
		push("", "", nil).
		push("", "", nil)
	mgr := newTestManager(t, fg)

	n, err := mgr.PruneDiscarded(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("olderThan=0 should prune all parseable; got %d", n)
	}
}

// TestNewManager_NilSafe: methods on nil Manager return clear
// errors rather than panic. Important because cmd/seek constructs
// the Manager conditionally (only in git repos) and downstream
// tools can encounter a nil reference.
func TestNewManager_NilSafe(t *testing.T) {
	var m *Manager
	if _, err := m.Create(context.Background(), "", ""); err == nil {
		t.Error("nil Manager Create should error")
	}
	if _, err := m.Status(context.Background(), "/x"); err == nil {
		t.Error("nil Manager Status should error")
	}
	if _, err := m.Cleanup(context.Background(), "/x", "keep"); err == nil {
		t.Error("nil Manager Cleanup should error")
	}
	if _, err := m.PruneDiscarded(context.Background(), time.Hour); err == nil {
		t.Error("nil Manager PruneDiscarded should error")
	}
	// MapToProject and List degrade silently rather than error —
	// they're called from hot paths (every hook check / every
	// /worktrees render) where erroring is worse than no-op.
	if got := m.MapToProject("/x"); got != "/x" {
		t.Errorf("nil Manager MapToProject pass-through expected, got %q", got)
	}
	if got := m.List(); got != nil {
		t.Errorf("nil Manager List should return nil; got %v", got)
	}
}
