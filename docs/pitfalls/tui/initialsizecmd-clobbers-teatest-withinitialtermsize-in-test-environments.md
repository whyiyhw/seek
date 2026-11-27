### `initialSizeCmd` clobbers `teatest.WithInitialTermSize` in test environments
- **Saw**: a teatest case with `WithInitialTermSize(80, 40)` still rendered 80x24, clipping the help overlay so assertions on top-of-panel strings ("Help — seek") timed out though the overlay was rendering.
- **Why**: `Init()` returns `initialSizeCmd()` calling `term.GetSize(os.Stdout.Fd())`; under `go test` stdout isn't a TTY, it fails and falls back to hard-coded `(80, 24)`, sending a `WindowSizeMsg` that overrides teatest's size — racing an explicit `WindowSizeMsg` against it is non-deterministic.
- **Fix**: assert on bottom-of-panel viewport strings ("Shift+Tab", "Scrollback:") instead of the title, and document the constraint inline in the test.
- **Lesson**: design teatest assertions for a 24-row viewport — bubbletea writes only what fits, not the full rendered buffer; for a taller viewport, patch `initialSizeCmd` behind a test-only env var rather than racing it.
- **Refs**: `internal/tui/model.go:initialSizeCmd`, `internal/tui/render_test.go`
