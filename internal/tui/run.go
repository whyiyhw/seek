package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// Run starts the seek TUI program in inline mode. It blocks until the
// user quits or the underlying context is cancelled.
//
// Architectural note: inline mode (no `tea.WithAltScreen()`) lets the
// terminal own scrollback, click-and-drag text selection, and the
// mouse wheel. Committed conversation lines (user prompts, tool
// results, assistant messages) are emitted via `tea.Println` from
// `appendHistory`, which under inline mode flushes them immediately
// into the terminal's native scrollback. The bubbletea live region
// holds only volatile state — streaming text, active tool spinners,
// popups, the input textarea, and the status bar — and sits naturally
// at the end of the output stream.
//
// CRITICAL invariant: do NOT pad the live region with trailing
// newlines to "pin" the input to the absolute terminal floor. That
// pattern (counter-tracked `scrollbackLines` + `strings.Repeat("\n",
// pad)`) is what caused the M3-era drift bugs documented in
// `docs/pitfalls.md` and led to the alt-screen detour. The renderer's
// cursor-up + EraseScreenBelow handles frame-to-frame height changes
// natively; trying to outsmart it reintroduces the drift class.
//
// No mouse capture. Wheel scroll, click-drag selection, PgUp/PgDn —
// the terminal handles them all. No `tea.WithMouseCellMotion()`.
//
// The welcome banner is printed once to stdout BEFORE `tea.NewProgram`
// so it lives in terminal scrollback like any other shell-startup
// output, never re-entering the live region.
func Run(opts Options) error {
	// Apply the colour theme before anything renders so every
	// package-level style var picks up the right palette.
	SetTheme(opts.Theme)

	// Resume replay. When the session was loaded with prior messages
	// (--resume / --continue), dump them to scrollback BEFORE the
	// program starts so they land as native terminal output rather
	// than as N per-message tea.Println cycles. A 100-message resume
	// without this fix triggers 100 redraw passes inside Update() and
	// visibly floods the screen at startup.
	//
	// Query the terminal width for markdown rendering. Under inline
	// mode we own stdout before bubbletea starts, so term.GetSize
	// returns the real terminal dimensions even when stdout is a TTY.
	// Fall back to 80 when piped / redirecting (same as initialSizeCmd).
	replayWidth := 0
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		replayWidth = w
	} else {
		replayWidth = 80
	}
	if hist := renderReplayHistory(opts.Session, false, replayWidth, opts.GlamourStyle); hist != "" {
		fmt.Fprintln(os.Stdout, hist)
	}

	m := New(opts)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	// Session/resume hint after the live region tears down. The
	// conversation itself is already in scrollback — `tea.Println`
	// under inline mode flushed each commit live, no exit dump needed.
	if m, ok := finalModel.(Model); ok {
		if sess := m.opts.Session; sess != nil {
			fmt.Fprintf(os.Stderr, "session: %s  (seek --resume %s)\n",
				sess.ID, sess.ID)
		}
	}

	return nil
}
