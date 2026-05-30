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
// they should learn that 5-field cron is now supported.
func TestParseSchedule_FiveFieldCronGivesUsefulHint(t *testing.T) {
	for _, in := range []string{
		"* * * * *",
		"0 9 * * *",
		"*/5 * * * *",
	} {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("ParseSchedule(%q) should now succeed, got err: %v", in, err)
			continue
		}
		if s.Cron == nil {
			t.Errorf("ParseSchedule(%q).Cron is nil, want parsed cron expression", in)
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
	cronS, _ := ParseSchedule("*/15 * * * *")
	if cronS.IsZero() {
		t.Error("parsed cron Schedule should not IsZero")
	}
}

// --- 5-field cron expression parsing ---

// TestParseCron_ValidExpressions verifies that standard 5-field
// cron expressions parse without error.
func TestParseCron_ValidExpressions(t *testing.T) {
	cases := []string{
		"* * * * *",
		"0 9 * * *",
		"30 6 * * 1-5",
		"0 0 1 1 *",
		"15 14 1 * *",
		"0 22 * * 1-5",
		"*/15 * * * *",
		"0 */2 * * *",
		"0 0 */3 * *",
		"1,2,3 9 * * *",
		"0-30 9 * * *",
		"1-10/3 9 * * *",
		"0 9 1-15 */2 1-5",
	}
	for _, in := range cases {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("ParseSchedule(%q) unexpected err: %v", in, err)
			continue
		}
		if s.Cron == nil {
			t.Errorf("ParseSchedule(%q).Cron is nil, want non-nil", in)
		}
		if s.Raw != in {
			t.Errorf("ParseSchedule(%q).Raw = %q, want %q", in, s.Raw, in)
		}
	}
}

// TestParseCron_WithNames verifies 3-letter month and weekday
// abbreviations are accepted in month and weekday fields.
func TestParseCron_WithNames(t *testing.T) {
	cases := []struct {
		in  string
		msg string
	}{
		{"0 0 * jan *", "single month name"},
		{"0 0 * jan-dec *", "month name range"},
		{"0 9 * * mon-fri", "weekday name range"},
		{"0 0 * jun,dec *", "comma month names"},
		{"30 6 * * mon,wed,fri", "comma weekday names"},
	}
	for _, tc := range cases {
		s, err := ParseSchedule(tc.in)
		if err != nil {
			t.Errorf("ParseSchedule(%q) [%s] err: %v", tc.in, tc.msg, err)
			continue
		}
		if s.Cron == nil {
			t.Errorf("ParseSchedule(%q) [%s] Cron is nil", tc.in, tc.msg)
		}
	}
}

// TestParseCron_InvalidExpressions verifies that malformed cron
// expressions are rejected with a clear error.
func TestParseCron_InvalidExpressions(t *testing.T) {
	cases := []string{
		"* * * *",           // 4 fields
		"* * * * * *",       // 6 fields
		"70 * * * *",        // minute out of range
		"* 25 * * *",        // hour out of range
		"* * 32 * *",        // day-of-month out of range
		"* * * 13 *",        // month out of range
		"* * * * 7",         // day-of-week out of range
		"*/* * * * *",       // bad step
		"*/ * * * *",        // empty step
		"a * * * *",         // non-numeric, non-name
	}
	for _, in := range cases {
		_, err := ParseSchedule(in)
		if err == nil {
			t.Errorf("ParseSchedule(%q) should error", in)
			continue
		}
		if !strings.Contains(err.Error(), "routines:") {
			t.Errorf("ParseSchedule(%q) error should mention routines: %v", in, err)
		}
	}
}

