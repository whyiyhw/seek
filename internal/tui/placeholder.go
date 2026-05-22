package tui

import (
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
)

// Rotating tips shown in the input placeholder once the user is past
// the first turn and no override condition (yolo / off-peak / cache
// health) applies. Picked by (turn count) mod len so the tip cycles
// through as the conversation grows.
//
// Kept short — placeholders truncate at narrow terminal widths.
var rotatingTips = []string{
	"💡 type @ to reference a file",
	"💡 type / for slash commands",
	"💡 /think <task> for deep reasoning (V4-Flash thinking mode)",
	"💡 Esc interrupts a runaway response without losing history",
	"💡 ↑ recalls your previous prompt when the input is empty",
	"💡 Ctrl+R toggles reasoning visibility on assistant messages",
}

// refreshPlaceholder updates the textarea's hint text based on the
// current Model state. Called at points where the inputs to the
// decision change: turn end, off-peak window crossing, yolo flip,
// stream end. Each spot is cheap — it's just a string format — so we
// don't bother caching.
func (m *Model) refreshPlaceholder() {
	m.input.Placeholder = computePlaceholder(m)
}

// computePlaceholder picks the best hint for "right now". Priority is
// strict — the first matching condition wins. The ordering reflects
// "what does the user most need to know about the current session":
//
//  1. Yolo: actively dangerous mode → red warning takes precedence
//     over any cute tip.
//  2. Off-peak: time-sensitive pricing fact; only relevant while the
//     window is open.
//  3. First turn: welcome new users to the basics (no @/-/Ctrl history
//     yet — they haven't done anything).
//  4. Cache extremes: after a few turns we have real signal about
//     whether the prompt is cache-friendly; surface either side.
//  5. Rotating tips: teach incrementally; less urgent than the above.
func computePlaceholder(m *Model) string {
	if m.opts.Yolo {
		return "⚠ YOLO active — bash and out-of-CWD writes auto-approved"
	}

	now := m.now
	if now.IsZero() {
		now = time.Now()
	}

	if pricing.CurrentTier(now) == pricing.TierOffPeak {
		return "🌙 off-peak — half price · type your prompt or / for commands"
	}

	if m.turns == 0 {
		return "Ask seek anything — Enter sends · / commands · @ files · Ctrl+C quits"
	}

	// Cache extremes after enough turns to be a real signal.
	if m.turns >= 3 {
		u := m.opts.Tracker.Cumulative()
		ratio := u.HitRatio()
		switch {
		case ratio >= 0.80 && u.PromptCacheHitTokens > 0:
			return "🎯 cache hot — runs are cheap · type your prompt"
		case ratio < 0.30 && u.PromptTokens > 1000:
			// Only complain about low hit rate once the prompt is
			// big enough that the cache COULD have helped — short
			// prompts naturally miss (see pitfalls #cache-best-effort).
			return "💡 cache hit ratio is low — see PRD §4.8.1 for prompt-stability tips"
		}
	}

	return rotatingTips[(m.turns-1)%len(rotatingTips)]
}
