// file.go — content-addressed file checkpoint layer.
//
// Per-file undo/redo backed by sha256 blobs:
//
//   1. write/edit tools call SnapshotFile(path) just before
//      mutating the file. We hash the prior content, stash it
//      under <session-dir>/checkpoints/blobs/sha256/<aa>/<bb>/<rest>
//      (deduped — identical content = identical blob path), and
//      append one event to <session-dir>/checkpoints/index.jsonl.
//
//   2. tool finishes the write. Caller calls FinaliseSnapshot(path,
//      afterContent) which back-fills the after_sha on the same
//      event line. Two-phase so the event records BOTH "what was
//      there before" and "what's there now" — needed to detect
//      external modification on undo.
//
//   3. /undo walks the jsonl in reverse, skips already-undone
//      events, restores blob → working tree, writes a kind:"undo"
//      event marking the original as undone.
//
//   4. /redo walks forward, restoring blob → working tree for the
//      most recent undo. New write/edit calls truncate the redo
//      history (classic editor semantics).
//
// External-modification detection. Before restoring on undo, we
// re-hash the on-disk content and compare to the recorded
// after_sha. If they differ, the user (or an IDE / build tool)
// touched the file outside of seek; we refuse with a clear error
// unless --force.
//
// Binary / .seek path filtering. The skip rules live in
// shouldSkipPath. Binary detection sniffs the first 1 KiB for NUL
// bytes (the standard heuristic).

