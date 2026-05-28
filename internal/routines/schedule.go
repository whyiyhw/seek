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
// MVP scope:
//
//   - Schedule parser: @every <Go duration> + @hourly / @daily /
//     @midnight / @weekly aliases. 5-field cron (`* * * * *`) is
//     a v0.6.x dot release candidate; rejected here with a clear
//     hint.
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

// Schedule is the parsed form of a schedule expression. Today
// only Every is populated; if/when a 5-field cron parser lands,
// it'll add fields here (e.g. CronFields []int).
type Schedule struct {
	// Raw is the original input string ("@every 5m" /
	// "@daily" / ...). Kept verbatim so List output round-trips
	// what the user typed.
	Raw string
	// Every is the fixed interval for @every-class schedules.
	// Always >= MinSchedule when Schedule is the result of a
	// successful ParseSchedule.
	Every time.Duration
}

// ParseSchedule decodes a schedule expression to its in-memory
// form. Returned errors are user-facing — they include a hint at
// the valid syntax so CLI users + the model can self-correct.
//
// Supported forms (MVP):
//
//	@every <Go duration>  e.g. @every 5m, @every 24h, @every 2h30m
//	@hourly               ≡ @every 1h
//	@daily / @midnight    ≡ @every 24h
//	@weekly               ≡ @every 168h
//
// 5-field cron (`* * * * *`) is detected and rejected with a
// pointer at the v0.6.x dot release roadmap, so users who
// reach for it get a clear "not yet" rather than a confusing
// parse error.
func ParseSchedule(raw string) (Schedule, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Schedule{}, errors.New("routines: empty schedule; try `@every 5m` / `@hourly` / `@daily` / `@weekly`")
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

	// 5-field cron detection. Heuristic: more than two whitespace-
	// separated fields and no leading `@`. We don't need to be
	// precise — a precise check requires the parser we don't have
	// yet. Catching the common `* * * * *` / `0 9 * * *` shapes is
	// enough to hand the user a useful pointer rather than a
	// generic "unknown".
	if !strings.HasPrefix(trimmed, "@") && strings.Count(trimmed, " ") >= 2 {
		return Schedule{}, fmt.Errorf("routines: 5-field cron (%q) not supported in MVP — use `@every <duration>` for now (5-field landed v0.6.x dot)", trimmed)
	}

	return Schedule{}, fmt.Errorf("routines: unknown schedule %q; try `@every 5m` / `@hourly` / `@daily` / `@weekly`", trimmed)
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
// @every-class schedules this is simply after + Every. The
// strictly-after semantics matter when callers loop forward
// after a fire: passing the just-fired moment in and getting
// the next slot back, never the same one again.
//
// Zero Every (uninitialised Schedule) returns the zero time;
// callers shouldn't reach Next on an unparsed Schedule but the
// guard avoids panicking on misuse.
func (s Schedule) Next(after time.Time) time.Time {
	if s.Every <= 0 {
		return time.Time{}
	}
	return after.Add(s.Every).UTC()
}

// IsZero reports whether Schedule is the zero value. Used by
// Store to distinguish "never parsed" from "parsed but empty
// after JSON load".
func (s Schedule) IsZero() bool { return s.Every <= 0 && s.Raw == "" }
