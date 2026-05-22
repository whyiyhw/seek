package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the seek TUI program in inline mode (PRD §4.9). It blocks
// until the user quits or the underlying context is cancelled.
//
// Architectural note: NO tea.WithAltScreen() — we want the terminal's
// native scrollback to own committed conversation history (so Cmd+C
// works across the entire session and the conversation survives exit).
// NO tea.WithMouseCellMotion() either — that captures mouse events
// and breaks native click-and-drag selection. Both choices are
// deliberate; flipping them re-introduces fixed bugs.
func Run(opts Options) error {
	// Apply the colour theme before anything renders (banner, styles,
	// etc.) so every package-level style var picks up the right palette.
	SetTheme(opts.Theme)

	// The welcome banner goes directly to stdout before bubbletea
	// takes over so it ends up in scrollback above the live region.
	PrintPixelWelcomeBanner(opts)

	m := New(opts)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
