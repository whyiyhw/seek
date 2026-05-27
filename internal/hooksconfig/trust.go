package hooksconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// TrustEntry records the user's approval of one project-level hooks
// file. Match (ProjectPath, SHA256) tuple to consult on each startup —
// when the file content changes (sha256 differs) the trust expires and
// the user is asked again. Per PRD §3.5.
type TrustEntry struct {
	ProjectPath string `json:"project_path"`
	SHA256      string `json:"sha256"`
	// ApprovedAt is RFC3339 — not strictly needed by the gate logic but
	// invaluable for `seek hooks trust --list` and forensic debugging.
	ApprovedAt string `json:"approved_at"`
}

// TrustStore is the in-memory + on-disk registry that backs the
// project-hooks trust gate. Concurrent-safe: a Save can happen on a
// background goroutine (TUI trust dialog) while a Lookup runs on the
// main goroutine (startup).
type TrustStore struct {
	mu      sync.RWMutex
	path    string
	entries map[string]TrustEntry // key = ProjectPath
}

// NewTrustStore opens (or initialises) the trust file at the given
// path. ENOENT is fine — first invocation just starts with an empty
// map and writes on the first Approve.
func NewTrustStore(path string) (*TrustStore, error) {
	s := &TrustStore{
		path:    path,
		entries: make(map[string]TrustEntry),
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("hooksconfig: open trust file %s: %w", path, err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("hooksconfig: read trust file: %w", err)
	}
	if len(body) == 0 {
		return s, nil
	}
	var raw struct {
		Entries []TrustEntry `json:"entries"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		// Corrupt trust file: rather than fail-close (which would
		// disable EVERY trusted project on a single bad byte), fail
		// open into "nothing trusted" so the user re-approves each
		// project at next visit. PRD §2.5 failure-degrade-not-block.
		return s, fmt.Errorf("hooksconfig: trust file %s unreadable, treating as empty: %w", path, err)
	}
	for _, e := range raw.Entries {
		s.entries[e.ProjectPath] = e
	}
	return s, nil
}

// IsTrusted returns true iff the given (project path, current file
// sha256) pair is registered. The sha256 comparison is what makes
// "trust on change" work: edit hooks.toml, sha256 changes, the entry
// no longer matches, the trust gate fires again.
func (s *TrustStore) IsTrusted(projectPath, currentSha string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[projectPath]
	if !ok {
		return false
	}
	return e.SHA256 == currentSha
}

// Approve records trust for (projectPath, sha256) and persists the
// store to disk atomically. Calling Approve a second time with a
// different sha overwrites the previous entry — that's the
// "user re-approves after edit" path.
func (s *TrustStore) Approve(projectPath, sha, approvedAt string) error {
	if s == nil {
		return errors.New("hooksconfig: nil TrustStore")
	}
	s.mu.Lock()
	s.entries[projectPath] = TrustEntry{
		ProjectPath: projectPath,
		SHA256:      sha,
		ApprovedAt:  approvedAt,
	}
	s.mu.Unlock()
	return s.save()
}

// Reset removes the entry for a given project — used by
// `seek hooks trust --reset`. ResetAll wipes the whole store. Both
// persist immediately.
func (s *TrustStore) Reset(projectPath string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	delete(s.entries, projectPath)
	s.mu.Unlock()
	return s.save()
}

func (s *TrustStore) ResetAll() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.entries = make(map[string]TrustEntry)
	s.mu.Unlock()
	return s.save()
}

// List returns a stable-ordered copy of all entries — used by
// `seek hooks trust` (no arg = list).
func (s *TrustStore) List() []TrustEntry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TrustEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectPath < out[j].ProjectPath })
	return out
}

// save serialises the entries to JSON and writes atomically via a
// .tmp rename. Concurrent saves are serialised by the caller's lock
// release — we re-acquire the read lock here.
func (s *TrustStore) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := make([]TrustEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ProjectPath < entries[j].ProjectPath })
	payload := struct {
		Entries []TrustEntry `json:"entries"`
	}{Entries: entries}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("hooksconfig: marshal trust: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("hooksconfig: mkdir trust dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("hooksconfig: write trust tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("hooksconfig: rename trust: %w", err)
	}
	return nil
}

// Sha256Hex returns the lowercase-hex sha256 of the given bytes.
// Centralised here so callers (the loader, the trust gate, tests) all
// produce identical fingerprints. Empty input is valid and produces
// the well-known constant sha256("") — a missing file shouldn't reach
// here, but if it does the empty hash stays stable.
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Sha256File reads the file at path and returns Sha256Hex of its
// contents. ENOENT returns ("", os.ErrNotExist) — the caller decides
// whether that's a normal state (project has no hooks.toml: no trust
// check needed) or an error.
func Sha256File(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Sha256Hex(body), nil
}
