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
	gap = max(gap, 1)
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
		out = append(out, styleMuted.Render(streamingStatusLabel(s)))
	} else {
		out = append(out, styleMuted.Render("○ idle"))
	}
	if s.UpgradeAvailable != "" {
		out = append(out, lipgloss.NewStyle().Foreground(colourOk).Render("↑ "+s.UpgradeAvailable))
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

func rightSegments(s StatusSnapshot) []string {
	out := []string{}

	out = append(out, fmt.Sprintf("turns:%d  tools:%d", s.Turns, s.ToolCalls))

	cache := fmt.Sprintf("cache %s", deepseek.FormatHitRatio(s.Usage))
	if s.Usage.PromptCacheHitTokens > 0 {
		cache = lipgloss.NewStyle().Foreground(colourOk).Render(cache)
	}
	out = append(out, cache)

	cost := pricing.FormatCost(s.CumulativeCost)
	out = append(out, "cost "+cost)

	out = append(out, formatBudget(s))
	out = append(out, formatTier(s))
	return out
}

// formatBudget renders the context-window utilisation. Stays muted in
// the safe zone; tints yellow above 80%; tints red above 95% with a
// `/compact` nudge.
func formatBudget(s StatusSnapshot) string {
	// Use the last turn's prompt tokens, not the cumulative sum.
	// Cumulative grows quadratically (each turn re-sends the full history)
	// and hits 1M+ in ~55 turns even when the actual context is only 20k.
	used := s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens
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
