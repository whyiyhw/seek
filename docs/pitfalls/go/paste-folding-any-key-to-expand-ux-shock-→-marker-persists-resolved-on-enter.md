### Paste-folding "any key to expand" UX shock → marker persists, resolved on Enter
- **Saw**: pasting >5 lines folded the content into `"📋 pasted N lines — press any key to expand"`; typing ANY character instantly restored the full multi-line content, exploding the input area.
- **Why**: the restore block in `handleKey` ran unconditionally on every keypress when `m.pastedContent != ""` — the design assumed "first keypress = ready to edit/submit" but any key triggered expansion.
- **Fix**: restore block removed; the marker persists on every non-Enter keypress and on Enter (submit/queue/steer) is replaced via `strings.Replace(val, marker, m.pastedContent, 1)` before sending — editing/deleting the marker silently discards the paste.
- **Lesson**: a fold needed for stability (large paste in fixed-height textarea) needs a narrow unfold trigger (Enter = submission), not "any key"; the trade-off is the user can't edit pasted content before sending.
- **Refs**: `internal/tui/update.go:handlePasteFolding`, `internal/tui/update.go:handleKey` (streaming + non-streaming submit paths), `internal/tui/model.go:pastedContent/pastedLineCount`
