package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/clipimage"
)

// M-imgpaste.2: Ctrl+V image paste.

func TestTryClipboardPaste_Image(t *testing.T) {
	m := testModel().BuildPtr()
	m.opts.Ctx = context.Background()
	m.opts.GrabImage = func(context.Context) (string, error) { return "/tmp/clip-xyz.png", nil }

	nm, ok := m.tryClipboardPaste()
	if !ok {
		t.Fatal("a clipboard image should be handled")
	}
	if nm.pastedImagePath != "/tmp/clip-xyz.png" {
		t.Fatalf("pastedImagePath = %q", nm.pastedImagePath)
	}
	if !strings.Contains(nm.input.Value(), imagePasteMarker) {
		t.Fatalf("input should show the image fold marker: %q", nm.input.Value())
	}
}

func TestResolvePasteInInput_Image(t *testing.T) {
	m := testModel().BuildPtr()
	m.pastedImagePath = "/tmp/clip-xyz.png"
	m.input.SetValue("what is in " + imagePasteMarker)

	m.resolvePasteInInput()

	if got := m.input.Value(); got != "what is in @/tmp/clip-xyz.png" {
		t.Fatalf("marker should resolve to @<path>, got %q", got)
	}
	if m.pastedImagePath != "" {
		t.Fatal("pastedImagePath must be cleared after resolve")
	}
}

func TestTryClipboardPaste_NoImageFallsThrough(t *testing.T) {
	m := testModel().BuildPtr()
	m.opts.Ctx = context.Background()
	m.opts.GrabImage = func(context.Context) (string, error) { return "", clipimage.ErrNoImage }

	// No image → the image branch must NOT fire (no marker, no path). The
	// text fallback hits the real OS clipboard, which we don't assert on.
	nm, _ := m.tryClipboardPaste()
	if nm.pastedImagePath != "" {
		t.Fatal("no image → pastedImagePath must stay empty")
	}
	if strings.Contains(nm.input.Value(), imagePasteMarker) {
		t.Fatal("no image → the image marker must not be inserted")
	}
}

func TestResolvePasteInInput_NoPendingNoop(t *testing.T) {
	m := testModel().BuildPtr()
	m.input.SetValue("just text")
	m.resolvePasteInInput()
	if m.input.Value() != "just text" {
		t.Fatalf("resolve with nothing pending must be a no-op, got %q", m.input.Value())
	}
}
