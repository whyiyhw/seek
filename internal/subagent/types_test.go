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

	// 1000 IDs at the same timestamp must all be unique (collision
	// probability for 24 random bits is ~6e-4 over 1000 draws,
	// well below flake threshold).
	seen := make(map[string]bool, 1000)
	for range 1000 {
		s := newSubSid(now)
		if seen[s] {
			t.Errorf("duplicate sub-sid: %s", s)
		}
		seen[s] = true
	}
}
