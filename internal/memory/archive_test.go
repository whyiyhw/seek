package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunGC_StaleSinceSetOnInitialFlip(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	old := now.Add(-60 * day) // 60-day-old, single recall → score ≈ 0.27 (stale)

	plantEntry(t, p, Entry{
		Name:           "first-time",
		Tagline:        "x",
		CreatedAt:      old,
		UpdatedAt:      old,
		LastRecalledAt: old,
		RecallCount:    1,
	})

	if _, err := p.RunGC(now); err != nil {
		t.Fatalf("RunGC: %v", err)
	}

	got, _ := p.Get("first-time")
	if !got.Stale {
		t.Fatalf("entry should be stale after GC")
	}
	if !got.StaleSince.Equal(now) {
		t.Errorf("StaleSince = %v, want %v (the GC tick)", got.StaleSince, now)
	}
}

func TestRunGC_StaleSinceClearedOnRecovery(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	plantEntry(t, p, Entry{
		Name:           "back-to-life",
		Tagline:        "x",
		CreatedAt:      now.Add(-90 * day),
		UpdatedAt:      now.Add(-90 * day),
		LastRecalledAt: now.Add(-5 * time.Minute), // recently recalled → score ≈ 1.0
		RecallCount:    1,
		Stale:          true,
		StaleSince:     now.Add(-30 * day),
	})

	if _, err := p.RunGC(now); err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	got, _ := p.Get("back-to-life")
	if got.Stale {
		t.Errorf("entry should be unmarked after recovery")
	}
	if !got.StaleSince.IsZero() {
		t.Errorf("StaleSince should be cleared on recovery, got %v", got.StaleSince)
	}
}

