package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

// pasteEnterGap is how long after the last KeyRunes we treat Enter as a
// newline rather than submit. Windows terminals without bracketed paste
// inject CRLF as "text" + Enter per line; the gap is always sub-millisecond.
const pasteEnterGap = 50 * time.Millisecond

// normalizePasteText converts Windows CRLF (and lone CR) to LF so pasted
// content doesn't carry stray \r bytes into the agent prompt.
func normalizePasteText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func pasteLineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 1
	}
	return strings.Count(s, "\n") + 1
}

func pasteFoldMarker(lineCount int) string {
	return fmt.Sprintf("📋 pasted %d lines — press Enter to send", lineCount)
}

// enterInsertsNewlineDuringPaste reports whether an Enter key should insert
// a newline instead of submitting. Terminals that lack bracketed paste
// (notably legacy Windows conhost) deliver each pasted line as KeyRunes
// immediately followed by Enter (\r).
func (m Model) enterInsertsNewlineDuringPaste() bool {
	if m.lastInputRunesAt.IsZero() {
		return false
	}
	return time.Since(m.lastInputRunesAt) < pasteEnterGap
}

// insertPasteText normalizes and inserts clipboard/terminal paste content,
// then folds when the result exceeds the textarea height.
func (m Model) insertPasteText(text string) Model {
	text = normalizePasteText(text)
	if text == "" {
		return m
	}
	m.input.InsertString(text)
	return m.handlePasteFolding()
}

// resolvePasteInInput replaces a fold marker with the stored paste body.
func (m *Model) resolvePasteInInput() {
	if m.pastedContent == "" {
		return
	}
	marker := pasteFoldMarker(m.pastedLineCount)
	val := m.input.Value()
	if strings.Contains(val, marker) {
		m.input.SetValue(strings.Replace(val, marker, m.pastedContent, 1))
	}
	m.pastedContent = ""
	m.pastedLineCount = 0
}

func (m Model) tryClipboardPaste() (Model, bool) {
	text, err := clipboard.ReadAll()
	if err != nil || text == "" {
		return m, false
	}
	return m.insertPasteText(text), true
}
