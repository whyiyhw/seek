//go:build windows

package clipimage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pasteGrabBudget mirrors the TUI's per-grab timeout (internal/tui/paste.go
// tryClipboardPaste): a default that can't answer within it silently degrades
// to text paste, so the real grabber must fit inside it.
const pasteGrabBudget = 3 * time.Second

// TestGrab_RealClipboard_E2E exercises the real Windows PowerShell grabber
// (the default command, M-imgpaste.3) against an image actually on the
// clipboard. Gated behind SEEK_CLIPIMAGE_E2E because it needs the clipboard
// pre-seeded — copy an image in any viewer, or from a -STA PowerShell:
//
//	Add-Type -AssemblyName System.Windows.Forms
//	Add-Type -AssemblyName System.Drawing
//	[System.Windows.Forms.Clipboard]::SetImage([System.Drawing.Image]::FromFile("C:\tmp\x.png"))
//
// Run:
//
//	SEEK_CLIPIMAGE_E2E=1 go test -run TestGrab_RealClipboard_E2E ./internal/clipimage/
func TestGrab_RealClipboard_E2E(t *testing.T) {
	if os.Getenv("SEEK_CLIPIMAGE_E2E") != "1" {
		t.Skip("set SEEK_CLIPIMAGE_E2E=1 (and put an image on the clipboard) to run the real Windows grabber e2e")
	}
	start := time.Now()
	// A space AND an apostrophe in the cache dir are deliberate: the output
	// path must never be re-parsed as PowerShell command text (see
	// windowsOutEnv), and a Windows profile like "C:\Users\John Smith" or
	// "C:\Users\O'Brien" makes both the common case, not an exotic one.
	cacheDir := filepath.Join(t.TempDir(), "clip cache O'Brien")
	path, err := Grab(context.Background(), Options{CacheDir: cacheDir})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Grab with the Windows default failed (is an image on the clipboard?): %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// PNG magic: 0x89 'P' 'N' 'G'.
	if len(b) < 8 || string(b[1:4]) != "PNG" {
		head := b
		if len(head) > 8 {
			head = head[:8]
		}
		t.Fatalf("grabbed file is not a PNG (len=%d, head=%q)", len(b), head)
	}
	if elapsed > pasteGrabBudget {
		t.Errorf("grab took %v — over the TUI's %v budget, Ctrl+V would degrade to a text paste", elapsed, pasteGrabBudget)
	}
	t.Logf("grabbed %d-byte PNG via the powershell default grabber in %v", len(b), elapsed)
}
