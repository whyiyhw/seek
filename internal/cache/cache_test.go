package cache

import (
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestTracker_Empty(t *testing.T) {
	tr := New()
	if got := tr.Cumulative(); got.TotalTokens != 0 {
		t.Errorf("Cumulative on empty = %+v", got)
	}
	if got := tr.HitRatio(); got != 0 {
		t.Errorf("HitRatio on empty = %v", got)
	}
	if !strings.Contains(tr.Summary(), "no turns") {
		t.Errorf("Summary on empty = %q", tr.Summary())
	}
}

func TestTracker_CumulativeAcrossTurns(t *testing.T) {
	tr := New()
	tr.Record(deepseek.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, PromptCacheHitTokens: 0, PromptCacheMissTokens: 100})
	tr.Record(deepseek.Usage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230, PromptCacheHitTokens: 150, PromptCacheMissTokens: 50})
	tr.Record(deepseek.Usage{PromptTokens: 200, CompletionTokens: 25, TotalTokens: 225, PromptCacheHitTokens: 180, PromptCacheMissTokens: 20})

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
	tr.Record(deepseek.Usage{PromptTokens: 100})
	tr.Record(deepseek.Usage{PromptTokens: 200})
	tr.Record(deepseek.Usage{PromptTokens: 50})
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
	tr.Record(deepseek.Usage{PromptTokens: 1})
	turns := tr.Turns()
	turns[0].PromptTokens = 999 // mutate the snapshot
	if got := tr.Cumulative().PromptTokens; got != 1 {
		t.Errorf("internal state mutated via Turns() snapshot: %d", got)
	}
}

func TestTracker_SummaryFormat(t *testing.T) {
	tr := New()
	tr.Record(deepseek.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, PromptCacheHitTokens: 50, PromptCacheMissTokens: 50})
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
			tr.Record(deepseek.Usage{PromptTokens: 1, PromptCacheHitTokens: 1})
		}()
	}
	wg.Wait()
	if c := tr.Cumulative(); c.PromptTokens != 100 || c.PromptCacheHitTokens != 100 {
		t.Errorf("race-lost writes: %+v", c)
	}
}
