package subagent

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
)

// event is the on-disk record of one lifecycle transition. See
// docs/prd/feature-subagent.md §3.4 for the per-event-kind schema.
//
// The Kind field is exported as "event" in JSON so the on-disk
// representation matches the PRD examples verbatim. New event kinds
// added in future must keep the (Kind, SubSid, TS) triple required;
// other fields are kind-specific and may be optional.
type event struct {
	Kind         string    `json:"event"`
	SubSid       string    `json:"sub_sid"`
	TS           time.Time `json:"ts"`
	ParentSid    string    `json:"parent_sid,omitempty"`
	ParentTurn   int       `json:"parent_turn,omitempty"`
	Type         Type      `json:"type,omitempty"`
	Description  string    `json:"description,omitempty"`
	WorktreePath string    `json:"worktree_path,omitempty"`
	Tokens       *Tokens   `json:"tokens,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

// indexLock serialises concurrent append callers to a single index
// file. Concurrent Spawn calls on a Manager (LLM emitting parallel
// `agent` tool calls in one turn) write to the same subagents.jsonl;
// without a mutex, two appends could interleave and corrupt a line.
//
// One lock per process is enough because the file is project-scoped
// and a single seek process owns one project at a time. Cross-process
// races (two seek instances on the same project) fall back to OS
// O_APPEND atomicity for single-write() syscalls (we batch the JSON
// line + newline into one Write so this holds; see appendEvent).
var indexLock sync.Mutex

// appendEvent writes one event to path. Creates the file (and any
// missing parent directories) lazily. Each line is a single
// json.Marshal + "\n", written in one Write call so O_APPEND's
// per-syscall atomicity prevents cross-process interleave.
//
// Lazy directory creation: callers Spawn before the first event
// without ensuring ProjectDir exists; we MkdirAll here so the index
// path "just works" for fresh projects. The created dirs match the
// permission model of internal/paths helpers (0o755).
func appendEvent(path string, e event) error {
	if e.Kind == "" || e.SubSid == "" {
		return fmt.Errorf("subagent: appendEvent: kind and sub_sid required")
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("subagent: marshal event: %w", err)
	}
	indexLock.Lock()
	defer indexLock.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("subagent: mkdir index parent: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("subagent: open index: %w", err)
	}
	defer f.Close()
	// Single Write so O_APPEND's syscall atomicity protects against
	// interleaving with another process appending concurrently.
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("subagent: write index event: %w", err)
	}
	return nil
}

// readEvents returns every event line in path in file order. ENOENT
// returns an empty slice + nil error (fresh project has no index).
// Other I/O errors propagate.
//
// Malformed lines (not valid JSON) are SKIPPED with no error — this
// matches plan-mode's reconstruct.go tolerance: a single bad event
// shouldn't crash list / panel rendering. The skipped count isn't
// surfaced; tests can wedge invariants via the folder if needed.
func readEvents(path string) ([]event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("subagent: open index: %w", err)
	}
	defer f.Close()

	var out []event
	scanner := bufio.NewScanner(f)
	// Increase buffer ceiling — descriptions / future tokens
	// payloads can push individual lines beyond bufio default 64 KB.
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // tolerate malformed lines; see method doc
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("subagent: scan index: %w", err)
	}
	return out, nil
}

// foldEvents collapses the event stream into the current Subagent
// state per sub_sid, per docs/prd/feature-subagent.md §3.4 folding
// rules:
//
//   - The `started` event seeds immutable fields (ParentSid, Type,
//     Description, WorktreePath, StartedAt).
//   - Subsequent events update Status; terminal events (completed /
//     failed / killed / orphaned / promoted) also set EndedAt,
//     Tokens (if present), Reason (if present).
//   - Events for unknown sub_sid (no preceding started) are skipped
//     — the index is logically inconsistent there.
//
// The returned slice is sorted by StartedAt descending (newest
// first), which is what `/agents` and `seek subagent list` want
// for default presentation.
func foldEvents(events []event) []Subagent {
	bySid := make(map[string]*Subagent, len(events))
	order := make([]string, 0, len(events))
	for _, e := range events {
		if e.Kind == "started" {
			if _, exists := bySid[e.SubSid]; exists {
				// Duplicate started for the same sub_sid is a
				// programmer error (sub_sid space collision).
				// Keep the first; ignore the dup.
				continue
			}
			bySid[e.SubSid] = &Subagent{
				SubSid:       e.SubSid,
				ParentSid:    e.ParentSid,
				ParentTurn:   e.ParentTurn,
				Type:         e.Type,
				Description:  e.Description,
				WorktreePath: e.WorktreePath,
				StartedAt:    e.TS,
				Status:       StatusActive,
			}
			order = append(order, e.SubSid)
			continue
		}
		s := bySid[e.SubSid]
		if s == nil {
			// Terminal event for a sub_sid we never saw started.
			// Skip — see method doc.
			continue
		}
		switch e.Kind {
		case "completed":
			s.Status = StatusCompleted
			s.EndedAt = e.TS
			if e.Tokens != nil {
				s.Tokens = *e.Tokens
			}
		case "failed":
			s.Status = StatusFailed
			s.EndedAt = e.TS
			s.Reason = e.Reason
			if e.Tokens != nil {
				s.Tokens = *e.Tokens
			}
		case "killed":
			s.Status = StatusKilled
			s.EndedAt = e.TS
		case "orphaned":
			s.Status = StatusOrphaned
			s.EndedAt = e.TS
		case "promoted":
			s.Status = StatusPromoted
			s.EndedAt = e.TS
		default:
			// Unknown event kind — ignore. A future seek version
			// might emit kinds we don't recognise; the safe
			// behaviour is "the current state we already have".
		}
	}
	out := make([]Subagent, 0, len(order))
	for _, sid := range order {
		out = append(out, *bySid[sid])
	}
	// Newest-first by StartedAt; stable so duplicate start times
	// preserve insertion order.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// OrphanRecover scans path for `started` events whose sub_sid has
// no terminal event, and appends a `orphaned` event for each. This
// is the seek-startup hook called on resume to clean up subagents
// whose owning process crashed (see PRD §8 risk "resume 时 zombie 子").
//
// Returns the list of sub_sids that were marked orphaned. Empty
// list + nil error is the steady-state happy path.
//
// Safe to call repeatedly — once a sub_sid has an `orphaned` event,
// the folder sees a terminal state and no further orphan event is
// emitted for that sub_sid.
func OrphanRecover(path string) ([]string, error) {
	events, err := readEvents(path)
	if err != nil {
		return nil, err
	}
	folded := foldEvents(events)
	var orphaned []string
	now := time.Now().UTC()
	for _, s := range folded {
		if s.Status == StatusActive {
			if err := appendEvent(path, event{
				Kind:   "orphaned",
				SubSid: s.SubSid,
				TS:     now,
			}); err != nil {
				return orphaned, fmt.Errorf("subagent: orphan recover %s: %w", s.SubSid, err)
			}
			orphaned = append(orphaned, s.SubSid)
		}
	}
	return orphaned, nil
}
