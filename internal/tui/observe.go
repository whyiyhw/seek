package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/memory"
)

// waitForObserveResult returns a tea.Cmd that receives one ObserveResult
// from the channel and wraps it as an observeDoneMsg for the TUI event loop.
func waitForObserveResult(ch <-chan memory.ObserveResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil // channel closed, session ending
		}
		return observeDoneMsg{
			Name:    r.Name,
			Tagline: r.Tagline,
			OK:      r.OK,
			Err:     r.Err,
		}
	}
}

// handleObserveDone processes one async observe filter result.
// On success (OK=true): prints a scrollback notification.
// On failure (OK=false): prints an error notification.
// Rejects and timeouts are silent (no message sent to channel).
func (m *Model) handleObserveDone(msg observeDoneMsg) []tea.Cmd {
	if msg.OK {
		line := styleMuted.Render(fmt.Sprintf("  \u00b7 saved to M: %s (auto-sourced)", msg.Tagline))
		m.scrollbackLines += scrollbackLineCount(line)
		return []tea.Cmd{tea.Println(line)}
	}

	// Failure or rejected confirmed-entry.
	line := styleErr.Render(fmt.Sprintf("  \u0021 observe: %s", msg.Err))
	m.scrollbackLines += scrollbackLineCount(line)
	return []tea.Cmd{tea.Println(line)}
}
