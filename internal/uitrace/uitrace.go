// Package uitrace appends one JSONL row per user-interaction fact in the
// TUI: an injection accepted at a turn boundary, a message queued for
// round end, a steer that cancelled the round, a withdrawal, an Esc, a
// compact, a goal verdict.
//
// # Why a sidecar at all
//
// AGENTS.md's default question when a feature wants new state is "can
// this be folded from the transcript?". For THIS data the answer is no,
// and the reason is precise: the transcript records what the model saw,
// not how it got there. A steered message and an injected message land
// as byte-identical `user` rows; a withdrawn queue leaves nothing at all;
// an Esc that cancelled a round is indistinguishable from a round that
// simply produced no output. The same argument internal/sessmeta makes
// for its rename overrides ("not derivable from any transcript") — with
// the same consequence: a small purpose-built file is the honest shape,
// not a second copy of something already on disk.
//
// # Why local, global, append-only
//
// Local: the point is tuning seek's own workflow (which nudges help,
// whether the boundary injection replaced steering in practice), and
// that question must be answerable offline with jq, not through a
// service. Global single file rather than per-session: the questions are
// cross-session ratios ("what share of turns involve an intervention?"),
// and ~/.seek/hooks-audit.jsonl sets the precedent for this exact class
// of interaction audit. Append-only: a row is a fact, never re-edited.
//
// What is deliberately NOT stored: the text. Only its length. Content
// is already in the session JSONL when it matters, and this file's job
// is counting, not reading. That also keeps rows comfortably inside the
// single-write atomicity window.
//
// # Best-effort
//
// Append never returns an error to the caller's control flow: a failing
// trace must never fail the keystroke it describes. Callers ignore the
// error (see Model.traceEvent).
package uitrace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event names. These are wire format once a report or a script counts
// them — additions go after, never renamed (the same discipline
// internal/tools/plan's marker strings follow).
const (
	// EventInject: the agent accepted the text for the next turn
	// boundary of the CURRENT round (pkg/agent.QueueUserMessage).
	EventInject = "inject"
	// EventQueue: parked in queuedText, waiting for the whole round to
	// return. The fallback when injection isn't possible.
	EventQueue = "queue"
	// EventSteer: the expensive fallback — the round was cancelled and
	// the text will be resubmitted.
	EventSteer = "steer"
	// EventDeliver: a queued/steered text actually fired as the next
	// prompt. Pairs with EventQueue/EventSteer to expose "queued but
	// never delivered" (the Esc case).
	EventDeliver = "deliver"
	// EventWithdraw: the user took a pending message back without
	// cancelling the stream (empty Enter).
	EventWithdraw = "withdraw"
	// EventCancel: Esc cancelled the round.
	EventCancel = "cancel"
	// EventCompact: /compact or auto-compact completed.
	EventCompact = "compact"
	// EventGoal: a /goal verdict landed (the loop continues or stops).
	EventGoal = "goal"
	// EventGuidance: a deterministic turn-boundary hint was injected
	// (internal/guidance). Detail names the rule
	// (retry-loop / failure-run / silence-run) so a report can ask
	// "which rule fires, and does firing it reduce steer counts?".
	EventGuidance = "guidance"
)

// Event is one JSONL row. Field set is kept minimal on purpose: every
// field must earn its place by answering a question a future tuning pass
// actually asks. Content is absent by design (len only).
type Event struct {
	// TS is RFC3339 UTC. Written by Append when empty so callers don't
	// each have to format it.
	TS string `json:"ts"`
	// SID is the session id the row belongs to. Present even though the
	// file is global: it is what lets a report slice by session.
	SID string `json:"sid,omitempty"`
	// Event is one of the Event* constants.
	Event string `json:"event"`
	// Turn is the TUI's turn counter at the time — the coarse position
	// inside the conversation, so "interventions cluster late" is
	// answerable.
	Turn int `json:"turn,omitempty"`
	// Chars is the length of the text involved (0 where no text is).
	// Length only: the text itself is in the session JSONL.
	Chars int `json:"chars,omitempty"`
	// Tokens is a token magnitude where the row is about context size,
	// not text: currently only "compact" (the pre-compact prompt-token
	// count). A separate field rather than overloading Chars — one
	// field, one meaning, so a jq query never has to branch on event.
	Tokens int `json:"tokens,omitempty"`
	// DurMS is an optional elapsed measurement (e.g. how long a compact
	// took). 0 = not measured.
	DurMS int64 `json:"dur_ms,omitempty"`
	// Detail is a short machine-readable discriminator: "queue"/"steer"
	// on a withdraw, the stop reason on a goal verdict, "manual"/"auto"
	// on a compact (who asked). Free-form but should stay short enough
	// to grep.
	Detail string `json:"detail,omitempty"`
}

// maxRowBytes mirrors internal/skillstats' cap: the single-write
// atomicity argument (POSIX O_APPEND is atomic under PIPE_BUF, 4 KiB
// minimum across platforms) only holds while rows stay small. A row
// this large means a field got abused for prose.
const maxRowBytes = 3072

