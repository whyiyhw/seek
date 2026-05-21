package permission

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestYoloAllowsEverything(t *testing.T) {
	p, err := New(t.TempDir(), ModeYolo)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Action{
		{Kind: KindBash, Command: "rm -rf /"},
		{Kind: KindWrite, Path: "/etc/hosts"},
		{Kind: KindEdit, Path: "/no/such/place/file"},
	} {
		if err := p.Check(a); err != nil {
			t.Errorf("yolo denied %v: %v", a, err)
		}
	}
}

func TestBashRequiresYolo(t *testing.T) {
	p, _ := New(t.TempDir(), ModeDeny)
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestWriteInsideCWD(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, ModeDeny)
	for _, path := range []string{
		filepath.Join(root, "a.txt"),
		filepath.Join(root, "deep", "nested", "b.txt"),
		root, // the root itself
	} {
		if err := p.Check(Action{Kind: KindWrite, Path: path}); err != nil {
			t.Errorf("denied %q: %v", path, err)
		}
	}
}

func TestWriteOutsideCWD(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir() // different dir
	p, _ := New(root, ModeDeny)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(other, "x")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestEditAlsoCWDGated(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, ModeDeny)
	err := p.Check(Action{Kind: KindEdit, Path: "/etc/hosts"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestUnknownKind(t *testing.T) {
	p, _ := New(t.TempDir(), ModeDeny)
	err := p.Check(Action{Kind: "voodoo"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}
