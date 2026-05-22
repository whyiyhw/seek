package tui

import (
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// approvalRequestMsg carries an inline approval prompt from the policy
// askFn. The TUI takes over input until the user answers, then writes
// the boolean to req.Reply.
type approvalRequestMsg struct {
	req permission.ApprovalRequest
}

// agentEventMsg wraps a single event from agent.Prompt's channel. The
// TUI polls one event per tea.Cmd to stay friendly with Bubble Tea's
// single-msg-per-Cmd model.
type agentEventMsg struct{ Event agent.Event }

// streamEndMsg fires when the agent's event channel is closed (turn
// complete, error, or context cancellation).
type streamEndMsg struct{}

// promptSubmittedMsg fires when the user pressed Enter in the input
// area, carrying the trimmed text.
type promptSubmittedMsg struct{ Text string }

// statusTickMsg keeps the status bar live so the off-peak window shifts
// in real time. We tick once a minute — enough resolution given the
// off-peak boundary is a minute granularity.
type statusTickMsg struct{}

// compactDoneMsg fires when /compact's async Summarise call returns.
// Update() handles it by swapping the agent's history via Reset and
// persisting the now-compacted session.
type compactDoneMsg struct {
	summary string
	usage   deepseek.Usage
	err     error
}
