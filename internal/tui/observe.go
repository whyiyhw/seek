package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
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
// On success (OK=true): commits a notification to history.
// On failure (OK=false): commits an error notification.
// Rejects and timeouts are silent (no message sent to channel).
func (m *Model) handleObserveDone(msg observeDoneMsg) []tea.Cmd {
	if msg.OK {
		return []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf("  · saved to M: %s (auto-sourced)", msg.Tagline)))}
	}
	// Failure or rejected confirmed-entry.
	return []tea.Cmd{m.appendHistory(styleErr.Render(fmt.Sprintf("  ! observe: %s", msg.Err)))}
}