func TestRunGC_BackfillsLegacyStaleSince(t *testing.T) {
	// Upgrade scenario: existing entry has stale=true but StaleSince
	// is zero (pre-M5.5 data). RunGC should backfill StaleSince to
	// "now" so the archive clock starts fresh, not "ten years ago".
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	plantEntry(t, p, Entry{
		Name:           "legacy",
		Tagline:        "x",
		CreatedAt:      now.Add(-365 * day),
		UpdatedAt:      now.Add(-365 * day),
		LastRecalledAt: now.Add(-365 * day),
		RecallCount:    1,
		Stale:          true,
		// StaleSince intentionally zero — simulates pre-M5.5 on-disk data
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}

	// First pass: backfill happens, archive does NOT fire (the clock
	// was just started).
	if report.Archived != 0 {
		t.Errorf("legacy entry should NOT be archived on backfill pass, got %+v", report)
	}
	got, _ := p.Get("legacy")
	if got.StaleSince.IsZero() {
		t.Errorf("StaleSince should have been backfilled to now, still zero")
	}
}

func TestRunGC_ArchivesAfterSustainedStale(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	plantEntry(t, p, Entry{
		Name:           "old-news",
		Tagline:        "should archive",
		Content:        "body",
		CreatedAt:      now.Add(-200 * day),
		UpdatedAt:      now.Add(-200 * day),
		LastRecalledAt: now.Add(-200 * day),
		RecallCount:    0,
		Stale:          true,
		StaleSince:     now.Add(-90 * day), // stale for 90 days > 60-day threshold
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.Archived != 1 {
		t.Errorf("expected Archived=1, got %+v", report)
	}

	// Active set: gone from Get / Index / Entries.
	if _, ok := p.Get("old-news"); ok {
		t.Errorf("archived entry should be removed from active map")
	}
	if len(p.Index()) != 0 {
		t.Errorf("archived entry should be absent from Index: %+v", p.Index())
	}
	if len(p.Entries()) != 0 {
		t.Errorf("archived entry should be absent from Entries: %+v", p.Entries())
	}

	// archived.jsonl: contains exactly the entry we archived.
	archived, err := p.LoadArchived()
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if len(archived) != 1 || archived[0].Name != "old-news" {
		t.Errorf("archived.jsonl should contain old-news, got %+v", archived)
	}
	if archived[0].Content != "body" {
		t.Errorf("archived entry content lost: %+v", archived[0])
	}

	// Persistence: a fresh reload should agree.
	p2, _ := LoadOrCreate(cwd)
	if _, ok := p2.Get("old-news"); ok {
		t.Errorf("archived entry survived in memory.jsonl after reload")
	}
}

func TestRunGC_DoesNotArchiveStaleButShallow(t *testing.T) {
	// Score below stalenessThreshold (0.5) but ABOVE archiveThreshold
	// (0.1) — the entry is stale but not deeply stale. Archive must
	// not fire even if it's been stale for >60 days.
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	// 60 days old + 1 recall → score ≈ 2 * 0.25 = 0.5; nudge slightly
	// older so it's stale (just below 0.5) but well above 0.1.
	plantEntry(t, p, Entry{
		Name:           "shallow-stale",
		Tagline:        "still useful",
		CreatedAt:      now.Add(-65 * day),
		UpdatedAt:      now.Add(-65 * day),
		LastRecalledAt: now.Add(-65 * day),
		RecallCount:    1,
		Stale:          true,
		StaleSince:     now.Add(-70 * day),
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.Archived != 0 {
		t.Errorf("shallow-stale entry should NOT be archived (score > 0.1); got %+v", report)
	}
	if _, ok := p.Get("shallow-stale"); !ok {
		t.Errorf("entry should still be in active map")
	}
}

func TestRunGC_DoesNotArchiveBefore60Days(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	plantEntry(t, p, Entry{
		Name:           "young-stale",
		Tagline:        "x",
		CreatedAt:      now.Add(-200 * day),
		UpdatedAt:      now.Add(-200 * day),
		LastRecalledAt: now.Add(-200 * day),
		RecallCount:    0,
		Stale:          true,
		StaleSince:     now.Add(-30 * day), // stale only 30 days — below 60d threshold
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.Archived != 0 {
		t.Errorf("entry stale <60d should NOT archive, got %+v", report)
	}
}

func TestRunGC_RecoveryResetsArchiveClock(t *testing.T) {
	// An entry that was stale, recovers, then goes stale again must
	// get a FRESH 60-day clock — not carry forward the old StaleSince.
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)

	// Phase 1: deeply stale + StaleSince far in the past.
	plantEntry(t, p, Entry{
		Name:           "yo-yo",
		Tagline:        "x",
		CreatedAt:      now.Add(-200 * day),
		UpdatedAt:      now.Add(-200 * day),
		LastRecalledAt: now.Add(-5 * time.Minute), // recently recalled
		RecallCount:    1,
		Stale:          true,
		StaleSince:     now.Add(-90 * day),
	})

	// Phase 2: GC sees the recovery, clears StaleSince.
	if _, err := p.RunGC(now); err != nil {
		t.Fatalf("RunGC phase 2: %v", err)
	}
	got, _ := p.Get("yo-yo")
	if got.Stale || !got.StaleSince.IsZero() {
		t.Fatalf("phase 2 failed: %+v", got)
	}

	// Phase 3: simulate user not touching it for 200 more days. GC
	// re-flips stale and sets a NEW StaleSince. The old 90-day cold
	// streak must NOT carry over.
	future := now.Add(200 * day)
	// Plant new state directly (bypass Add to avoid UpdatedAt bump).
	plantEntry(t, p, Entry{
		Name:           "yo-yo",
		Tagline:        "x",
		CreatedAt:      got.CreatedAt,
		UpdatedAt:      got.UpdatedAt,
		LastRecalledAt: got.LastRecalledAt, // unchanged from phase 2 (~5min before `now`)
		RecallCount:    1,
		Stale:          false,
		// StaleSince zero — was cleared
	})

	report, err := p.RunGC(future)
	if err != nil {
		t.Fatalf("RunGC phase 3: %v", err)
	}
	if report.MarkedStale != 1 {
		t.Errorf("expected re-marking as stale, got %+v", report)
	}
	if report.Archived != 0 {
		t.Errorf("must NOT archive on the SAME pass that re-marks — new clock")
	}
	got, _ = p.Get("yo-yo")
	if !got.StaleSince.Equal(future) {
		t.Errorf("StaleSince should be the new GC tick %v, got %v", future, got.StaleSince)
	}
}

func TestEntry_StaleSinceOmittedWhenZero(t *testing.T) {
	// json:",omitzero" guarantees fresh entries don't write a useless
	// "0001-01-01T00:00:00Z" stale_since field. Verify by reading
	// memory.jsonl bytes directly.
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	if err := p.Add(Entry{Name: "fresh", Tagline: "x", Content: "y"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(p.Dir, "memory.jsonl"))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if strings.Contains(string(data), "stale_since") {
		t.Errorf("fresh entry leaked stale_since into JSONL: %s", data)
	}
	if strings.Contains(string(data), "0001-01-01") {
		t.Errorf("fresh entry leaked zero-time into JSONL: %s", data)
	}
}

func TestLoadArchived_EmptyWhenNoArchiveFile(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	archived, err := p.LoadArchived()
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if archived != nil {
		t.Errorf("expected nil slice with no archive file, got %+v", archived)
	}
}
