package write

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
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
