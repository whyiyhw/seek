package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// ErrBindingNotFound is returned by Get / Delete when the conversation
// has no binding. The M2 router treats this as "prompt the user to
// /bind" rather than an error to surface.
var ErrBindingNotFound = errors.New("controlplane: binding not found")

// Binding is one persisted conversation→project mapping (D-C2):
// which directory a ConvID's messages exec seek in, and which seek
// session to -resume for multi-turn continuity.
//
// This is plane-owned runtime state, not agent state — persisting it
// does not violate the "reconstruct from transcript" discipline (that
// constraint scopes agent/plan state; see AGENTS.md). Bindings are
// user-recreatable via /bind, which is what makes the corruption
// posture below (rebuild-from-empty on unparseable files) acceptable.
type Binding struct {
	ConvID      ConvID    `json:"conv_id"`
	ProjectPath string    `json:"project_abs_path"`
	SessionID   string    `json:"session_id,omitempty"` // empty until the first exec completes (D-C3)
	Yolo        bool      `json:"yolo,omitempty"`       // per-binding exec with --yolo (D-C5); default off
	UpdatedAt   time.Time `json:"updated_at"`           // last mutation, UTC
}

// BindingStore persists Bindings to ~/.seek/serve/bindings.json —
// a single JSON array of Binding objects, rewritten atomically on
// every mutation (write-tmp-rename, same dance as session.Save and
// routines.Store). One store per serve process; an in-process mutex
// serialises callers. The serve process is the only writer, so no
// cross-process locking is needed (unlike the cron tick lock).
//
// Corruption posture (the session.Repair stance, adapted to a small
// single-file store — salvage what's parseable, never fail the boot):
//
//   - ENOENT / empty file  → empty store, no error (fresh install).
//   - unparseable JSON     → empty store, no error. A syntactically
//     broken file can only come from external damage (the atomic
//     rename keeps crashes from producing one); resetting to empty
//     loses only re-creatable /bind state, and the next Put rewrites
//     a valid file.
//   - parseable array with malformed ENTRIES (bad field types,
//     invalid conv_id) → the bad entry is skipped, the rest load.
//   - duplicate conv_id → last entry wins (matches Put's upsert).
type BindingStore struct {
	path string
	mu   sync.Mutex
}

// OpenBindingStore resolves the default bindings path under
// paths.ServeDir. Does NOT create the file or parent directory —
// those happen lazily on first write; reads tolerate ENOENT.
func OpenBindingStore() (*BindingStore, error) {
	dir, err := paths.ServeDir()
	if err != nil {
		return nil, fmt.Errorf("controlplane: resolve serve dir: %w", err)
	}
	return &BindingStore{path: filepath.Join(dir, "bindings.json")}, nil
}

// OpenBindingStoreAt opens a store at an explicit path — the test /
// injection seam that avoids the host-global paths.ServeDir. Production
// code calls OpenBindingStore.
func OpenBindingStoreAt(path string) *BindingStore {
	return &BindingStore{path: path}
}

// Path returns the resolved on-disk location.
func (s *BindingStore) Path() string { return s.path }

