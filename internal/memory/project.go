package memory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Project is the M-layer handle for a single project.
// Safe for concurrent reads; write methods must be externally serialised
// (the agent loop provides this — see note on concurrent access below).
//
// The embedded sync.RWMutex guards entries and order against the async
// memory_observe goroutine (ObserveEnqueue) racing with OnPrePrompt or
// OnSessionStart (RunGC). Read paths (Index, Get, Entries) take RLock;
// write paths (Add, Remove, TouchRecall, RunGC) take Lock.
type Project struct {
	mu sync.RWMutex

	ID       string
	Dir      string
	AbsPath  string
	Manifest Manifest

	entries map[string]Entry
	order   []string // insertion order for deterministic iteration
}

// LoadOrCreate returns the Project for cwd. Resolution order matches
// PRD §4:
//
//  1. If <abs(cwd)>/.seek/project-id exists, read its hash — this is
//     how a moved project recovers its M.
//  2. Otherwise compute sha256(abs(cwd))[:16].
//
// Side effects: creates ~/.seek/projects/<id>/ on first visit, writes
// (or updates) manifest.json with LastSeen = now, and best-effort drops
// <cwd>/.seek/project-id so the project can be found again after a
// directory move. A read-only filesystem at cwd is tolerated — the
// pointer is recovery, not correctness.
func LoadOrCreate(cwd string) (*Project, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("memory: abs(%q): %w", cwd, err)
	}

	id, err := resolveProjectID(abs)
	if err != nil {
		return nil, err
	}

	projectsRoot, err := paths.Projects()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(projectsRoot, id)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: mkdir %q: %w", dir, err)
	}

	p := &Project{
		ID:      id,
		Dir:     dir,
		AbsPath: abs,
		entries: map[string]Entry{},
	}

	if err := p.loadManifest(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := time.Now().UTC()
	if p.Manifest.SchemaVersion == 0 {
		p.Manifest = Manifest{
			SchemaVersion: SchemaVersion,
			ProjectID:     id,
			AbsPath:       abs,
			FirstSeen:     now,
			LastSeen:      now,
		}
	} else {
		p.Manifest.LastSeen = now
		if p.Manifest.AbsPath != abs {
			// Project moved — update so future hash-based probes (when
			// the pointer file is missing too) still resolve here.
			p.Manifest.AbsPath = abs
		}
	}

	if err := p.loadEntries(); err != nil {
		return nil, err
	}

	if err := p.writeManifest(); err != nil {
		return nil, err
	}
	// Pointer is best-effort: a read-only project tree (CI checkout,
	// example dir under a system path) shouldn't fail the session.
	_ = writeProjectPointer(abs, id)

	return p, nil
}

// resolveProjectID picks the project's ID following PRD §4 priority.
func resolveProjectID(abs string) (string, error) {
	pointer := filepath.Join(abs, ".seek", "project-id")
	if data, err := os.ReadFile(pointer); err == nil {
		id := strings.TrimSpace(string(data))
		if isValidProjectID(id) {
			return id, nil
		}
	}
	return projectID(abs), nil
}

func writeProjectPointer(abs, id string) error {
	dir := filepath.Join(abs, ".seek")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "project-id"), []byte(id+"\n"), 0o644)
}

func (p *Project) loadManifest() error {
	data, err := os.ReadFile(filepath.Join(p.Dir, "manifest.json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &p.Manifest)
}

func (p *Project) writeManifest() error {
	data, err := json.MarshalIndent(&p.Manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(p.Dir, "manifest.json"), data)
}

// loadEntries reads memory.jsonl line-by-line with bufio.Scanner so a
// single corrupt line doesn't poison the rest of the file. Empty lines
// are skipped; entries with no Name are dropped (corrupt).
//
// We deliberately avoid json.Decoder here: Decoder's internal state
// across line boundaries makes single-line recovery ambiguous, and the
// pitfall log already warns about it.
func (p *Project) loadEntries() error {
	f, err := os.Open(filepath.Join(p.Dir, "memory.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Raise the per-line buffer from the 64KB default — a Content field
	// can legitimately hold multi-paragraph rationale, well past 64KB.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // corrupt line — drop, keep going
		}
		if e.Name == "" {
			continue
		}
		if _, dup := p.entries[e.Name]; !dup {
			p.order = append(p.order, e.Name)
		}
		p.entries[e.Name] = e
	}
	return scanner.Err()
}

// writeEntries serialises in insertion order using json.Encoder (which
// per the JSONL pitfall already appends \n per record), then atomic-
// renames over memory.jsonl.
func (p *Project) writeEntries() error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, name := range p.order {
		e := p.entries[name]
		if err := enc.Encode(&e); err != nil {
			return err
		}
	}
	return atomicWrite(filepath.Join(p.Dir, "memory.jsonl"), buf.Bytes())
}

// Save persists manifest + memory.jsonl. Add / Remove / TouchRecall
// already write on each mutation; Save exists for callers that batched
// changes externally (e.g. a future migration tool).
func (p *Project) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.writeManifest(); err != nil {
		return err
	}
	return p.writeEntries()
}

// Add inserts (or replaces) an entry by Name. SchemaVersion is forced
// to current; CreatedAt and LastRecalledAt are set if zero; UpdatedAt
// is always bumped to now. The on-disk file is rewritten before Add
// returns — callers don't need to remember to Save.
func (p *Project) Add(e Entry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.Name == "" {
		return errors.New("memory: Entry.Name is required")
	}
	now := time.Now().UTC()
	e.SchemaVersion = SchemaVersion
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if e.LastRecalledAt.IsZero() {
		e.LastRecalledAt = e.CreatedAt
	}
	if _, exists := p.entries[e.Name]; !exists {
		p.order = append(p.order, e.Name)
	}
	p.entries[e.Name] = e
	return p.writeEntries()
}

