package routines

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Default GC bounds — chosen so a default-config install never
// fills the disk and never silently discards diagnostically-useful
// history. The two-axis bound (keep-recent OR max-age) lets a busy
// user (many runs/day) keep more than 30 days IF count < KeepRecent,
// and lets a quiet user (one run/week) drop ancient runs even when
// count is well under KeepRecent. Either trigger is sufficient.
const (
	// DefaultRunsKeepRecent: a power-user running @every 5m for a
	// week produces ~2k runs; 100 covers ~8 hours at that pace,
	// which is what an operator typically wants to grep. Older
	// runs are still on disk via age-cap if needed.
	DefaultRunsKeepRecent = 100

	// DefaultRunsMaxAge: 30 days. Long enough for monthly
	// review patterns; short enough that a runaway @every 1m
	// job (~43k runs/month) won't quietly fill 100MB+ of JSONL.
	DefaultRunsMaxAge = 30 * 24 * time.Hour

	// DefaultMalformedKeepRecent: malformed-trigger forensics are
	// usually "what did the producer send" debugging — 20 recent
	// payloads is plenty. Operators rarely review malformed files
	// older than a session.
	DefaultMalformedKeepRecent = 20

	// DefaultMalformedMaxAge: 14 days. Short by design — a
	// quarantined payload that survived a fortnight without
	// inspection isn't going to be inspected.
	DefaultMalformedMaxAge = 14 * 24 * time.Hour
)

// gcEntry holds a directory entry with mtime cached, so sort
// doesn't restat every comparison. Pre-filtered by extension.
type gcEntry struct {
	name  string // basename, e.g. "20260528-100000-abc.jsonl"
	path  string // absolute path
	mtime time.Time
}

// gcByAgeAndCount enforces a two-axis retention policy on dir:
// keep the keepRecent most-recent entries; additionally drop any
// entry older than now-maxAge. Operates only on plain files whose
// name ends in suffix — other entries (subdirs, symlinks, lock
// files in a shared directory) are skipped.
//
// Returns the count of files actually removed plus a (possibly
// empty) joined error of per-file failures. A failure on one file
// does NOT abort the sweep — partial cleanup is better than no
// cleanup, and the caller (Tick) logs errors but doesn't fail.
//
// keepRecent <= 0 disables the count axis (only age applies).
// maxAge == 0 disables the age axis (only count applies). Setting
// both to disabled is a no-op (returns 0, nil) — callers wanting
// to disable GC entirely should not call this function.
//
// Concurrency: caller must hold tick.lock (or otherwise guarantee
// no concurrent writer is producing new files in dir mid-sweep).
// Without that, a freshly-created file could be tagged as
// "older than the keepRecent cutoff" and deleted milliseconds
// after the writer closed it. Tick holds tick.lock; trigger
// processing runs inside that same lock.
func gcByAgeAndCount(dir, suffix string, keepRecent int, maxAge time.Duration, now time.Time) (int, error) {
	if keepRecent <= 0 && maxAge <= 0 {
		return 0, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Directory missing = nothing to GC. Tick may run
			// before any cron job has fired (runs/ dir not yet
			// created); that's normal, not an error.
			return 0, nil
		}
		return 0, fmt.Errorf("routines: gc readdir %s: %w", dir, err)
	}

	// Filter to plain files matching suffix. We use d.Info() for
	// mtime since ReadDir returns DirEntry without it.
	filtered := make([]gcEntry, 0, len(entries))
	for _, d := range entries {
		if !d.Type().IsRegular() {
			continue // skip subdirs (e.g. .malformed/), symlinks
		}
		name := d.Name()
		if !strings.HasSuffix(name, suffix) {
			continue // skip e.g. <job>.lock files mixed in runsDir
		}
		info, err := d.Info()
		if err != nil {
			// Race with concurrent unlink — d existed at ReadDir
			// but disappeared before Info. Skip silently; the
			// file isn't there to GC anyway.
			continue
		}
		filtered = append(filtered, gcEntry{
			name:  name,
			path:  filepath.Join(dir, name),
			mtime: info.ModTime(),
		})
	}

	if len(filtered) == 0 {
		return 0, nil
	}

	// Sort by mtime DESCENDING (newest first). The "keep recent"
	// axis is now a simple "keep the first keepRecent of this
	// list"; the "max age" axis is a per-entry threshold check.
	slices.SortFunc(filtered, func(a, b gcEntry) int {
		if a.mtime.After(b.mtime) {
			return -1
		}
		if a.mtime.Before(b.mtime) {
			return 1
		}
		// Same mtime (1-second filesystem resolution can collide
		// on a fast tick): tiebreak by name DESC so the
		// lexicographically-greater (later id) wins. Run IDs
		// embed a timestamp so this approximates "newer first"
		// for files that share mtime.
		if a.name > b.name {
			return -1
		}
		if a.name < b.name {
			return 1
		}
		return 0
	})

	removed := 0
	var errs []error
	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = now.Add(-maxAge)
	}
	for i, e := range filtered {
		drop := false
		if keepRecent > 0 && i >= keepRecent {
			drop = true
		}
		if !drop && maxAge > 0 && e.mtime.Before(cutoff) {
			drop = true
		}
		if !drop {
			continue
		}
		if err := os.Remove(e.path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Lost a race with someone else (manual rm,
				// another GC pass). Not our problem.
				continue
			}
			errs = append(errs, fmt.Errorf("remove %s: %w", e.path, err))
			continue
		}
		removed++
	}
	if len(errs) > 0 {
		return removed, errors.Join(errs...)
	}
	return removed, nil
}

