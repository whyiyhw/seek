package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

	// No image → the image branch must NOT fire. The text fallback hits
	// the REAL OS clipboard, so assert only what the image branch owns
	// (the pending path); asserting on the input VALUE would couple the
	// test to whatever the developer last copied — including, at least
	// once, the marker text itself.
	nm, _ := m.tryClipboardPaste()
	if nm.pastedImagePath != "" {
		t.Fatal("no image → pastedImagePath must stay empty")
	}
}

// TestUpdate_EmptyPasteGrabsImage pins the interception-proof image path:
// a terminal-level paste on an image-only clipboard arrives as an EMPTY
// PasteMsg (the terminal eats the chord, finds no text format, emits just
// the bracketed-paste wrappers) — verified against a real Windows
// Terminal, where BOTH ctrl+v and ctrl+shift+v produce exactly this and
// never a keypress. That empty paste is the user's image intent reaching
// seek by its only channel, so it must trigger the grab.
func TestUpdate_EmptyPasteGrabsImage(t *testing.T) {
	m := testModel().BuildPtr()
	m.opts.Ctx = context.Background()
	m.opts.GrabImage = func(context.Context) (string, error) { return "/tmp/clip-xyz.png", nil }

	out, _ := m.Update(tea.PasteMsg{Content: ""})
	nm, ok := out.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", out)
	}
	if nm.pastedImagePath != "/tmp/clip-xyz.png" {
		t.Errorf("empty paste must trigger the image grab; pastedImagePath = %q", nm.pastedImagePath)
	}
	if !strings.Contains(nm.input.Value(), imagePasteMarker) {
		t.Errorf("input should show the image fold marker: %q", nm.input.Value())
	}
}

// TestBackspace_DeletesImageMarkerAtomically pins the token-style delete:
// one Backspace with the caret at the end of the input removes the ENTIRE
// image marker and cancels the pending attachment — the marker behaves
// like a chip, not 30+ runes to grind through (whose partial remains
// resolvePasteInInput would silently drop while leaking fragments into
// the sent message).
func TestBackspace_DeletesImageMarkerAtomically(t *testing.T) {
	m := testModel().BuildPtr()
	m.pastedImagePath = "/tmp/clip-xyz.png"
	m.input.SetValue("look " + imagePasteMarker)

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	nm, ok := out.(Model)
	if !ok {
		t.Fatalf("handleKey returned %T, want Model", out)
	}
	if got := nm.input.Value(); got != "look " {
		t.Fatalf("one Backspace should drop the WHOLE marker, got %q", got)
	}
	if nm.pastedImagePath != "" {
		t.Fatal("cancelling the marker must clear pastedImagePath")
	}
}

// TestBackspace_MidEditLeavesMarkerAtomicityOff: with the caret NOT at the
// end (user moved back to edit earlier text), Backspace must stay a
// normal rune delete — silently cancelling the pending image because the
// marker sits at the end of the value would be data loss.
func TestBackspace_MidEditLeavesMarkerAtomicityOff(t *testing.T) {
	m := testModel().BuildPtr()
	m.pastedImagePath = "/tmp/clip-xyz.png"
	m.input.SetValue("look " + imagePasteMarker)

	// Move the caret one rune left (between the marker's last two chars)
	// first: Backspace then deletes the rune BEFORE the caret, proving
	// the atomic path stayed off.
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	out, _ = out.(Model).handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	nm := out.(Model)

	want, got := []rune("look "+imagePasteMarker), []rune(nm.input.Value())
	if len(got) != len(want)-1 {
		t.Fatalf("mid-edit Backspace should delete exactly ONE rune, got %d runes (was %d): %q",
			len(got), len(want), nm.input.Value())
	}
	if nm.pastedImagePath != "/tmp/clip-xyz.png" {
		t.Fatal("mid-edit Backspace must NOT cancel the pending image")
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

// TestHandleKey_PasteKeys_ReachImageGrab pins the DUAL binding: both
// ctrl+v and ctrl+shift+v must route into the image grab. Windows
// terminals eat one of the two by default and pass the other (Windows
// Terminal: plain ctrl+v is its own text paste; conhost with the
// Ctrl+Shift+C/V option checked: ctrl+shift+v), so either key alone
// leaves the grab unreachable on a stock terminal — the exact "Ctrl+V
// does nothing" report that shipped the Windows grabber in the first
// place. And the terminal's paste event can never substitute: paste
// carries UTF-8 text, the bitmap only lives in the OS clipboard.
func TestHandleKey_PasteKeys_ReachImageGrab(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'v', Mod: tea.ModCtrl},
		{Code: 'v', Mod: tea.ModCtrl | tea.ModShift},
		// Windows console input capitalises the rune under Shift — the
		// same physical chord can arrive as 'V'. Must still match.
		{Code: 'V', Mod: tea.ModCtrl | tea.ModShift},
	} {
		m := testModel().BuildPtr()
		m.opts.Ctx = context.Background()
		m.opts.GrabImage = func(context.Context) (string, error) { return "/tmp/clip-xyz.png", nil }

		out, _ := m.handleKey(key)
		nm, ok := out.(Model)
		if !ok {
			t.Fatalf("%s: handleKey returned %T, want Model", key.String(), out)
		}
		if nm.pastedImagePath != "/tmp/clip-xyz.png" {
			t.Errorf("%s: key must reach the image grab; pastedImagePath = %q", key.String(), nm.pastedImagePath)
		}
		if !strings.Contains(nm.input.Value(), imagePasteMarker) {
			t.Errorf("%s: input should show the image fold marker: %q", key.String(), nm.input.Value())
		}
	}
}
