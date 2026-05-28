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
	"slices"
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
//
// baseUsage / baseCost hold the cumulative state from a resumed session
// (loaded via SetBase). They contribute to Cumulative()/CumulativeCost()
// but are EXCLUDED from Last()/LastCost() — those return only the most
// recent genuine turn. Without this split, recording a resumed session's
// aggregate Usage as a "turn" would make Last() return a cumulative sum
// and cause the ctx% indicator in the status bar to exceed 100%
// (docs/pitfalls.md "cumulative prompt tokens are meaningless").
//
// children carries adopted subagent Trackers (v5 柱 G — see
// docs/prd/feature-subagent.md §3.7). The parent walks the slice on
// Cumulative()/CumulativeCost() so a session's displayed total includes
// every active/completed subagent's usage. children DOES NOT affect
// Last()/LastCost() — those remain "this Tracker's own most recent
// turn" since the status bar's ctx% indicator pressures the parent's
// own context window, not the child's independent window.
type Tracker struct {
	mu        sync.Mutex
	turns     []turnRecord
	baseUsage deepseek.Usage // cumulative from prior session; included in Cumulative() only
	baseCost  float64        // dollar cost of baseUsage
	children  []*Tracker     // adopted subagent Trackers (see AdoptChild)
}

// New returns an empty Tracker.
func New() *Tracker { return &Tracker{} }

// SetBase loads cumulative usage from a prior session (--resume / --continue).
// The usage and its estimated cost contribute to Cumulative()/CumulativeCost()
// but are excluded from Last()/LastCost(). Must be called before any Record
// call — in practice this happens at startup before the TUI begins.
//
// model and tier are the values active at resume time; the cost is an
// approximation when the session spanned multiple models or tier transitions.
func (t *Tracker) SetBase(u deepseek.Usage, model string, tier pricing.Tier) {
	cost := pricing.Cost(model, tier, u)
	t.mu.Lock()
	t.baseUsage = u
	t.baseCost = cost
	t.mu.Unlock()
}

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

// AdoptChild registers a subagent's Tracker so its Record() calls roll
// into THIS Tracker's Cumulative() / CumulativeCost() output. The child
// Tracker continues to receive its own Record() calls independently;
// AdoptChild only changes the parent's aggregation surface.
//
// Multiple children (parallel subagents) aggregate additively. Calling
// AdoptChild with the same *Tracker pointer twice is a no-op (pointer
// identity dedup) so spawn-retry paths are safe to re-call.
//
// SAFETY — do not adopt a child whose token usage has ALREADY been
// folded into this Tracker's baseUsage (i.e. a child resumed from disk
// whose prior turns are already accounted for in the parent's saved
// cumulative). The intended caller is internal/subagent at spawn time,
// where a freshly-spawned child has zero usage by construction. The
// orchestrator is responsible for honouring this contract — Tracker
// has no way to verify it. See docs/prd/feature-subagent.md §8 risk
// "resume cost 双重计数" for the rationale.
//
// SAFETY — nested adoption is forbidden. v5 limits spawn depth = 1
// (docs/prd/v5.md §2; feature-subagent.md §2 anti-goals). If child
// already has children of its own, AdoptChild panics. This is the
// AdoptChild-side defense in depth; the agent tool's exclusion from
// subagent registries is the first line.
//
// Concurrency: takes child.mu briefly (to check child.children
// emptiness) BEFORE acquiring t.mu, so the two locks never overlap on
// any code path. Other callers that hold t.mu while interacting with a
// child go through Cumulative's snapshot-then-release pattern, so
// AdoptChild can never deadlock with concurrent Cumulative.
func (t *Tracker) AdoptChild(child *Tracker) {
	if t == nil || child == nil || child == t {
		return
	}
	child.mu.Lock()
	nested := len(child.children) > 0
	child.mu.Unlock()
	if nested {
		panic("cache: AdoptChild on a Tracker that already has children — v5 forbids spawn depth > 1")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if slices.Contains(t.children, child) {
		return
	}
	t.children = append(t.children, child)
}

// Cumulative returns the sum of token usage across all recorded turns PLUS
// any base loaded via SetBase (prior session on --resume) PLUS every
// adopted subagent Tracker's Cumulative (transitively, though v5 forbids
// nesting deeper than 1). Cost is NOT derivable from this aggregate —
// use CumulativeCost.
//
// Concurrency: snapshots the children slice under t.mu, then releases
// the lock BEFORE recursing into each child's Cumulative. Holding t.mu
// across the recursion would block concurrent Record callers on the
// parent for the duration of every child's walk; with the snapshot we
// pay only a slice-copy on the hot path.
func (t *Tracker) Cumulative() deepseek.Usage {
	t.mu.Lock()
	sum := t.baseUsage
	for _, r := range t.turns {
		sum.PromptTokens += r.Usage.PromptTokens
		sum.CompletionTokens += r.Usage.CompletionTokens
		sum.TotalTokens += r.Usage.TotalTokens
		sum.PromptCacheHitTokens += r.Usage.PromptCacheHitTokens
		sum.PromptCacheMissTokens += r.Usage.PromptCacheMissTokens
	}
	children := append([]*Tracker(nil), t.children...)
	t.mu.Unlock()
	for _, c := range children {
		cu := c.Cumulative()
		sum.PromptTokens += cu.PromptTokens
		sum.CompletionTokens += cu.CompletionTokens
		sum.TotalTokens += cu.TotalTokens
		sum.PromptCacheHitTokens += cu.PromptCacheHitTokens
		sum.PromptCacheMissTokens += cu.PromptCacheMissTokens
	}
	return sum
}

// CumulativeCost returns the sum of per-turn costs (USD), including the
// base cost from SetBase AND every adopted subagent Tracker's
// CumulativeCost. Because each turn's cost was priced at the model+tier
// that were active when the turn was recorded, switching model or
// crossing a tier boundary does not retroactively re-price prior turns.
// Children apply the same rule independently inside their own walk.
//
// Concurrency: same snapshot-then-release pattern as Cumulative.
func (t *Tracker) CumulativeCost() float64 {
	t.mu.Lock()
	sum := t.baseCost
	for _, r := range t.turns {
		sum += r.Cost
	}
	children := append([]*Tracker(nil), t.children...)
	t.mu.Unlock()
	for _, c := range children {
		sum += c.CumulativeCost()
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
