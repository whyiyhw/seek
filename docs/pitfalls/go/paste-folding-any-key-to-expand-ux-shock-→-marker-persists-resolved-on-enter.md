### Paste-folding "any key to expand" UX shock → marker persists, resolved on Enter
- **Saw**: pasting >5 lines folded into `"📋 pasted N lines — press any key to expand"`; typing ANY character instantly restored the full multi-line content, exploding the input area.
- **Why**: the restore block in `handleKey` ran on every keypress when `m.pastedContent != ""` — the design assumed "first keypress = ready to edit/submit", any key expanded.
- **Fix**: restore block removed; the marker persists on every non-Enter keypress and is replaced on Enter (submit/queue/steer) via `strings.Replace(val, marker, m.pastedContent, 1)` before sending — editing/deleting the marker silently discards the paste.
- **Lesson**: a fold needed for stability (large paste in fixed-height textarea) needs a narrow unfold trigger (Enter = submission), not "any key"; the cost: the user can't edit pasted content before sending.
- **Refs**: `internal/tui/update.go:handlePasteFolding`, `internal/tui/update.go:handleKey` (streaming + non-streaming submit), `internal/tui/model.go:pastedContent/pastedLineCount`
