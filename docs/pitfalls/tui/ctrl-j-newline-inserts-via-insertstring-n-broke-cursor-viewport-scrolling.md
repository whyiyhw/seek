### Ctrl+J newline inserts via `InsertString("\n")` broke cursor/viewport scrolling
- **Saw**: typing Ctrl+J with 3+ lines in the textarea made the cursor disappear or land at the wrong position — the first line didn't scroll up as content was added below.
- **Why**: `handleKey` intercepted `tea.KeyCtrlJ` and called `m.input.InsertString("\n")`, which writes the character but bypasses the textarea's `Update()` — cursor position, line tracking, and viewport scroll offset were never updated.
- **Fix**: removed the `case tea.KeyCtrlJ:` intercept; `ta.KeyMap.InsertNewline.SetKeys("ctrl+j")` (model.go:311) already binds it, so Ctrl+J falls through to `m.input.Update(msg)` and the textarea handles cursor/scroll natively.
- **Lesson**: never bypass a bubbletea component's `Update()` for key-driven mutations — `InsertString` is a data-level op that skips the component's state machine (cursor, scroll, offsets); if a key binding exists on the component, let the event reach it.
- **Refs**: `internal/tui/update.go:handleKey` (removed KeyCtrlJ case), `internal/tui/model.go:311`
