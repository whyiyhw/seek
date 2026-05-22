package memory

import (
	"math"
	"os"
	"strconv"
	"time"
)

// defaultHalfLife is the decay half-life used when SEEK_MEMORY_HALFLIFE_DAYS
// is unset. Picked at 30 days per PRD §6 — long enough that "used last month"
// stays salient, short enough that single-recall remnants from a year ago
// don't crowd the index.
const defaultHalfLife = 30 * 24 * time.Hour

// stalenessThreshold is the score below which an entry is considered too
// cold to surface in the M-index. PRD §6 calibration: at half_life=30d,
// a 30-day-old entry recalled ≥1 times sits at ~1.0 (kept), a 60-day-old
// entry recalled once sits at ~0.27 (stale).
const stalenessThreshold = 0.5

// gracePeriod blocks GC evaluation for entries younger than this. PRD §6:
// fresh entries with recall_count=0 should not be evaluated for staleness;
// they haven't had a chance to be useful yet.
const gracePeriod = 7 * 24 * time.Hour

// halfLifeFromEnv resolves SEEK_MEMORY_HALFLIFE_DAYS to a Duration,
// falling back to defaultHalfLife on absent / unparseable / non-positive.
// Misconfiguration silently degrades rather than failing the session —
// a broken env var is annoying, but blocking startup over it is worse.
func halfLifeFromEnv() time.Duration {
	v := os.Getenv("SEEK_MEMORY_HALFLIFE_DAYS")
	if v == "" {
		return defaultHalfLife
	}
	days, err := strconv.ParseFloat(v, 64)
	if err != nil || days <= 0 {
		return defaultHalfLife
	}
	return time.Duration(days * float64(24*time.Hour))
}

// Score returns the decay-weighted recall score per PRD §6:
//
//	score = (recall_count + 1) * exp(-(now - last_active_at) / half_life)
//	last_active_at = max(created_at, updated_at, last_recalled_at)
//
// The +1 baseline keeps brand-new entries (recall_count=0) competitive
// with old ones that have been recalled once recently — without it,
// every new entry would score 0 and be marked stale immediately.
//
// Exposed for tests + future telemetry; production callers should use
// RunGC.
func Score(e Entry, now time.Time, halfLife time.Duration) float64 {
	lastActive := e.LastRecalledAt
	if e.UpdatedAt.After(lastActive) {
		lastActive = e.UpdatedAt
	}
	if e.CreatedAt.After(lastActive) {
		lastActive = e.CreatedAt
	}
	age := now.Sub(lastActive)
	if age < 0 {
		age = 0
	}
	decay := math.Exp(-float64(age) / float64(halfLife))
	return float64(e.RecallCount+1) * decay
}

// GCReport summarises what RunGC did. Returned for telemetry (status
// bar / debug log) — callers don't need to read it to make decisions.
type GCReport struct {
	Examined      int // entries the rule was applied to
	Skipped       int // pinned + grace-period (kept untouched)
	MarkedStale   int
	UnmarkedStale int // entries that came back above threshold via recall
	HalfLife      time.Duration
}

// RunGC applies the decay-score rule from PRD §6 to every entry,
// flipping stale based on the current score. Pinned and grace-period
// entries are skipped (the rule explicitly does NOT touch them).
//
// State changes persist before RunGC returns. A no-op pass (no flips)
// does NOT rewrite memory.jsonl — keeping disk I/O proportional to
// actual change is important for prefix-cache stability on the next
// PrePromptHook injection.
//
// halfLife of 0 falls back to defaultHalfLife (configurable via
// SEEK_MEMORY_HALFLIFE_DAYS).
func (p *Project) RunGC(now time.Time) (GCReport, error) {
	halfLife := halfLifeFromEnv()
	report := GCReport{HalfLife: halfLife}

	dirty := false
	for name, e := range p.entries {
		report.Examined++

		if e.Pinned {
			report.Skipped++
			continue
		}
		if now.Sub(e.CreatedAt) < gracePeriod {
			report.Skipped++
			continue
		}

		score := Score(e, now, halfLife)
		wantStale := score < stalenessThreshold
		if wantStale == e.Stale {
			continue
		}

		e.Stale = wantStale
		p.entries[name] = e
		dirty = true
		if wantStale {
			report.MarkedStale++
		} else {
			report.UnmarkedStale++
		}
	}

	if !dirty {
		return report, nil
	}
	return report, p.writeEntries()
}
