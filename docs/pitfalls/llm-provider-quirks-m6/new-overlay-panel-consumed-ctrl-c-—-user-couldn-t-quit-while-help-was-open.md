### New overlay panel consumed Ctrl+C — user couldn't quit while help was open
- **Saw**: after opening the help overlay (`/help` or `?`), pressing Ctrl+C had no effect — the overlay stayed open and the app wouldn't quit
- **Why**: the overlay's key handler in `handleKey` returned `m, nil` for any key that didn't match Esc/Enter/q, accidentally swallowing Ctrl+C before it reached the `case tea.KeyCtrlC: return m, tea.Quit` branch below
- **Fix**: added explicit `case tea.KeyCtrlC` in the overlay's key switch before the generic fallback, plus case-insensitive `q` close check. Commit (this one)
- **Lesson**: every modal/key-grabbing layer in a TUI needs its own Ctrl+C path — don't rely on a downstream switch that the layer's early-return bypasses
- **Refs**: `internal/tui/update.go:handleKey`
