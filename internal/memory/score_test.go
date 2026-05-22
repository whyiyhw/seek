package memory

import (
	"math"
	"os"
	"testing"
	"time"
)

const day = 24 * time.Hour

// scoreFixtures encodes PRD §6's intuitive correspondence table verbatim
// so a future tweak to the formula must explicitly justify breaking the
// documented behaviour. Half-life is the doc default (30 days).
func TestScore_PRDFixtureTable(t *testing.T) {
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	halfLife := defaultHalfLife

	cases := []struct {
		name         string
		recallCount  int
		ageDays      float64
		wantScoreMin float64 // inclusive
		wantScoreMax float64 // inclusive
		wantStale    bool    // expected after RunGC (ignoring grace)
	}{
		{
			name:        "weekly-used common entry",
			recallCount: 5, ageDays: 7,
			wantScoreMin: 4.5, wantScoreMax: 5.0,
			wantStale: false,
		},
		{
			name:        "60-day-old single-recall",
			recallCount: 1, ageDays: 60,
			wantScoreMin: 0.20, wantScoreMax: 0.35,
			wantStale: true,
		},
		{
			name:        "60-day-old frequent-recall",
			recallCount: 5, ageDays: 60,
			wantScoreMin: 0.70, wantScoreMax: 0.95,
			wantStale: false,
		},
		{
			name:        "90-day-old single-recall (edge)",
			recallCount: 1, ageDays: 90,
			wantScoreMin: 0.05, wantScoreMax: 0.20,
			wantStale: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Entry{
				RecallCount:    c.recallCount,
				CreatedAt:      now.Add(-time.Duration(c.ageDays * float64(day))),
				LastRecalledAt: now.Add(-time.Duration(c.ageDays * float64(day))),
			}
			got := Score(e, now, halfLife)
			if got < c.wantScoreMin || got > c.wantScoreMax {
				t.Errorf("Score = %.3f, want in [%.3f, %.3f]", got, c.wantScoreMin, c.wantScoreMax)
			}
			gotStale := got < stalenessThreshold
			if gotStale != c.wantStale {
				t.Errorf("stale verdict = %v, want %v (score=%.3f)", gotStale, c.wantStale, got)
			}
		})
	}
}

func TestScore_UsesMostRecentOfTimestamps(t *testing.T) {
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	halfLife := defaultHalfLife

	// CreatedAt is oldest, LastRecalledAt is newest — Score should anchor
	// on LastRecalledAt (least decay). If it accidentally used CreatedAt
	// the score would be much lower.
	e := Entry{
		RecallCount:    0,
		CreatedAt:      now.Add(-60 * day),
		LastRecalledAt: now.Add(-3 * day),
	}
	got := Score(e, now, halfLife)
	if got < 0.9 {
		t.Errorf("Score = %.3f; expected ≥0.9 when LastRecalledAt is 3 days ago", got)
	}
}

func TestScore_FuturisticTimestampClampsToZeroAge(t *testing.T) {
	// Clock skew / manual edits could produce a last_active in the
	// future. Score must not return NaN or absurdly large values.
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	e := Entry{
		RecallCount:    0,
		CreatedAt:      now.Add(7 * day),
		LastRecalledAt: now.Add(7 * day),
	}
	got := Score(e, now, defaultHalfLife)
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("Score with future timestamp should be finite, got %v", got)
	}
	if got != 1.0 {
		t.Errorf("future timestamp should clamp age to 0 → score=1.0, got %v", got)
	}
}

