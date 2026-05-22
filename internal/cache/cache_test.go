package cache

import (
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// recordFlash is shorthand for the most common call shape in these
// tests — V4-Flash, standard tier. Tests that care about cost lock-in
// across model/tier transitions construct their own Record calls.
func recordFlash(tr *Tracker, u deepseek.Usage) {
	tr.Record(u, deepseek.ModelV4Flash, pricing.TierStandard)
}

func TestTracker_Empty(t *testing.T) {
	tr := New()
	if got := tr.Cumulative(); got.TotalTokens != 0 {
		t.Errorf("Cumulative on empty = %+v", got)
	}
	if got := tr.HitRatio(); got != 0 {
		t.Errorf("HitRatio on empty = %v", got)
	}
	if got := tr.CumulativeCost(); got != 0 {
		t.Errorf("CumulativeCost on empty = %v", got)
	}
	if !strings.Contains(tr.Summary(), "no turns") {
		t.Errorf("Summary on empty = %q", tr.Summary())
	}
}

func TestTracker_CumulativeAcrossTurns(t *testing.T) {
	tr := New()
	recordFlash(tr, deepseek.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, PromptCacheHitTokens: 0, PromptCacheMissTokens: 100})
	recordFlash(tr, deepseek.Usage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230, PromptCacheHitTokens: 150, PromptCacheMissTokens: 50})
	recordFlash(tr, deepseek.Usage{PromptTokens: 200, CompletionTokens: 25, TotalTokens: 225, PromptCacheHitTokens: 180, PromptCacheMissTokens: 20})

	c := tr.Cumulative()
	if c.PromptTokens != 500 {
		t.Errorf("PromptTokens = %d, want 500", c.PromptTokens)
	}
	if c.CompletionTokens != 75 {
		t.Errorf("CompletionTokens = %d, want 75", c.CompletionTokens)
	}
	if c.PromptCacheHitTokens != 330 || c.PromptCacheMissTokens != 170 {
		t.Errorf("cache split = (%d, %d), want (330, 170)", c.PromptCacheHitTokens, c.PromptCacheMissTokens)
	}
	// Hit ratio = 330 / (330 + 170) = 0.66
	if got := tr.HitRatio(); got < 0.659 || got > 0.661 {
		t.Errorf("HitRatio = %v, want ≈0.66", got)
	}
	if got := tr.SavedTokens(); got != 330 {
		t.Errorf("SavedTokens = %d, want 330", got)
	}
}

func TestTracker_Last(t *testing.T) {
	tr := New()
	// Empty tracker returns zero usage.
	if got := tr.Last(); got.PromptTokens != 0 {
		t.Errorf("Last() on empty = %+v, want zero", got)
	}
	recordFlash(tr, deepseek.Usage{PromptTokens: 100})
	recordFlash(tr, deepseek.Usage{PromptTokens: 200})
	recordFlash(tr, deepseek.Usage{PromptTokens: 50})
	// Last() returns the most recent turn, NOT the cumulative.
	if got := tr.Last().PromptTokens; got != 50 {
		t.Errorf("Last().PromptTokens = %d, want 50", got)
	}
	// Cumulative should still be 350.
	if got := tr.Cumulative().PromptTokens; got != 350 {
		t.Errorf("Cumulative().PromptTokens = %d, want 350", got)
	}
}

func TestTracker_TurnsReturnsCopy(t *testing.T) {
	tr := New()
	recordFlash(tr, deepseek.Usage{PromptTokens: 1})
	turns := tr.Turns()
	turns[0].PromptTokens = 999 // mutate the snapshot
	if got := tr.Cumulative().PromptTokens; got != 1 {
		t.Errorf("internal state mutated via Turns() snapshot: %d", got)
	}
}

func TestTracker_SummaryFormat(t *testing.T) {
	tr := New()
	recordFlash(tr, deepseek.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, PromptCacheHitTokens: 50, PromptCacheMissTokens: 50})
	s := tr.Summary()
	// Expect a single line containing key fields. Loose checks so cosmetic
	// formatting tweaks don't break the test.
	for _, frag := range []string{"1 turn", "hit 50", "miss 50", "50.0%"} {
		if !strings.Contains(s, frag) {
			t.Errorf("Summary missing %q: %q", frag, s)
		}
	}
}

func TestTracker_ConcurrentSafe(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordFlash(tr, deepseek.Usage{PromptTokens: 1, PromptCacheHitTokens: 1})
		}()
	}
	wg.Wait()
	if c := tr.Cumulative(); c.PromptTokens != 100 || c.PromptCacheHitTokens != 100 {
		t.Errorf("race-lost writes: %+v", c)
	}
}

// TestTracker_CumulativeCostLockedInAtRecord is the load-bearing pin
// for the cross-model cost bug. Switching from V4-Flash to V4-Pro
// mid-session must NOT retroactively re-price the V4-Flash turn at
// V4-Pro's ~3.1× rate, and switching back must not re-price the
// V4-Pro turn at the cheaper V4-Flash rate either.
func TestTracker_CumulativeCostLockedInAtRecord(t *testing.T) {
	tr := New()

	// Turn 1 — 1M completion @ V4-Flash standard ($0.28/M output) = $0.28.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierStandard,
	)
	if got := tr.CumulativeCost(); got < 0.279 || got > 0.281 {
		t.Errorf("after V4-Flash turn: cost = %v, want ≈0.28", got)
	}

	// Turn 2 — 1M completion @ V4-Pro standard ($0.87/M output) = $0.87.
	// Cumulative now = 0.28 + 0.87 = 1.15.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Pro, pricing.TierStandard,
	)
	if got := tr.CumulativeCost(); got < 1.149 || got > 1.151 {
		t.Errorf("after V4-Pro turn: cost = %v, want ≈1.15", got)
	}

	// Turn 3 — 1M completion @ V4-Flash standard ($0.28/M) = $0.28.
	// Cumulative now = 1.15 + 0.28 = 1.43. If we were re-pricing all
	// turns at the CURRENT model (V4-Flash) this would come out to
	// 3 × 0.28 = 0.84 instead — the exact bug this pins against.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierStandard,
	)
	if got := tr.CumulativeCost(); got < 1.429 || got > 1.431 {
		t.Errorf("after switch-back to V4-Flash: cost = %v, want ≈1.43 (NOT 0.84 — that would mean we re-priced)", got)
	}
}

// TestTracker_TierTransitionDoesNotRePrice covers the off-peak
// boundary case — same root cause as the cross-model bug, different
// trigger.
func TestTracker_TierTransitionDoesNotRePrice(t *testing.T) {
	tr := New()
	// Standard tier: 1M completion @ V4-Flash = $0.28.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierStandard,
	)
	// Off-peak tier (50% discount): 1M completion = $0.14.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierOffPeak,
	)
	// Expected = 0.28 + 0.14 = 0.42. NOT 0.28 (both at off-peak)
	// and NOT 0.56 (both at standard).
	if got := tr.CumulativeCost(); got < 0.419 || got > 0.421 {
		t.Errorf("CumulativeCost across tier boundary = %v, want ≈0.42 (each turn priced at its own tier)", got)
	}
}
