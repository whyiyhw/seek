package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// StatusSnapshot is the pure-data view of everything the status bar
// renders. Extracted into a struct so the formatter is testable without
// a live bubbletea program.
type StatusSnapshot struct {
	Model string
	// Effort mirrors the session's /effort selection: "" | "high" |
	// "max". An empty string suppresses the badge so the default
	// (off) is silent — only the explicit escalations show up.
	Effort string
	Yolo   bool
	Plan   bool
	// PlanSubstate is "analyze" / "execute" / "" — only meaningful
	// when Plan=true. Empty substate renders the original "PLAN"
	// badge (back-compat with sessions / callers that pre-date the
	// substate split). See PRD docs/prd/feature-plan-mode.md §2.6.
	PlanSubstate string
	// PlanStepsTotal and PlanStepsDone drive the "N/M" counter on
	// the PLAN:EXEC badge. Zero values suppress the counter — the
	// badge falls back to the bare label until the `plan` tool has
	// produced any state. PlanStepsDone counts both completed and
	// skipped steps (both flavours of "no more work here").
	PlanStepsTotal int
	PlanStepsDone  int
	Tier           pricing.Tier
	NextTier       pricing.Tier
	NextAt         time.Time
	Turns          int
	ToolCalls      int
	Usage          deepseek.Usage // cumulative
	Streaming      bool           // "thinking" indicator state
	Now            time.Time      // for "until next tier" countdown
	Width          int            // for right-padding

	// StreamElapsed and StreamDeltaBytes drive the live "Ns · ↓~Xtok"
	// counter. Both are zero when Streaming is false or the stream just
	// started (< 1 s elapsed).
	StreamElapsed    time.Duration
	StreamDeltaBytes int

	// LastUsage is the most recent completed turn's usage. Used by
	// formatBudget so the context-window indicator shows the size of the
	// CURRENT context (last turn's prompt tokens), not the cumulative sum
	// of all turns' prompt tokens which grows quadratically and is
	// meaningless as a context-limit signal.
	LastUsage deepseek.Usage

	// CumulativeCost is the session's total dollar cost, with each
	// turn priced at the (model, tier) that were active when it ran.
	// The status bar reads this directly rather than recomputing via
	// pricing.Cost(s.Model, s.Tier, s.Usage) — that derivation broke
	// when the user switched models mid-session because the cumulative
	// usage would silently re-price at the new model's rate. See
	// internal/cache.Tracker.CumulativeCost.
	CumulativeCost float64

	// UpgradeAvailable is the tag of a newer release (e.g. "v0.2.0") found
	// by the startup probe. Empty when up-to-date or the probe was
	// skipped — in which case no segment is rendered.
	UpgradeAvailable string

	// SubagentsActive is the count of in-flight subagents at render
	// time (v5 柱 G; PRD docs/prd/feature-subagent.md §4.3).
	// Zero suppresses the badge so idle sessions show nothing —
	// the indicator's job is to remind the user "something is
	// happening in the background", and zero count means nothing
	// is happening.
	SubagentsActive int

	// CronsRegistered is the count of registered cron jobs at
	// render time (v5 柱 H; PRD docs/prd/feature-routines.md
	// §4.3). Zero suppresses the badge — registering a job is
	// a one-time action; the indicator's job is to remind the
	// user "I have N background routines configured" when the
	// list is non-trivial. Mirrors the SubagentsActive pattern
	// but counts REGISTERED (durable state) rather than ACTIVE
	// (transient state) — cron jobs aren't "running right now"
	// from the TUI's vantage point; they fire async via tick.
	CronsRegistered int

	// GoalActive + GoalTurns/GoalMaxTurns drive the "🎯 N/M" badge while a
	// /goal loop is running (M-goal.2). Suppressed when no goal is active.
	GoalActive   bool
	GoalTurns    int
	GoalMaxTurns int
}

// statusSegment is one rendered status-bar chunk plus its drop priority.
// Lower prio = more important; prioPin (0) is never dropped. Under width
// pressure RenderStatusBar sheds the highest-prio (least important)
// segments first, so the bar stays single-line in a narrow pane (tmux
// split, side-by-side editor terminal) instead of wrapping or clipping
// mid-badge.
type statusSegment struct {
	text string
	prio int
}

const (
	prioPin  = 0 // identity, mode/safety badge, active stream, critical ctx
	prioHigh = 1 // model, active goal, ctx warning
	prioMed  = 2 // cost, cache, active subagents
	prioLow  = 3 // turns/tools, tier, effort, upgrade, crons, idle, normal ctx
)

// RenderStatusBar produces a single line styled with lipgloss. Width=0
// returns the raw text with every segment (useful for tests).
func RenderStatusBar(s StatusSnapshot) string {
	left := leftSegments(s)
	right := rightSegments(s)

	if s.Width <= 0 {
		return joinSegments(left) + "  •  " + joinSegments(right)
	}

	left, right = foldStatusBar(left, right, s.Width)

	leftText := joinSegments(left)
	rightText := joinSegments(right)
	gap := s.Width - lipgloss.Width(leftText) - lipgloss.Width(rightText)
	gap = max(gap, 1)
	full := leftText + strings.Repeat(" ", gap) + rightText
	return styleStatusBar.Width(s.Width).Render(full)
}