package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileEvent is one line of <session-dir>/checkpoints/index.jsonl.
// Wire format — fields are documented in PRD §3.2.
//
// "ZeroSHA" is the magic before/after_sha value for "file didn't
// exist". We deliberately use the literal string "0" (1 byte) to
// keep jsonl lines short; tools that parse the file should not
// rely on the value's length, only on equality with ZeroSHA.
type FileEvent struct {
	Seq        int64     `json:"seq"`
	TS         time.Time `json:"ts"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"` // create | edit | undo | redo | missing-blob
	BeforeSHA  string    `json:"before_sha"`
	AfterSHA   string    `json:"after_sha"`
	Tool       string    `json:"tool,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	// UndoOf points back to the seq of the event being undone /
	// redone. Set on kind=="undo" / kind=="redo".
	UndoOf int64 `json:"undo_of,omitempty"`
	// Binary marks paths we recognised as binary — index keeps the
	// audit trail but no blob exists.
	Binary bool `json:"binary,omitempty"`
}

// ZeroSHA represents "file did not exist" in BeforeSHA / AfterSHA.
const ZeroSHA = "0"

// fileIndex is the in-memory cache of the parsed index.jsonl. We
// load it lazily on first access (some Manager lifetimes never see
// a write/edit) and keep it in sync via direct mutation under m.mu.
type fileIndex struct {
	events []FileEvent
	// undone is the set of seqs that have a matching "undo" event
	// without a subsequent "redo". Used for /undo's "last
	// not-yet-undone event" search.
	undone map[int64]bool
	// nextSeq is the next seq to issue.
	nextSeq int64
}

// loadFileIndexLocked parses index.jsonl into m.fileState. Called
// once per Manager lifetime under m.mu when a file checkpoint
// operation needs the state.
func (m *Manager) loadFileIndexLocked() error {
	if m.fileLoaded {
		return nil
	}
	m.fileState = fileIndex{undone: make(map[int64]bool), nextSeq: 1}
	path, err := m.fileIndexPath()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		m.fileLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	for {
		var ev FileEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Skip malformed lines but keep going; corruption is
			// recoverable for everything else.
			m.sinkPlain.Warn(fmt.Sprintf("checkpoint file-index: skip malformed line: %v", err))
			continue
		}
		m.fileState.events = append(m.fileState.events, ev)
		if ev.Seq >= m.fileState.nextSeq {
			m.fileState.nextSeq = ev.Seq + 1
		}
		switch ev.Kind {
		case "undo":
			if ev.UndoOf > 0 {
				m.fileState.undone[ev.UndoOf] = true
			}
		case "redo":
			if ev.UndoOf > 0 {
				delete(m.fileState.undone, ev.UndoOf)
			}
		}
	}
	m.fileLoaded = true
	return nil
}

// appendFileEventLocked writes ev to index.jsonl AND adds it to the
// in-memory cache. Caller must hold m.mu.
func (m *Manager) appendFileEventLocked(ev FileEvent) error {
	path, err := m.fileIndexPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return err
	}
	m.fileState.events = append(m.fileState.events, ev)
	switch ev.Kind {
	case "undo":
		if ev.UndoOf > 0 {
			m.fileState.undone[ev.UndoOf] = true
		}
	case "redo":
		if ev.UndoOf > 0 {
			delete(m.fileState.undone, ev.UndoOf)
		}
	}
	return nil
}

// SnapshotFile records the pre-write state of `path`. Call this
// from write.Execute / edit.Execute BEFORE writing. ToolName /
// ToolCallID populate the event for audit. Empty BeforeSHA in the
// jsonl indicates a missing prior file (a fresh `write` of a new
// path).
//
// Returns nil on every success path AND on every safety-net failure:
// per PRD §5 "Snapshot failures don't block the write". The error
// channel is reserved for "manager misconfigured" which the caller
// should never see in production.
func (m *Manager) SnapshotFile(path, toolName, callID string) error {
	if m == nil || m.sessionID == "" || m.projectAbs == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		m.sinkPlain.Warn(fmt.Sprintf("checkpoint snapshot: abs(%s): %v", path, err))
		return nil
	}
	if m.shouldSkipPath(abs) {
		return nil
	}

	content, exists, isBinary, err := readPriorContent(abs)
	if err != nil {
		m.sinkPlain.Warn(fmt.Sprintf("checkpoint snapshot: read %s: %v", abs, err))
		return nil
	}

	var beforeSHA string
	if !exists {
		beforeSHA = ZeroSHA
	} else if isBinary {
		// Record the event but don't store the blob — binary blobs
		// blow up disk usage with little benefit (we can't safely
		// diff them anyway). Per PRD §3.2 acceptance #9.
		beforeSHA = hashBytes(content)
	} else {
		beforeSHA = hashBytes(content)
		if err := m.storeBlobLocked(beforeSHA, content); err != nil {
			m.sinkPlain.Warn(fmt.Sprintf("checkpoint snapshot: blob %s: %v", path, err))
			return nil
		}
	}

	kind := "edit"
	if !exists {
		kind = "create"
	}

	m.mu.Lock()
	if err := m.loadFileIndexLocked(); err != nil {
		m.mu.Unlock()
		m.sinkPlain.Warn(fmt.Sprintf("checkpoint snapshot: load index: %v", err))
		return nil
	}
	seq := m.fileState.nextSeq
	// A new write/edit invalidates any redo history for this path —
	// classic editor undo semantics. We materialise that as: when
	// the model writes/edits NOW, all "undone" events for this same
	// path become permanently undone (they can never be redone).
	//
	// Implementation: drop those seqs from m.fileState.undone. The
	// jsonl line stays in place (audit), but Redo's "find a
	// recently-undone event" search will skip them because they're
	// no longer in undone[].
	for s := range m.fileState.undone {
		// Find the original event by seq; if its path matches,
		// drop it from the redo pool.
		for _, e := range m.fileState.events {
			if e.Seq == s && e.Path == abs {
				delete(m.fileState.undone, s)
				break
			}
		}
	}
	ev := FileEvent{
		Seq:        seq,
		TS:         time.Now().UTC(),
		Path:       abs,
		Kind:       kind,
		BeforeSHA:  beforeSHA,
		AfterSHA:   "", // filled in by FinaliseSnapshot
		Tool:       toolName,
		ToolCallID: callID,
		Binary:     isBinary,
	}
	// Bump nextSeq BEFORE the append so concurrent SnapshotFile
	// callers under the mutex still see monotonically-increasing
	// seqs even if appendFileEventLocked is slow.
	m.fileState.nextSeq = seq + 1
	if err := m.appendFileEventLocked(ev); err != nil {
		m.mu.Unlock()
		m.sinkPlain.Warn(fmt.Sprintf("checkpoint snapshot: append: %v", err))
		return nil
	}
	m.mu.Unlock()
	m.emit(CheckpointEvent{Kind: "file", Path: abs, Seq: seq})
	return nil
}

// FinaliseSnapshot records the post-write sha256 onto the most
// recent snapshot event for `path` that hasn't been finalised yet.
// Tools call this AFTER the write succeeds. Failure is non-fatal:
// undo will still work on the before_sha; only the external-mod
// detection degrades to "always allow" for the missing after_sha.
func (m *Manager) FinaliseSnapshot(path string, after []byte) error {
	if m == nil || m.sessionID == "" || m.projectAbs == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadFileIndexLocked(); err != nil {
		return nil
	}
	// Find the most recent unfinalised event for this path. We walk
	// backwards because that's where it will be.
	for i := len(m.fileState.events) - 1; i >= 0; i-- {
		ev := &m.fileState.events[i]
		if ev.Path != abs {
			continue
		}
		if ev.Kind != "create" && ev.Kind != "edit" {
			continue
		}
		if ev.AfterSHA != "" {
			break // already finalised — nothing to do
		}
		afterSHA := hashBytes(after)
		ev.AfterSHA = afterSHA
		// Best-effort: rewrite the index file so the on-disk value
		// matches in-memory. If this fails, we still keep the
		// in-memory copy for the rest of this session.
		if err := m.rewriteFileIndexLocked(); err != nil {
			m.sinkPlain.Warn(fmt.Sprintf("checkpoint finalise: rewrite index: %v", err))
		}
		// Store the after blob too, so redo (after an undo) has
		// content to restore.
		if !ev.Binary && afterSHA != ZeroSHA {
			if err := m.storeBlobLocked(afterSHA, after); err != nil {
				m.sinkPlain.Warn(fmt.Sprintf("checkpoint finalise: store after-blob: %v", err))
			}
		}
		break
	}
	return nil
}

// rewriteFileIndexLocked rewrites index.jsonl with the current
// in-memory event slice. Used after FinaliseSnapshot back-fills
// after_sha. Atomic via tmp+rename.
func (m *Manager) rewriteFileIndexLocked() error {
	path, err := m.fileIndexPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range m.fileState.events {
		if err := enc.Encode(e); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// blobPath maps sha256 → on-disk path. Two-level sharding (aa/bb)
// keeps inode counts manageable on large blob counts.
func (m *Manager) blobPath(sha string) (string, error) {
	if sha == "" || sha == ZeroSHA {
		return "", fmt.Errorf("checkpoint: blobPath on empty / zero sha")
	}
	root, err := m.blobsDir()
	if err != nil {
		return "", err
	}
	if len(sha) < 4 {
		return "", fmt.Errorf("checkpoint: sha too short: %q", sha)
	}
	return filepath.Join(root, sha[:2], sha[2:4], sha[4:]), nil
}

// storeBlobLocked writes content under blobs/sha256/<aa>/<bb>/<rest>.
// Dedup via filesystem: if the file already exists, we leave it
// alone (it has the same hash, hence the same content).
//
// Note: despite the "Locked" suffix this method does NOT need
// m.mu held — the content-addressable layout means two concurrent
// writers of the same blob produce byte-identical results, and
// the unique-tmp-name strategy lets parallel WriteFile + Rename
// races resolve safely. Naming kept consistent with the rest of
// the package because the surrounding state IS lock-protected.
func (m *Manager) storeBlobLocked(sha string, content []byte) error {
	if sha == ZeroSHA || sha == "" {
		return nil
	}
	bp, err := m.blobPath(sha)
	if err != nil {
		return err
	}
	if _, err := os.Stat(bp); err == nil {
		// Already exists — dedup hit.
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(bp), 0o700); err != nil {
		return err
	}
	// Atomic: tmp + rename so a partial write never appears as a
	// "good" blob with truncated content. The tmp filename includes
	// the PID + a goroutine-unique counter so two parallel writers
	// of the SAME hash (which can happen — identical content from
	// concurrent edits) don't race on a shared tmp file.
	tmp, err := os.CreateTemp(filepath.Dir(bp), filepath.Base(bp)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	// Rename. A concurrent writer may already have created bp;
	// that's fine because the content is identical. On any rename
	// error, fall back to "does the target exist now?" to absorb
	// the race.
	if err := os.Rename(tmpPath, bp); err != nil {
		if _, statErr := os.Stat(bp); statErr == nil {
			// Another goroutine landed it first. Drop our copy.
			os.Remove(tmpPath)
			return nil
		}
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// readBlobLocked retrieves a previously stored blob. Returns the
// "missing blob" sentinel error if the on-disk file is gone (user
// rm'd the checkpoints dir, or session was --keep-checkpoints'd
// across versions and the layout changed).
func (m *Manager) readBlob(sha string) ([]byte, error) {
	if sha == ZeroSHA {
		return nil, nil
	}
	bp, err := m.blobPath(sha)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(bp)
}

// UndoOptions modulates Undo behaviour.
type UndoOptions struct {
	Path  string // empty → undo last across all paths
	Count int    // 0 or 1 → one step; > 1 → that many steps
	Force bool   // skip external-mod check
}

// UndoResult reports one undo step's outcome.
type UndoResult struct {
	Event     FileEvent
	Restored  string // human-readable: "restored content (123 bytes)"
	Path      string
	BeforeSHA string
}

// Undo reverts the most-recent matching write/edit. Per PRD §3.2.
// Returns the events undone in order. A partial undo (some steps
// failed) returns both the completed steps AND the error so the
// caller can report "did N/M, failed because Y".
func (m *Manager) Undo(opts UndoOptions) ([]UndoResult, error) {
	if m == nil || m.sessionID == "" {
		return nil, fmt.Errorf("checkpoint: manager not configured")
	}
	steps := opts.Count
	if steps <= 0 {
		steps = 1
	}
	var done []UndoResult
	for i := 0; i < steps; i++ {
		r, err := m.undoOne(opts.Path, opts.Force)
		if err != nil {
			return done, err
		}
		done = append(done, r)
	}
	return done, nil
}

// undoOne locates and reverts the single most-recent matching event.
func (m *Manager) undoOne(path string, force bool) (UndoResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadFileIndexLocked(); err != nil {
		return UndoResult{}, err
	}

	var match *FileEvent
	for i := len(m.fileState.events) - 1; i >= 0; i-- {
		ev := &m.fileState.events[i]
		if ev.Kind != "create" && ev.Kind != "edit" {
			continue
		}
		if m.fileState.undone[ev.Seq] {
			continue
		}
		if path != "" {
			abs, _ := filepath.Abs(path)
			if ev.Path != abs {
				continue
			}
		}
		match = ev
		break
	}
	if match == nil {
		if path != "" {
			return UndoResult{}, fmt.Errorf("no undoable event for %s", path)
		}
		return UndoResult{}, fmt.Errorf("no undoable event")
	}

	if match.Binary {
		return UndoResult{}, fmt.Errorf("cannot undo binary-file edit: %s (no blob stored)", match.Path)
	}

	// External-mod detection. If after_sha is empty (FinaliseSnapshot
	// never fired — write failed or tool crashed before back-fill),
	// we bypass the check: there's nothing meaningful to compare
	// against.
	if match.AfterSHA != "" && !force {
		actual, err := hashFile(match.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return UndoResult{}, fmt.Errorf("undo: read %s: %w", match.Path, err)
		}
		// File-not-found case: if after_sha != ZeroSHA, the file
		// went missing externally. Refuse without --force.
		if errors.Is(err, os.ErrNotExist) {
			if match.AfterSHA != ZeroSHA {
				return UndoResult{}, fmt.Errorf("undo: file modified externally since last seek edit (file missing) — re-run with --force to override: %s", match.Path)
			}
			actual = ZeroSHA
		}
		if actual != match.AfterSHA {
			return UndoResult{}, fmt.Errorf("undo: file modified externally since last seek edit — re-run with --force to override: %s", match.Path)
		}
	}

	// Restore the before content.
	if match.BeforeSHA == ZeroSHA {
		// File did not exist before — delete it now.
		if err := os.Remove(match.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return UndoResult{}, fmt.Errorf("undo: remove %s: %w", match.Path, err)
		}
	} else {
		content, err := m.readBlob(match.BeforeSHA)
		if err != nil {
			return UndoResult{}, fmt.Errorf("undo: read blob: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(match.Path), 0o755); err != nil {
			return UndoResult{}, fmt.Errorf("undo: mkdir: %w", err)
		}
		if err := os.WriteFile(match.Path, content, 0o644); err != nil {
			return UndoResult{}, fmt.Errorf("undo: write %s: %w", match.Path, err)
		}
	}

	// Emit the undo event.
	undoEv := FileEvent{
		Seq:    m.fileState.nextSeq,
		TS:     time.Now().UTC(),
		Path:   match.Path,
		Kind:   "undo",
		UndoOf: match.Seq,
	}
	m.fileState.nextSeq++
	if err := m.appendFileEventLocked(undoEv); err != nil {
		return UndoResult{}, fmt.Errorf("undo: append event: %w", err)
	}

	return UndoResult{
		Event:     *match,
		Path:      match.Path,
		BeforeSHA: match.BeforeSHA,
		Restored:  describeUndo(match),
	}, nil
}

// RedoOptions modulates Redo behaviour.
type RedoOptions struct {
	Path  string
	Count int
	Force bool
}

// RedoResult reports one redo outcome.
type RedoResult struct {
	Event    FileEvent
	Path     string
	Restored string
}

// Redo re-applies the most recently undone write/edit (per path
// when Path is set, global when not). Reverse of Undo.
func (m *Manager) Redo(opts RedoOptions) ([]RedoResult, error) {
	if m == nil || m.sessionID == "" {
		return nil, fmt.Errorf("checkpoint: manager not configured")
	}
	steps := opts.Count
	if steps <= 0 {
		steps = 1
	}
	var done []RedoResult
	for i := 0; i < steps; i++ {
		r, err := m.redoOne(opts.Path, opts.Force)
		if err != nil {
			return done, err
		}
		done = append(done, r)
	}
	return done, nil
}

func (m *Manager) redoOne(path string, force bool) (RedoResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadFileIndexLocked(); err != nil {
		return RedoResult{}, err
	}
	// Walk forward for the OLDEST undone event matching the path —
	// classic editor redo: undoing twice then redoing once should
	// redo the first undo (i.e. the deepest one).
	//
	// Implementation: iterate undone[] but order by seq.
	var seqs []int64
	for s := range m.fileState.undone {
		seqs = append(seqs, s)
	}
	// In-place selection sort — undone size is bounded (cap of
	// edits per session) and we want stable readable output.
	for i := 0; i < len(seqs); i++ {
		for j := i + 1; j < len(seqs); j++ {
			if seqs[j] < seqs[i] {
				seqs[i], seqs[j] = seqs[j], seqs[i]
			}
		}
	}

	var match *FileEvent
	for _, s := range seqs {
		for i := range m.fileState.events {
			if m.fileState.events[i].Seq != s {
				continue
			}
			ev := &m.fileState.events[i]
			if path != "" {
				abs, _ := filepath.Abs(path)
				if ev.Path != abs {
					continue
				}
			}
			match = ev
			break
		}
		if match != nil {
			break
		}
	}
	if match == nil {
		if path != "" {
			return RedoResult{}, fmt.Errorf("no redoable event for %s", path)
		}
		return RedoResult{}, fmt.Errorf("no redoable event")
	}

	if match.Binary {
		return RedoResult{}, fmt.Errorf("cannot redo binary-file edit: %s", match.Path)
	}
	if match.AfterSHA == "" {
		return RedoResult{}, fmt.Errorf("redo: original write did not record after_sha (tool crashed before finalise?) — manual restoration required")
	}

	// External-mod detection mirrors undo: the on-disk content
	// should currently match BEFORE the redo (i.e. what undo
	// restored). before_sha is the right comparison target here.
	if !force {
		actual, err := hashFile(match.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RedoResult{}, fmt.Errorf("redo: read %s: %w", match.Path, err)
		}
		if errors.Is(err, os.ErrNotExist) {
			actual = ZeroSHA
		}
		if actual != match.BeforeSHA {
			return RedoResult{}, fmt.Errorf("redo: file modified externally since last seek edit — re-run with --force to override: %s", match.Path)
		}
	}

	// Restore the after content.
	if match.AfterSHA == ZeroSHA {
		if err := os.Remove(match.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return RedoResult{}, fmt.Errorf("redo: remove %s: %w", match.Path, err)
		}
	} else {
		content, err := m.readBlob(match.AfterSHA)
		if err != nil {
			return RedoResult{}, fmt.Errorf("redo: read blob: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(match.Path), 0o755); err != nil {
			return RedoResult{}, fmt.Errorf("redo: mkdir: %w", err)
		}
		if err := os.WriteFile(match.Path, content, 0o644); err != nil {
			return RedoResult{}, fmt.Errorf("redo: write %s: %w", match.Path, err)
		}
	}

	redoEv := FileEvent{
		Seq:    m.fileState.nextSeq,
		TS:     time.Now().UTC(),
		Path:   match.Path,
		Kind:   "redo",
		UndoOf: match.Seq,
	}
	m.fileState.nextSeq++
	if err := m.appendFileEventLocked(redoEv); err != nil {
		return RedoResult{}, fmt.Errorf("redo: append event: %w", err)
	}
	return RedoResult{
		Event:    *match,
		Path:     match.Path,
		Restored: describeRedo(match),
	}, nil
}

// FileEvents returns a snapshot of all file events. Mostly for the
// `seek checkpoint list` JSON output and tests.
func (m *Manager) FileEvents() ([]FileEvent, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadFileIndexLocked(); err != nil {
		return nil, err
	}
	cp := make([]FileEvent, len(m.fileState.events))
	copy(cp, m.fileState.events)
	return cp, nil
}

// shouldSkipPath honours the PRD's "don't snapshot" rules:
// .seek/ subtree and binary files.
//
// The binary check is content-based, not extension-based; the
// caller does its own read so we don't double-read.
//
// shouldSkipPath is called BEFORE we read the file — it only
// catches path-based skip rules. The binary check fires inside
// readPriorContent (returns isBinary=true).
func (m *Manager) shouldSkipPath(abs string) bool {
	// .seek/ directory anywhere on the path. PRD §3.2: skill /
	// memory / session storage manages its own state.
	if containsSeekDir(abs) {
		return true
	}
	return false
}

// containsSeekDir reports whether `abs` lives under a directory
// component literally named ".seek".
func containsSeekDir(abs string) bool {
	abs = filepath.ToSlash(abs)
	parts := strings.Split(abs, "/")
	for _, p := range parts {
		if p == ".seek" {
			return true
		}
	}
	return false
}

// readPriorContent loads the file at `path` if it exists. Reports:
//   - content: file bytes (nil if missing)
//   - exists:  true if the file was on disk
//   - binary:  true if the first 1 KiB contains a NUL byte
//   - err:     fatal I/O error
func readPriorContent(path string) (content []byte, exists, binary bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	return b, true, bytes.IndexByte(head, 0) >= 0, nil
}

// hashBytes computes sha256 of `b` and returns the lowercase hex.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFile reads `path` and hashes it. Used by external-mod
// detection on Undo.
func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(b), nil
}

func describeUndo(ev *FileEvent) string {
	switch {
	case ev.BeforeSHA == ZeroSHA:
		return fmt.Sprintf("removed %s (file did not exist before)", ev.Path)
	default:
		return fmt.Sprintf("restored %s to pre-%s state", ev.Path, ev.Kind)
	}
}

func describeRedo(ev *FileEvent) string {
	switch {
	case ev.AfterSHA == ZeroSHA:
		return fmt.Sprintf("re-removed %s", ev.Path)
	default:
		return fmt.Sprintf("re-applied %s to %s", ev.Kind, ev.Path)
	}
}

// ----- internal helpers used by other files in this package -----

// _ keeps the unused-import linter happy when sync isn't needed
// in this file alone (the file-state mutex lives on Manager).
var _ = sync.Mutex{}
