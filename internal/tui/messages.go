package tui

import (
	"github.com/whyiyhw/seek/internal/memory"
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

// distillDoneMsg fires when /distill's reasoner pass returns. On
// success, candidates is the list to review (possibly empty — the
// reasoner can legitimately decide nothing is worth keeping). On error,
// err is non-nil and the TUI prints the message to scrollback.
type distillDoneMsg struct {
	candidates []memory.Candidate
	err        error
}

// versionCheckDoneMsg fires after the startup background upgrade probe.
// NewerTag is empty when the running binary is up to date OR the check
// silently failed (network down, GitHub rate-limit) — the TUI never
// surfaces failures, only successful "you have an older version" hits.
type versionCheckDoneMsg struct {
	NewerTag string
}

// upgradeDoneMsg fires after a /upgrade slash command finishes its
// async download + replace flow. Err is set when the upgrade failed.
// AlreadyLatest is true on the "nothing to do" no-op; the TUI prints
// a muted note rather than an error in that case.
type upgradeDoneMsg struct {
	NewTag        string
	AlreadyLatest bool
	DryRun        bool
	Err           error
}

// observeDoneMsg fires when an async memory_observe filter completes.
// The TUI renders a scrollback notification line.
type observeDoneMsg struct {
	Name    string
	Tagline string
	OK      bool
	Err     string
}
