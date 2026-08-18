package tui

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// renderExitSummary builds the stats block printed when the TUI shuts
// down (see run.go). Two lines:
//
//	· 12 turns · 277 tools · ↑1.2M prompt (80.0% cache) · ↓456k · total 1.7M · $0.42
//	· llm 46m53s · tools 28m8s · first-token avg 3.3s · 127 tok/s
//
// Line 1 is SESSION totals: turn/tool counts are reconstructed from the
// transcript on --resume and the token/cost numbers come from the
// cache.Tracker (which folds in prior runs via SetBase). Line 2 is THIS
// RUN only — wall-clock timing cannot be reconstructed from the
// transcript, so it is inherently process-local. On a resumed session
// line 1 gains a "resumed" marker so the mixed scope is visible rather
// than silently presented as whole-session stats.
//
// Returns "" when the session did no work (nothing to summarise) — e.g.
// the user opened the TUI and quit without prompting.
func renderExitSummary(m Model) string {
	if m.turns == 0 && m.firstTokN == 0 {
		return ""
	}
	lines := []string{"· " + countsLine(m)}
	if tl := timingLine(m); tl != "" {
		lines = append(lines, "· "+tl)
	}
	return strings.Join(lines, "\n")
}

// countsLine renders the session-total line: turn/tool counts, token
// accounting (cache %, completion, total) and locked-in dollar cost.
// Tracker may be nil in tests — the token/cost segments are omitted
// then.
func countsLine(m Model) string {
	parts := []string{fmt.Sprintf("%d turns", m.turns), fmt.Sprintf("%d tools", m.toolCalls)}
	if m.opts.Tracker != nil {
		c := m.opts.Tracker.Cumulative()
		parts = append(parts,
			"↑"+formatTokensK(c.PromptTokens)+" prompt ("+deepseek.FormatHitRatio(c)+" cache)",
			"↓"+formatTokensK(c.CompletionTokens),
			"total "+formatTokensK(c.TotalTokens),
			pricing.FormatCost(m.opts.Tracker.CumulativeCost()),
		)
	}
	if m.resumed {
		parts = append(parts, "resumed")
	}
	return strings.Join(parts, " · ")
}

// timingLine renders the this-run timing line. Segments that have no
// data are omitted individually (a resumed-then-quit session shows no
// timing line at all, not a row of zeros).
func timingLine(m Model) string {
	var parts []string
	if m.llmTime > 0 {
		parts = append(parts, "llm "+formatStreamElapsed(m.llmTime))
	}
	if m.toolTime > 0 {
		parts = append(parts, "tools "+formatStreamElapsed(m.toolTime))
	}
	if m.firstTokN > 0 {
		parts = append(parts, fmt.Sprintf("first-token avg %.1fs", m.firstTokSum.Seconds()/float64(m.firstTokN)))
	}
	if m.llmTime > 0 && m.completionTok > 0 {
		parts = append(parts, fmt.Sprintf("%d tok/s", int(float64(m.completionTok)/m.llmTime.Seconds())))
	}
	return strings.Join(parts, " · ")
}
