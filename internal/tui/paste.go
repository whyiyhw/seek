package tui

import (
	"context"
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

// imagePasteMarker is the fold placeholder shown in the input after a
// clipboard image is grabbed (M-imgpaste.2); resolved to `@<path>` on
// submit so the OCR pipeline picks it up.
const imagePasteMarker = "📋 image — press Enter to OCR & send"

// resolvePasteInInput replaces pending fold markers with their bodies just
// before submit: the text-paste marker → the full pasted text; the image
// marker → `@<temp PNG path>` (M-imgpaste.2) so ExpandInput / ocr.Expand
// OCRs it. No-op when nothing is pending.
func (m *Model) resolvePasteInInput() {
	if m.pastedContent == "" && m.pastedImagePath == "" {
		return
	}
	val := m.input.Value()
	if m.pastedContent != "" {
		if marker := pasteFoldMarker(m.pastedLineCount); strings.Contains(val, marker) {
			val = strings.Replace(val, marker, m.pastedContent, 1)
		}
		m.pastedContent = ""
		m.pastedLineCount = 0
	}
	if m.pastedImagePath != "" {
		if strings.Contains(val, imagePasteMarker) {
			val = strings.Replace(val, imagePasteMarker, "@"+m.pastedImagePath, 1)
		}
		m.pastedImagePath = ""
	}
	m.input.SetValue(val)
}

// tryClipboardPaste handles Ctrl+V. M-imgpaste.2: if the clipboard holds an
// image (and a grabber is wired), grab it to a temp PNG and insert a fold
// marker that resolves to `@<path>` on submit — reusing the 柱 Q OCR
// pipeline. Otherwise fall back to text paste. A slow/hanging grabber is
// bounded by a short timeout so the UI never wedges.
func (m Model) tryClipboardPaste() (Model, bool) {
	if m.opts.GrabImage != nil {
		base := m.opts.Ctx
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithTimeout(base, 3*time.Second)
		path, err := m.opts.GrabImage(ctx)
		cancel()
		if err == nil && path != "" {
			m.pastedImagePath = path
			m.input.InsertString(imagePasteMarker)
			return m, true
		}
	}
	text, err := clipboard.ReadAll()
	if err != nil || text == "" {
		return m, false
	}
	return m.insertPasteText(text), true
}
