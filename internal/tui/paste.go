package tui

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

// pasteEnterGap is how long after the last typed character we treat Enter
// as a newline rather than submit. Windows terminals without bracketed
// paste inject CRLF as "text" + Enter per line; the gap is always
// sub-millisecond.
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
// (notably legacy Windows conhost) deliver each pasted line as character
// payloads immediately followed by Enter (\r).
//
// The guard is deliberately scoped to legacy conhost (see
// legacyConhostInput): everywhere else the same "Enter within pasteEnterGap
// of typed characters" signature is produced by Windows Terminal forwarding
// the IME-commit Enter key (text + Enter in one dispatch), so firing there
// turns every Chinese/Japanese/Korean message into a stray newline instead
// of a
// send. Conhost is safe because it suppresses keys during active IME
// composition instead of forwarding them.
func (m Model) enterInsertsNewlineDuringPaste() bool {
	if !m.legacyConhostInput {
		return false
	}
	if m.lastInputRunesAt.IsZero() {
		return false
	}
	return time.Since(m.lastInputRunesAt) < pasteEnterGap
}

// legacyConhostInputEnvOverride lets users force the paste-guard on or off
// when auto-detection is wrong for their terminal:
//
//	SEEK_LEGACY_CONHOST_INPUT=1   force the CRLF-paste guard on
//	SEEK_LEGACY_CONHOST_INPUT=0   force it off
const legacyConhostInputEnvOverride = "SEEK_LEGACY_CONHOST_INPUT"

// detectLegacyConhostInput decides once (at Model construction) whether the
// Enter→newline paste guard may fire. True only on a legacy Windows console
// host — the only environment that BOTH lacks bracketed paste (guard needed)
// AND suppresses IME-commit keys (guard safe).
//
// SEEK_LEGACY_CONHOST_INPUT overrides detection: "1"/"true"/"on" force the
// guard on, "0"/"false"/"off" force it off, anything else falls back to
// auto-detection.
func detectLegacyConhostInput() bool {
	if v := os.Getenv(legacyConhostInputEnvOverride); v != "" {
		if v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") {
			return true
		}
		if v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "off") {
			return false
		}
		// Unknown value: fall through to auto-detection.
	}
	return legacyConhostInputFor(runtime.GOOS)
}

// legacyConhostInputFor is the GOOS + env-var detection, split out so tests
// can exercise the windows branch on any host. Terminal emulators on Windows
// identify themselves via env vars; legacy conhost sets none of them:
//
//   - Windows Terminal sets WT_SESSION
//   - VS Code, WezTerm, mintty, Hyper, Alacritty set TERM_PROGRAM
//   - ConEmu / Cmder set ConEmuPID
func legacyConhostInputFor(goos string) bool {
	if goos != "windows" {
		return false
	}
	if os.Getenv("WT_SESSION") != "" {
		return false
	}
	if os.Getenv("TERM_PROGRAM") != "" {
		return false
	}
	if os.Getenv("ConEmuPID") != "" {
		return false
	}
	return true
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
