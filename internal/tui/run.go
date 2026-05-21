package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the seek TUI program. It blocks until the user quits
// (Ctrl+C / Ctrl+Q) or the underlying context is cancelled.
func Run(opts Options) error {
	m := New(opts)
	// NOTE: deliberately NO tea.WithMouseCellMotion() — capturing mouse
	// events breaks the terminal's native click-and-drag selection,
	// which means users can't copy any text out of seek. PgUp/PgDn and
	// arrow keys cover scroll; losing mouse scroll is a cheap trade for
	// keeping copy/paste functional. (Hold Option on macOS terminals to
	// force selection if a future feature needs mouse events back.)
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