// joinSegments renders segments into the "  "-separated text the bar
// lays out at one end.
func joinSegments(segs []statusSegment) string {
	parts := make([]string, len(segs))
	for i, seg := range segs {
		parts[i] = seg.text
	}
	return strings.Join(parts, "  ")
}

// foldStatusBar drops least-important segments until both sides plus a
// minimum gap fit within width. prioPin segments are never dropped, so a
// pathologically narrow pane still shows identity + mode + stream state
// (overflowing if it must) rather than silently hiding a YOLO/PLAN
// badge. Ties drop the right side (metrics) before the left, and later
// segments before earlier ones, keeping the most important context
// anchored at each end.
func foldStatusBar(left, right []statusSegment, width int) ([]statusSegment, []statusSegment) {
	const minGap = 2
	fits := func() bool {
		return lipgloss.Width(joinSegments(left))+minGap+lipgloss.Width(joinSegments(right)) <= width
	}
	for !fits() {
		side, idx, prio := -1, -1, prioPin
		for i := len(right) - 1; i >= 0; i-- {
			if right[i].prio > prio {
				side, idx, prio = 1, i, right[i].prio
			}
		}
		for i := len(left) - 1; i >= 0; i-- {
			if left[i].prio > prio {
				side, idx, prio = 0, i, left[i].prio
			}
		}
		if idx < 0 {
			break // only pinned segments remain — nothing left to shed
		}
		if side == 1 {
			right = append(right[:idx], right[idx+1:]...)
		} else {
			left = append(left[:idx], left[idx+1:]...)
		}
	}
	return left, right
}

func leftSegments(s StatusSnapshot) []statusSegment {
	out := []statusSegment{
		{styleStatusBar.Bold(true).Render(" seek "), prioPin},
		{s.Model, prioHigh},
	}
	if s.Yolo {
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourBannerFg).Background(colourToolErr).Bold(true).Padding(0, 1).Render("YOLO"), prioPin})
	} else if s.Plan {
		// Substate decides the badge text + background:
		//   ""        → "PLAN" (legacy / no-substate callers)
		//   "analyze" → "PLAN:ANALYZE" green — read-only exploration
		//   "execute" → "PLAN:EXEC"    orange — writes unlocked; user
		//                                       should be visually
		//                                       warned the permission
		//                                       gate is now Ask not Plan
		label := "PLAN"
		bg := colourOk
		switch s.PlanSubstate {
		case "analyze":
			label = "PLAN:ANALYZE"
		case "execute":
			label = "PLAN:EXEC"
			bg = colourTool
		}
		if s.PlanStepsTotal > 0 {
			label = fmt.Sprintf("%s %d/%d", label, s.PlanStepsDone, s.PlanStepsTotal)
		}
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourBannerFg).Background(bg).Bold(true).Padding(0, 1).Render(label), prioPin})
	}
	// /goal badge: "🎯 N/M" while a goal loop is running (M-goal.2).
	if s.GoalActive {
		label := "🎯 goal"
		if s.GoalMaxTurns > 0 {
			label = fmt.Sprintf("🎯 goal %d/%d", s.GoalTurns, s.GoalMaxTurns)
		}
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourBannerFg).Background(colourTool).Bold(true).Padding(0, 1).Render(label), prioHigh})
	}
	// Effort badge: "high" is muted (the user opted in but it's the
	// cheaper of the two escalations); "max" is tinted to make the
	// cost premium visible at a glance, mirroring how a /compact
	// warning escalates on the right side.
	switch s.Effort {
	case "high":
		out = append(out, statusSegment{styleMuted.Render("effort:high"), prioLow})
	case "max":
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourTool).Bold(true).Render("effort:max"), prioLow})
	}
	if s.Streaming {
		out = append(out, statusSegment{styleMuted.Render(streamingStatusLabel(s)), prioPin})
	} else {
		out = append(out, statusSegment{styleMuted.Render("○ idle"), prioLow})
	}
	if s.UpgradeAvailable != "" {
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourOk).Render("↑ "+s.UpgradeAvailable), prioLow})
	}
	return out
}

// streamingStatusLabel builds the "● Ns · ↓~Xtok" live indicator.
// Falls back to "● streaming" in the first second or before any
// completion bytes have arrived (e.g. waiting for first content token).
func streamingStatusLabel(s StatusSnapshot) string {
	if s.StreamElapsed < time.Second {
		return "● streaming"
	}
	el := formatStreamElapsed(s.StreamElapsed)
	if s.StreamDeltaBytes == 0 {
		return "● " + el
	}
	return fmt.Sprintf("● %s · ↓~%s", el, formatTokenEst(s.StreamDeltaBytes))
}

func formatStreamElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%ds", s/60, s%60)
}

