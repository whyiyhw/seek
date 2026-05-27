package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func TestNormalizePasteText_CRLF(t *testing.T) {
	got := normalizePasteText("a\r\nb\rc")
	want := "a\nb\nc"
	if got != want {
		t.Fatalf("normalizePasteText() = %q, want %q", got, want)
	}
}

func TestPasteLineCount_CRLF(t *testing.T) {
	if n := pasteLineCount("a\r\nb\r\n"); n != 2 {
		t.Fatalf("pasteLineCount(CRLF) = %d, want 2", n)
	}
}

func TestEnterInsertsNewlineDuringPaste(t *testing.T) {
	m := Model{lastInputRunesAt: time.Now()}
	if !m.enterInsertsNewlineDuringPaste() {
		t.Fatal("expected Enter within pasteEnterGap to insert newline")
	}
	m.lastInputRunesAt = time.Now().Add(-pasteEnterGap - time.Millisecond)
	if m.enterInsertsNewlineDuringPaste() {
		t.Fatal("expected stale Enter to submit, not newline")
	}
}

func TestPasteBurstEnterInsertsNewline(t *testing.T) {
	m := Model{input: textarea.New()}
	// Set in the future so the time check is always within pasteEnterGap,
	// regardless of goroutine scheduling delays under -race or heavy load.
	m.lastInputRunesAt = time.Now().Add(pasteEnterGap)
	m.input.SetValue("line1")

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := out.(Model)
	if mm.input.Value() != "line1\n" {
		t.Fatalf("burst Enter should insert newline, got %q", mm.input.Value())
	}
}

func TestInsertPasteText_FoldsLongPaste(t *testing.T) {
	m := Model{input: textarea.New()}
	m.input.SetHeight(3)
	lines := strings.Join([]string{"l1", "l2", "l3", "l4", "l5", "l6"}, "\n")

	out := m.insertPasteText(lines)
	if out.pastedContent != lines {
		t.Fatalf("pastedContent not stored: %q", out.pastedContent)
	}
	if !strings.Contains(out.input.Value(), "pasted 6 lines") {
		t.Fatalf("expected fold marker, got %q", out.input.Value())
	}
}

func TestResolvePasteInInput(t *testing.T) {
	m := Model{input: textarea.New()}
	m.pastedContent = "real\nbody"
	m.pastedLineCount = 2
	m.input.SetValue(pasteFoldMarker(2))

	(&m).resolvePasteInInput()
	if m.pastedContent != "" {
		t.Fatal("pastedContent should be cleared")
	}
	if got := m.input.Value(); got != "real\nbody" {
		t.Fatalf("resolvePasteInInput() = %q", got)
	}
}