// Get returns the binding for conv, or ErrBindingNotFound.
func (s *BindingStore) Get(conv ConvID) (Binding, error) {
	if s == nil {
		return Binding{}, errors.New("controlplane: nil BindingStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return Binding{}, err
	}
	b, ok := all[conv]
	if !ok {
		return Binding{}, fmt.Errorf("%w: %s", ErrBindingNotFound, conv)
	}
	return b, nil
}

// List returns every binding sorted by ConvID (stable rendering for
// /projects). ENOENT on the file returns an empty slice + nil error.
func (s *BindingStore) List() ([]Binding, error) {
	if s == nil {
		return nil, errors.New("controlplane: nil BindingStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byConv, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]Binding, 0, len(byConv))
	for _, b := range byConv {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConvID < out[j].ConvID })
	return out, nil
}

// Put upserts the binding for b.ConvID and stamps UpdatedAt to now
// (UTC). Validates the ConvID shape and a non-empty project path —
// existence of the directory is the M2 /bind command's job, not the
// store's.
func (s *BindingStore) Put(b Binding) error {
	if s == nil {
		return errors.New("controlplane: nil BindingStore")
	}
	if b.ConvID.FrontendName() == "" {
		return fmt.Errorf("controlplane: Put with invalid conv_id %q", b.ConvID)
	}
	if b.ProjectPath == "" {
		return fmt.Errorf("controlplane: Put with empty project path (conv %s)", b.ConvID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	byConv, err := s.loadLocked()
	if err != nil {
		return err
	}
	b.UpdatedAt = time.Now().UTC()
	byConv[b.ConvID] = b
	return s.saveLocked(byConv)
}

// Delete removes the binding for conv. Returns ErrBindingNotFound when
// absent, mirroring routines.Store.Delete.
func (s *BindingStore) Delete(conv ConvID) error {
	if s == nil {
		return errors.New("controlplane: nil BindingStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	byConv, err := s.loadLocked()
	if err != nil {
		return err
	}
	if _, ok := byConv[conv]; !ok {
		return fmt.Errorf("%w: %s", ErrBindingNotFound, conv)
	}
	delete(byConv, conv)
	return s.saveLocked(byConv)
}

// loadLocked reads bindings.json into a conv-keyed map. Caller MUST
// hold s.mu. Applies the corruption ladder documented on BindingStore;
// the only error returned is a non-tolerable one (unreadable file,
// save-side failures happen elsewhere).
func (s *BindingStore) loadLocked() (map[ConvID]Binding, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[ConvID]Binding{}, nil
		}
		return nil, fmt.Errorf("controlplane: read bindings %s: %w", s.path, err)
	}

	// Entry-level tolerance: decode the array as raw messages first so
	// one bad entry (wrong field types, invalid conv_id) can't take the
	// whole file down; then unmarshal entries individually, skipping
	// the malformed ones. A syntax-broken file (truncation, garbage)
	// fails the array decode itself and rebuilds from empty.
	var raws []json.RawMessage
	if err := json.Unmarshal(raw, &raws); err != nil {
		// Empty file (manual truncation, not a crash artifact — the
		// atomic rename prevents those) is a valid empty store. Treat
		// it the same as ENOENT. Anything else unparseable also
		// rebuilds empty: see the corruption ladder.
		return map[ConvID]Binding{}, nil
	}

	byConv := make(map[ConvID]Binding, len(raws))
	for _, r := range raws {
		var b Binding
		if err := json.Unmarshal(r, &b); err != nil {
			continue // skip malformed entry; never fail the whole load
		}
		if b.ConvID.FrontendName() == "" {
			continue // invalid conv_id can never be addressed back
		}
		byConv[b.ConvID] = b // duplicate conv_id: last wins, like Put
	}
	return byConv, nil
}

// saveLocked rewrites bindings.json with the supplied bindings using
// the write-tmp-rename atomic dance (same pattern as session.Save /
// routines.Store.saveLocked). Caller MUST hold s.mu.
//
// The serve dir is created 0o700 and the file 0o600 because the
// sibling env file in the same directory holds frontend credentials
// (§9); there is no reason for bindings to be looser.
func (s *BindingStore) saveLocked(byConv map[ConvID]Binding) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("controlplane: mkdir serve dir: %w", err)
	}

	// CreateTemp under the same dir as the final file — required for
	// the rename to be an atomic move (within the same filesystem).
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("controlplane: create tmp: %w", err)
	}
	tmpPath := tmp.Name()

	entries := make([]Binding, 0, len(byConv))
	for _, b := range byConv {
		entries = append(entries, b)
	}
	// Sorted by conv_id: byte-stable file for identical logical state,
	// so accidental rewrites diff cleanly and tests can compare files.
	sort.Slice(entries, func(i, j int) bool { return entries[i].ConvID < entries[j].ConvID })

	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false) // matches session.Save / subagent.appendEvent
	enc.SetIndent("", "  ")  // small file; human-debuggable wins over bytes
	if err := enc.Encode(entries); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("controlplane: encode bindings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("controlplane: sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("controlplane: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("controlplane: rename tmp → bindings: %w", err)
	}
	return nil
}
