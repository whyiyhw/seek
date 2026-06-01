package clipimage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// writeCmd builds a fake grabber that writes fixed content to the output
// path (its last arg, $1) — i.e. "the clipboard has an image".
func writeCmd(content string) []string {
	return []string{"sh", "-c", `printf '%s' ` + shquote(content) + ` > "$1"`, "sh"}
}

// shquote single-quotes s for sh.
func shquote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
		} else {
			out += string(r)
		}
	}
	return out + "'"
}

func TestGrab_HasImage(t *testing.T) {
	path, err := Grab(context.Background(), Options{
		Command:  writeCmd("FAKE-PNG-DATA"),
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "FAKE-PNG-DATA" {
		t.Fatalf("grabbed content = %q", b)
	}
}

func TestGrab_NoImage_EmptyOutput(t *testing.T) {
	// Grabber exits 0 but writes nothing (no image on clipboard).
	dir := t.TempDir()
	_, err := Grab(context.Background(), Options{
		Command:  []string{"sh", "-c", "exit 0", "sh"},
		CacheDir: dir,
	})
	if !errors.Is(err, ErrNoImage) {
		t.Fatalf("empty grab should be ErrNoImage, got %v", err)
	}
	// No stray temp file left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("temp file leaked on no-image: %v", entries)
	}
}

func TestGrab_NoImage_NonZeroExit(t *testing.T) {
	_, err := Grab(context.Background(), Options{
		Command:  []string{"sh", "-c", "exit 3", "sh"},
		CacheDir: t.TempDir(),
	})
	if !errors.Is(err, ErrNoImage) {
		t.Fatalf("non-zero grabber exit should be ErrNoImage, got %v", err)
	}
}

func TestGrab_NoGrabber(t *testing.T) {
	// No explicit command + no platform default → ErrNoGrabber.
	orig := defaultGrabCommand
	defaultGrabCommand = func() []string { return nil }
	defer func() { defaultGrabCommand = orig }()

	if _, err := Grab(context.Background(), Options{CacheDir: t.TempDir()}); !errors.Is(err, ErrNoGrabber) {
		t.Fatalf("no grabber should be ErrNoGrabber, got %v", err)
	}
}

func TestGrab_CanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Canceled ctx → the grabber can't run → ErrNoImage (degrade, no crash).
	if _, err := Grab(ctx, Options{Command: writeCmd("x"), CacheDir: t.TempDir()}); err == nil {
		t.Fatal("canceled ctx should not yield a successful grab")
	}
}

func TestDefaultGrabCommand_Platform(t *testing.T) {
	// macOS/Linux have a default; elsewhere nil. A non-nil default must be
	// a real argv (sanity that it isn't accidentally an empty slice).
	if got := defaultGrabCommand(); got != nil && len(got) == 0 {
		t.Fatal("non-nil default must be a real argv")
	}
}

func TestGCOldGrabs(t *testing.T) {
	dir := t.TempDir()
	// An old grab + a fresh grab + an unrelated file.
	old := dir + "/clip-old.png"
	os.WriteFile(old, []byte("x"), 0o644)
	os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	fresh := dir + "/clip-fresh.png"
	os.WriteFile(fresh, []byte("x"), 0o644)
	keep := dir + "/notes.txt"
	os.WriteFile(keep, []byte("x"), 0o644)

	gcOldGrabs(dir)

	if _, err := os.Stat(old); err == nil {
		t.Error("old clip PNG should be GC'd")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh clip PNG must be kept")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("unrelated file must not be touched")
	}
}
