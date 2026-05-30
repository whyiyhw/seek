// Package routines implements v5 柱 H — time-axis orchestration.
// Cron jobs (registered via `seek cron create`), one-shot wakeups
// (model-driven via `schedule_wakeup`), and external triggers
// (CI webhooks via file bridge) all share the same Store + tick
// engine + per-platform OS notification adapter.
//
// See docs/prd/feature-routines.md for the full design — in
// particular §3 (data model + signatures) and §5 (integration
// with subagent / session / permission).
//
// Supported schedule syntax:
//
//   - @every <Go duration>  e.g. @every 5m, @every 24h, @every 2h30m
//   - @hourly / @daily / @midnight / @weekly aliases
//   - 5-field cron expression  e.g. */15 * * * *, 0 9 * * 1-5
//   - Store: jobs.jsonl persistence with atomic rewrite (write-
//     tmp-rename, same dance as session.Save).
//   - tick engine: scan jobs.jsonl + triggers/, fork-and-record
//     per due job. Zero daemon (PRD v5.md §2.5).
//
// The package depends ONLY on stdlib + internal/paths. Cron-tool
// surface lives in sibling packages: internal/routinescli (CLI),
// internal/tools/wakeup (LLM tool), internal/tui (status bar
// badge).
package routines

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinSchedule is the floor for @every <duration>. Anything
// shorter would let the OS scheduler (1-minute cadence) miss
// fires regardless, AND it would let a malformed @every 1ns
// burn CPU in a tight tick loop (feature-routines.md §8 risk).
// One minute is also the smallest interval common OS schedulers
// (cron / launchd StartInterval / systemd-timer OnUnitActiveSec)
// support reliably.
const MinSchedule = 1 * time.Minute

// Schedule is the parsed form of a schedule expression.
type Schedule struct {
	// Raw is the original input string ("@every 5m" /
	// "@daily" / "*/15 * * * *" / ...). Kept verbatim so List
	// output round-trips what the user typed.
	Raw string
	// Every is the fixed interval for @every-class schedules.
	// Always >= MinSchedule when Schedule is the result of a
	// successful ParseSchedule. Used only when Cron is nil.
	Every time.Duration
	// Cron holds the parsed 5-field cron expression when the raw
	// input is a cron expression (e.g. "*/15 * * * *"). Non-nil
	// means this is a cron schedule; nil means @every or alias.
	Cron *CronExpr
}

// CronExpr is a parsed 5-field cron expression. Each field is a bitmask
// of allowed values -- a bit set at position N means that N is a valid
// value for the field (e.g. Minute bit 0 = midnight minute 00 is allowed).
// All-zero fields do not occur (parse requires at least one value per field).
type CronExpr struct {
	Minute     uint64 // bits 0-59
	Hour       uint64 // bits 0-23
	DayOfMonth uint64 // bits 1-31
	Month      uint64 // bits 1-12 (1=January)
	DayOfWeek  uint64 // bits 0-6 (0=Sunday)
}

// cronFieldNames maps 3-letter abbreviations to numeric values for
// month and weekday fields. Full names are NOT supported; the
// three-letter form matches standard cron convention.
var (
	monthNamesLower = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4,
		"may": 5, "jun": 6, "jul": 7, "aug": 8,
		"sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	weekdayNamesLower = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3,
		"thu": 4, "fri": 5, "sat": 6,
	}
)

// parseCronExpr parses a 5-field cron expression ("minute hour
// day-of-month month day-of-week") into a CronExpr. Each field
// supports: * (any), */N (every N), N-M (range), N-M/S (range
// with step), or comma-separated lists of the above. Month and
// weekday fields accept 3-letter English abbreviations (jan, mon,
// etc.).
func parseCronExpr(raw string) (*CronExpr, error) {
	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return nil, fmt.Errorf("routines: cron expression must have 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}

	minute, err := parseCronField(fields[0], 0, 59, nil)
	if err != nil {
		return nil, fmt.Errorf("routines: bad minute field %q: %v", fields[0], err)
	}
	hour, err := parseCronField(fields[1], 0, 23, nil)
	if err != nil {
		return nil, fmt.Errorf("routines: bad hour field %q: %v", fields[1], err)
	}
	dom, err := parseCronField(fields[2], 1, 31, nil)
	if err != nil {
		return nil, fmt.Errorf("routines: bad day-of-month field %q: %v", fields[2], err)
	}
	month, err := parseCronField(fields[3], 1, 12, monthNamesLower)
	if err != nil {
		return nil, fmt.Errorf("routines: bad month field %q: %v", fields[3], err)
	}
	dow, err := parseCronField(fields[4], 0, 6, weekdayNamesLower)
	if err != nil {
		return nil, fmt.Errorf("routines: bad day-of-week field %q: %v", fields[4], err)
	}

	return &CronExpr{
		Minute:     minute,
		Hour:       hour,
		DayOfMonth: dom,
		Month:      month,
		DayOfWeek:  dow,
	}, nil
}

// parseCronField parses a single cron field string (e.g. "*/15",
// "1-5", "1,3,5", "jan-dec", "mon-fri") into a bitmask. min/max
// define the valid range. names is an optional lookup for 3-letter
// abbreviations (non-nil for month and weekday fields).
func parseCronField(field string, min, max int, names map[string]int) (uint64, error) {
	parts := strings.Split(field, ",")
	var mask uint64
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("empty field part")
		}
		m, err := parseCronItem(part, min, max, names)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	if mask == 0 {
		return 0, fmt.Errorf("no valid values in field")
	}
	return mask, nil
}

