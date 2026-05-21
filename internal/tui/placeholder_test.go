package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// modelAt returns a Model wired with the given options for placeholder
// tests. We avoid spinning up bubbletea internals — computePlaceholder
// reads from Model fields directly.
func modelAt(now time.Time, turns int, yolo bool, usage deepseek.Usage) *Model {
	tr := cache.New()
	if usage != (deepseek.Usage{}) {
		tr.Record(usage)
	}
	return &Model{
		opts: Options{
			Tracker: tr,
			Yolo:    yolo,
			Model:   deepseek.ModelChat,
		},
		turns: turns,
		now:   now,
	}
}

// noon = clearly inside the standard (non off-peak) window.
func noon(t *testing.T) time.Time {
	return time.Date(2026, time.January, 15, 12, 0, 0, 0, pricing.Shanghai)
}

func TestPlaceholder_YoloWinsOverEverything(t *testing.T) {
	m := modelAt(noon(t), 5, true, deepseek.Usage{PromptCacheHitTokens: 9000, PromptCacheMissTokens: 1000, PromptTokens: 10000})
	if got := computePlaceholder(m); !strings.Contains(got, "YOLO") {
		t.Errorf("got %q, want a YOLO warning", got)
	}
}

func TestPlaceholder_OffPeakInPriorityOrder(t *testing.T) {
	offPeak := time.Date(2026, time.January, 15, 3, 0, 0, 0, pricing.Shanghai)
	m := modelAt(offPeak, 5, false, deepseek.Usage{})
	if got := computePlaceholder(m); !strings.Contains(got, "off-peak") {
		t.Errorf("got %q, want off-peak hint", got)
	}
}

func TestPlaceholder_FirstTurnWelcome(t *testing.T) {
	m := modelAt(noon(t), 0, false, deepseek.Usage{})
	got := computePlaceholder(m)
	for _, frag := range []string{"Ask seek", "Enter", "/", "@"} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in welcome: %s", frag, got)
		}
	}
}

func TestPlaceholder_HotCacheAfterThreeTurns(t *testing.T) {
	m := modelAt(noon(t), 4, false, deepseek.Usage{
		PromptCacheHitTokens:  8000,
		PromptCacheMissTokens: 2000,
		PromptTokens:          10000,
	})
	if got := computePlaceholder(m); !strings.Contains(got, "cache hot") {
		t.Errorf("got %q, want hot-cache hint", got)
	}
}

func TestPlaceholder_LowCacheOnlyForBigPrompts(t *testing.T) {
	// Low ratio but small prompt → fall through to rotating tip (don't
	// complain about cache when the prompt is too small to cache).
	tiny := modelAt(noon(t), 4, false, deepseek.Usage{
		PromptCacheHitTokens:  0,
		PromptCacheMissTokens: 200,
		PromptTokens:          200,
	})
	if got := computePlaceholder(tiny); strings.Contains(got, "cache hit ratio is low") {
		t.Errorf("small prompts shouldn't trigger low-cache hint: %s", got)
	}

	// Big prompt, low ratio → the complaint surfaces.
	big := modelAt(noon(t), 4, false, deepseek.Usage{
		PromptCacheHitTokens:  100,
		PromptCacheMissTokens: 4000,
		PromptTokens:          4100,
	})
	if got := computePlaceholder(big); !strings.Contains(got, "cache hit ratio is low") {
		t.Errorf("got %q, want low-cache complaint", got)
	}
}

func TestPlaceholder_RotatesByTurn(t *testing.T) {
	// Use turn counts 1..len(tips)+1 — none should hit the cache rules
	// since usage is empty, so the rotation is what we observe.
	seen := map[string]bool{}
	for i := 1; i <= len(rotatingTips)+1; i++ {
		m := modelAt(noon(t), i, false, deepseek.Usage{})
		seen[computePlaceholder(m)] = true
	}
	// We should have hit at least 3 distinct rotating tips.
	if len(seen) < 3 {
		t.Errorf("rotation produced only %d distinct placeholders: %v", len(seen), seen)
	}
}
