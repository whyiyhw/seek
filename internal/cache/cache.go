// Package cache tracks DeepSeek prefix-cache usage across an agent
// session. seek's primary optimisation surface — every turn that lands
// on the same stable prefix pays input tokens at the cache-hit rate
// (roughly 5% of the miss rate), so a session's hit ratio directly maps
// to dollars saved (PRD §4.8.1).
//
// Tracker also locks in PER-TURN COST at Record time. Cost is computed
// once, with the (model, tier) that were active when that turn settled,
// and never re-derived from cumulative tokens at render time. This
// matters because:
//
//   - Switching model mid-session (e.g. /model deepseek-v4-pro after
//     running on deepseek-v4-flash) would otherwise re-price every
//     prior turn's tokens at the new model's rate. V4-Pro costs ~3.1×
//     V4-Flash, so the displayed cumulative cost would jump retroactively
//     by 3× even though DeepSeek already billed those tokens at the
//     lower rate.
//
//   - Off-peak tier transitions (00:30 / 08:30 Beijing) would do the same,
//     halving or doubling the displayed cumulative at the tier boundary
//     while the actual billed amount stays put.
//
// Wire Tracker from the agent's TurnEnd events; pass the model name and
// pricing tier that were in effect for the just-completed turn.
package cache

import (
	"fmt"
	"sync"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// turnRecord is the per-turn data the Tracker keeps. Cost is pre-computed
// at Record time and never re-derived afterwards — see package doc.
type turnRecord struct {
	Usage deepseek.Usage
	Model string
	Tier  pricing.Tier
	Cost  float64
}

// Tracker accumulates per-turn Usage + cost records and computes
// session-level rollups. Safe for concurrent Record/Snapshot calls.
type Tracker struct {
	mu    sync.Mutex
	turns []turnRecord
}

// New returns an empty Tracker.
func New() *Tracker { return &Tracker{} }

// Record appends a turn to the history. cost is locked in at this
// moment using (model, tier); the same usage replayed later under a
// different model/tier will NOT re-price.
//
// model is the model id that was active when this turn ran; tier is
// the pricing tier (typically pricing.CurrentTier(time.Now())) at
// that moment. Unknown model names fall back to deepseek-chat rates
// per pricing.PricingFor — same fallback the renderer used pre-fix.
func (t *Tracker) Record(u deepseek.Usage, model string, tier pricing.Tier) {
	cost := pricing.Cost(model, tier, u)
	t.mu.Lock()
	t.turns = append(t.turns, turnRecord{Usage: u, Model: model, Tier: tier, Cost: cost})
	t.mu.Unlock()
}

// Cumulative returns the sum of token usage across all recorded turns.
// Cost is NOT derivable from this aggregate — use CumulativeCost.
func (t *Tracker) Cumulative() deepseek.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum deepseek.Usage
	for _, r := range t.turns {
		sum.PromptTokens += r.Usage.PromptTokens
		sum.CompletionTokens += r.Usage.CompletionTokens
		sum.TotalTokens += r.Usage.TotalTokens
		sum.PromptCacheHitTokens += r.Usage.PromptCacheHitTokens
		sum.PromptCacheMissTokens += r.Usage.PromptCacheMissTokens
	}
	return sum
}

// CumulativeCost returns the sum of per-turn costs (USD). Because each
// turn's cost was priced at the model+tier that were active when the
// turn was recorded, switching model or crossing a tier boundary does
// not retroactively re-price prior turns.
func (t *Tracker) CumulativeCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum float64
	for _, r := range t.turns {
		sum += r.Cost
	}
	return sum
}

// Last returns the most recently recorded turn's Usage, or a zero
// value if no turns have been recorded yet. Used by the budget indicator
// to show the current context-window utilisation (last turn's prompt
// size) rather than the cumulative sum across all turns.
func (t *Tracker) Last() deepseek.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.turns) == 0 {
		return deepseek.Usage{}
	}
	return t.turns[len(t.turns)-1].Usage
}

// LastCost returns the locked-in dollar cost of the most recent turn,
// or 0 if no turns have been recorded. Pairs with Last() so callers
// rendering the per-turn footer don't have to recompute via
// pricing.Cost at the current model/tier — which would mis-price the
// turn if /model was switched between record and render.
func (t *Tracker) LastCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.turns) == 0 {
		return 0
	}
	return t.turns[len(t.turns)-1].Cost
}

// Turns returns a copy of per-turn Usage history.
func (t *Tracker) Turns() []deepseek.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]deepseek.Usage, len(t.turns))
	for i, r := range t.turns {
		out[i] = r.Usage
	}
	return out
}

// HitRatio returns the session-wide prefix-cache hit ratio (0..1).
func (t *Tracker) HitRatio() float64 { return t.Cumulative().HitRatio() }

// SavedTokens estimates input tokens that paid the cheap rate instead of
// the expensive one. Equivalent to PromptCacheHitTokens — that's the
// count of tokens that would otherwise have been billed at the
// cache-miss rate.
func (t *Tracker) SavedTokens() int { return t.Cumulative().PromptCacheHitTokens }

// Summary produces a multi-line text report suitable for the CLI stats
// footer. Layout is intentionally fixed-width so the same text can ship
// to the TUI status bar with minimal massaging.
func (t *Tracker) Summary() string {
	c := t.Cumulative()
	turns := len(t.Turns())
	if turns == 0 {
		return "cache: no turns recorded"
	}
	return fmt.Sprintf(
		"cache: %d turn(s); prompt %d (hit %d / miss %d, ratio %s); completion %d; total %d",
		turns,
		c.PromptTokens,
		c.PromptCacheHitTokens,
		c.PromptCacheMissTokens,
		deepseek.FormatHitRatio(c),
		c.CompletionTokens,
		c.TotalTokens,
	)
}
