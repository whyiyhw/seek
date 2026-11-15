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

	// Shape: "20260601-103412-<16hex>" — 15+1+16 = 32 chars.
	if len(id) != 32 {
		t.Errorf("sub-sid length = %d, want 32 — got %q", len(id), id)
	}
	if !strings.HasPrefix(id, "20260601-103412-") {
		t.Errorf("sub-sid missing timestamp prefix: %q", id)
	}

	// Different timestamps disambiguate deterministically — the format is
	// sortable at second resolution, so lexical order == creation order.
	if a, b := newSubSid(now), newSubSid(now.Add(time.Second)); a == b {
		t.Errorf("different timestamps produced identical IDs: %q", a)
	}

	// Same-timestamp uniqueness rests on the 64-bit (16 hex) random
	// suffix. Over 1000 draws the birthday probability of *some*
	// collision is 1000²/(2·2⁶⁴) ≈ 2.7e-14 — asserting zero collisions
	// is safe; a degenerate RNG (constant / all-zero suffix) still fails
	// immediately. (The 24-bit predecessor needed a relaxed ≥990 assert
	// here — see docs/pitfalls.md "birthday-paradox flake trap".)
	const draws = 1000
	seen := make(map[string]bool, draws)
	for range draws {
		s := newSubSid(now)
		if seen[s] {
			t.Errorf("duplicate sub-sid: %s", s)
		}
		seen[s] = true
	}
}
