package autopilot

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/pricing"
)

// Event is the webhook event name for this report (reuses 柱 M's
// WebhookDispatcher event convention "<source>.<status>").
func (r Report) Event() string {
	if r.Done == 0 && r.Failed > 0 {
		return "autopilot.failed"
	}
	return "autopilot.completed"
}

// Title is the push-notification title.
func (r Report) Title() string {
	return fmt.Sprintf("autopilot: %d/%d done", r.Done, len(r.Outcomes))
}

// Body is the human-readable summary — the push body and the
// `seek autopilot` stdout. One header line (with the run's total cost)
// + one line per task, each pointing at its worktree so the user can
// review the commits, with the evidence suffix
// "Δ<files>f +<ins>/-<del> · <tokens>tok · $<cost>" (v8 柱 R R1).
func (r Report) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "autopilot %q — %d/%d done", trunc(r.Goal, 60), r.Done, len(r.Outcomes))
	if r.Failed > 0 {
		fmt.Fprintf(&b, ", %d failed", r.Failed)
	}
	fmt.Fprintf(&b, ", %s", pricing.FormatCost(r.TotalCostUSD))
	b.WriteByte('\n')
	for _, o := range r.Outcomes {
		mark := "✓"
		if o.Status != "done" {
			mark = "✗"
		}
		fmt.Fprintf(&b, "  %s %s", mark, o.Task.Title)
		if o.Commit != "" {
			fmt.Fprintf(&b, "  [%s]", o.Commit)
		}
		if o.Worktree != "" {
			fmt.Fprintf(&b, "  → %s", o.Worktree)
		}
		fmt.Fprintf(&b, "  Δ%df +%d/-%d · %dtok · %s",
			o.Files, o.Insertions, o.Deletions, o.Tokens, pricing.FormatCost(o.CostUSD))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
