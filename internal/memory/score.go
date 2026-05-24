package memory

import (
	"fmt"
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

// archiveThreshold is the second cliff from PRD §6: an entry that has
// been stale AND scored below this for archiveStalePersistence gets
// moved to archived.jsonl and removed from the active set. At half_life=
// 30d, score=0.1 corresponds roughly to "single-recall + ~100 days
// idle" — by then the entry hasn't proven its keep.
const archiveThreshold = 0.1

// archiveStalePersistence is the "must have been continuously stale for
// at least this long" gate. PRD §6 sets it at 60 days so a short cold
// spell doesn't bin an entry that just hasn't been needed lately.
const archiveStalePersistence = 60 * 24 * time.Hour

// gracePeriod blocks GC evaluation for entries younger than this. PRD §6:
// fresh entries with recall_count=0 should not be evaluated for staleness;
// they haven't had a chance to be useful yet.
const gracePeriod = 7 * 24 * time.Hour

// autoSourcedGracePeriod is the extended grace period for AutoSourced
// entries. 30 days gives users a reasonable window to /distill-review
// new observations before GC acts on them. After the grace period,
// auto-sourced entries participate in normal score-based GC — an entry
// the user never reviewed and never recalled will naturally decay out,
// preventing the unbounded growth that a hard exemption would cause.
const autoSourcedGracePeriod = 30 * 24 * time.Hour

// HalfLifeFromEnv resolves SEEK_MEMORY_HALFLIFE_DAYS to a Duration,
// falling back to defaultHalfLife on absent / unparseable / non-positive.
// Misconfiguration silently degrades rather than failing the session —
// a broken env var is annoying, but blocking startup over it is worse.
func HalfLifeFromEnv() time.Duration {
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
	Archived      int // moved from memory.jsonl → archived.jsonl
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
	p.mu.Lock()
	defer p.mu.Unlock()
	halfLife := HalfLifeFromEnv()
	report := GCReport{HalfLife: halfLife}

	var toArchive []Entry
	dirty := false
	for name, e := range p.entries {
		report.Examined++

		if e.Pinned {
			report.Skipped++
			continue
		}
		// AutoSourced entries get an extended grace period (30 days)
		// instead of a hard exemption. After that they participate in
		// normal score-based GC — unreviewed entries the model never
		// recalls will naturally decay out, preventing unbounded growth.
		if e.AutoSourced && now.Sub(e.CreatedAt) < autoSourcedGracePeriod {
			report.Skipped++
			continue
		}
		if now.Sub(e.CreatedAt) < gracePeriod {
			report.Skipped++
			continue
		}

		// Legacy data backfill: existing stale entries from before the
		// StaleSince field existed have stale=true + StaleSince zero.
		// Treat them as "just marked stale this GC pass" so they get
		// a fresh 60-day archive clock instead of being archived
		// immediately on upgrade.
		if e.Stale && e.StaleSince.IsZero() {
			e.StaleSince = now
			p.entries[name] = e
			dirty = true
		}

		score := Score(e, now, halfLife)
		wantStale := score < stalenessThreshold

		switch {
		case !e.Stale && wantStale:
			e.Stale = true
			e.StaleSince = now
			p.entries[name] = e
			dirty = true
			report.MarkedStale++
		case e.Stale && !wantStale:
			e.Stale = false
			e.StaleSince = time.Time{}
			p.entries[name] = e
			dirty = true
			report.UnmarkedStale++
		case e.Stale && wantStale:
			// Sustained-stale path: eligible for archive once score
			// has fallen below archiveThreshold AND the entry has
			// been continuously stale for archiveStalePersistence.
			if score < archiveThreshold && !e.StaleSince.IsZero() &&
				now.Sub(e.StaleSince) >= archiveStalePersistence {
				toArchive = append(toArchive, e)
			}
		}
	}

	// Archive happens after the iteration: appending to archived.jsonl
	// then removing from the live entries+order is a two-step write,
	// so we batch all archives + persist in one writeEntries pass.
	for _, e := range toArchive {
		if err := p.appendArchive(e); err != nil {
			// Surface the failure so callers can log it, but keep the
			// entry in active set rather than deleting without a
			// durable archive copy.
			return report, fmt.Errorf("memory: archive %q: %w", e.Name, err)
		}
		delete(p.entries, e.Name)
		for i, n := range p.order {
			if n == e.Name {
				p.order = append(p.order[:i], p.order[i+1:]...)
				break
			}
		}
		report.Archived++
		dirty = true
	}

	if !dirty {
		return report, nil
	}
	return report, p.writeEntries()
}
