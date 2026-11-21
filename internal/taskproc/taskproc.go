// Package taskproc is the on-disk registry for lifetime-detached task
// processes (v8 柱 V1, docs/prd/feature-task-lifetime.md). It is the
// deliberate sibling — NOT a replacement — of internal/bgjob:
//
//   - bgjob is session-scoped, lives in memory, and its Shutdown kills
//     every survivor. Its life contract is "dies with the seek process".
//   - taskproc is process-detached: the task outlives the seek process
//     that started it, so its ONLY durable home is
//     $SEEK_HOME/tasks/<id>.jsonl plus <id>.log. Nothing here holds
//     runtime state — `seek ps` reads the record and folds a status from
//     it (facts on disk, status computed), which is the same discipline
//     AGENTS.md applies to plan state.
//
// The record is an append-only JSONL fact log, not a mutated JSON
// object: three independent writers contribute facts (the spawner
// writes `start`, the wrapper writes `exit`, `seek kill` writes
// `killed`) and O_APPEND keeps them from clobbering each other. Folding
// is order-independent, so a late `killed` never erases a real
// `exit_code`.
package taskproc

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Event kinds. Wire format: the strings are written by one seek process
// and parsed by another, so they are contract, not prose.
const (
	KindStart  = "start"
	KindExit   = "exit"
	KindKilled = "killed"
)

// Status is a task's lifecycle state, FOLDED from the record plus one
// live process probe. Never persisted.
type Status string

const (
	// StatusStarting is the sub-second window between the spawner writing
	// the record and the wrapper reporting its pid. It exists so that
	// window never reads as `lost` — misreporting a live task as dead is
	// the one direction an operator cannot recover from.
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusExited   Status = "exited" // exit code 0
	StatusFailed   Status = "failed" // exit code != 0
	StatusKilled   Status = "killed" // `seek kill` reached it
	StatusZombie   Status = "zombie" // dead, waiting for its parent to reap
	StatusLost     Status = "lost"   // gone with no exit fact (OOM / external SIGKILL)
)

// Terminal reports whether the status is final (no further transitions
// are expected, so `attach` may stop following).
func (s Status) Terminal() bool {
	switch s {
	case StatusExited, StatusFailed, StatusKilled, StatusLost:
		return true
	}
	return false
}

// Probe is the outcome of asking the OS "is this pid alive".
type Probe int

const (
	ProbeGone Probe = iota
	ProbeAlive
	ProbeZombie
)

// ProbeFunc answers the liveness question for one pid. Injected
// everywhere so tests can present a fake/missing/zombie process table
// without spawning anything.
type ProbeFunc func(pid int) Probe

