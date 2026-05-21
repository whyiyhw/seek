package edit

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

func setup(t *testing.T, body string) (string, Tool) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, _ := permission.New(dir, false)
	return p, New(pol)
}

func run(t *testing.T, tool Tool, args Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return tool.Execute(context.Background(), b)
}

func TestEdit_BasicReplace(t *testing.T) {
	path, tool := setup(t, "hello world\n")
	out, err := run(t, tool, Args{Path: path, OldString: "world", NewString: "there"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 replacement") {
		t.Errorf("output: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello there\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_NotFound(t *testing.T) {
	path, tool := setup(t, "hello\n")
	_, err := run(t, tool, Args{Path: path, OldString: "missing", NewString: "x"})
	if err == nil || !strings.Contains(err.Error(), "occurs 0 times") {
		t.Errorf("err = %v", err)
	}
}

func TestEdit_AmbiguousMatchAtomic(t *testing.T) {
	path, tool := setup(t, "foo foo foo\n")
	_, err := run(t, tool, Args{Path: path, OldString: "foo", NewString: "bar"})
	if err == nil || !strings.Contains(err.Error(), "occurs 3 times") {
		t.Errorf("err = %v", err)
	}
	// File must be unchanged after a failed edit.
	got, _ := os.ReadFile(path)
	if string(got) != "foo foo foo\n" {
		t.Errorf("file mutated: %q", got)
	}
}

func TestEdit_ExpectedReplacements(t *testing.T) {
	path, tool := setup(t, "x x x\n")
	out, err := run(t, tool, Args{Path: path, OldString: "x", NewString: "y", ExpectedReplacements: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 replacement") {
		t.Errorf("output: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "y y y\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_DeleteWithEmptyNew(t *testing.T) {
	path, tool := setup(t, "abcXYZdef\n")
	_, err := run(t, tool, Args{Path: path, OldString: "XYZ", NewString: ""})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "abcdef\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_NoOpRejected(t *testing.T) {
	path, tool := setup(t, "same\n")
	_, err := run(t, tool, Args{Path: path, OldString: "same", NewString: "same"})
	if err == nil || !strings.Contains(err.Error(), "no-op") {
		t.Errorf("err = %v", err)
	}
}

func TestEdit_OutsideCWDDenied(t *testing.T) {
	dir := t.TempDir()
	pol, _ := permission.New(dir, false)
	tool := New(pol)
	other := t.TempDir()
	p := filepath.Join(other, "x.txt")
	os.WriteFile(p, []byte("a"), 0o644)
	_, err := run(t, tool, Args{Path: p, OldString: "a", NewString: "b"})
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("err = %v", err)
	}
}