// Get returns the entry by name. Stale entries are still returned —
// stale only affects index injection (PRD §6), not direct memory_recall.
func (p *Project) Get(name string) (Entry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.entries[name]
	return e, ok
}

// Remove deletes an entry by name. Idempotent — removing an absent
// entry is not an error. Returns the deleted entry's existence so
// callers can distinguish "removed something" from "no-op".
func (p *Project) Remove(name string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.entries[name]; !ok {
		return false, nil
	}
	delete(p.entries, name)
	for i, n := range p.order {
		if n == name {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	return true, p.writeEntries()
}

// Archive moves an entry from the active set to archived.jsonl and
// removes it from the in-memory map. The reason is recorded alongside
// the entry in the archive. Returns ErrNotFound when name doesn't exist.
func (p *Project) Archive(name, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	e, ok := p.entries[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err := p.appendArchive(e); err != nil {
		return fmt.Errorf("archive append: %w", err)
	}

	delete(p.entries, name)
	for i, n := range p.order {
		if n == name {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	return p.writeEntries()
}

// Amend appends new content to an existing entry's rationale. The
// appended text is separated with a horizontal rule and timestamp.
// UpdatedAt and LastRecalledAt are bumped to now. Returns ErrNotFound
// when name doesn't exist.
func (p *Project) Amend(name, appendContent string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	e, ok := p.entries[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	var sb strings.Builder
	sb.WriteString(e.Content)
	sb.WriteString("\n\n---\n")
	sb.WriteString(now.Format(time.RFC3339))
	sb.WriteString("\n\n")
	sb.WriteString(appendContent)
	e.Content = sb.String()
	e.UpdatedAt = now
	e.LastRecalledAt = now
	p.entries[name] = e
	return p.writeEntries()
}

// TouchRecall increments recall_count and updates last_recalled_at,
// then persists. Returns ErrNotFound for missing names so callers can
// distinguish a typo from a successful no-op (Add/Remove are idempotent
// because there's a single sensible behaviour; TouchRecall isn't).
func (p *Project) TouchRecall(name string, t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	e.RecallCount++
	e.LastRecalledAt = t.UTC()
	p.entries[name] = e
	return p.writeEntries()
}

// Index returns the active (non-stale) entries sorted by Name. The sort
// is what makes PrePromptHook injection byte-stable across runs — PRD
// §5 acceptance #7 (SHA-256 round-trip equality) depends on this.
func (p *Project) Index() []IndexEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]IndexEntry, 0, len(p.entries))
	for name, e := range p.entries {
		if e.Stale {
			continue
		}
		out = append(out, IndexEntry{Name: name, Tagline: e.Tagline})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Entries returns all entries (stale included) in insertion order.
// Used by /distill and GC; callers that want the injection-ready list
// should call Index() instead.
func (p *Project) Entries() []Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Entry, 0, len(p.entries))
	for _, name := range p.order {
		out = append(out, p.entries[name])
	}
	return out
}

// ListProjects walks ~/.seek/projects/ and returns a read-only handle
// for every subdirectory that contains a valid manifest.json. Unlike
// LoadOrCreate this has NO side effects — no manifest LastSeen bump,
// no .seek/project-id pointer write — so it's safe for `seek -dream`
// to scan without disturbing the M-layer state.
//
// Subdirs without a parseable manifest are silently skipped (could be
// half-written, foreign tool, or an in-progress migration). The
// returned slice is sorted by ProjectID for deterministic iteration.
func ListProjects() ([]*Project, error) {
	root, err := paths.Projects()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read projects: %w", err)
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if !isValidProjectID(id) {
			continue
		}
		p, err := loadProjectReadOnly(filepath.Join(root, id))
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// archiveFileName is the file holding entries that have been deeply
// stale long enough to retire from the active set (PRD §6). The active
// set lives in memory.jsonl; archived.jsonl is grep-only by design —
// no tool path reads it back into Project.
const archiveFileName = "archived.jsonl"

// appendArchive writes one Entry to <Dir>/archived.jsonl as a new
// JSONL record. Append-mode rather than the full atomic-rewrite path
// used by memory.jsonl: archived entries are write-once, and the
// failure modes are different — partial write here means a duplicate
// archive line on the next GC, which is harmless (archived.jsonl is
// not authoritative for anything; memory.jsonl is the source of truth
// for what's "active").
func (p *Project) appendArchive(e Entry) error {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(p.Dir, archiveFileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(&e)
}

// LoadArchived reads <Dir>/archived.jsonl for inspection / debugging.
// Returns nil when the file doesn't exist (no entries have ever been
// archived in this project). Per PRD §6 the active surface NEVER
// reaches archived entries — this exposes the data for grep-like
// tooling only; production code paths (Index, Get, Entries) keep
// ignoring them.
func (p *Project) LoadArchived() ([]Entry, error) {
	f, err := os.Open(filepath.Join(p.Dir, archiveFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var out []Entry
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // tolerant: same recovery semantics as loadEntries
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}

// loadProjectReadOnly reads a project's manifest + entries from disk
// without creating or modifying anything. Used by ListProjects.
// Returns os.ErrNotExist when the manifest is missing — callers
// (currently just ListProjects) skip those.
func loadProjectReadOnly(dir string) (*Project, error) {
	p := &Project{Dir: dir, entries: map[string]Entry{}}
	if err := p.loadManifest(); err != nil {
		return nil, err
	}
	p.ID = p.Manifest.ProjectID
	p.AbsPath = p.Manifest.AbsPath
	if err := p.loadEntries(); err != nil {
		return nil, err
	}
	return p, nil
}
