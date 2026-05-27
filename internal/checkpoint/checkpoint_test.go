package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/permission"
)

// withSeekHome redirects $SEEK_HOME so checkpoint state lands in a
// tempdir. Returns the resolved home for assertions.
func withSeekHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, had := os.LookupEnv("SEEK_HOME")
	os.Setenv("SEEK_HOME", dir)
	t.Cleanup(func() {
		if had {
			os.Setenv("SEEK_HOME", prev)
		} else {
			os.Unsetenv("SEEK_HOME")
		}
	})
	return dir
}

// recordingSink captures Warn + checkpoint events for assertions.
type recordingSink struct {
	mu       sync.Mutex
	warnings []string
	events   []CheckpointEvent
}

func (s *recordingSink) Warn(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnings = append(s.warnings, m)
}

func (s *recordingSink) OnCheckpoint(ev CheckpointEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *recordingSink) snapshot() ([]string, []CheckpointEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := append([]string(nil), s.warnings...)
	e := append([]CheckpointEvent(nil), s.events...)
	return w, e
}

// fakeGit is a programmable runGit for unit tests. Each call appends
// to .calls; responses are matched in registration order.
type fakeGit struct {
	mu        sync.Mutex
	calls     [][]string
	responses []fakeGitResp
}

type fakeGitResp struct {
	stdout, stderr string
	err            error
}

func (g *fakeGit) run(_ context.Context, _ string, args ...string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, append([]string(nil), args...))
	if len(g.responses) == 0 {
		return "", "", fmt.Errorf("fakeGit: no response queued for %v", args)
	}
	r := g.responses[0]
	g.responses = g.responses[1:]
	return r.stdout, r.stderr, r.err
}

func (g *fakeGit) queue(stdout, stderr string, err error) {
	g.responses = append(g.responses, fakeGitResp{stdout: stdout, stderr: stderr, err: err})
}

func newTestManager(t *testing.T, sink Sink) *Manager {
	t.Helper()
	withSeekHome(t)
	cwd := t.TempDir()
	m := New(Config{
		SessionID:  "test-sess",
		ProjectAbs: cwd,
		CWD:        cwd,
		Sink:       sink,
	})
	return m
}

// ----- Git checkpoint tests -----

// TestGit_OnePerTurn verifies acceptance #1: only one git checkpoint
// per turn even when multiple destructive actions fire; the next
// turn produces a second.
func TestGit_OnePerTurn(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	g := &fakeGit{}
	// Turn 1: rev-parse → true, then for every checkpoint write
	// queue: rev-parse HEAD, symbolic-ref, stash create, update-ref.
	g.queue("true\n", "", nil)            // rev-parse --is-inside-work-tree
	g.queue("abc1234\n", "", nil)         // rev-parse HEAD
	g.queue("main\n", "", nil)            // symbolic-ref
	g.queue("deadbeef\n", "", nil)        // stash create
	g.queue("", "", nil)                  // update-ref
	m.runGit = g.run

	ctx := context.Background()
	m.MaybeCreateGit(ctx, permission.Action{Kind: permission.KindWrite, Path: "foo.go"})
	// Second destructive action in same turn — should be a no-op.
	m.MaybeCreateGit(ctx, permission.Action{Kind: permission.KindEdit, Path: "bar.go"})

	list, err := m.ListGitCheckpoints()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 checkpoint after one turn, got %d", len(list))
	}
	if list[0].Turn != 1 {
		t.Errorf("first checkpoint turn = %d, want 1", list[0].Turn)
	}

	// New turn → new checkpoint.
	m.OnPreTurn(ctx, nil)
	g.queue("abc1234\n", "", nil)  // rev-parse HEAD
	g.queue("main\n", "", nil)     // symbolic-ref
	g.queue("cafebabe\n", "", nil) // stash create
	g.queue("", "", nil)           // update-ref
	m.MaybeCreateGit(ctx, permission.Action{Kind: permission.KindWrite, Path: "baz.go"})

	list, _ = m.ListGitCheckpoints()
	if len(list) != 2 {
		t.Fatalf("expected 2 checkpoints after second turn, got %d", len(list))
	}
	if list[1].Turn != 2 {
		t.Errorf("second checkpoint turn = %d, want 2", list[1].Turn)
	}
}

// TestGit_ReadOnlyBashSkipped verifies that bash with ReadOnly=true
// does NOT consume a checkpoint slot — acceptance #1 implies turns
// of `go vet` shouldn't churn.
func TestGit_ReadOnlyBashSkipped(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)
	g := &fakeGit{}
	m.runGit = g.run

	m.MaybeCreateGit(context.Background(),
		permission.Action{Kind: permission.KindBash, Command: "go vet ./...", ReadOnly: true})

	if len(g.calls) != 0 {
		t.Errorf("expected zero git calls for read-only bash, got %v", g.calls)
	}
	list, _ := m.ListGitCheckpoints()
	if len(list) != 0 {
		t.Errorf("expected no checkpoints from read-only bash, got %d", len(list))
	}
}

