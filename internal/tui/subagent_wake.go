package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// subagentWakeMsg fires when a background subagent reaches a
// terminal state (feature-orchestration PRD §4.1 D2). The wake only
// refreshes the UI — the result text itself is delivered by the
// subagent Notifier PrePrompt hook on the next Prompt, never from
// here (peeking here must not drain the delivery queue).
type subagentWakeMsg struct{}

// waitForSubagentWake mirrors waitForObserveResult: one receive per
// Cmd, re-armed by the Update handler. The channel is the Manager's
// cap-1 wake signal, so a wake lost to a full channel coalesces with
// the pending one — no notification is lost, only the extra redraw.
func waitForSubagentWake(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return subagentWakeMsg{}
	}
}

// handleSubagentWake prints a muted hint naming the most recent
// finished background agent (PendingNotices PEEKS — the queue stays
// full for the Notifier) and re-arms the wake. The status bar's
// agents badge drops on the same redraw because View recomputes
// ActiveCount.
func (m *Model) handleSubagentWake() []tea.Cmd {
	if m.opts.SubagentWake == nil {
		return nil
	}
	var cmds []tea.Cmd
	if m.opts.Subagents != nil {
		if pending := m.opts.Subagents.PendingNotices(); len(pending) > 0 {
			latest := pending[len(pending)-1]
			cmds = append(cmds, tea.Println(styleMuted.Render(fmt.Sprintf(
				"  ⤴ background agent %s: %s — send any message to ingest the result",
				latest.Status, truncateOneLine(latest.Description, 60)))))
		}
	}
	cmds = append(cmds, waitForSubagentWake(m.opts.SubagentWake))
	return cmds
}
