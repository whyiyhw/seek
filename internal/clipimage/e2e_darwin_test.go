//go:build darwin

package clipimage

import (
	"context"
	"os"
	"testing"
)

// TestGrab_RealClipboard_E2E exercises the real macOS osascript grabber
// (the default command, M-imgpaste.3) against an image actually on the
// clipboard. Gated behind SEEK_CLIPIMAGE_E2E because it needs the clipboard
// pre-seeded (the harness does that with osascript before running). Run:
//
//	osascript -e 'set the clipboard to (read (POSIX file "/tmp/x.png") as «class PNGf»)'
//	SEEK_CLIPIMAGE_E2E=1 go test -run TestGrab_RealClipboard_E2E ./internal/clipimage/
func TestGrab_RealClipboard_E2E(t *testing.T) {
	if os.Getenv("SEEK_CLIPIMAGE_E2E") != "1" {
		t.Skip("set SEEK_CLIPIMAGE_E2E=1 (and put an image on the clipboard) to run the real macOS grabber e2e")
	}
	path, err := Grab(context.Background(), Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Grab with the macOS default failed (is an image on the clipboard?): %v", err)
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
	t.Logf("grabbed %d-byte PNG via the osascript default grabber", len(b))
}
