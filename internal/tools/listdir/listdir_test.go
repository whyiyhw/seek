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

// TestListDir_UnknownFieldSurfacesActionableError is the regression
// for the real-world failure where the model called
//
//	list_dir({"directory": "/path", "depth": 1})
//
// — using `directory` instead of `path`. Pre-fix the error was the
// useless "list_dir: path is required" (because Go's json.Unmarshal
// silently drops unknown fields). With DisallowUnknownFields the
// model now sees `unknown field "directory"` plus a list of the
// valid fields, and the next call gets it right.
func TestListDir_UnknownFieldSurfacesActionableError(t *testing.T) {
	_, err := New().Execute(context.Background(),
		json.RawMessage(`{"directory":"/tmp","depth":1}`))
	if err == nil {
		t.Fatal("expected unknown-field error")
	}
	msg := err.Error()
	// Must name the bad field — that's the diagnostic the model
	// needs to self-correct.
	if !strings.Contains(msg, "directory") {
		t.Errorf("error should name the unknown field 'directory': %q", msg)
	}
	// Must enumerate the valid fields so the model knows what to
	// send next.
	for _, valid := range []string{"path", "depth", "show_hidden"} {
		if !strings.Contains(msg, valid) {
			t.Errorf("error should list valid field %q: %q", valid, msg)
		}
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
