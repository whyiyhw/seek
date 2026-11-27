package memory

// active_memory_test.go locks the "injection is effective use"
// lifecycle (docs/prd/archive/feature-active-memory.md §1.1–1.2, first slice,
// card c_ff4pcp): a continuously-injected entry must survive the
// Day-21 stale cliff, while recall-only decay semantics for everything
// else stay untouched.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
)

func TestTouchInjections_RefreshesAndPersists(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	if err := p.Add(Entry{Name: "alpha", Tagline: "a"}); err != nil {
		t.Fatalf("Add alpha: %v", err)
	}
	if err := p.Add(Entry{Name: "beta", Tagline: "b"}); err != nil {
		t.Fatalf("Add beta: %v", err)
	}

	ts := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	// One missing name in the batch must not fail the whole pass —
	// the injected set is computed from Entries() moments earlier and
	// a concurrent Remove can only shrink it.
	if err := p.TouchInjections([]string{"alpha", "beta", "ghost"}, ts); err != nil {
		t.Fatalf("TouchInjections: %v", err)
	}

	for _, name := range []string{"alpha", "beta"} {
		got, ok := p.Get(name)
		if !ok {
			t.Fatalf("%s missing after touch", name)
		}
		if !got.LastInjectedAt.Equal(ts) {
			t.Errorf("%s LastInjectedAt = %v, want %v", name, got.LastInjectedAt, ts)
		}
		// The touch must not leak into recall semantics.
		if got.RecallCount != 0 {
			t.Errorf("%s RecallCount = %d, want 0 (injection is not recall)", name, got.RecallCount)
		}
		if !got.LastRecalledAt.Equal(got.CreatedAt) {
			t.Errorf("%s LastRecalledAt moved without a recall: %v", name, got.LastRecalledAt)
		}
	}

	// Persisted: a fresh load sees the refreshed timestamps.
	p2, _ := LoadOrCreate(cwd)
	got, _ := p2.Get("alpha")
	if !got.LastInjectedAt.Equal(ts) {
		t.Errorf("LastInjectedAt did not survive reload: %v, want %v", got.LastInjectedAt, ts)
	}
}