// formatTokenEst converts a byte count to a readable token estimate.
// Approximation: 1 token ≈ 4 UTF-8 bytes for typical code/prose.
func formatTokenEst(bytes int) string {
	tok := bytes / 4
	if tok < 1000 {
		return fmt.Sprintf("%dtok", tok)
	}
	return fmt.Sprintf("%.1fktok", float64(tok)/1000)
}

func rightSegments(s StatusSnapshot) []statusSegment {
	out := []statusSegment{
		{fmt.Sprintf("turns:%d  tools:%d", s.Turns, s.ToolCalls), prioLow},
	}

	cache := fmt.Sprintf("cache %s", deepseek.FormatHitRatio(s.Usage))
	if s.Usage.PromptCacheHitTokens > 0 {
		cache = lipgloss.NewStyle().Foreground(colourOk).Render(cache)
	}
	out = append(out, statusSegment{cache, prioMed})

	out = append(out, statusSegment{"cost " + pricing.FormatCost(s.CumulativeCost), prioMed})

	out = append(out, statusSegment{formatBudget(s), ctxPrio(s)})
	out = append(out, statusSegment{formatTier(s), prioLow})

	// Subagent badge: only shown while at least one subagent is
	// active. Tinted ok-colour to signal "background work is
	// progressing" rather than alarm — the badge is informational,
	// not a warning. ⤴ glyph chosen for "delegated upward to a
	// sibling context"; falls back to a plain "agents:N" when the
	// terminal can't render the rune (most modern terminals can).
	if s.SubagentsActive > 0 {
		badge := fmt.Sprintf("⤴ %d agent", s.SubagentsActive)
		if s.SubagentsActive != 1 {
			badge += "s"
		}
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourOk).Render(badge), prioMed})
	}
	// Cron badge: shown while at least one cron job is
	// registered. Counts REGISTERED jobs not active runs — the
	// signal is "I have N routines configured" so the user
	// remembers what's running unattended. ⏰ glyph reads as
	// "scheduled" without ambiguity. Same muted ok-colour as
	// the agent badge.
	if s.CronsRegistered > 0 {
		badge := fmt.Sprintf("⏰ %d cron", s.CronsRegistered)
		out = append(out, statusSegment{lipgloss.NewStyle().Foreground(colourOk).Render(badge), prioLow})
	}
	return out
}

// ctxPrio escalates the context-window segment's keep-priority with
// severity: a critical "/compact soon" warning is pinned (never folded
// away), a warn-level nudge stays high, and steady-state ctx% is low —
// the first metric to go when space is tight, since the colour tint
// already carries any urgency.
func ctxPrio(s StatusSnapshot) int {
	used := s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens
	switch budget.Classify(s.Model, used) {
	case budget.SeverityCritical:
		return prioPin
	case budget.SeverityWarn:
		return prioHigh
	default:
		return prioLow
	}
}

// formatBudget renders the context-window utilisation. Stays muted in
// the safe zone; tints amber above budget.WarnFraction (60%); tints red
// above budget.CriticalFraction (75%) with a `/compact` nudge.
func formatBudget(s StatusSnapshot) string {
	// Use the last turn's prompt tokens, not the cumulative sum.
	// Cumulative grows quadratically (each turn re-sends the full history)
	// and hits 1M+ in ~55 turns even when the actual context is only 20k.
	used := s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens
	frac := budget.Fraction(s.Model, used)
	pct := int(frac * 100)
	// Pure-percent label: the percentage is the only actionable signal
	// (colour tints + the /compact nudge cover urgency), and the
	// model's context limit is a static value that doesn't change
	// turn-to-turn. The raw token counts were noise in the steady
	// state.
	label := fmt.Sprintf("ctx %d%%", pct)

	switch budget.Classify(s.Model, used) {
	case budget.SeverityCritical:
		return lipgloss.NewStyle().Foreground(colourToolErr).Bold(true).Render("⚠ " + label + " — /compact soon")
	case budget.SeverityWarn:
		return lipgloss.NewStyle().Foreground(colourTool).Render("⚠ " + label)
	default:
		return styleMuted.Render(label)
	}
}

func formatTier(s StatusSnapshot) string {
	label := pricing.TierLabel(s.Tier)
	if s.Tier == pricing.TierOffPeak {
		return styleStatusOffPeak.Render("🌙 " + label)
	}
	// Standard tier: show countdown to off-peak.
	dur := untilTransition(s.NextAt, s.Now)
	if dur <= 0 {
		return "☀️ " + label
	}
	return fmt.Sprintf("☀️ %s (next 🌙 in %s)", label, formatDuration(dur))
}

func untilTransition(when, now time.Time) time.Duration {
	if when.IsZero() || now.IsZero() {
		return 0
	}
	return when.Sub(now)
}

// formatDuration renders a Duration like "2h17m". Resolution is minutes
// — the off-peak boundary is minute-granular and second-level updates
// just churn the status bar.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	if d < time.Minute {
		return "<1m"
	}
	mins := int(d.Minutes())
	h := mins / 60
	m := mins % 60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
