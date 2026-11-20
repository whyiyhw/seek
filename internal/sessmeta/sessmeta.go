// Package sessmeta persists serve-side session presentation overrides
// (title renames + archive flags) for the web session sidebar (W5 card
// c_rqcuaa, feature-web-frontend §13.1 row: "存储决策留实现卡" — this
// package IS that decision).
//
// The two constraints the PRD pins:
//
//   - The serve process never writes the session JSONL the child seek
//     process owns (transcript ownership). Overrides live beside it,
//     keyed by sid: serve-owned state, exactly the BindingStore family
//     posture (plane-owned runtime state, not agent state — the
//     "reconstruct from transcript" discipline scopes agent/plan state,
//     and a rename is not derivable from any transcript).
//   - Archive is a list-filter bit, never file deletion.
//
// On-disk shape (BindingStore discipline throughout):
//
//	~/.seek/serve/sessions-meta.json          state, map[sid]Entry,
//	                                          rewritten atomically per
//	                                          mutation (tmp+rename)
//	~/.seek/serve/sessions-meta-audit.jsonl   append-only audit, one
//	                                          line per APPLIED mutation
//
// Corruption posture mirrors BindingStore: ENOENT/empty/garbage state
// file → empty store (renames/archives are re-creatable UI facts, same
// reasoning as bindings); the next write rewrites a valid file. The
// audit log is never read back by serve — it exists for `tail -f`.
package sessmeta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Entry is one session's presentation overrides. Title empty = no
// override (list falls back to the derived first-user-message title).
type Entry struct {
	SID       string    `json:"sid"`
	Title     string    `json:"title,omitempty"`
	Archived  bool      `json:"archived,omitempty"`
	UpdatedAt time.Time `json:"updated_at"` // last mutation, UTC
}

// auditEvent is one line of sessions-meta-audit.jsonl. Only applied
// mutations are logged — a no-op archive call (already archived) is
// idempotent API surface, not a state transition, so it writes no row.
type auditEvent struct {
	TS    string `json:"ts"`
	Event string `json:"event"`           // "rename" | "archive" | "unarchive"
	SID   string `json:"sid"`
	Title string `json:"title,omitempty"` // rename: the new title
	Prev  string `json:"prev,omitempty"`  // rename: the override it replaced ("" = was derived)
}

// Store is the meta state + audit pair. One per serve process; an
// in-process mutex serialises mutations so concurrent RPC dispatches
// read-modify-write one file without losing each other's entries. The
// serve process is the only writer (same single-writer claim as
// BindingStore), so no cross-process locking.
type Store struct {
	statePath string
	auditPath string
	mu        sync.Mutex
}

// Open resolves the default location under paths.ServeDir. Does NOT
// create files or the parent directory — both happen lazily on the
// first write; reads tolerate ENOENT.
func Open() (*Store, error) {
	dir, err := paths.ServeDir()
	if err != nil {
		return nil, fmt.Errorf("sessmeta: resolve serve dir: %w", err)
	}
	return &Store{
		statePath: filepath.Join(dir, "sessions-meta.json"),
		auditPath: filepath.Join(dir, "sessions-meta-audit.jsonl"),
	}, nil
}

// OpenAt opens a store at explicit paths — the test/injection seam
// that avoids the host-global paths.ServeDir. Production code calls
// Open. An empty auditPath disables audit logging (wire-layer off
// switch, mirroring hooksconfig.NewAuditLog).
func OpenAt(statePath, auditPath string) *Store {
	return &Store{statePath: statePath, auditPath: auditPath}
}

// All returns the current overrides (empty map when none). Corrupt or
// missing state reads as empty — a browse view must not die because a
// side file is damaged; the next mutation rewrites it valid.
func (s *Store) All() (map[string]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// load reads the state file. Caller holds s.mu.
func (s *Store) load() (map[string]Entry, error) {
	raw, err := os.ReadFile(s.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, fmt.Errorf("sessmeta: read %s: %w", s.statePath, err)
	}
	if len(raw) == 0 {
		return map[string]Entry{}, nil
	}
	out := map[string]Entry{}
	if err := json.Unmarshal(raw, &out); err != nil {
		// BindingStore stance: salvage-nothing-but-don't-fail-boot. A
		// syntactically broken file can only come from external damage
		// (the atomic rename keeps crashes from producing one).
		return map[string]Entry{}, nil
	}
	return out, nil
}

// save writes the state atomically (tmp+rename, BindingStore's dance
// verbatim: Encoder without HTML escaping, indented — small file,
// human-debuggable wins over bytes; map keys marshal sorted, so the
// file is byte-stable for identical logical state).
func (s *Store) save(entries map[string]Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o700); err != nil {
		return fmt.Errorf("sessmeta: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.statePath), ".sessmeta-*")
	if err != nil {
		return fmt.Errorf("sessmeta: create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sessmeta: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sessmeta: sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("sessmeta: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, s.statePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("sessmeta: rename: %w", err)
	}
	return nil
}

// appendAudit writes one event line. O_APPEND + one Write per line is
// atomic to PIPE_BUF (the hooksconfig.AuditLog argument, entries well
// under 4 KiB). Called with s.mu held so audit order matches state
// order. Failure is returned but the state change has already landed —
// callers surface it rather than rolling back (a lost audit line is
// survivable, a half-rolled-back rename is not).
func (s *Store) appendAudit(e auditEvent) error {
	if s.auditPath == "" {
		return nil
	}
	e.TS = time.Now().UTC().Format(time.RFC3339)
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("sessmeta: marshal audit: %w", err)
	}
	body = append(body, '\n')
	f, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("sessmeta: open audit: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("sessmeta: write audit: %w", err)
	}
	return nil
}

// SetTitle records a rename override for sid (empty title deletes the
// override, falling the list back to the derived title). Concurrent
// SetTitle calls on the same sid serialise last-writer-wins; on
// different sids both survive — one mutex over the whole
// read-modify-write, so the file is never a torn merge.
func (s *Store) SetTitle(sid, title string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return Entry{}, err
	}
	e := entries[sid] // zero Entry when absent
	prev := e.Title
	e.SID = sid
	e.Title = title
	e.UpdatedAt = time.Now().UTC()
	if title == "" && !e.Archived {
		delete(entries, sid) // no overrides left: drop the entry
	} else {
		entries[sid] = e
	}
	if err := s.save(entries); err != nil {
		return Entry{}, err
	}
	if title != prev {
		err = s.appendAudit(auditEvent{Event: "rename", SID: sid, Title: title, Prev: prev})
	}
	return e, err
}

// SetArchived flips the archive bit. Idempotent: setting the bit to
// its current value writes nothing, logs nothing, and reports
// changed=false. An entry whose overrides are all withdrawn (unarchive
// with no title) is deleted — the map only carries live overrides.
func (s *Store) SetArchived(sid string, archived bool) (e Entry, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return Entry{}, false, err
	}
	e = entries[sid]
	if e.Archived == archived {
		return e, false, nil // idempotent no-op: no write, no audit row
	}
	e.SID = sid
	e.Archived = archived
	e.UpdatedAt = time.Now().UTC()
	if !archived && e.Title == "" {
		delete(entries, sid)
	} else {
		entries[sid] = e
	}
	if err := s.save(entries); err != nil {
		return Entry{}, false, err
	}
	event := "archive"
	if !archived {
		event = "unarchive"
	}
	err = s.appendAudit(auditEvent{Event: event, SID: sid})
	return e, true, err
}