func TestTouchInjections_NoopSkipsWrite(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	if err := p.Add(Entry{Name: "alpha", Tagline: "a"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Empty batch and all-missing batch are no-ops, not errors.
	if err := p.TouchInjections(nil, time.Now()); err != nil {
		t.Fatalf("TouchInjections(nil): %v", err)
	}
	if err := p.TouchInjections([]string{"ghost"}, time.Now()); err != nil {
		t.Fatalf("TouchInjections(ghost): %v", err)
	}
	got, _ := p.Get("alpha")
	if !got.LastInjectedAt.IsZero() {
		t.Errorf("untouched entry got LastInjectedAt = %v, want zero", got.LastInjectedAt)
	}
}

func TestScore_AnchorsOnLastInjectedAt(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

	// Created 60d ago, never recalled, injected 3d ago — the
	// injection anchor must keep the score high.
	e := Entry{
		RecallCount:    0,
		CreatedAt:      now.Add(-60 * day),
		LastRecalledAt: now.Add(-60 * day),
		LastInjectedAt: now.Add(-3 * day),
	}
	if got := Score(e, now, defaultHalfLife); got < 0.9 {
		t.Errorf("Score = %.3f; expected ≥0.9 when LastInjectedAt is 3 days ago", got)
	}

	// Legacy entries (zero LastInjectedAt) keep the old formula: the
	// zero value must never win the max anchor.
	legacy := e
	legacy.LastInjectedAt = time.Time{}
	if got := Score(legacy, now, defaultHalfLife); got >= stalenessThreshold {
		t.Errorf("legacy Score = %.3f; expected < %.2f (60d idle, never recalled)", got, stalenessThreshold)
	}
}

// TestHook_OnPrePrompt_InjectionCountsAsUse is the end-to-end seam:
// the snapshot injection itself must refresh LastInjectedAt for the
// entries that actually made it into the prompt, and must NOT touch
// stale entries (they were never injected).
func TestHook_OnPrePrompt_InjectionCountsAsUse(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	old := now.Add(-90 * day)
	plantEntry(t, p, Entry{
		Name: "live", Tagline: "in the index", Content: "x",
		CreatedAt: old, UpdatedAt: old, LastRecalledAt: old,
	})
	plantEntry(t, p, Entry{
		Name: "buried", Tagline: "stale, never injected", Content: "x",
		CreatedAt: old, UpdatedAt: old, LastRecalledAt: old, Stale: true,
	})

	ts := now.Truncate(time.Second)
	h := &Hook{Project: p, Now: func() time.Time { return ts }}
	if _, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{}); err != nil {
		t.Fatalf("OnPrePrompt: %v", err)
	}

	got, _ := p.Get("live")
	if got.LastInjectedAt.IsZero() {
		t.Fatal("injected entry did not get LastInjectedAt refreshed")
	}
	if !got.LastInjectedAt.Equal(ts) {
		t.Errorf("live LastInjectedAt = %v, want hook timestamp %v", got.LastInjectedAt, ts)
	}

	buried, _ := p.Get("buried")
	if !buried.LastInjectedAt.IsZero() {
		t.Errorf("stale entry must not be touched, got LastInjectedAt = %v", buried.LastInjectedAt)
	}
}

// TestLifecycle_InjectedEntrySurvivesDay21 replays the PRD §1.2
// scenario with the fix in place: an entry whose tagline is injected
// every session must NOT hit the Day-21 stale cliff or vanish from the
// index, while a stale sibling (never injected — Index() skips it)
// stays stale: freshness never resurrects what the prompt never sees.
//
// Note there is deliberately NO "never-injected non-stale sibling"
// case here: under the fast path the snapshot injects every non-stale
// entry, so such an entry WOULD be injected and refreshed — that is
// the card's decision, not a leak. The non-injection escape hatches
// (born-stale, budget-squeeze, project-abandoned) are covered by this
// test, TestHook_OnPrePrompt_TruncatedEntriesNotTouched, and
// TestLifecycle_FreshnessExpiresWhenInjectionStops respectively.
func TestLifecycle_InjectedEntrySurvivesDay21(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	day0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	old := day0
	plantEntry(t, p, Entry{
		Name: "injected-every-day", Tagline: "daily preference", Content: "x",
		CreatedAt: old, UpdatedAt: old, LastRecalledAt: old,
	})
	// Born 90d before day0 AND already stale: its score never climbs
	// back over the threshold, so GC keeps it stale and the snapshot
	// never injects it. (A Day0-anchored stale entry would be
	// legitimately unmarked by the very first RunGC — score ≈ 1.0.)
	plantEntry(t, p, Entry{
		Name: "born-stale", Tagline: "already cold", Content: "x",
		CreatedAt: old.Add(-90 * day), UpdatedAt: old.Add(-90 * day),
		LastRecalledAt: old.Add(-90 * day), Stale: true,
	})

	// Simulate 22 session-days: each day a session starts (RunGC) and
	// its first prompt injects the M-index snapshot. A fresh Hook per
	// day models independent sessions (snapshot state resets).
	var indexContent string
	for d := 0; d <= 21; d++ {
		now := day0.Add(time.Duration(d) * day)
		if _, err := p.RunGC(now); err != nil {
			t.Fatalf("day %d RunGC: %v", d, err)
		}
		h := &Hook{Project: p, Now: func() time.Time { return now }}
		out, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
		if err != nil {
			t.Fatalf("day %d OnPrePrompt: %v", d, err)
		}
		if len(out.Prepend) == 0 {
			t.Fatalf("day %d: M-index snapshot vanished entirely", d)
		}
		indexContent = out.Prepend[0].Content
	}

	got, ok := p.Get("injected-every-day")
	if !ok {
		t.Fatal("continuously-injected entry disappeared from the project")
	}
	if got.Stale {
		t.Error("continuously-injected entry went stale (PRD §1.2 Day-21 cliff hit)")
	}
	if !got.LastInjectedAt.Equal(day0.Add(21 * day)) {
		t.Errorf("LastInjectedAt = %v, want day-21 timestamp (last session)", got.LastInjectedAt)
	}
	if strings.Contains(indexContent, "born-stale") {
		t.Error("stale entry must never appear in the injected index")
	}
	if e, _ := p.Get("born-stale"); !e.Stale || !e.LastInjectedAt.IsZero() {
		t.Errorf("born-stale revived by freshness: Stale=%v LastInjectedAt=%v", e.Stale, e.LastInjectedAt)
	}

	// The survivor must still be in the injection-ready index.
	names := map[string]struct{}{}
	for _, ie := range p.Index() {
		names[ie.Name] = struct{}{}
	}
	if _, in := names["injected-every-day"]; !in {
		t.Error("continuously-injected entry dropped out of Index()")
	}
}

// TestHook_OnPrePrompt_TruncatedEntriesNotTouched locks the accounting
// set: only entries that actually made it into the injected bytes get
// refreshed. When the token budget forces truncation, squeezed-out
// entries keep a zero LastInjectedAt — they stop being refreshed and
// decay on the original anchors. This is the self-limiting half of
// "injection is use": without it, freshness would be a permanent
// exemption and GC would never retire anything.
func TestHook_OnPrePrompt_TruncatedEntriesNotTouched(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	old := now.Add(-90 * day)
	// High recall count wins the truncation sort (pinned desc →
	// recall_count desc → name asc) — survivor stays in the budget.
	plantEntry(t, p, Entry{
		Name: "a-survivor", Tagline: "well earned", Content: "x",
		CreatedAt: old, UpdatedAt: old, LastRecalledAt: now.Add(-time.Hour),
		RecallCount: 9,
	})
	plantEntry(t, p, Entry{
		Name: "zzz-squeezed-out", Tagline: "lowest rank loses", Content: "x",
		CreatedAt: old, UpdatedAt: old, LastRecalledAt: old,
	})
	// Filler to push the fast-path rendering over maxMIndexTokens so
	// the truncation sort actually runs.
	for i := 0; i < 200; i++ {
		plantEntry(t, p, Entry{
			Name:      "filler-" + fmt.Sprintf("%03d", i),
			Tagline:   "padding tagline to exceed the m-index token budget",
			Content:   "x",
			CreatedAt: old, UpdatedAt: old, LastRecalledAt: old,
		})
	}

	h := &Hook{Project: p}
	out, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if err != nil {
		t.Fatalf("OnPrePrompt: %v", err)
	}
	if len(out.Prepend) == 0 {
		t.Fatal("expected an M-index message")
	}
	content := out.Prepend[0].Content
	if !strings.Contains(content, "a-survivor") {
		t.Error("survivor missing from injected index")
	}
	if strings.Contains(content, "zzz-squeezed-out") {
		t.Error("squeezed-out entry leaked into injected index")
	}
	// No assertion on the truncation notice: whether it renders depends
	// on the greedy packing margin (body flush against the budget leaves
	// no room for the note) — the squeeze itself is already proven above.

	if e, _ := p.Get("a-survivor"); e.LastInjectedAt.IsZero() {
		t.Error("injected survivor did not get LastInjectedAt refreshed")
	}
	if e, _ := p.Get("zzz-squeezed-out"); !e.LastInjectedAt.IsZero() {
		t.Errorf("squeezed-out entry got refreshed: %v — accounting set must be the injected set", e.LastInjectedAt)
	}
}

// TestLifecycle_FreshnessExpiresWhenInjectionStops proves the
// freshness signal is not a permanent exemption: once sessions stop
// (no more snapshot injections), the entry decays on its remaining
// anchors, goes stale, and eventually archives — the GC escape hatch
// stays open for dead projects.
func TestLifecycle_FreshnessExpiresWhenInjectionStops(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	day0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	old := day0
	plantEntry(t, p, Entry{
		Name: "was-daily", Tagline: "used for a month", Content: "x",
		CreatedAt: old, UpdatedAt: old, LastRecalledAt: old,
	})

	// 30 days of daily sessions (injected), then the project dies.
	for d := 0; d <= 30; d++ {
		now := day0.Add(time.Duration(d) * day)
		if _, err := p.RunGC(now); err != nil {
			t.Fatalf("day %d RunGC: %v", d, err)
		}
		h := &Hook{Project: p, Now: func() time.Time { return now }}
		if _, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{}); err != nil {
			t.Fatalf("day %d OnPrePrompt: %v", d, err)
		}
	}

	// Project abandoned: no more sessions, GC keeps firing (other
	// projects' dream scans / manual `seek memory gc` could do this).
	// Day 31..131 with no injection — entry must go stale around
	// lastActive+21d and archive after 60d of continuous staleness.
	var archived bool
	for d := 31; d <= 131 && !archived; d++ {
		now := day0.Add(time.Duration(d) * day)
		if _, err := p.RunGC(now); err != nil {
			t.Fatalf("day %d RunGC: %v", d, err)
		}
		_, ok := p.Get("was-daily")
		archived = !ok
	}
	if !archived {
		t.Error("entry injected for 30d then abandoned never archived — freshness became a permanent exemption")
	}
}
