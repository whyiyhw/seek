package clipimage

import (
	"context"
	"errors"
	"os"
	"strings"
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
	defaultGrabCommand = func() ([]string, string) { return nil, "" }
	defer func() { defaultGrabCommand = orig }()

	if _, err := Grab(context.Background(), Options{CacheDir: t.TempDir()}); !errors.Is(err, ErrNoGrabber) {
		t.Fatalf("no grabber should be ErrNoGrabber, got %v", err)
	}
}

// TestGrab_OutEnvDeliversPath: when the default names an outEnv, Grab must
// pass the reserved path through that env var and NOT append it to argv — the
// Windows default depends on this (argv would be re-parsed as PowerShell
// command text, splitting any path with a space).
func TestGrab_OutEnvDeliversPath(t *testing.T) {
	orig := defaultGrabCommand
	// Fake "windows-like" default: writes $FAKE_OUT to the file at that path.
	defaultGrabCommand = func() ([]string, string) {
		return []string{"sh", "-c", `printf '%s' "$FAKE_OUT" > "$FAKE_OUT"`, "sh"}, "FAKE_OUT"
	}
	defer func() { defaultGrabCommand = orig }()

	path, err := Grab(context.Background(), Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("env-delivered path should grab: %v", err)
	}
	defer os.Remove(path)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != path {
		t.Fatalf("grabber wrote to %q, want the reserved path %q", b, path)
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
	// macOS/Linux/Windows have a default; elsewhere nil. A non-nil default
	// must be a real argv (sanity that it isn't accidentally an empty slice).
	if got, _ := defaultGrabCommand(); got != nil && len(got) == 0 {
		t.Fatal("non-nil default must be a real argv")
	}
}

// TestDefaultGrabCommandFor_Platforms pins every branch on every host — the
// switch is on a goos PARAMETER, not runtime.GOOS, so a Linux CI box still
// exercises the Windows entry (the branch that was missing until the Windows
// default landed: Ctrl+V silently pasted nothing there).
func TestDefaultGrabCommandFor_Platforms(t *testing.T) {
	tests := []struct {
		goos   string
		first  string // "" = expect no default at all
		outEnv string
	}{
		{"darwin", "osascript", ""},
		{"linux", "sh", ""},
		{"windows", "powershell", windowsOutEnv},
		{"plan9", "", ""},
	}
	for _, tc := range tests {
		got, outEnv := defaultGrabCommandFor(tc.goos)
		if tc.first == "" {
			if got != nil {
				t.Errorf("%s: want nil (no default grabber), got %q", tc.goos, got)
			}
			continue
		}
		if len(got) == 0 || got[0] != tc.first {
			t.Errorf("%s: want argv starting with %q, got %q", tc.goos, tc.first, got)
			continue
		}
		if outEnv != tc.outEnv {
			t.Errorf("%s: outEnv = %q, want %q", tc.goos, outEnv, tc.outEnv)
		}
	}
}

// TestDefaultGrabCommandFor_WindowsShape guards the contract the Windows
// default depends on: the output path CANNOT ride on argv, because
// `powershell -Command` re-parses everything after -Command as command TEXT —
// a path with a space (C:\Users\John Smith\…) splits, and an apostrophe
// (C:\Users\O'Brien\…) is a parse error. So: the argv must NOT invite a
// trailing path (outEnv is set) and the script must read the env var, or the
// grab writes nowhere and Ctrl+V degrades to text paste. See docs/pitfalls.md
// "PowerShell `-Command`: everything after it is command TEXT".
func TestDefaultGrabCommandFor_WindowsShape(t *testing.T) {
	got, outEnv := defaultGrabCommandFor("windows")
	if len(got) < 2 {
		t.Fatalf("windows default too short: %q", got)
	}
	if got[len(got)-2] != "-Command" {
		t.Fatalf("windows default must end with `-Command <script>`; got %q", got)
	}
	if outEnv != windowsOutEnv {
		t.Fatalf("windows default must deliver the path via %s (argv is command text), got outEnv %q", windowsOutEnv, outEnv)
	}
	script := got[len(got)-1]
	if !strings.Contains(script, "$env:"+windowsOutEnv) {
		t.Errorf("script must read the output path from $env:%s: %q", windowsOutEnv, script)
	}
	if !strings.Contains(script, "Clipboard]::GetImage()") {
		t.Errorf("script must read the clipboard bitmap: %q", script)
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