// TestGit_NotARepoLogsOnce verifies acceptance #2: non-git case
// degrades gracefully, hint goes out exactly once.
func TestGit_NotARepoLogsOnce(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	g := &fakeGit{}
	g.queue("", "", fmt.Errorf("not a git repo")) // first rev-parse fails
	m.runGit = g.run

	for i := 0; i < 3; i++ {
		m.MaybeCreateGit(context.Background(),
			permission.Action{Kind: permission.KindWrite, Path: "foo"})
		m.OnPreTurn(context.Background(), nil)
	}
	warns, _ := sink.snapshot()
	hits := 0
	for _, w := range warns {
		if strings.Contains(w, "git checkpoint disabled") {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("expected hint exactly once, got %d (warns=%v)", hits, warns)
	}
}

// TestGit_CleanWorkingTreeNoOp verifies an empty `git stash create`
// returns no checkpoint and no error — there was nothing to snapshot.
func TestGit_CleanWorkingTreeNoOp(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)
	g := &fakeGit{}
	g.queue("true\n", "", nil)
	g.queue("abc\n", "", nil)
	g.queue("main\n", "", nil)
	g.queue("", "", nil) // stash create with clean tree
	// no update-ref because we return early on empty sha
	m.runGit = g.run

	m.MaybeCreateGit(context.Background(),
		permission.Action{Kind: permission.KindEdit, Path: "x"})

	list, _ := m.ListGitCheckpoints()
	if len(list) != 0 {
		t.Errorf("clean tree should produce 0 checkpoints, got %d", len(list))
	}
}

// TestGit_RestoreUnknownTurn verifies the user-facing error path.
func TestGit_RestoreUnknownTurn(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)
	m.runGit = (&fakeGit{}).run

	_, err := m.RestoreGit(context.Background(), RestoreOptions{Turn: 99})
	if err == nil || !strings.Contains(err.Error(), "no git checkpoints") {
		t.Errorf("expected 'no git checkpoints' error, got %v", err)
	}
}

// TestGit_RestoreDirtyRefuses verifies acceptance #3 helper: dirty
// working tree blocks restore unless --force.
func TestGit_RestoreDirtyRefuses(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	// Write one fake entry directly to the index so we don't need a
	// real git setup for the dirty-check side.
	path, _ := m.gitIndexPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := GitCheckpoint{Turn: 1, TS: time.Now(), Ref: "refs/seek/x", Commit: "deadbeef"}
	b, _ := json.Marshal(entry)
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	g := &fakeGit{}
	g.queue(" M foo.go\n", "", nil) // diff --name-only call
	g.queue(" M foo.go\n", "", nil) // status --porcelain → dirty
	m.runGit = g.run

	_, err := m.RestoreGit(context.Background(), RestoreOptions{Turn: 1})
	if err == nil || !strings.Contains(err.Error(), "unsaved changes") {
		t.Errorf("expected dirty-tree error, got %v", err)
	}
}

// ----- File checkpoint tests -----

// TestFile_BlobDedup verifies acceptance #4: identical content
// shares a single blob even across multiple edits.
func TestFile_BlobDedup(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	path := filepath.Join(m.cwd, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Snapshot (records "hello" blob).
	if err := m.SnapshotFile(path, "write", "call1"); err != nil {
		t.Fatal(err)
	}
	if err := m.FinaliseSnapshot(path, []byte("hello2")); err != nil {
		t.Fatal(err)
	}
	// Rewrite to "hello" again, snapshot once more.
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.SnapshotFile(path, "write", "call2"); err != nil {
		t.Fatal(err)
	}

	root, _ := m.blobsDir()
	count := 0
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, _ error) error {
		if info == nil {
			return nil
		}
		if !info.IsDir() && !strings.HasSuffix(info.Name(), ".tmp") {
			count++
		}
		return nil
	})
	// Expect 2 unique blobs: "hello" (from snapshot-before, both times same SHA) and "hello2" (from finalise).
	if count != 2 {
		t.Errorf("expected 2 unique blobs, got %d (files in %s)", count, root)
	}
}

