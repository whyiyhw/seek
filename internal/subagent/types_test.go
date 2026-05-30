package subagent

import (
	"strings"
	"testing"
	"time"
)

func TestType_IsValid(t *testing.T) {
	for _, v := range []Type{TypeGeneralPurpose, TypeExplore, TypePlan} {
		if !v.IsValid() {
			t.Errorf("%s.IsValid() = false, want true", v)
		}
	}
	for _, v := range []Type{"", "invalid", "GENERAL-PURPOSE"} {
		if v.IsValid() {
			t.Errorf("%q.IsValid() = true, want false", v)
		}
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	terminals := []Status{StatusCompleted, StatusFailed, StatusKilled, StatusOrphaned, StatusPromoted}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("%s.IsTerminal() = false, want true", s)
		}
	}
	if StatusActive.IsTerminal() {
		t.Error("StatusActive.IsTerminal() = true, want false")
	}
}

// TestNewSubSid_FormatAndUniqueness locks in the shape (matching
// session.generateID) and rough uniqueness — two consecutive calls
// with different timestamps produce different IDs.
func TestNewSubSid_FormatAndUniqueness(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 34, 12, 0, time.UTC)
	id := newSubSid(now)

	// Shape: "20260601-103412-<6hex>" — 15+1+6 = 22 chars.
	if len(id) != 22 {
		t.Errorf("sub-sid length = %d, want 22 — got %q", len(id), id)
	}
	if !strings.HasPrefix(id, "20260601-103412-") {
		t.Errorf("sub-sid missing timestamp prefix: %q", id)
	}

	// Different timestamps disambiguate deterministically — the format is
	// sortable at second resolution, so lexical order == creation order.
	if a, b := newSubSid(now), newSubSid(now.Add(time.Second)); a == b {
		t.Errorf("different timestamps produced identical IDs: %q", a)
	}

	// Same-timestamp uniqueness rests entirely on the 24-bit (6 hex)
	// random suffix. That is NOT collision-free at scale: over 1000 draws
	// the birthday probability of *some* collision is ~3%
	// (1000²/(2·2²⁴) ≈ 0.03) — so asserting zero collisions made this
	// test flake ~1 run in 34 (the original "~6e-4" comment miscalculated
	// it). What we CAN assert robustly is that the RNG is healthy: a
	// working suffix yields ~1000 distinct IDs; a degenerate one
	// (constant / all-zero) yields a handful. Require ≥990 distinct —
	// tripping that needs ≥11 collisions (P ≈ 1e-15), while a broken RNG
	// fails immediately. Production is unaffected: real subagent fan-out
	// is far below 1000 sub-sids/second, and the timestamp disambiguates
	// across seconds.
	const draws = 1000
	seen := make(map[string]bool, draws)
	for range draws {
		seen[newSubSid(now)] = true
	}
	if distinct := len(seen); distinct < 990 {
		t.Errorf("only %d/%d distinct sub-sids at a fixed timestamp — random suffix looks degenerate", distinct, draws)
	}
}