// parseCronItem parses a single item (one element of a comma-
// separated list): *, */N, N-M, N-M/S, a bare number, or a name.
func parseCronItem(item string, min, max int, names map[string]int) (uint64, error) {
	// Wildcard: all values in the valid range.
	if item == "*" {
		return cronStep(min, max, 1), nil
	}

	// Check name mapping first (e.g. "jan", "mon-fri").
	lower := strings.ToLower(item)
	if names != nil {
		if v, ok := names[lower]; ok {
			if v < min || v > max {
				return 0, fmt.Errorf("name value %d out of range [%d-%d]", v, min, max)
			}
			return 1 << uint(v), nil
		}
		// Name range: "jan-dec" or "mon-fri".
		if strings.Contains(lower, "-") {
			if m, err := parseCronNameRange(lower, names, min, max); err == nil {
				return m, nil
			}
		}
	}

	// */step pattern.
	if strings.HasPrefix(item, "*/") {
		step, err := strconv.Atoi(item[2:])
		if err != nil || step <= 0 {
			return 0, fmt.Errorf("bad step %q", item[2:])
		}
		return cronStep(min, max, step), nil
	}

	// range/step pattern (e.g. "1-10/3").
	if strings.Contains(item, "/") {
		parts := strings.SplitN(item, "/", 2)
		step, err := strconv.Atoi(parts[1])
		if err != nil || step <= 0 {
			return 0, fmt.Errorf("bad step %q", parts[1])
		}
		rangeMask, err := parseCronRange(parts[0], min, max)
		if err != nil {
			return 0, err
		}
		return applyStep(rangeMask, step), nil
	}

	// range pattern (e.g. "1-5").
	if strings.Contains(item, "-") {
		return parseCronRange(item, min, max)
	}

	// single value (e.g. "15").
	v, err := strconv.Atoi(item)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", item)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("value %d out of range [%d-%d]", v, min, max)
	}
	return 1 << uint(v), nil
}

// parseCronNameRange parses "jan-dec" style month/weekday ranges.
func parseCronNameRange(s string, names map[string]int, min, max int) (uint64, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("not a name range")
	}
	lo, ok := names[parts[0]]
	if !ok {
		return 0, fmt.Errorf("unknown name %q", parts[0])
	}
	hi, ok := names[parts[1]]
	if !ok {
		return 0, fmt.Errorf("unknown name %q", parts[1])
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	var mask uint64
	for i := lo; i <= hi; i++ {
		mask |= 1 << uint(i)
	}
	return mask, nil
}

// cronStep builds a bitmask for every step-th value from min to
// max inclusive (e.g. min=0, max=59, step=15 -> bits 0,15,30,45).
func cronStep(min, max, step int) uint64 {
	var mask uint64
	for i := min; i <= max; i += step {
		mask |= 1 << uint(i)
	}
	return mask
}

// parseCronRange parses a "N-M" range expression.
func parseCronRange(s string, min, max int) (uint64, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("bad range %q", s)
	}
	lo, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("bad range start %q", parts[0])
	}
	hi, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("bad range end %q", parts[1])
	}
	if lo < min || hi > max || lo > hi {
		return 0, fmt.Errorf("range %d-%d out of bounds [%d-%d]", lo, hi, min, max)
	}
	var mask uint64
	for i := lo; i <= hi; i++ {
		mask |= 1 << uint(i)
	}
	return mask, nil
}

// applyStep keeps only every step-th bit set in mask, counting
// from the lowest set bit (index 0). Used for range/step patterns
// like "1-10/3".
func applyStep(mask uint64, step int) uint64 {
	var positions []int
	for i := 0; i < 64; i++ {
		if mask>>uint(i)&1 == 1 {
			positions = append(positions, i)
		}
	}
	var result uint64
	for idx, pos := range positions {
		if idx%step == 0 {
			result |= 1 << uint(pos)
		}
	}
	return result
}

