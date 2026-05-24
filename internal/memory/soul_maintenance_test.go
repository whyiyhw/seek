package memory

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluatePending_Promotion(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-20 * 24 * time.Hour) // 20 days ago
	lastSeen := now.Add(-5 * 24 * time.Hour)

	// Build a pending section with one promotable candidate.
	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{
			Trait:     "prefers explicit error handling",
			Why:       "seen across projects",
			Sources:   []string{"proj-a", "proj-b", "proj-c"},
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
		},
	})

	promoted, kept := EvaluatePending(markdown, now)
	if len(promoted) != 1 {
		t.Fatalf("expected 1 promoted, got %d", len(promoted))
	}
	if len(kept) != 0 {
		t.Errorf("expected 0 kept, got %d", len(kept))
	}
	if promoted[0].Trait != "prefers explicit error handling" {
		t.Errorf("unexpected promoted trait: %q", promoted[0].Trait)
	}
}

func TestEvaluatePending_Keep(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-5 * 24 * time.Hour) // too recent (5 days < 14)
	lastSeen := now.Add(-1 * 24 * time.Hour)

	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{
			Trait:     "early trait",
			Why:       "not old enough",
			Sources:   []string{"proj-a", "proj-b", "proj-c"},
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
		},
	})

	promoted, kept := EvaluatePending(markdown, now)
	if len(promoted) != 0 {
		t.Fatalf("expected 0 promoted (too recent), got %d", len(promoted))
	}
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept, got %d", len(kept))
	}
}

func TestEvaluatePending_Delete(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-60 * 24 * time.Hour)
	lastSeen := now.Add(-40 * 24 * time.Hour) // 40 days > 30 → delete

	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{
			Trait:     "stale trait",
			Why:       "no evidence in a while",
			Sources:   []string{"proj-a", "proj-b"},
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
		},
	})

	promoted, kept := EvaluatePending(markdown, now)
	if len(promoted) != 0 {
		t.Errorf("expected 0 promoted, got %d", len(promoted))
	}
	if len(kept) != 0 {
		t.Errorf("expected 0 kept (stale → delete), got %d", len(kept))
	}
}

func TestEvaluatePending_NotEnoughSources(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-20 * 24 * time.Hour) // old enough
	lastSeen := now.Add(-5 * 24 * time.Hour)

	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{
			Trait:     "under-sourced trait",
			Why:       "only 2 projects",
			Sources:   []string{"proj-a", "proj-b"}, // only 2 < 3
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
		},
	})

	promoted, kept := EvaluatePending(markdown, now)
	if len(promoted) != 0 {
		t.Errorf("expected 0 promoted (only 2 sources), got %d", len(promoted))
	}
	if len(kept) != 1 {
		t.Errorf("expected 1 kept, got %d", len(kept))
	}
}

func TestEvaluatePending_MultipleCandidates(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	old := now.Add(-25 * 24 * time.Hour)
	recent := now.Add(-5 * 24 * time.Hour)
	stale := now.Add(-50 * 24 * time.Hour)

	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{Trait: "promotable", Why: "w", Sources: []string{"a", "b", "c"}, FirstSeen: old, LastSeen: recent},
		{Trait: "kept", Why: "w", Sources: []string{"a", "b"}, FirstSeen: old, LastSeen: recent},
		{Trait: "stale", Why: "w", Sources: []string{"a", "b", "c"}, FirstSeen: old, LastSeen: stale},
	})

	promoted, kept := EvaluatePending(markdown, now)
	if len(promoted) != 1 || promoted[0].Trait != "promotable" {
		t.Errorf("expected 1 promoted (promotable), got %d: %v", len(promoted), traitsOf(promoted))
	}
	if len(kept) != 1 || kept[0].Trait != "kept" {
		t.Errorf("expected 1 kept (kept), got %d: %v", len(kept), traitsOf(kept))
	}
}

func traitsOf(cs []LCandidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Trait)
	}
	return out
}

func TestEvaluatePending_SessionSourcesDoNotCount(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-20 * 24 * time.Hour)
	lastSeen := now.Add(-5 * 24 * time.Hour)

	// session: sources don't count toward distinct project count.
	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{
			Trait:     "mixed sources",
			Why:       "w",
			Sources:   []string{"proj-a", "session:abc", "session:def"},
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
		},
	})

	promoted, _ := EvaluatePending(markdown, now)
	if len(promoted) != 0 {
		t.Errorf("session: sources should not count; expected 0 promoted, got %d", len(promoted))
	}
}

func TestApplyMaintenance_WritesToDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	soul, _ := LoadSoul()
	soul.Stable = "- **existing stable trait**\n  - sources: proj-x"

	now := time.Now().UTC()
	promoted := []LCandidate{
		{Trait: "promoted trait", Why: "good evidence", Sources: []string{"a", "b", "c"}, FirstSeen: now, LastSeen: now},
	}
	kept := []LCandidate{
		{Trait: "kept trait", Why: "still accumulating", Sources: []string{"a", "b"}, FirstSeen: now, LastSeen: now},
	}

	if err := soul.ApplyMaintenance(promoted, kept); err != nil {
		t.Fatalf("ApplyMaintenance: %v", err)
	}

	// Reload and verify.
	reloaded, err := LoadSoul()
	if err != nil {
		t.Fatalf("LoadSoul: %v", err)
	}
	if !strings.Contains(reloaded.Stable, "existing stable trait") {
		t.Error("existing Stable content should be preserved")
	}
	if !strings.Contains(reloaded.Stable, "promoted trait") {
		t.Error("promoted trait should appear in Stable")
	}
	if !strings.Contains(reloaded.Pending, "kept trait") {
		t.Error("kept trait should remain in Pending")
	}
	if strings.Contains(reloaded.Pending, "promoted trait") {
		t.Error("promoted trait should NOT remain in Pending")
	}
}

func TestApplyMaintenance_EmptyStablePreserved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	soul, _ := LoadSoul()
	soul.Stable = ""

	now := time.Now().UTC()
	promoted := []LCandidate{
		{Trait: "first stable entry", Why: "w", Sources: []string{"a", "b", "c"}, FirstSeen: now, LastSeen: now},
	}

	if err := soul.ApplyMaintenance(promoted, nil); err != nil {
		t.Fatalf("ApplyMaintenance: %v", err)
	}

	reloaded, _ := LoadSoul()
	if !strings.Contains(reloaded.Stable, "first stable entry") {
		t.Errorf("promoted trait should appear in Stable when starting empty, got:\n%s", reloaded.Stable)
	}
	if reloaded.Pending != "" {
		t.Errorf("Pending should be empty when all candidates are promoted, got:\n%s", reloaded.Pending)
	}
}

// TestEvaluatePending_ZeroTimestampKept verifies that candidates with no
// timestamps (freshly merged from the current dream pass) are never
// promoted or deleted — they stay in Pending to accumulate age.
func TestEvaluatePending_ZeroTimestampKept(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	// Build a markdown candidate without timestamps.
	markdown := FormatLCandidatesMarkdown([]LCandidate{
		{Trait: "new candidate", Why: "just observed", Sources: []string{"a", "b", "c"}},
	})

	promoted, kept := EvaluatePending(markdown, now)
	if len(promoted) != 0 {
		t.Errorf("zero-timestamp candidate should NOT be promoted, got %d: %v", len(promoted), traitsOf(promoted))
	}
	if len(kept) != 1 {
		t.Errorf("zero-timestamp candidate should be kept, got %d", len(kept))
	}
}