func TestRunGC_MarksStaleAndPersists(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	old := now.Add(-90 * day) // way past grace, single recall → stale

	// Plant directly (NOT via Add) — Add() bumps UpdatedAt to now, which
	// would dominate the max(CreatedAt, UpdatedAt, LastRecalledAt)
	// anchor and keep the entry "fresh". Production GC fires against
	// entries that were written long ago, where UpdatedAt is actually
	// historical.
	plantEntry(t, p, Entry{
		Name:           "old-entry",
		Tagline:        "fades",
		Content:        "x",
		CreatedAt:      old,
		UpdatedAt:      old,
		LastRecalledAt: old,
		RecallCount:    1,
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.MarkedStale != 1 {
		t.Errorf("MarkedStale = %d, want 1 (report: %+v)", report.MarkedStale, report)
	}

	// Persisted: a fresh load sees the stale flag.
	p2, _ := LoadOrCreate(cwd)
	got, _ := p2.Get("old-entry")
	if !got.Stale {
		t.Errorf("stale flag did not survive reload")
	}
}

// plantEntry writes an Entry directly into the in-memory map + on-disk
// JSONL, bypassing Add()'s UpdatedAt-bump. Used by GC tests that need
// to simulate entries authored long ago.
func plantEntry(t *testing.T, p *Project, e Entry) {
	t.Helper()
	e.SchemaVersion = SchemaVersion
	if _, dup := p.entries[e.Name]; !dup {
		p.order = append(p.order, e.Name)
	}
	p.entries[e.Name] = e
	if err := p.writeEntries(); err != nil {
		t.Fatalf("plantEntry write: %v", err)
	}
}

func TestRunGC_PinnedSkipped(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	old := now.Add(-1000 * day) // would be deeply stale without pinned

	plantEntry(t, p, Entry{
		Name:           "pinned-entry",
		Tagline:        "load-bearing",
		Content:        "x",
		CreatedAt:      old,
		UpdatedAt:      old,
		LastRecalledAt: old,
		Pinned:         true,
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.Skipped != 1 || report.MarkedStale != 0 {
		t.Errorf("pinned should be skipped, got report=%+v", report)
	}
	got, _ := p.Get("pinned-entry")
	if got.Stale {
		t.Errorf("pinned entry was marked stale")
	}
}

func TestRunGC_GracePeriodSkipped(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	young := now.Add(-3 * day) // inside the 7-day grace

	if err := p.Add(Entry{
		Name:           "fresh-entry",
		Tagline:        "brand new",
		Content:        "x",
		CreatedAt:      young,
		LastRecalledAt: young,
		RecallCount:    0,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.Skipped != 1 || report.MarkedStale != 0 {
		t.Errorf("grace-period entry should be skipped, got report=%+v", report)
	}
}

func TestRunGC_UnmarksStaleWhenScoreRecovers(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	plantEntry(t, p, Entry{
		Name:           "revived",
		Tagline:        "back from dead",
		Content:        "x",
		CreatedAt:      now.Add(-90 * day),
		UpdatedAt:      now.Add(-90 * day),
		LastRecalledAt: now.Add(-5 * time.Minute), // recalled recently
		RecallCount:    1,
		Stale:          true, // prior GC marked it
	})

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.UnmarkedStale != 1 {
		t.Errorf("expected UnmarkedStale=1, got %+v", report)
	}
	got, _ := p.Get("revived")
	if got.Stale {
		t.Errorf("entry should be unmarked after recovery")
	}
}

func TestRunGC_NoChangeDoesNotRewriteJSONL(t *testing.T) {
	// When nothing changes, we want to avoid touching memory.jsonl —
	// rewrites change file mtime + (potentially) byte order, both of
	// which can perturb downstream cache assumptions.
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Now().UTC()
	if err := p.Add(Entry{
		Name:           "fresh-and-good",
		Tagline:        "x",
		Content:        "y",
		CreatedAt:      now.Add(-3 * day), // grace period → skipped
		LastRecalledAt: now.Add(-3 * day),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	jsonl := p.Dir + "/memory.jsonl"
	statBefore, err := os.Stat(jsonl)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	// Yield a moment so a rewrite would clearly change mtime.
	time.Sleep(10 * time.Millisecond)

	report, err := p.RunGC(now)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if report.MarkedStale != 0 || report.UnmarkedStale != 0 {
		t.Errorf("expected no flips, got %+v", report)
	}

	statAfter, err := os.Stat(jsonl)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("RunGC should not rewrite memory.jsonl when no flips happen (mtime changed)")
	}
}
