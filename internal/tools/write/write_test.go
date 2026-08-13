package write

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/fsobserve"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools/read"
)

func TestWrite_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	w := New(p)

	target := filepath.Join(dir, "hello.txt")
	args, _ := json.Marshal(Args{Path: target, Content: "hi"})
	out, err := w.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote 2 bytes") {
		t.Errorf("output: %s", out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Errorf("file contents = %q", got)
	}
}

func TestWrite_CreatesParents(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	target := filepath.Join(dir, "a", "b", "c", "deep.txt")
	args, _ := json.Marshal(Args{Path: target, Content: "x"})
	if _, err := New(p).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("stat: %v", err)
	}
}

func TestWrite_Overwrites(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	target := filepath.Join(dir, "f.txt")
	os.WriteFile(target, []byte("original"), 0o644)

	args, _ := json.Marshal(Args{Path: target, Content: "replaced"})
	if _, err := New(p).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "replaced" {
		t.Errorf("not overwritten: %q", got)
	}
}

func TestWrite_RefusesOutsideCWD(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	args, _ := json.Marshal(Args{Path: filepath.Join(other, "evil"), Content: "x"})
	_, err := New(p).Execute(context.Background(), args)
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestWrite_YoloAllowsOutsideCWD(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	p, _ := permission.New(dir, permission.PrefYolo)
	target := filepath.Join(other, "ok")
	args, _ := json.Marshal(Args{Path: target, Content: "x"})
	if _, err := New(p).Execute(context.Background(), args); err != nil {
		t.Errorf("yolo write blocked: %v", err)
	}
}

func TestWrite_MissingPath(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefDeny)
	_, err := New(p).Execute(context.Background(), json.RawMessage(`{"content":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("err = %v", err)
	}
}

// --- Failure modes that weren't covered before -----------------------

func TestWrite_BadJSONArguments(t *testing.T) {
	// LLM-produced JSON occasionally lands malformed (truncated mid-
	// stream, escape-character drift, etc.). The tool must surface
	// it as a tool-level error, not panic or run a write with garbage.
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	_, err := New(p).Execute(context.Background(), json.RawMessage("not json at all"))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("error should attribute to bad arguments: %v", err)
	}
}

func TestWrite_MkdirFailsWhenParentIsAFile(t *testing.T) {
	// A common LLM mistake: trying to write to "config/settings.json"
	// when "config" is already a regular file. os.MkdirAll fails with
	// "not a directory"; the tool must surface that cleanly without
	// panicking or partially-applying.
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(Args{Path: filepath.Join(blocker, "child.txt"), Content: "x"})
	_, err := New(p).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected mkdir error, got nil")
	}
	if !strings.Contains(err.Error(), "write") && !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error should mention the failed operation: %v", err)
	}
}

func TestWrite_EmptyContent(t *testing.T) {
	// Writing a zero-byte file is a legitimate operation (create a
	// .gitkeep, truncate a log, etc.). Make sure we don't accidentally
	// short-circuit on len(content)==0.
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	target := filepath.Join(dir, "empty.txt")
	args, _ := json.Marshal(Args{Path: target, Content: ""})
	out, err := New(p).Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote 0 bytes") {
		t.Errorf("output should report 0 bytes: %q", out)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("file size = %d, want 0", info.Size())
	}
}

func TestWrite_LargeContent(t *testing.T) {
	// 1 MB write — exercises the bytes >> any obvious 4K/64K buffer
	// boundary. Catches silent truncation or buffer-reuse bugs that
	// only manifest above a threshold.
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	target := filepath.Join(dir, "big.bin")
	content := strings.Repeat("a", 1<<20)
	args, _ := json.Marshal(Args{Path: target, Content: content})
	if _, err := New(p).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(content) {
		t.Errorf("wrote %d bytes, want %d", len(got), len(content))
	}
}

// TestWrite_SchemaIsByteStable pins PRD §4.8.1's cache-stability rule:
// every Schema() call must return identical bytes so DeepSeek's prefix
// cache hits across turns. If we ever switch from a package-level
// []byte constant to a built-on-demand schema, this catches it.
func TestWrite_SchemaIsByteStable(t *testing.T) {
	tool := New(nil)
	a := string(tool.Schema())
	b := string(tool.Schema())
	if a != b {
		t.Errorf("Schema() drifted between calls — kills cache prefix:\nfirst:  %s\nsecond: %s", a, b)
	}
	// Smoke-check the rest of the public surface while we're here.
	if tool.Name() != "write" {
		t.Errorf("Name() = %q, want write", tool.Name())
	}
	if tool.Description() == "" {
		t.Errorf("Description should not be empty")
	}
}

// TestWrite_SymlinkInsideCWDPointingOutsideIsDenied cross-references
// permission.TestIsWithin_SymlinkInsideCWDPointingOutsideIsDenied.
// The policy now resolves symlinks so a symlink inside CWD that points
// outside is correctly denied.
func TestWrite_SymlinkInsideCWDPointingOutsideIsDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	p, _ := permission.New(root, permission.PrefDeny)
	target := filepath.Join(link, "leaked.txt")
	args, _ := json.Marshal(Args{Path: target, Content: "leaked content"})
	_, err := New(p).Execute(context.Background(), args)
	if err == nil {
		t.Error("symlink-escape write was allowed — symlink resolution not working")
	}
}

// TestWrite_RelativePath_AnchoredToPolicyCWD locks the worktree-isolation
// fix found by autopilot e2e: a RELATIVE path must resolve against the
// policy's CWD (the worktree for an isolation:"worktree" subagent), NOT
// the process working directory. Before the fix, a subagent writing
// "README.md" hit the MAIN tree.
func TestWrite_RelativePath_AnchoredToPolicyCWD(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefYolo)

	args, _ := json.Marshal(Args{Path: "rel-iso.txt", Content: "x"})
	if _, err := New(p).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rel-iso.txt")); err != nil {
		t.Fatalf("relative write must land in policy CWD %s: %v", dir, err)
	}
	if _, err := os.Stat("rel-iso.txt"); err == nil {
		_ = os.Remove("rel-iso.txt")
		t.Fatal("relative write leaked into process cwd — worktree isolation broken")
	}
}

// ---- blind-overwrite guard (internal/fsobserve) ----

func guardedWrite(t *testing.T, dir string) (Tool, *fsobserve.Store) {
	t.Helper()
	p, _ := permission.New(dir, permission.PrefYolo)
	obs := fsobserve.New()
	return New(p).WithObserver(obs), obs
}

func doWrite(t *testing.T, w Tool, path, content string) (string, error) {
	t.Helper()
	args, _ := json.Marshal(Args{Path: path, Content: content})
	return w.Execute(context.Background(), args)
}

func TestWrite_NewFileNeedsNoPriorRead(t *testing.T) {
	dir := t.TempDir()
	w, _ := guardedWrite(t, dir)

	// Creating a file is the common case and must stay frictionless.
	if _, err := doWrite(t, w, filepath.Join(dir, "new.go"), "package main\n"); err != nil {
		t.Fatalf("creating a new file was refused: %v", err)
	}
}

func TestWrite_ExistingUnreadFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	w, _ := guardedWrite(t, dir)
	target := filepath.Join(dir, "existing.go")
	if err := os.WriteFile(target, []byte("precious content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := doWrite(t, w, target, "clobbered\n")
	if err == nil {
		t.Fatal("overwriting an unread file was allowed")
	}
	if !strings.Contains(err.Error(), "has not been read") {
		t.Errorf("refusal does not explain why: %v", err)
	}
	// The file must be untouched — a refusal that still wrote would be
	// the worst of both worlds.
	got, _ := os.ReadFile(target)
	if string(got) != "precious content\n" {
		t.Errorf("file was modified despite the refusal: %q", got)
	}
}

func TestWrite_AfterObserveIsAllowed(t *testing.T) {
	dir := t.TempDir()
	w, obs := guardedWrite(t, dir)
	target := filepath.Join(dir, "seen.go")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	obs.Observe(target) // stands in for a `read` call
	if _, err := doWrite(t, w, target, "new\n"); err != nil {
		t.Fatalf("write after read was refused: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new\n" {
		t.Errorf("content = %q, want new", got)
	}
}

func TestWrite_StaleFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	w, obs := guardedWrite(t, dir)
	target := filepath.Join(dir, "raced.go")
	if err := os.WriteFile(target, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	obs.Observe(target)

	// A concurrent editor / build step changes the file.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(target, []byte("v2 from a colleague\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := doWrite(t, w, target, "v3 based on v1\n")
	if err == nil {
		t.Fatal("overwriting a file that changed on disk was allowed")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Errorf("refusal does not explain the race: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v2 from a colleague\n" {
		t.Errorf("colleague's change was clobbered: %q", got)
	}
}

// TestWrite_SecondWriteIsAllowed guards against the guard misfiring on
// the tool's own output: write → write must not report "changed on disk".
func TestWrite_SecondWriteIsAllowed(t *testing.T) {
	dir := t.TempDir()
	w, _ := guardedWrite(t, dir)
	target := filepath.Join(dir, "iterated.go")

	if _, err := doWrite(t, w, target, "first\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := doWrite(t, w, target, "second\n"); err != nil {
		t.Fatalf("second write to a file this tool just wrote was refused: %v", err)
	}
}

func TestWrite_NilObserverIsUnguarded(t *testing.T) {
	// Backwards compatibility: a write tool built without an observer
	// must behave exactly as it did before the guard existed.
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefYolo)
	w := New(p)
	target := filepath.Join(dir, "unguarded.go")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := doWrite(t, w, target, "new\n"); err != nil {
		t.Fatalf("unguarded write was refused: %v", err)
	}
}

// TestWrite_GuardRunsAfterPermissionCheck keeps the ordering explicit:
// a permission denial must win over a guard refusal, so the model sees
// the more fundamental reason first.
func TestWrite_GuardRunsAfterPermissionCheck(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefDeny)
	w := New(p).WithObserver(fsobserve.New())

	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := doWrite(t, w, outside, "y")
	if err == nil {
		t.Fatal("expected a denial")
	}
	if strings.Contains(err.Error(), "has not been read") {
		t.Errorf("guard preempted the permission denial: %v", err)
	}
}

// TestWrite_ReadThenWrite_Integration proves the two tools agree on the
// observation key. Both resolve through policy.Resolve, but if that ever
// diverged (symlink handling, relative-vs-absolute), the guard would
// refuse every write forever and the unit tests above — which call
// Observe directly — would not notice.
func TestWrite_ReadThenWrite_Integration(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefYolo)
	obs := fsobserve.New()

	r := read.New(p).WithObserver(obs)
	w := New(p).WithObserver(obs)

	target := filepath.Join(dir, "roundtrip.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unread: refused.
	if _, err := doWrite(t, w, target, "clobber\n"); err == nil {
		t.Fatal("write before read was allowed")
	}

	// The model reads it through the real read tool.
	rargs, _ := json.Marshal(map[string]string{"path": target})
	if _, err := r.Execute(context.Background(), rargs); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Now the write is permitted.
	if _, err := doWrite(t, w, target, "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("write after a real read was refused — the tools disagree on the observation key: %v", err)
	}
}

// TestWrite_RelativePathMatchesAbsoluteObservation covers the same key
// question from the other side: the model may read with an absolute path
// and write with a repo-relative one (or vice versa).
func TestWrite_RelativePathMatchesAbsoluteObservation(t *testing.T) {
	dir := t.TempDir()
	p, _ := permission.New(dir, permission.PrefYolo)
	obs := fsobserve.New()
	r := read.New(p).WithObserver(obs)
	w := New(p).WithObserver(obs)

	if err := os.WriteFile(filepath.Join(dir, "rel.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rargs, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "rel.go")})
	if _, err := r.Execute(context.Background(), rargs); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := doWrite(t, w, "rel.go", "y\n"); err != nil {
		t.Fatalf("relative-path write after absolute-path read was refused: %v", err)
	}
}

// ---- writeGuarded: the syscall-level half of the guard ----
//
// These drive writeGuarded directly with a hand-built plan, because the
// races they cover happen BETWEEN Plan and the write — a window that
// cannot be opened from outside Execute.

// TestWriteGuarded_ExclusiveCreateRefusesRaceWinner is the O_EXCL case:
// the plan said the target was absent, but it exists by the time we
// write. A plain os.WriteFile would silently clobber it — that is the
// blind overwrite the guard exists to prevent, arriving through the
// check-then-write window instead of through the model.
func TestWriteGuarded_ExclusiveCreateRefusesRaceWinner(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "appeared.go")
	if err := os.WriteFile(target, []byte("written by someone else\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plan captured when the path was still empty.
	plan := fsobserve.Decision{Status: fsobserve.StatusOK, Guarded: true, Exists: false}

	err := writeGuarded(target, []byte("clobber\n"), plan)
	if err == nil {
		t.Fatal("exclusive create silently overwrote a file that appeared in the race window")
	}
	if !strings.Contains(err.Error(), "has not been read") {
		t.Errorf("unexpected refusal text: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "written by someone else\n" {
		t.Errorf("racer's content was clobbered: %q", got)
	}
}

func TestWriteGuarded_ExclusiveCreateSucceedsOnAbsentTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fresh.go")
	plan := fsobserve.Decision{Status: fsobserve.StatusOK, Guarded: true, Exists: false}

	if err := writeGuarded(target, []byte("hello\n"), plan); err != nil {
		t.Fatalf("exclusive create of an absent target failed: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "hello\n" {
		t.Errorf("content = %q", got)
	}
}

// TestWriteGuarded_ReplaceRefusesTokenMismatch covers the other window:
// the plan authorised replacing a specific version of the file, and the
// file changed before the write landed.
func TestWriteGuarded_ReplaceRefusesTokenMismatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "raced.go")
	if err := os.WriteFile(target, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	obs := fsobserve.New()
	obs.Observe(target)
	plan := obs.Plan(target)
	if !plan.Guarded || !plan.Exists {
		t.Fatalf("precondition: plan = %+v", plan)
	}

	// The world moves between plan and write.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(target, []byte("v2 from elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeGuarded(target, []byte("v3\n"), plan)
	if err == nil {
		t.Fatal("guarded replace overwrote a file that changed after the plan")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Errorf("unexpected refusal text: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v2 from elsewhere\n" {
		t.Errorf("racer's content was clobbered: %q", got)
	}
}

func TestWriteGuarded_ReplaceSucceedsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "stable.go")
	if err := os.WriteFile(target, []byte("original content that is long\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	obs := fsobserve.New()
	obs.Observe(target)
	plan := obs.Plan(target)

	if err := writeGuarded(target, []byte("short\n"), plan); err != nil {
		t.Fatalf("guarded replace of an unchanged file failed: %v", err)
	}
	got, _ := os.ReadFile(target)
	// Truncation must be complete — no tail of the longer original.
	if string(got) != "short\n" {
		t.Errorf("content = %q, want exactly \"short\\n\" (stale tail not truncated?)", got)
	}
}

// TestWriteGuarded_ReplaceRefusesIfDeleted: the plan authorised replacing
// an existing file. If it is gone, recreating it would resurrect content
// the user (or a git operation) deliberately removed.
func TestWriteGuarded_ReplaceRefusesIfDeleted(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "deleted.go")
	if err := os.WriteFile(target, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	obs := fsobserve.New()
	obs.Observe(target)
	plan := obs.Plan(target)

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	err := writeGuarded(target, []byte("resurrected\n"), plan)
	if err == nil {
		t.Fatal("guarded replace recreated a file that had been deleted")
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Error("the file was recreated despite the refusal")
	}
}

func TestWriteGuarded_UnguardedIsUnconditional(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "unguarded.go")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Guarded:false is what a nil observer plans.
	if err := writeGuarded(target, []byte("new\n"), fsobserve.Decision{Status: fsobserve.StatusOK}); err != nil {
		t.Fatalf("unguarded write failed: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new\n" {
		t.Errorf("content = %q", got)
	}
}

// TestWriteGuarded_PreservesExistingMode: replacing a file must not
// reset its permission bits — an executable script stays executable.
func TestWriteGuarded_PreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	obs := fsobserve.New()
	obs.Observe(target)
	plan := obs.Plan(target)

	if err := writeGuarded(target, []byte("#!/bin/sh\necho new\n"), plan); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable bit lost: mode = %v", fi.Mode().Perm())
	}
}