// TestFile_UndoRedoRoundTrip verifies acceptance #5: /undo then
// /redo restores the post-edit state. A new write truncates redo.
func TestFile_UndoRedoRoundTrip(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	path := filepath.Join(m.cwd, "f.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate edit v1 → v2.
	if err := m.SnapshotFile(path, "edit", "c1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.FinaliseSnapshot(path, []byte("v2")); err != nil {
		t.Fatal(err)
	}

	// Undo → file back to "v1".
	results, err := m.Undo(UndoOptions{Path: path})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 undo result, got %d", len(results))
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v1" {
		t.Errorf("after undo, file=%q, want %q", got, "v1")
	}

	// Redo → back to "v2".
	rresults, err := m.Redo(RedoOptions{Path: path})
	if err != nil {
		t.Fatalf("redo: %v", err)
	}
	if len(rresults) != 1 {
		t.Fatalf("expected 1 redo result, got %d", len(rresults))
	}
	got, _ = os.ReadFile(path)
	if string(got) != "v2" {
		t.Errorf("after redo, file=%q, want %q", got, "v2")
	}

	// Undo again, then a NEW write — redo should be truncated.
	if _, err := m.Undo(UndoOptions{Path: path}); err != nil {
		t.Fatalf("undo round 2: %v", err)
	}
	// New write — model just modified the file fresh.
	if err := m.SnapshotFile(path, "edit", "c2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.FinaliseSnapshot(path, []byte("v3")); err != nil {
		t.Fatal(err)
	}

	// Try redo — should fail because the redo history was cleared.
	if _, err := m.Redo(RedoOptions{Path: path}); err == nil {
		t.Errorf("expected redo to fail after new write, got success")
	}
}

// TestFile_SessionEndCleanup verifies acceptance #6: SessionEnd
// removes the checkpoint subdir unless KeepOnExit.
func TestFile_SessionEndCleanup(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	// Create some file checkpoint state.
	path := filepath.Join(m.cwd, "x.txt")
	if err := os.WriteFile(path, []byte("foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.SnapshotFile(path, "write", ""); err != nil {
		t.Fatal(err)
	}
	sub, _ := m.CheckpointSubDir()
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("expected checkpoints subdir to exist: %v", err)
	}
	m.OnSessionEnd(context.Background(), nil)
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("expected checkpoints subdir to be removed, got %v", err)
	}
}

func TestFile_SessionEndCleanup_KeepOnExit(t *testing.T) {
	withSeekHome(t)
	cwd := t.TempDir()
	m := New(Config{SessionID: "s", ProjectAbs: cwd, CWD: cwd, KeepOnExit: true, Sink: &recordingSink{}})
	path := filepath.Join(cwd, "x.txt")
	os.WriteFile(path, []byte("foo"), 0o644)
	m.SnapshotFile(path, "write", "")
	sub, _ := m.CheckpointSubDir()
	m.OnSessionEnd(context.Background(), nil)
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("expected checkpoints dir kept, got err %v", err)
	}
}

// TestFile_ExternalModificationRefused verifies acceptance #7.
func TestFile_ExternalModificationRefused(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)
	path := filepath.Join(m.cwd, "f.txt")
	os.WriteFile(path, []byte("v1"), 0o644)
	m.SnapshotFile(path, "edit", "")
	os.WriteFile(path, []byte("v2"), 0o644)
	m.FinaliseSnapshot(path, []byte("v2"))

	// External mutation between edit and undo.
	os.WriteFile(path, []byte("ide-overwrote"), 0o644)

	if _, err := m.Undo(UndoOptions{Path: path}); err == nil ||
		!strings.Contains(err.Error(), "modified externally") {
		t.Errorf("expected external-modification error, got %v", err)
	}

	// --force overrides.
	if _, err := m.Undo(UndoOptions{Path: path, Force: true}); err != nil {
		t.Errorf("force undo failed: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v1" {
		t.Errorf("after force undo, file=%q, want v1", got)
	}
}

// TestFile_SkipsSeekDir verifies acceptance #8: .seek paths don't
// generate file checkpoints.
func TestFile_SkipsSeekDir(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	seekFile := filepath.Join(m.cwd, ".seek", "internal", "foo.txt")
	os.MkdirAll(filepath.Dir(seekFile), 0o755)
	os.WriteFile(seekFile, []byte("hi"), 0o644)
	if err := m.SnapshotFile(seekFile, "write", ""); err != nil {
		t.Fatal(err)
	}

	events, _ := m.FileEvents()
	if len(events) != 0 {
		t.Errorf(".seek paths should not be snapshotted, got %+v", events)
	}
}

// TestFile_BinaryDetected verifies acceptance #9: binary file
// snapshot records the event but stores no blob.
func TestFile_BinaryDetected(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	path := filepath.Join(m.cwd, "bin.dat")
	content := append([]byte{0x00, 0x01, 0x02}, bytes.Repeat([]byte{0xff}, 100)...)
	os.WriteFile(path, content, 0o644)
	if err := m.SnapshotFile(path, "write", ""); err != nil {
		t.Fatal(err)
	}

	events, _ := m.FileEvents()
	if len(events) != 1 || !events[0].Binary {
		t.Errorf("expected one event with Binary=true, got %+v", events)
	}
	// Verify blob NOT stored.
	root, _ := m.blobsDir()
	var hadBlob bool
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			hadBlob = true
		}
		return nil
	})
	if hadBlob {
		t.Errorf("expected no blob stored for binary file")
	}
}

