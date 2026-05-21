package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the seek TUI program. It blocks until the user quits
// (Ctrl+C / Ctrl+Q) or the underlying context is cancelled.
func Run(opts Options) error {
	m := New(opts)
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
