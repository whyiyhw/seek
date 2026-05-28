package routines

import (
	"strings"
	"testing"
	"time"
)

// TestParseSchedule_HappyAliases pins each name alias to its
// expected duration. Changes here are user-visible and break
// existing jobs.jsonl entries — bump cautiously.
func TestParseSchedule_HappyAliases(t *testing.T) {
	cases := map[string]time.Duration{
		"@hourly":   1 * time.Hour,
		"@daily":    24 * time.Hour,
		"@midnight": 24 * time.Hour,
		"@weekly":   7 * 24 * time.Hour,
	}
	for in, want := range cases {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("ParseSchedule(%q) err = %v", in, err)
			continue
		}
		if s.Every != want {
			t.Errorf("ParseSchedule(%q).Every = %v, want %v", in, s.Every, want)
		}
		if s.Raw != in {
			t.Errorf("ParseSchedule(%q).Raw = %q, want %q", in, s.Raw, in)
		}
	}
}

// TestParseSchedule_EveryWithGoDuration: @every accepts every
// shape Go's time.ParseDuration does, AS LONG AS the result
// >= MinSchedule.
func TestParseSchedule_EveryWithGoDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"@every 1m":      1 * time.Minute,
		"@every 5m":      5 * time.Minute,
		"@every 30m":     30 * time.Minute,
		"@every 1h":      1 * time.Hour,
		"@every 2h30m":   2*time.Hour + 30*time.Minute,
		"@every 24h":     24 * time.Hour,
		"@every 168h":    168 * time.Hour,
	}
	for in, want := range cases {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("ParseSchedule(%q) err = %v", in, err)
			continue
		}
		if s.Every != want {
			t.Errorf("ParseSchedule(%q).Every = %v, want %v", in, s.Every, want)
		}
	}
}

// TestParseSchedule_BelowMinimumRejected: anything < 1m gets a
// clear error mentioning the floor. Load-bearing for the
// feature-routines.md §8 "CPU pathology" risk.
func TestParseSchedule_BelowMinimumRejected(t *testing.T) {
	for _, in := range []string{
		"@every 1ns",
		"@every 100ms",
		"@every 30s",
		"@every 59s",
	} {
		_, err := ParseSchedule(in)
		if err == nil {
			t.Errorf("ParseSchedule(%q) should reject below-minimum", in)
			continue
		}
		if !strings.Contains(err.Error(), "minimum is") {
			t.Errorf("ParseSchedule(%q) error should mention minimum: %v", in, err)
		}
	}
}

// TestParseSchedule_EmptyAndWhitespace: empty / whitespace
// input is rejected with the standard hint listing all valid
// forms. Discoverability matters — users hit this when they
// pass --at "" by accident.
func TestParseSchedule_EmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "  ", "\t\n"} {
		_, err := ParseSchedule(in)
		if err == nil {
			t.Errorf("ParseSchedule(%q) should reject empty", in)
			continue
		}
		if !strings.Contains(err.Error(), "@every") {
			t.Errorf("error should hint valid forms: %v", err)
		}
	}
}

// TestParseSchedule_FiveFieldCronGivesUsefulHint: users
// reaching for `* * * * *` shouldn't see a generic "unknown" —
// they should learn that 5-field is on the roadmap, not
// permanently rejected.
func TestParseSchedule_FiveFieldCronGivesUsefulHint(t *testing.T) {
	for _, in := range []string{
		"* * * * *",
		"0 9 * * *",
		"*/5 * * * *",
	} {
		_, err := ParseSchedule(in)
		if err == nil {
			t.Errorf("ParseSchedule(%q) should reject 5-field for now", in)
			continue
		}
		if !strings.Contains(err.Error(), "5-field") {
			t.Errorf("5-field error should name the form: %v", err)
		}
		if !strings.Contains(err.Error(), "v0.6.x dot") {
			t.Errorf("5-field error should point at the roadmap entry: %v", err)
		}
	}
}

// TestParseSchedule_UnknownFormat: anything else gets the
// "unknown schedule" hint.
func TestParseSchedule_UnknownFormat(t *testing.T) {
	for _, in := range []string{
		"every 5m",        // missing @
		"@yearly",         // not yet supported
		"@every",          // missing duration
		"@every notvalid", // bad Go duration
	} {
		_, err := ParseSchedule(in)
		if err == nil {
			t.Errorf("ParseSchedule(%q) should error", in)
		}
	}
}

// TestNext_AddsEvery: Next is simply after + Every for @every-
// class schedules. The Add returns UTC regardless of caller's
// timezone so persisted next_run_at fields are stable.
func TestNext_AddsEvery(t *testing.T) {
	s, err := ParseSchedule("@every 1h")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	next := s.Next(base)
	want := base.Add(1 * time.Hour)
	if !next.Equal(want) {
		t.Errorf("Next() = %v, want %v", next, want)
	}
	if next.Location() != time.UTC {
		t.Errorf("Next() location = %v, want UTC", next.Location())
	}
}

// TestNext_StrictlyAfter: passing the just-fired moment gives
// the NEXT slot, not the same one. This matters when tick
// loops "fire then schedule next" — without strict-after the
// engine could fire the same slot twice.
func TestNext_StrictlyAfter(t *testing.T) {
	s, _ := ParseSchedule("@every 30m")
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	first := s.Next(base)
	second := s.Next(first)
	if !second.After(first) {
		t.Errorf("Next(first) should be strictly after first; got %v after %v", second, first)
	}
}

// TestNext_ZeroScheduleReturnsZero: defensive — a Schedule
// loaded from a malformed jsonl entry might be uninitialised.
// Next must not panic.
func TestNext_ZeroScheduleReturnsZero(t *testing.T) {
	var s Schedule
	if got := s.Next(time.Now()); !got.IsZero() {
		t.Errorf("Next() on zero Schedule = %v, want zero", got)
	}
}

func TestIsZero(t *testing.T) {
	var z Schedule
	if !z.IsZero() {
		t.Error("zero Schedule should IsZero")
	}
	s, _ := ParseSchedule("@hourly")
	if s.IsZero() {
		t.Error("parsed Schedule should not IsZero")
	}
}
