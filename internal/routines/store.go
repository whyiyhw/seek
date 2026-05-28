package routines

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Store persists the set of registered cron jobs in
// ~/.seek/cron/jobs.jsonl. One Store instance per process;
// mutations are atomic at the filesystem level (write-tmp-
// rename, same dance as session.Save) so a power cut during
// rewrite leaves the prior committed state intact + a stray
// .tmp file for forensics.
//
// Concurrency: a sync.Mutex serialises in-process callers.
// CROSS-process concurrency (the tick engine vs `seek cron
// create` running simultaneously) is handled by tick.lock,
// added in Step 2 alongside the tick engine. For Step 1 the
// Store is callable from a single process only — fine for the
// CLI surface that lands in Step 3.
type Store struct {
	path string // jobs.jsonl path; resolved once at OpenStore
	mu   sync.Mutex
}

// OpenStore resolves the on-disk jobs.jsonl path. Does NOT
// create the file or parent directory — those happen lazily on
// first write. Read paths (List / Get) tolerate ENOENT and
// return an empty result.
func OpenStore() (*Store, error) {
	p, err := paths.CronJobs()
	if err != nil {
		return nil, fmt.Errorf("routines: resolve jobs path: %w", err)
	}
	return &Store{path: p}, nil
}

// path access for tests that want to inject a custom location
// without going through paths.CronJobs (which is host-global).
// Public for cross-package tests in routinescli; production
// code calls OpenStore.
func OpenStoreAt(jobsPath string) *Store {
	return &Store{path: jobsPath}
}

// List returns every registered job sorted by Name (stable
// rendering for CLI / TUI). ENOENT on the jobs file returns an
// empty slice + nil error — fresh installations look exactly
// like "no jobs yet".
func (s *Store) List() ([]Job, error) {
	if s == nil {
		return nil, errors.New("routines: nil Store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Get returns the job with the given name, or ErrJobNotFound.
func (s *Store) Get(name string) (Job, error) {
	if s == nil {
		return Job{}, errors.New("routines: nil Store")
	}
	if err := ValidateName(name); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.loadLocked()
	if err != nil {
		return Job{}, err
	}
	for _, j := range jobs {
		if j.Name == name {
			return j, nil
		}
	}
	return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, name)
}

// ErrJobNotFound is returned by Get / Delete when the named
// job isn't registered. Callers errors.Is against it.
var ErrJobNotFound = errors.New("routines: job not found")

// ErrJobExists is returned by Create when a job with the same
// name already exists AND opts.Force=false. Idempotent retries
// (same name, same prompt, same schedule) at the CLI surface
// can swallow this; user-initiated re-creates should surface
// it as "use --force to overwrite".
var ErrJobExists = errors.New("routines: job already exists (use Force to overwrite)")

// CreateOptions controls Create's overwrite behaviour. Default
// is "fail on duplicate name" — silent overwrite would lose
// the user's prior prompt without acknowledgement.
type CreateOptions struct {
	Force bool
}

// Create upserts a job. Validates Name + Notify + Schedule
// before any write. Behavior on duplicate name:
//
//   - opts.Force=false (default): returns ErrJobExists,
//     leaving the prior entry intact.
//   - opts.Force=true: silently overwrites. CLI gates this
//     behind explicit --force.
//
// Sets Created to now when zero; preserves it on overwrite so
// "edit by recreate" doesn't reset created_at history.
func (s *Store) Create(j Job, opts CreateOptions) error {
	if s == nil {
		return errors.New("routines: nil Store")
	}
	if err := ValidateName(j.Name); err != nil {
		return err
	}
	if err := ValidateNotify(j.Notify); err != nil {
		return err
	}
	if j.Schedule.IsZero() {
		return errors.New("routines: Create: Schedule required (use ParseSchedule first)")
	}
	if j.Prompt == "" {
		return errors.New("routines: Create: Prompt required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.loadLocked()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if j.Created.IsZero() {
		j.Created = now
	}
	if j.NextRun.IsZero() {
		// First fire defaults to NOW + Schedule.Every so the
		// initial wait matches the period. A scheduled job
		// doesn't fire "immediately on registration".
		j.NextRun = j.Schedule.Next(now)
	}
	if j.LastStatus == "" {
		j.LastStatus = StatusScheduled
	}
	if j.Notify == "" {
		j.Notify = NotifyAlways
	}

	// Find existing entry by name. Force=true → overwrite,
	// preserving created_at + run_count. Force=false →
	// ErrJobExists.
	idx := -1
	for i, e := range existing {
		if e.Name == j.Name {
			idx = i
			break
		}
	}
	if idx >= 0 {
		if !opts.Force {
			return fmt.Errorf("%w: %s", ErrJobExists, j.Name)
		}
		// Preserve historical fields on overwrite. The user is
		// updating definition, not resetting accounting.
		j.Created = existing[idx].Created
		j.RunCount = existing[idx].RunCount
		j.LastRun = existing[idx].LastRun
		j.LastRunID = existing[idx].LastRunID
		// LastStatus / LastError reflect prior run; keep them.
		if existing[idx].LastStatus != "" {
			j.LastStatus = existing[idx].LastStatus
		}
		j.LastError = existing[idx].LastError
		existing[idx] = j
	} else {
		existing = append(existing, j)
	}

	return s.saveLocked(existing)
}

// Delete removes the named job. Returns ErrJobNotFound if it
// doesn't exist — callers errors.Is to distinguish "already
// gone" from real I/O errors.
func (s *Store) Delete(name string) error {
	if s == nil {
		return errors.New("routines: nil Store")
	}
	if err := ValidateName(name); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]Job, 0, len(existing))
	found := false
	for _, j := range existing {
		if j.Name == name {
			found = true
			continue
		}
		out = append(out, j)
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrJobNotFound, name)
	}
	return s.saveLocked(out)
}