// Writer appends rows to one JSONL file. Safe for concurrent use from
// multiple goroutines in this process (mutex) and from multiple seek
// processes (O_APPEND + one Write per row under PIPE_BUF).
type Writer struct {
	path string

	mu      sync.Mutex
	created bool
}

// New returns a writer for path. The parent directory is created lazily
// on the first Append so constructing the writer at startup costs
// nothing for users who never intervene.
func New(path string) *Writer { return &Writer{path: path} }

// Path returns the file this writer appends to (for help text / logs).
func (w *Writer) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

// Append serialises e as one line and appends it. A nil writer is a
// no-op so call sites don't need nil checks (same posture as
// hooksconfig.AuditLog).
func (w *Writer) Append(e Event) error {
	if w == nil {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if e.Event == "" {
		return fmt.Errorf("uitrace: empty event name")
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("uitrace: marshal: %w", err)
	}
	line := append(raw, '\n')
	if len(line) > maxRowBytes {
		return fmt.Errorf("uitrace: row too large (%d bytes, cap %d); the single-write atomicity argument depends on small rows",
			len(line), maxRowBytes)
	}

	if err := w.ensureParent(); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("uitrace: open %s: %w", w.path, err)
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// ensureParent creates the file's directory once.
func (w *Writer) ensureParent() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.created {
		return nil
	}
	if dir := filepath.Dir(w.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("uitrace: mkdir %s: %w", dir, err)
		}
	}
	w.created = true
	return nil
}

// Read loads every row of the file at path. A missing file is not an
// error (the common "nobody has intervened yet" state). Malformed lines
// are skipped silently — the file is append-only, so the only realistic
// corruption is a torn tail from a crash mid-write, and dropping just
// that row beats failing the whole report.
func Read(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("uitrace: read %s: %w", path, err)
	}
	var out []Event
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Summary is the folded picture of a trace file. Every number here has
// exactly one definition, written next to it — a tuning pass must be
// able to recompute the report from the rows, or the report is folklore.
type Summary struct {
	// Rows is the number of well-formed rows read.
	Rows int `json:"rows"`
	// ByEvent counts rows per event name.
	ByEvent map[string]int `json:"by_event"`
	// CharsByEvent sums Chars per event name (scale of the text
	// involved, without the text).
	CharsByEvent map[string]int `json:"chars_by_event"`
	// TokensByEvent sums Tokens per event name. Separate from
	// CharsByEvent because the two measure different things (text vs
	// context size); merging them would make the number meaningless.
	TokensByEvent map[string]int `json:"tokens_by_event"`
	// DurMSByEvent sums DurMS per event name — the "how long did this
	// cost me" axis (currently compact). A sum, not a mean, so the report
	// keeps exactly one definition per field; a caller wanting the average
	// divides by ByEvent.
	DurMSByEvent map[string]int64 `json:"dur_ms_by_event"`
	// Intervened is inject+queue+steer: how many times the user tried
	// to say something while a round was running. The numerator of the
	// headline ratio.
	Intervened int `json:"intervened"`
	// Delivered is deliver rows. Delivered < Intervened means some
	// interventions never reached the model (Esc, or a round that
	// ended first) — that gap is the honest cost measure.
	Delivered int `json:"delivered"`
	// Canceled is cancel rows.
	Canceled int `json:"canceled"`
	// Withdrawn is withdraw rows.
	Withdrawn int `json:"withdrawn"`
	// FirstTS / LastTS bound the window the rows cover (empty when no
	// rows).
	FirstTS string `json:"first_ts,omitempty"`
	LastTS  string `json:"last_ts,omitempty"`
}

// Fold reduces rows to a Summary. Pure function, no I/O — the RPC/CLI
// surface (a later card) will call exactly this, so the definitions live
// here rather than in whichever frontend reads it first.
func Fold(events []Event) Summary {
	s := Summary{
		ByEvent:       map[string]int{},
		CharsByEvent:  map[string]int{},
		TokensByEvent: map[string]int{},
		DurMSByEvent:  map[string]int64{},
	}
	for _, e := range events {
		s.Rows++
		s.ByEvent[e.Event]++
		s.CharsByEvent[e.Event] += e.Chars
		s.TokensByEvent[e.Event] += e.Tokens
		s.DurMSByEvent[e.Event] += e.DurMS
		switch e.Event {
		case EventInject, EventQueue, EventSteer:
			s.Intervened++
		case EventDeliver:
			s.Delivered++
		case EventCancel:
			s.Canceled++
		case EventWithdraw:
			s.Withdrawn++
		}
		// Rows are appended chronologically, but a folded set could be
		// assembled by hand in a test — take the min/max rather than
		// trusting order.
		if e.TS != "" {
			if s.FirstTS == "" || e.TS < s.FirstTS {
				s.FirstTS = e.TS
			}
			if s.LastTS == "" || e.TS > s.LastTS {
				s.LastTS = e.TS
			}
		}
	}
	return s
}

// splitLines splits on '\n' without allocating for a trailing newline.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
