// Package cache tracks DeepSeek prefix-cache usage across an agent
// session. seek's primary optimisation surface — every turn that lands
// on the same stable prefix pays input tokens at the cache-hit rate
// (roughly 5% of the miss rate), so a session's hit ratio directly maps
// to dollars saved (PRD §4.8.1).
//
// Tracker only consumes the deepseek.Usage struct — no callbacks into
// the LLM client itself. Wire it from the agent's TurnEnd events.
package cache

import (
	"fmt"
	"sync"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tracker accumulates per-turn Usage records and computes session-level
// rollups. Safe for concurrent Record/Snapshot calls.
type Tracker struct {
	mu    sync.Mutex
	turns []deepseek.Usage
}

// New returns an empty Tracker.
func New() *Tracker { return &Tracker{} }

// Record appends a turn's Usage to the history.
func (t *Tracker) Record(u deepseek.Usage) {
	t.mu.Lock()
	t.turns = append(t.turns, u)
	t.mu.Unlock()
}

// Cumulative returns the sum across all recorded turns.
func (t *Tracker) Cumulative() deepseek.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum deepseek.Usage
	for _, u := range t.turns {
		sum.PromptTokens += u.PromptTokens
		sum.CompletionTokens += u.CompletionTokens
		sum.TotalTokens += u.TotalTokens
		sum.PromptCacheHitTokens += u.PromptCacheHitTokens
		sum.PromptCacheMissTokens += u.PromptCacheMissTokens
	}
	return sum
}

// Turns returns a copy of per-turn Usage history.
func (t *Tracker) Turns() []deepseek.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]deepseek.Usage, len(t.turns))
	copy(out, t.turns)
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