// GCRunsOptions controls the runs/<id>.jsonl sweep. Zero values
// mean "use the package default"; pass -1 to KeepRecent to disable
// the count axis (age-only). Pass 0 to MaxAge to disable the age
// axis (count-only). Disabling both via SetDefault zero is a
// caller error — call GCRuns only when you intend to GC.
type GCRunsOptions struct {
	Dir        string        // required
	KeepRecent int           // 0 → DefaultRunsKeepRecent; -1 → disabled
	MaxAge     time.Duration // 0 → DefaultRunsMaxAge; -1 → disabled
	Now        time.Time     // 0 → time.Now()
}

// GCRuns deletes old runs/<run-id>.jsonl files per the two-axis
// policy in gcByAgeAndCount. Filters by `.jsonl` suffix so
// per-job `<name>.lock` files (live advisory locks in the same
// directory) are NEVER touched — deleting a held lock would not
// unlock anything but would re-create it under whoever holds it
// next, and deleting an unheld lock file is harmless but
// pointless.
//
// Best-effort: returns the count of files actually removed plus
// an error summarising per-file failures. The caller (Tick) is
// expected to log + continue.
func GCRuns(opts GCRunsOptions) (int, error) {
	if opts.Dir == "" {
		return 0, errors.New("routines: GCRuns: Dir required")
	}
	keep := opts.KeepRecent
	switch {
	case keep == 0:
		keep = DefaultRunsKeepRecent
	case keep < 0:
		keep = 0 // disabled
	}
	maxAge := opts.MaxAge
	switch {
	case maxAge == 0:
		maxAge = DefaultRunsMaxAge
	case maxAge < 0:
		maxAge = 0 // disabled
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	return gcByAgeAndCount(opts.Dir, ".jsonl", keep, maxAge, now)
}

// GCMalformedOptions mirrors GCRunsOptions for the
// triggers/.malformed/<trigger-id>.json sweep.
type GCMalformedOptions struct {
	Dir        string
	KeepRecent int
	MaxAge     time.Duration
	Now        time.Time
}

// GCMalformedTriggers deletes quarantined trigger files older
// than the bound. Smaller defaults than runs/ because malformed
// payloads are forensic, not historical — a 2-week-old malformed
// payload nobody investigated isn't going to be investigated.
func GCMalformedTriggers(opts GCMalformedOptions) (int, error) {
	if opts.Dir == "" {
		return 0, errors.New("routines: GCMalformedTriggers: Dir required")
	}
	keep := opts.KeepRecent
	switch {
	case keep == 0:
		keep = DefaultMalformedKeepRecent
	case keep < 0:
		keep = 0
	}
	maxAge := opts.MaxAge
	switch {
	case maxAge == 0:
		maxAge = DefaultMalformedMaxAge
	case maxAge < 0:
		maxAge = 0
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	return gcByAgeAndCount(opts.Dir, ".json", keep, maxAge, now)
}
