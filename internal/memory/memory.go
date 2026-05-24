// Package memory is the storage layer for seek's three-layer cognitive
// memory subsystem (PRD v1 §4):
//
//   - L (long-term / Soul) — ~/.seek/soul.md, cross-project user traits.
//     Authored by `seek -dream` and editable by the user.
//   - M (mid-term / Project) — ~/.seek/projects/<id>/, per-project
//     decisions + rationale. JSONL entries plus a manifest.
//   - S (session) — handled by internal/session; memory only reads it.
//
// This package is pure I/O: it knows the on-disk layout and how to
// round-trip Entry / Manifest / Soul records. It does NOT implement
// recall scoring, distillation, dreaming, or LLM-side injection — those
// land in later milestones (M5.1+).
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the wire version of Entry / Manifest. Bumped only on
// breaking changes; readers tolerant of additive fields don't bump.
const SchemaVersion = 1

// Entry is a single M-layer memory: one decision + its rationale, scoped
// to a project. Stored one-per-line in memory.jsonl.
//
// LastRecalledAt is always set (initialised to CreatedAt on first write)
// so the decay-score computation in PRD §6 has a stable last_active_at
// to anchor against — never null.
type Entry struct {
	SchemaVersion  int       `json:"schema_version"`
	Name           string    `json:"name"`
	Tagline        string    `json:"tagline"`
	Content        string    `json:"content"`
	Tags           []string  `json:"tags,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastRecalledAt time.Time `json:"last_recalled_at"`
	RecallCount    int       `json:"recall_count"`
	Pinned         bool      `json:"pinned,omitempty"`
	Stale          bool      `json:"stale,omitempty"`
	// StaleSince is the timestamp at which Stale was first flipped to
	// true by GC. Used by the archive rule (PRD §6) to require the
	// entry has been continuously stale for archiveStalePersistence
	// before being moved to archived.jsonl. Cleared (zero) when GC
	// recovers an entry back to non-stale.
	//
	// `omitzero` (Go 1.24+) is the correct elision for time.Time —
	// `omitempty` does nothing for struct types, which would leak
	// the "0001-01-01T00:00:00Z" zero value into every fresh entry.
	StaleSince      time.Time `json:"stale_since,omitzero"`
	SourceSessionID string    `json:"source_session_id,omitempty"`

	// AutoSourced flags entries written by the auto-distill path
	// (M5.7) — distilled at SessionEnd without going through the
	// y/n/e review modal. The next manual /distill pass surfaces
	// these for the user to confirm or revoke, preventing silent
	// drift from the model's own perceptions.
	AutoSourced bool `json:"auto_sourced,omitempty"`

	// ObserveCount tracks how many times memory_observe has written
	// (or overwritten) this entry across independent sessions.
	// Incremented by Add() when replacing an existing auto_sourced
	// entry. When ObserveCount reaches autoPromoteObservations (3),
	// Add auto-flips AutoSourced=false (M5.11 auto-promotion).
	// Zero for non-auto_sourced entries.
	ObserveCount int `json:"observe_count,omitempty"`
}

// Manifest is the project identity record at <projectDir>/manifest.json.
// ProjectID matches the directory name; AbsPath is the filesystem
// location seek saw last time. AbsPath drift triggers a manifest
// rewrite (project moved) but the project-id pointer file is the
// load-bearing recovery mechanism.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	ProjectID     string    `json:"project_id"`
	AbsPath       string    `json:"abs_path"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
}

// IndexEntry is the (name, tagline) pair used to build the M-index that
// PrePromptHook injects on every Prompt (PRD §5). Sorted by Name in
// Project.Index — byte stability is load-bearing for prefix-cache.
type IndexEntry struct {
	Name    string
	Tagline string
}

// projectID returns the 16-char hex prefix of sha256(absPath), the
// on-disk directory name under ~/.seek/projects/. 16 hex chars = 64
// bits of namespace; collision probability is negligible for personal
// scale (≤thousands of projects).
func projectID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:16]
}

// isValidProjectID enforces the 16-lowercase-hex shape so a malformed
// .seek/project-id (hand-edited file, foreign tool, truncated write)
// can't punch a hole in the projects directory tree.
func isValidProjectID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// atomicWrite stages data to <path>.tmp and renames over <path>. A
// crash between WriteFile and Rename leaves the original intact; a
// crash during rename is OS-atomic on POSIX. Used for manifest.json,
// memory.jsonl, and soul.md so a partial write never corrupts state.
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ErrNotFound is returned by Project.TouchRecall when the named entry
// doesn't exist. Add / Remove are idempotent and do NOT return this.
var ErrNotFound = errors.New("memory: entry not found")
