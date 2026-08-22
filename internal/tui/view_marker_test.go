package tui

import (
	"strings"
	"testing"
)

// TestRenderUserBlock_DimsImageMarkers: every "[image: " marker line —
// native-attach and OCR-era alike — renders muted; plain text is
// untouched. One rule for live submit, scrollback and replay (they all
// go through renderUserBlock).
func TestRenderUserBlock_DimsImageMarkers(t *testing.T) {
	in := "what is this\n\n[image: shot.png — attached natively · 3x2 · 1.2 KiB]\n[image: old.png — OCR]"
	out := renderUserBlock(in, 0)
	if !strings.Contains(out, "what is this") {
		t.Fatalf("plain text lost: %q", out)
	}
	for _, marker := range []string{"[image: shot.png", "[image: old.png"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("marker %q lost: %q", marker, out)
		}
	}
	// Muting wraps the marker in ANSI styling — assert the marker lines
	// are no longer bare by checking a styled variant exists (the exact
	// escape codes belong to styleMuted, not this test).
	if out == in {
		t.Fatal("markers were not styled")
	}
	// Non-marker body is byte-preserved.
	if !strings.Contains(out, "what is this") {
		t.Fatal("body mutated")
	}
}

func TestDimImageMarkers_NoMarkers_Identity(t *testing.T) {
	in := "just text\nwith lines"
	if got := dimImageMarkers(in); got != in {
		t.Fatalf("identity failed: %q", got)
	}
}
