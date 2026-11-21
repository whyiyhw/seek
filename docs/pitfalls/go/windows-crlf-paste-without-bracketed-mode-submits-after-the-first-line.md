### Windows CRLF paste without bracketed mode submits after the first line
- **Saw**: pasting multi-line text into the seek TUI on Windows sent only the first line immediately; remaining lines were lost
- **Why**: terminals without bracketed paste (or Ctrl+Shift+V paths that bypass it) inject each CRLF line as KeyRunes followed by `\r`, which bubbletea maps to KeyEnter — and seek treats Enter as submit. Bracketed paste (`msg.Paste`) was also fed through textarea.Update, letting `\r` bytes in the paste body through before fold logic ran
- **Fix**: wholesale paste injection for `msg.Paste` and Ctrl+V (clipboard.ReadAll), CRLF normalization, and a 50ms Enter→newline guard when Enter arrives immediately after KeyRunes (intra-paste `\r`). Fold marker resolution consolidated in `resolvePasteInInput`
- **Lesson**: on Windows, `\r` is Enter — never let raw CRLF paste reach the Enter/submit path line-by-line. Either handle the full paste body atomically (bracketed paste / clipboard) or treat rapid Enter-after-runes as newline insertion, not submit
- **Refs**: `internal/tui/paste.go`, `internal/tui/update_key.go`, `internal/tui/paste_test.go`