// Event is one fact line in the record.
type Event struct {
	Kind       string     `json:"kind"`
	ID         string     `json:"id,omitempty"`
	PID        int        `json:"pid,omitempty"`
	Command    string     `json:"command,omitempty"`
	CWD        string     `json:"cwd,omitempty"`
	Session    string     `json:"session,omitempty"`
	Project    string     `json:"project,omitempty"`
	StartedAt  time.Time  `json:"started_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	// Reason explains a failure that never produced a process (spawn or
	// log-open error). Human-facing only; never parsed.
	Reason string `json:"reason,omitempty"`
}

// Task is the folded view `seek ps` / `seek attach` / `seek kill` work
// from.
type Task struct {
	ID         string
	PID        int
	Command    string
	CWD        string
	Session    string
	Project    string
	StartedAt  time.Time
	ExitCode   *int
	FinishedAt *time.Time
	Signal     string
	// Reason carries a spawn-side failure explanation (e.g. the wrapper
	// binary could not be started). Empty for normal exits.
	Reason string
	Status Status

	// LogPath is where the task's stdout+stderr live. Filled by the
	// store, not the record (it is derivable, so it is not stored).
	LogPath string
}

// Elapsed is the run duration: live while running (measured to end),
// frozen at the finish instant once done — so `ps` doesn't report an
// ever-growing elapsed for a task that finished yesterday.
func (t Task) Elapsed(end time.Time) time.Duration {
	done := end
	if t.FinishedAt != nil && !t.FinishedAt.IsZero() {
		done = *t.FinishedAt
	}
	if t.StartedAt.IsZero() || done.Before(t.StartedAt) {
		return 0
	}
	return done.Sub(t.StartedAt)
}

// ErrNotFound is returned when no record exists for an id. Distinguished
// from a corrupt record so callers can say "no such task" instead of
// leaking a filesystem error.
var ErrNotFound = errors.New("taskproc: no such task")

// Store is the $SEEK_HOME/tasks directory. Zero-value is not usable;
// build one with Open (production) or OpenAt (tests).
type Store struct {
	dir string
	// Now is injectable so id minting and elapsed folding are
	// deterministic under test.
	Now func() time.Time
}

// Open resolves the production store root ($SEEK_HOME/tasks). It does
// not create the directory — first append does.
func Open() (*Store, error) {
	dir, err := paths.TasksDir()
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir, Now: time.Now}, nil
}

// OpenAt is the test/embedded constructor. dir is the tasks directory
// itself (i.e. <home>/tasks) — see OpenAtHome for the friendlier form.
func OpenAt(dir string) *Store { return &Store{dir: dir, Now: time.Now} }

// OpenAtHome builds a store for an explicit SEEK_HOME root, mirroring
// paths.TasksDir without touching the environment. Tests use this with
// t.TempDir() so nothing lands in the real ~/.seek (c_rzsvk5 lesson).
func OpenAtHome(home string) *Store {
	return OpenAt(filepath.Join(home, "tasks"))
}

// Dir returns the store root.
func (s *Store) Dir() string { return s.dir }

// HomeDir is the SEEK_HOME a spawned wrapper must be told about, so it
// appends its exit fact to the same registry (the wrapper's environment
// is otherwise scrubbed of SEEK_* — see docs/prd/feature-task-lifetime.md
// §1.2).
func (s *Store) HomeDir() string { return filepath.Dir(s.dir) }

// RecordPath is <dir>/<id>.jsonl.
func (s *Store) RecordPath(id string) string { return filepath.Join(s.dir, id+".jsonl") }

// LogPath is <dir>/<id>.log.
func (s *Store) LogPath(id string) string { return filepath.Join(s.dir, id+".log") }

// NewID mints a sortable, collision-resistant id: t-<unix>-<6 hex>.
// Sortable matters because `seek ps` orders by it without parsing
// timestamps out of every record.
func (s *Store) NewID() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("taskproc: mint id: %w", err)
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	return fmt.Sprintf("t-%d-%s", now().Unix(), hex.EncodeToString(b[:])), nil
}

// ProbeGoneFunc is the "nobody is running" oracle for metadata-only
// reads (a record just written, a listing that must not depend on the
// live process table).
func ProbeGoneFunc(int) Probe { return ProbeGone }

// Append adds one fact line. O_APPEND makes the write atomic for lines
// well under PIPE_BUF, which is why the record is line-oriented rather
// than a rewritten JSON document: three writers never need a lock.
func (s *Store) Append(id string, ev Event) error {
	if id == "" {
		return errors.New("taskproc: Append needs an ID")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("taskproc: mkdir %s: %w", s.dir, err)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("taskproc: marshal event: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(s.RecordPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("taskproc: open record %s: %w", id, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("taskproc: append record %s: %w", id, err)
	}
	return nil
}

// Read folds one record. probe is the liveness oracle for the
// still-running case; pass ProbeGoneFunc for a metadata-only read.
func (s *Store) Read(id string, probe ProbeFunc) (Task, error) {
	evs, err := s.readEvents(id)
	if err != nil {
		return Task{}, err
	}
	return fold(id, s.LogPath(id), evs, probe, s.now())
}

// List folds every record in the store, newest first. An unreadable or
// corrupt record is skipped rather than failing the whole listing —
// `seek ps` is a diagnostic surface and must not be taken down by one
// bad file.
func (s *Store) List(probe ProbeFunc) ([]Task, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("taskproc: read %s: %w", s.dir, err)
	}
	seen := map[string]bool{}
	var out []Task
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if seen[id] {
			continue
		}
		seen[id] = true
		t, err := s.Read(id, probe)
		if err != nil {
			continue // corrupt / half-written: skip, don't fail the listing
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// readEvents parses the record. A trailing partial line (writer mid
// append) is ignored; a malformed complete line is skipped, matching how
// the other append-only ledgers in this repo read.
func (s *Store) readEvents(id string) ([]Event, error) {
	f, err := os.Open(s.RecordPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("taskproc: open record %s: %w", id, err)
	}
	defer f.Close()
	var evs []Event
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && err != nil {
			break // partial trailing line
		}
		if len(line) > 1 {
			var ev Event
			if json.Unmarshal(line[:len(line)-1], &ev) == nil && ev.Kind != "" {
				evs = append(evs, ev)
			}
		}
		if err != nil {
			break
		}
	}
	if len(evs) == 0 {
		return nil, ErrNotFound
	}
	return evs, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// fold turns facts into a Task with a computed status. Rule order is
// deliberate: a real `exit` fact outranks `killed` (the process told us
// how it ended), and both outrank a live probe — a pid can be recycled
// after the process is gone.
func fold(id, logPath string, evs []Event, probe ProbeFunc, now time.Time) (Task, error) {
	t := Task{ID: id, LogPath: logPath}
	var start, exit, killed *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindStart:
			if start == nil {
				start = &evs[i]
			}
			// The spawner writes one `start` with the metadata before
			// forking and a second with the pid afterwards, so a task
			// that exits instantly cannot beat its own start line into
			// the record. Last non-zero pid wins; metadata stays first.
			if evs[i].PID != 0 {
				t.PID = evs[i].PID
			}
		case KindExit:
			exit = &evs[i]
		case KindKilled:
			killed = &evs[i]
		}
	}
	if start == nil {
		return Task{}, fmt.Errorf("%w: %s has no start fact", ErrNotFound, id)
	}
	t.Command = start.Command
	t.CWD = start.CWD
	t.Session = start.Session
	t.Project = start.Project
	t.StartedAt = start.StartedAt

	switch {
	case exit != nil:
		t.ExitCode = exit.ExitCode
		t.Reason = exit.Reason
		t.FinishedAt = finishTime(exit, now)
		if exit.ExitCode != nil && *exit.ExitCode != 0 {
			t.Status = StatusFailed
		} else {
			t.Status = StatusExited
		}
		return t, nil
	case killed != nil:
		t.FinishedAt = finishTime(killed, now)
		t.Signal = killed.Signal
		t.Status = StatusKilled
		return t, nil
	}

	// No terminal fact: the wrapper may not have reported its pid yet,
	// or the process is alive, a zombie, or gone with no record of why.
	// PID==0 must read as `starting`, never as `lost` — the spawn window
	// is microseconds wide and reporting a live task as dead is the one
	// direction an operator cannot recover from.
	if t.PID == 0 {
		t.Status = StatusStarting
		return t, nil
	}
	if probe == nil {
		// No oracle supplied: ask the OS rather than assuming death.
		// Assuming "gone" here would report every live task as `lost`.
		probe = ProbePID
	}
	switch probe(t.PID) {
	case ProbeAlive:
		t.Status = StatusRunning
	case ProbeZombie:
		t.Status = StatusZombie
	default:
		// Gone without a terminal fact: OOM-killed or SIGKILLed from
		// outside — the exit code is unknowable, which is exactly what
		// `lost` says.
		t.Status = StatusLost
	}
	return t, nil
}

func finishTime(ev *Event, now time.Time) *time.Time {
	if ev.FinishedAt != nil && !ev.FinishedAt.IsZero() {
		return ev.FinishedAt
	}
	return &now
}

// CopyLog appends the log bytes past offset to w and reports the new
// offset. A missing log is not an error (the task may not have written
// anything yet) — it just yields offset unchanged.
func (s *Store) CopyLog(w io.Writer, id string, offset int64) (int64, error) {
	f, err := os.Open(s.LogPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return offset, nil
		}
		return offset, fmt.Errorf("taskproc: open log %s: %w", id, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return offset, fmt.Errorf("taskproc: stat log %s: %w", id, err)
	}
	if st.Size() < offset {
		offset = 0 // truncated/rewritten: re-read from the top
	}
	if st.Size() == offset {
		return offset, nil
	}
	n, err := io.Copy(w, io.NewSectionReader(f, offset, st.Size()-offset))
	offset += n
	if err != nil {
		return offset, err
	}
	return offset, nil
}