// TestCronNext_EveryMinute: `* * * * *` fires on the next minute
// boundary and each minute thereafter.
func TestCronNext_EveryMinute(t *testing.T) {
	s, err := ParseSchedule("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 28, 9, 0, 30, 0, time.UTC)
	first := s.Next(base)
	wantFirst := time.Date(2026, 5, 28, 9, 1, 0, 0, time.UTC)
	if !first.Equal(wantFirst) {
		t.Errorf("Next(%v) = %v, want %v", base, first, wantFirst)
	}
	second := s.Next(first)
	wantSecond := time.Date(2026, 5, 28, 9, 2, 0, 0, time.UTC)
	if !second.Equal(wantSecond) {
		t.Errorf("Next(%v) = %v, want %v", first, second, wantSecond)
	}
}

// TestCronNext_SpecificTime: `30 6 * * *` fires at 06:30 daily.
func TestCronNext_SpecificTime(t *testing.T) {
	s, err := ParseSchedule("30 6 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// Before the time → same day.
	base := time.Date(2026, 5, 28, 5, 0, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2026, 5, 28, 6, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
	// After the time → next day.
	base2 := time.Date(2026, 5, 28, 7, 0, 0, 0, time.UTC)
	next2 := s.Next(base2)
	want2 := time.Date(2026, 5, 29, 6, 30, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Errorf("Next(%v) = %v, want %v", base2, next2, want2)
	}
}

// TestCronNext_StepEvery15Minutes: `*/15 * * * *` fires at
// minutes 0,15,30,45 of each hour.
func TestCronNext_StepEvery15Minutes(t *testing.T) {
	s, err := ParseSchedule("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	// Starting from minute 3 → next is minute 15.
	base := time.Date(2026, 5, 28, 9, 3, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2026, 5, 28, 9, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
	// From minute 46 → rolls to next hour at minute 0.
	base2 := time.Date(2026, 5, 28, 9, 46, 0, 0, time.UTC)
	next2 := s.Next(base2)
	want2 := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Errorf("Next(%v) = %v, want %v", base2, next2, want2)
	}
}

// TestCronNext_RolloverHour: `0 23 * * *` fires at 23:00 daily;
// after 23:00 the next fire is the following day at 23:00.
func TestCronNext_RolloverHour(t *testing.T) {
	s, err := ParseSchedule("0 23 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// Just before → same day.
	base := time.Date(2026, 5, 28, 22, 30, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2026, 5, 28, 23, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
	// After → next day.
	base2 := time.Date(2026, 5, 28, 23, 30, 0, 0, time.UTC)
	next2 := s.Next(base2)
	want2 := time.Date(2026, 5, 29, 23, 0, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Errorf("Next(%v) = %v, want %v", base2, next2, want2)
	}
}

// TestCronNext_WeekdayOnly: `0 9 * * 1-5` fires at 09:00
// Mon-Fri only. Saturday at 09:00 should NOT fire — next is
// Monday.
func TestCronNext_WeekdayOnly(t *testing.T) {
	s, err := ParseSchedule("0 9 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	// Saturday 2026-05-30 is a Saturday (weekday 6).
	sat := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	next := s.Next(sat)
	// Next Monday 2026-06-01 at 09:00.
	want := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(Saturday) = %v, want %v", next, want)
	}
	// Monday at 09:30 should fire next day (Tuesday).
	mon := time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC)
	next2 := s.Next(mon)
	want2 := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Errorf("Next(Monday 09:30) = %v, want %v", next2, want2)
	}
}

// TestCronNext_MonthBoundary: `0 0 1 * *` fires on the 1st of
// each month at midnight. After May 1, next is June 1.
func TestCronNext_MonthBoundary(t *testing.T) {
	s, err := ParseSchedule("0 0 1 * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
}

// TestCronNext_YearBoundary: `0 0 1 1 *` fires on Jan 1 at
// midnight. After July, next is Jan 1 of next year.
func TestCronNext_YearBoundary(t *testing.T) {
	s, err := ParseSchedule("0 0 1 1 *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
}

// TestCronNext_StrictlyAfter: passing the exact fire moment
// gives the NEXT slot, never the same one.
func TestCronNext_StrictlyAfter(t *testing.T) {
	s, err := ParseSchedule("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	fire := time.Date(2026, 5, 28, 9, 15, 0, 0, time.UTC)
	next := s.Next(fire)
	want := time.Date(2026, 5, 28, 9, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(fire=%v) = %v, want next slot %v", fire, next, want)
	}
}

// TestCronNext_CommaList: `15,30,45 9 * * *` fires at 09:15,
// 09:30, 09:45 each day.
func TestCronNext_CommaList(t *testing.T) {
	s, err := ParseSchedule("15,30,45 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	first := s.Next(base)
	want := time.Date(2026, 5, 28, 9, 15, 0, 0, time.UTC)
	if !first.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, first, want)
	}
	// After 09:45 rolls to next day 09:15.
	after := time.Date(2026, 5, 28, 9, 50, 0, 0, time.UTC)
	next := s.Next(after)
	want2 := time.Date(2026, 5, 29, 9, 15, 0, 0, time.UTC)
	if !next.Equal(want2) {
		t.Errorf("Next(%v) = %v, want %v", after, next, want2)
	}
}

// TestCronNext_RangeStep: `1-10/3 9 * * *` fires at 09:01,
// 09:04, 09:07, 09:10.
func TestCronNext_RangeStep(t *testing.T) {
	s, err := ParseSchedule("1-10/3 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2026, 5, 28, 9, 1, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
	// After last in range (10), rolls to next day.
	after := time.Date(2026, 5, 28, 9, 11, 0, 0, time.UTC)
	next2 := s.Next(after)
	want2 := time.Date(2026, 5, 29, 9, 1, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Errorf("Next(%v) = %v, want %v", after, next2, want2)
	}
}

// TestCronNext_ZeroCronExpr: a nil CronExpr on a Schedule should
// return zero (defensive guard).
func TestCronNext_ZeroCronExpr(t *testing.T) {
	var c *CronExpr
	got := c.Next(time.Now())
	if !got.IsZero() {
		t.Errorf("nil CronExpr.Next() = %v, want zero", got)
	}
}

// TestParseCron_FieldOrder: verify that the 5 fields are parsed
// in the correct order (minute, hour, dom, month, dow).
func TestParseCron_FieldOrder(t *testing.T) {
	s, err := ParseSchedule("30 14 15 6 3")
	if err != nil {
		t.Fatal(err)
	}
	// Should fire on June 15 at 14:30, which is a Monday in 2026.
	// Let's verify: June 15, 2026 is a Monday (weekday 1), but
	// our expression requires weekday 3 (Wednesday). So it won't
	// match June 15 — find next match.
	// Instead, verify the bitmasks directly.
	if s.Cron == nil {
		t.Fatal("Cron is nil")
	}
	if !hasBit(s.Cron.Minute, 30) {
		t.Error("minute 30 should be set")
	}
	if hasBit(s.Cron.Minute, 0) {
		t.Error("minute 0 should not be set")
	}
	if !hasBit(s.Cron.Hour, 14) {
		t.Error("hour 14 should be set")
	}
	if !hasBit(s.Cron.DayOfMonth, 15) {
		t.Error("day 15 should be set")
	}
	if !hasBit(s.Cron.Month, 6) {
		t.Error("month 6 (June) should be set")
	}
	if !hasBit(s.Cron.DayOfWeek, 3) {
		t.Error("weekday 3 (Wednesday) should be set")
	}
}
