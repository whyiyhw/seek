// Package skillstats writes call statistics for the `Skill` tool to
// a local JSONL file. Per PRD v2 §4.3 the schema is six fields
// (ts/name/session_id/project_id/model/provider) on a single line —
// enough to answer "which skills got called, how often, in which
// projects, on which providers" without anything resembling RAG.
//
// The package is intentionally tiny and leaf-positioned:
//   - skilltool depends on it (write side)
//   - skillmgr will depend on it later for queries (read side, M8.4)
//   - skillstats depends on nothing of ours, only stdlib
//
// This split avoids a skilltool ↔ skill import cycle (skill already
// owns the loader; stats would have created a cycle if added there)
// and keeps the loader's startup cost untouched.
package skillstats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Entry is one .stats.jsonl row. Every field is intentionally a
// plain string — even ts — so the JSONL stays grep/jq-friendly and
// we don't bake in time.Time's marshalling quirks. The caller (in
// practice skilltool) formats ts as RFC3339 before passing it in.
//
// Empty optional fields are omitted from the output JSON so a row
// stays compact and readable.
type Entry struct {
	TS        string `json:"ts,omitempty"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

// Writer appends Entries to a JSONL file. Safe for concurrent use:
// every Append serialises into a single []byte and emits it with
// one write(2), so on POSIX with payload < PIPE_BUF (4 KiB on
// macOS/Linux) the kernel guarantees the row isn't torn even
// without explicit locking. A small in-process mutex serialises
// open/create on the first write so two concurrent first-writers
// don't race on file creation.
type Writer struct {
	path string

	mu      sync.Mutex
	created bool // tracks whether we've ensured the parent dir exists yet
}

// New constructs a writer for the given JSONL path. The path's
// parent directory is created lazily on first Append, not here —
// some callers construct the writer eagerly at startup before the
// home directory is known to exist.
func New(path string) *Writer { return &Writer{path: path} }

// Path returns the file the writer is configured to append to.
// Exposed so the CLI can show users where to look.
func (w *Writer) Path() string { return w.path }

// Append serialises e to one JSON line and appends it to the file.
// Lines are written with a trailing "\n". Failed writes return the
// underlying error; callers (skilltool) should treat stats writes as
// best-effort and not fail the user-visible tool call on a stats
// error.
func (w *Writer) Append(e Entry) error {
	if err := w.ensureParent(); err != nil {
		return err
	}

	// Marshal first, then a single Write — this is the load-bearing
	// invariant that keeps concurrent writers from interleaving
	// each other's bytes mid-row.
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("skillstats: marshal entry: %w", err)
	}
	// Defensive: refuse to write a payload that could plausibly
	// exceed PIPE_BUF. Stats rows are short by construction
	// (six small fields); anything > 3 KiB suggests an Entry
	// field got abused for prose. 3 KiB picks a margin below
	// the 4 KiB minimum across platforms.
	line := append(raw, '\n')
	if len(line) > 3072 {
		return fmt.Errorf("skillstats: entry too large (%d bytes); the JSONL contract relies on single-write atomicity under PIPE_BUF", len(line))
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("skillstats: open %s: %w", w.path, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("skillstats: write %s: %w", w.path, err)
	}
	return nil
}

// ensureParent creates the parent directory of w.path on first
// invocation. Subsequent calls are a no-op — we cache the success
// flag rather than stat'ing every time. A failed mkdir is retried
// next call (state stays "uncreated").
func (w *Writer) ensureParent() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.created {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("skillstats: mkdir %s: %w", filepath.Dir(w.path), err)
	}
	w.created = true
	return nil
}
