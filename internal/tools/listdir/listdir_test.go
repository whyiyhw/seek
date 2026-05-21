package listdir

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Tree:
	//   root/
	//     a.txt           "hello" (5 bytes)
	//     .hidden
	//     sub/
	//       b.go          "package x" (9 bytes)
	//       deeper/
	//         c.md        "" (0 bytes)
	mustWrite(t, filepath.Join(root, "a.txt"), "hello")
	mustWrite(t, filepath.Join(root, ".hidden"), "x")
	must(t, os.MkdirAll(filepath.Join(root, "sub", "deeper"), 0o755))
	mustWrite(t, filepath.Join(root, "sub", "b.go"), "package x")
	mustWrite(t, filepath.Join(root, "sub", "deeper", "c.md"), "")
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, args Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return New().Execute(context.Background(), b)
}

func TestListDir_FlatDefault(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("missing a.txt: %s", out)
	}
	if !strings.Contains(out, "sub/") {
		t.Errorf("missing sub/: %s", out)
	}
	// Default hidden=false, .hidden should not appear.
	if strings.Contains(out, ".hidden") {
		t.Errorf("dot-file leaked at hidden=false: %s", out)
	}
	// Default depth=1, deeper/ contents should not appear.
	if strings.Contains(out, "c.md") {
		t.Errorf("recursed beyond depth 1: %s", out)
	}
}

func TestListDir_RecursiveDepth2(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Path: root, Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "b.go") {
		t.Errorf("missing b.go at depth 2: %s", out)
	}
	if strings.Contains(out, "c.md") {
		t.Errorf("recursed too deep: %s", out)
	}
}

func TestListDir_ShowHidden(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Path: root, ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ".hidden") {
		t.Errorf("hidden not shown: %s", out)
	}
}

func TestListDir_NotADirectory(t *testing.T) {
	root := setup(t)
	_, err := run(t, Args{Path: filepath.Join(root, "a.txt")})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v", err)
	}
}

func TestListDir_NotFound(t *testing.T) {
	_, err := run(t, Args{Path: "/no/such/path/exists"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListDir_MissingPath(t *testing.T) {
	_, err := run(t, Args{})
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("err = %v", err)
	}
}

func TestListDir_DirsBeforeFiles(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	// sub/ should come before a.txt in the listing.
	if strings.Index(out, "sub/") > strings.Index(out, "a.txt") {
		t.Errorf("dirs should precede files:\n%s", out)
	}
}