// TestFile_NilManagerSafe verifies the nil-receiver fast paths.
func TestFile_NilManagerSafe(t *testing.T) {
	var m *Manager
	if err := m.SnapshotFile("/x", "write", ""); err != nil {
		t.Errorf("nil-receiver SnapshotFile should be safe, got %v", err)
	}
	if err := m.FinaliseSnapshot("/x", nil); err != nil {
		t.Errorf("nil-receiver FinaliseSnapshot should be safe, got %v", err)
	}
	if _, err := m.Undo(UndoOptions{}); err == nil {
		t.Errorf("nil-receiver Undo should fail")
	}
	m.OnSessionEnd(context.Background(), nil)
	m.OnPreTurn(context.Background(), nil)
}

// TestFile_ConcurrentSnapshots verifies the mutex actually
// serialises concurrent snapshots without losing events. -race
// will catch any leaks here.
func TestFile_ConcurrentSnapshots(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := filepath.Join(m.cwd, fmt.Sprintf("f%d.txt", i))
			os.WriteFile(path, []byte("x"), 0o644)
			_ = m.SnapshotFile(path, "write", "")
			_ = m.FinaliseSnapshot(path, []byte("y"))
		}(i)
	}
	wg.Wait()
	events, _ := m.FileEvents()
	// Each goroutine: 1 SnapshotFile event (FinaliseSnapshot just
	// back-fills the after_sha and stores the after blob, no extra
	// event). Expect 20 distinct events with unique seqs.
	if len(events) != 20 {
		warns, _ := sink.snapshot()
		t.Errorf("expected 20 events from concurrent snapshots, got %d. warns=%v", len(events), warns)
	}
	// All seqs must be unique.
	seen := map[int64]bool{}
	for _, ev := range events {
		if seen[ev.Seq] {
			t.Errorf("duplicate seq %d", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}

// TestFile_UndoOfMissingBlob_NoCrash verifies a corrupted blob dir
// produces a clean error rather than a crash.
func TestFile_UndoOfMissingBlob_NoCrash(t *testing.T) {
	sink := &recordingSink{}
	m := newTestManager(t, sink)

	path := filepath.Join(m.cwd, "f.txt")
	os.WriteFile(path, []byte("v1"), 0o644)
	m.SnapshotFile(path, "edit", "")
	os.WriteFile(path, []byte("v2"), 0o644)
	m.FinaliseSnapshot(path, []byte("v2"))

	// Manually nuke the blob dir.
	bd, _ := m.blobsDir()
	os.RemoveAll(bd)

	_, err := m.Undo(UndoOptions{Path: path})
	if err == nil {
		t.Errorf("expected undo to error on missing blob, got success")
	}
}

// TestPermissionHook verifies the permission.Policy → checkpoint
// wiring fires on destructive actions and skips read-only ones.
func TestPermissionHook(t *testing.T) {
	withSeekHome(t)
	cwd := t.TempDir()
	pol, err := permission.New(cwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	var fired []permission.Action
	pol.SetOnDestructive(func(a permission.Action) {
		fired = append(fired, a)
	})

	// Destructive — should fire.
	pol.Check(permission.Action{Kind: permission.KindWrite, Path: "x"})
	pol.Check(permission.Action{Kind: permission.KindEdit, Path: "y"})
	pol.Check(permission.Action{Kind: permission.KindBash, Command: "rm -rf /"})
	// Read-only bash → no fire.
	pol.Check(permission.Action{Kind: permission.KindBash, ReadOnly: true, Command: "go vet ./..."})
	// Read → no fire.
	pol.Check(permission.Action{Kind: permission.KindRead, Path: "x"})

	if len(fired) != 3 {
		t.Errorf("expected 3 destructive fires, got %d: %+v", len(fired), fired)
	}
}

// Sanity: ensure hashBytes is the SHA-256 the rest of the world
// expects. Belt + braces: if a refactor accidentally swaps in a
// different hash function, the blob store layout breaks across
// versions.
func TestSHASanity(t *testing.T) {
	want := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte("hello")); return s[:] }())
	if got := hashBytes([]byte("hello")); got != want {
		t.Errorf("hashBytes('hello')=%q, want %q", got, want)
	}
}