// ParseSchedule decodes a schedule expression to its in-memory
// form. Returned errors are user-facing — they include a hint at
// the valid syntax so CLI users + the model can self-correct.
//
// Supported forms:
//
//	@every <Go duration>  e.g. @every 5m, @every 24h, @every 2h30m
//	@hourly               ≡ @every 1h
//	@daily / @midnight    ≡ @every 24h
//	@weekly               ≡ @every 168h
//	5-field cron          e.g. */15 * * * *, 0 9 * * 1-5
//
// The 5-field cron expression follows the standard POSIX layout:
//
//	minute hour day-of-month month day-of-week
//
// Each field supports * (any), */N (every N), N-M (range),
// N-M/S (range with step), comma-separated lists, and (for month
// and weekday) 3-letter English abbreviations (jan/feb/... and
// sun/mon/...).
func ParseSchedule(raw string) (Schedule, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Schedule{}, errors.New("routines: empty schedule; try `@every 5m` / `@hourly` / `@daily` / `@weekly` or a 5-field cron expression")
	}

	// Fixed-interval aliases.
	if d, ok := aliasInterval(trimmed); ok {
		if d < MinSchedule {
			return Schedule{}, fmt.Errorf("routines: schedule %q resolves to %v; minimum is %v", trimmed, d, MinSchedule)
		}
		return Schedule{Raw: trimmed, Every: d}, nil
	}

	// @every <duration> form.
	const everyPrefix = "@every "
	if strings.HasPrefix(trimmed, everyPrefix) {
		dur, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(trimmed, everyPrefix)))
		if err != nil {
			return Schedule{}, fmt.Errorf("routines: bad @every duration %q: %v (use Go syntax: 30s, 5m, 2h30m, 24h)", trimmed, err)
		}
		if dur < MinSchedule {
			return Schedule{}, fmt.Errorf("routines: schedule %q resolves to %v; minimum is %v", trimmed, dur, MinSchedule)
		}
		return Schedule{Raw: trimmed, Every: dur}, nil
	}

	// Candidate cron expression: has no leading @ and at least
	// 4 spaces (indicating 5+ whitespace-separated fields).
	if !strings.HasPrefix(trimmed, "@") && strings.Count(trimmed, " ") >= 4 {
		cron, err := parseCronExpr(trimmed)
		if err != nil {
			return Schedule{}, fmt.Errorf("routines: %v; try `@every <duration>` instead, or check field syntax (*/N, N-M, N-M/S, comma lists)", err)
		}
		return Schedule{Raw: trimmed, Cron: cron}, nil
	}

	return Schedule{}, fmt.Errorf("routines: unknown schedule %q; try `@every 5m` / `@hourly` / `@daily` / `@weekly` or a 5-field cron expression", trimmed)
}

// aliasInterval maps the named aliases to their duration
// equivalents. Returns ok=false for non-aliases so the caller
// can fall through to @every parsing.
func aliasInterval(s string) (time.Duration, bool) {
	switch s {
	case "@hourly":
		return 1 * time.Hour, true
	case "@daily", "@midnight":
		return 24 * time.Hour, true
	case "@weekly":
		return 7 * 24 * time.Hour, true
	}
	return 0, false
}

// Next returns the next fire time strictly after `after`. For
// @every-class schedules this is simply after + Every. For
// cron schedules it finds the next time all five fields match.
// The strictly-after semantics matter when callers loop forward
// after a fire: passing the just-fired moment in and getting
// the next slot back, never the same one again.
//
// An uninitialised Schedule (Every <= 0, Cron == nil) returns
// the zero time; callers shouldn't reach Next on an unparsed
// Schedule but the guard avoids panicking on misuse.
func (s Schedule) Next(after time.Time) time.Time {
	if s.Cron != nil {
		return s.Cron.Next(after)
	}
	if s.Every <= 0 {
		return time.Time{}
	}
	return after.Add(s.Every).UTC()
}

// IsZero reports whether Schedule is the zero value. Used by
// Store to distinguish "never parsed" from "parsed but empty
// after JSON load".
func (s Schedule) IsZero() bool { return s.Every <= 0 && s.Cron == nil && s.Raw == "" }

// Next returns the next time strictly after `after` when all
// five fields of this expression match. Uses a minute-by-minute
// forward scan capped at ~4 years (safety bound — a valid
// expression will match far sooner). Returns zero time on nil
// receiver (defensive guard).
func (c *CronExpr) Next(after time.Time) time.Time {
	if c == nil {
		return time.Time{}
	}
	t := after.Truncate(time.Minute).Add(time.Minute)

	maxAttempts := 366 * 24 * 60 * 4 // ~2M minutes, ~4 years
	for i := 0; i < maxAttempts; i++ {
		if c.match(t) {
			return t.UTC()
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// match reports whether the time t satisfies all five field
// constraints in this cron expression.
func (c *CronExpr) match(t time.Time) bool {
	return hasBit(c.Minute, t.Minute()) &&
		hasBit(c.Hour, t.Hour()) &&
		hasBit(c.DayOfMonth, t.Day()) &&
		hasBit(c.Month, int(t.Month())) &&
		hasBit(c.DayOfWeek, int(t.Weekday()))
}

// hasBit reports whether bit n is set in mask.
func hasBit(mask uint64, n int) bool {
	if n < 0 || n > 63 {
		return false
	}
	return mask>>uint(n)&1 == 1
}
