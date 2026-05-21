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
	Model     string
	Yolo      bool
	Tier      pricing.Tier
	NextTier  pricing.Tier
	NextAt    time.Time
	Turns     int
	ToolCalls int
	Usage     deepseek.Usage // cumulative
	Streaming bool           // "thinking" indicator state
	Now       time.Time      // for "until next tier" countdown
	Width     int            // for right-padding
}

// RenderStatusBar produces a single line styled with lipgloss. Width=0
// returns the raw text (useful for tests).
func RenderStatusBar(s StatusSnapshot) string {
	left := leftSegments(s)
	right := rightSegments(s)

	leftText := strings.Join(left, "  ")
	rightText := strings.Join(right, "  ")

	if s.Width <= 0 {
		return leftText + "  •  " + rightText
	}

	leftWidth := lipgloss.Width(leftText)
	rightWidth := lipgloss.Width(rightText)
	gap := s.Width - leftWidth - rightWidth
	if gap < 1 {
		gap = 1
	}
	full := leftText + strings.Repeat(" ", gap) + rightText
	return styleStatusBar.Width(s.Width).Render(full)
}

func leftSegments(s StatusSnapshot) []string {
	out := []string{
		styleStatusBar.Bold(true).Render(" seek "),
		s.Model,
	}
	if s.Yolo {
		out = append(out, lipgloss.NewStyle().Foreground(colourBannerFg).Background(colourToolErr).Bold(true).Padding(0, 1).Render("YOLO"))
	}
	if s.Streaming {
		out = append(out, styleMuted.Render("● streaming"))
	} else {
		out = append(out, styleMuted.Render("○ idle"))
	}
	return out
}

func rightSegments(s StatusSnapshot) []string {
	out := []string{}

	out = append(out, fmt.Sprintf("turns:%d  tools:%d", s.Turns, s.ToolCalls))

	cache := fmt.Sprintf("cache %s", deepseek.FormatHitRatio(s.Usage))
	if s.Usage.PromptCacheHitTokens > 0 {
		cache = lipgloss.NewStyle().Foreground(colourOk).Render(cache)
	}
	out = append(out, cache)

	cost := pricing.FormatCost(pricing.Cost(s.Model, s.Tier, s.Usage))
	out = append(out, "cost "+cost)

	out = append(out, formatBudget(s))
	out = append(out, formatTier(s))
	return out
}

// formatBudget renders the context-window utilisation. Stays muted in
// the safe zone; tints yellow above 80%; tints red above 95% with a
// `/compact` nudge.
func formatBudget(s StatusSnapshot) string {
	used := s.Usage.PromptTokens
	limit := budget.Limit(s.Model)
	frac := budget.Fraction(s.Model, used)
	pct := int(frac * 100)
	label := fmt.Sprintf("ctx %d%% (%d/%d)", pct, used, limit)

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