// MarkRun atomically records the outcome of a single fire:
// bumps RunCount, sets LastRun/LastRunID/LastStatus/LastError,
// advances NextRun to the next slot strictly after ranAt.
//
// When MaxRuns > 0 and RunCount reaches it, the job is
// DELETED rather than left around with an unreachable
// NextRun. This is load-bearing for schedule_wakeup
// (max_runs=1) — without auto-delete, the user's
// `seek cron list` fills up with completed one-shot wakeups
// (feature-routines.md §8 risk row).
//
// Returns ErrJobNotFound if name no longer exists (raced with
// Delete). Callers errors.Is.
func (s *Store) MarkRun(name, runID, status, errMsg string, ranAt time.Time) error {
	if s == nil {
		return errors.New("routines: nil Store")
	}
	if err := ValidateName(name); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.loadLocked()
	if err != nil {
		return err
	}
	idx := -1
	for i, j := range existing {
		if j.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %s", ErrJobNotFound, name)
	}
	j := existing[idx]
	j.RunCount++
	j.LastRun = ranAt.UTC()
	j.LastRunID = runID
	j.LastStatus = status
	j.LastError = errMsg
	j.NextRun = j.Schedule.Next(ranAt)

	// MaxRuns hit → delete instead of rewrite. The job has
	// completed its lifetime.
	if j.MaxRuns > 0 && j.RunCount >= j.MaxRuns {
		out := append(existing[:idx], existing[idx+1:]...)
		return s.saveLocked(out)
	}

	existing[idx] = j
	return s.saveLocked(existing)
}

// loadLocked reads jobs.jsonl into a slice, sorted by Name.
// Caller MUST hold s.mu. ENOENT returns nil slice + nil err
// (fresh install). Malformed lines are SKIPPED — the same
// tolerance as plan-mode reconstruct.go + subagent index. A
// single bad line shouldn't make all jobs unreachable.
func (s *Store) loadLocked() ([]Job, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("routines: open jobs: %w", err)
	}
	defer f.Close()

	var out []Job
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var j Job
		if err := json.Unmarshal(line, &j); err != nil {
			continue // skip malformed; never fail the whole load
		}
		out = append(out, j)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("routines: scan jobs: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// saveLocked rewrites jobs.jsonl with the supplied slice using
// the write-tmp-rename atomic dance (same pattern as
// session.Save). Caller MUST hold s.mu. Steps:
//
//  1. MkdirAll parent (lazy — first call on a fresh install).
//  2. Create unique tmp file under same dir (so rename is
//     atomic — cross-filesystem rename can fall back to copy).
//  3. Write all jobs as JSONL.
//  4. fsync the tmp file (durability against power loss
//     between rename and next OS write-back).
//  5. Rename tmp → final.
//
// Returns on first error. Leftover .tmp on partial failure is
// fine — the next save attempt cleans it (CreateTemp picks a
// fresh name) and the prior committed jobs.jsonl is intact.
func (s *Store) saveLocked(jobs []Job) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("routines: mkdir cron dir: %w", err)
	}

	// CreateTemp under the same dir as the final file — required
	// for the rename to be an atomic move (within the same
	// filesystem). Same-name fixed-tmp is unsafe under concurrent
	// callers, hence Temp.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("routines: create tmp: %w", err)
	}
	tmpPath := tmp.Name()

	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false) // matches session.Save / subagent.appendEvent
	for _, j := range jobs {
		if err := enc.Encode(j); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("routines: encode job %q: %w", j.Name, err)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("routines: sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("routines: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("routines: rename tmp → jobs: %w", err)
	}
	return nil
}
