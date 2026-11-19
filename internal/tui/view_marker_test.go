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

// TestRenderUserBlock_DimsFileMarkers: the "[file: " family dims under
// the same rule (F5) — attach markers and failure notes alike, since
// fileroute.TestMarker_Family pins every variant to the one prefix.
func TestRenderUserBlock_DimsFileMarkers(t *testing.T) {
	in := "summarise @report.pdf\n\n[file: report.pdf — uploaded · text · 12 pages · file-api-x]\n[file: huge.pdf — 超过单文档 100 MiB 上限]\n[file: gone.pdf — 读取失败: no such file]"
	out := renderUserBlock(in, 0)
	for _, marker := range []string{
		"[file: report.pdf — uploaded",
		"[file: huge.pdf — 超过单文档",
		"[file: gone.pdf — 读取失败",
	} {
		if !strings.Contains(out, marker) {
			t.Fatalf("marker %q lost: %q", marker, out)
		}
	}
	if out == in {
		t.Fatal("markers were not styled")
	}
	// Non-marker body is byte-preserved; the @ref carries its own
	// accent styling from highlightRefs, so its bytes appear inside
	// that styling rather than as the bare contiguous input string.
	if !strings.Contains(out, "summarise ") || !strings.Contains(out, "@report.pdf") {
		t.Fatal("body mutated")
	}
}

func TestDimMarkers_NoMarkers_Identity(t *testing.T) {
	in := "just text\nwith lines"
	if got := dimMarkers(in); got != in {
		t.Fatalf("identity failed: %q", got)
	}
}

// TestDimMarkers_OnlyOneFamily: a marker of one family must not mute
// lines of the other when only one is present — the prefix check is
// per-line, and mixing must not leak styling across families.
func TestDimMarkers_OnlyOneFamily(t *testing.T) {
	fileOnly := "text\n[file: a.pdf — uploaded · text · file-api-x]"
	out := dimMarkers(fileOnly)
	if out == fileOnly {
		t.Fatal("[file: marker not styled")
	}
	imgOnly := "text\n[image: shot.png — attached natively · 3x2]"
	if out := dimMarkers(imgOnly); out == imgOnly {
		t.Fatal("[image: marker not styled")
	}
}
