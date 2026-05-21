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
	p, _ := permission.New(dir, permission.ModeDeny)
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
	p, _ := permission.New(dir, permission.ModeDeny)
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
	p, _ := permission.New(dir, permission.ModeDeny)
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
	p, _ := permission.New(dir, permission.ModeDeny)
	args, _ := json.Marshal(Args{Path: filepath.Join(other, "evil"), Content: "x"})
	_, err := New(p).Execute(context.Background(), args)
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestWrite_YoloAllowsOutsideCWD(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	p, _ := permission.New(dir, permission.ModeYolo)
	target := filepath.Join(other, "ok")
	args, _ := json.Marshal(Args{Path: target, Content: "x"})
	if _, err := New(p).Execute(context.Background(), args); err != nil {
		t.Errorf("yolo write blocked: %v", err)
	}
}

func TestWrite_MissingPath(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.ModeDeny)
	_, err := New(p).Execute(context.Background(), json.RawMessage(`{"content":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("err = %v", err)
	}
}
