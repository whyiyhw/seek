package uitrace

// Tests for the interaction trace. The failure modes worth pinning:
//
//  1. Concurrent appends from two goroutines must not interleave bytes —
//     the single-write-under-PIPE_BUF argument is the whole reason this
//     file needs no locking protocol.
//  2. A missing file reads as "no rows", not an error (the common state).
//  3. A torn final line (crash mid-write) is skipped, not fatal — the
//     rest of the file must still be readable.
//  4. Fold's counters use one definition each; a row's Event determines
//     exactly one bucket.
//  5. Oversized rows are rejected rather than silently breaking the
//     atomicity argument.
//  6. nil writer is a no-op (call sites must not need nil checks).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendAndRead_RoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "ui-trace.jsonl")
	w := New(path)

	if err := w.Append(Event{Event: EventInject, SID: "s1", Turn: 3, Chars: 42}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Append(Event{Event: EventSteer, SID: "s1", Turn: 4, Chars: 7}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].Event != EventInject || got[0].Turn != 3 || got[0].Chars != 42 {
		t.Errorf("row 0 = %+v", got[0])
	}
	if got[1].Event != EventSteer {
		t.Errorf("row 1 event = %q", got[1].Event)
	}
	// TS is filled by Append when the caller omits it.
	for i, e := range got {
		if e.TS == "" {
			t.Errorf("row %d has empty ts", i)
		}
		if _, perr := time.Parse(time.RFC3339, e.TS); perr != nil {
			t.Errorf("row %d ts %q not RFC3339: %v", i, e.TS, perr)
		}
	}
}

func TestAppend_EmptyEventNameRejected(t *testing.T) {
	t.Parallel()
	w := New(filepath.Join(t.TempDir(), "ui-trace.jsonl"))
	if err := w.Append(Event{}); err == nil {
		t.Fatal("empty event name must be rejected — a nameless row can never be counted")
	}
}

func TestAppend_OversizedRowRejected(t *testing.T) {
	t.Parallel()
	w := New(filepath.Join(t.TempDir(), "ui-trace.jsonl"))
	huge := Event{Event: EventInject, Detail: strings.Repeat("x", maxRowBytes)}
	err := w.Append(huge)
	if err == nil {
		t.Fatal("oversized row must be rejected — the atomicity argument depends on small rows")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v, want it to name the size problem", err)
	}
	// Nothing was written.
	got, rerr := Read(w.Path())
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if len(got) != 0 {
		t.Errorf("rows = %d, want 0 after a rejected append", len(got))
	}
}

func TestRead_MissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("rows = %d, want 0", len(got))
	}
}

func TestRead_TornTailSkipped(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ui-trace.jsonl")
	good, _ := json.Marshal(Event{TS: "2026-11-23T00:00:00Z", Event: EventInject, Chars: 5})
	// Second line is a truncated write (crash mid-append).
	body := string(good) + "\n" + `{"event":"steer","chars":`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("a torn tail must not fail the read: %v", err)
	}
	if len(got) != 1 || got[0].Event != EventInject {
		t.Errorf("rows = %+v, want just the intact row", got)
	}
}

func TestAppend_ConcurrentDoesNotInterleave(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ui-trace.jsonl")
	w := New(path)

	const perGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = w.Append(Event{
					Event:  EventInject,
					Detail: fmt.Sprintf("g%d-%d", g, i),
					Chars:  g*1000 + i,
				})
			}
		}(g)
	}
	wg.Wait()

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Every row that parsed must be a complete row: interleaved bytes
	// would produce JSON that fails to unmarshal and get skipped.
	if len(got) != 4*perGoroutine {
		t.Fatalf("rows = %d, want %d (torn rows mean interleaved writes)", len(got), 4*perGoroutine)
	}
	seen := map[string]bool{}
	for _, e := range got {
		if e.Detail == "" || e.Chars < 0 {
			t.Errorf("torn row: %+v", e)
		}
		seen[e.Detail] = true
	}
	if len(seen) != 4*perGoroutine {
		t.Errorf("distinct details = %d, want %d (duplicates mean a lost/duplicated write)", len(seen), 4*perGoroutine)
	}
}

func TestNilWriterIsNoOp(t *testing.T) {
	t.Parallel()
	var w *Writer
	if err := w.Append(Event{Event: EventInject}); err != nil {
		t.Errorf("nil writer must be a no-op, got %v", err)
	}
	if w.Path() != "" {
		t.Errorf("nil writer Path = %q", w.Path())
	}
}

func TestFold_Counters(t *testing.T) {
	t.Parallel()
	rows := []Event{
		{TS: "2026-11-23T00:00:02Z", Event: EventInject, Chars: 10},
		{TS: "2026-11-23T00:00:01Z", Event: EventQueue, Chars: 20},
		{TS: "2026-11-23T00:00:05Z", Event: EventSteer, Chars: 30},
		{TS: "2026-11-23T00:00:04Z", Event: EventDeliver, Chars: 20},
		{TS: "2026-11-23T00:00:03Z", Event: EventWithdraw, Chars: 5},
		{TS: "2026-11-23T00:00:06Z", Event: EventCancel},
		{TS: "2026-11-23T00:00:07Z", Event: EventCompact, Tokens: 1234},
	}
	s := Fold(rows)

	if s.Rows != 7 {
		t.Errorf("Rows = %d, want 7", s.Rows)
	}
	// Intervened = inject+queue+steer. Cancel/withdraw/deliver are
	// separate outcomes, not interventions.
	if s.Intervened != 3 {
		t.Errorf("Intervened = %d, want 3", s.Intervened)
	}
	if s.Delivered != 1 {
		t.Errorf("Delivered = %d, want 1", s.Delivered)
	}
	if s.Canceled != 1 || s.Withdrawn != 1 {
		t.Errorf("Canceled/Withdrawn = %d/%d, want 1/1", s.Canceled, s.Withdrawn)
	}
	if s.ByEvent[EventInject] != 1 || s.ByEvent[EventQueue] != 1 {
		t.Errorf("ByEvent = %+v", s.ByEvent)
	}
	if s.CharsByEvent[EventSteer] != 30 {
		t.Errorf("CharsByEvent[steer] = %d, want 30", s.CharsByEvent[EventSteer])
	}
	// Tokens are summed separately: a compact row's magnitude must never
	// land in CharsByEvent.
	if s.TokensByEvent[EventCompact] != 1234 {
		t.Errorf("TokensByEvent[compact] = %d, want 1234", s.TokensByEvent[EventCompact])
	}
	if s.CharsByEvent[EventCompact] != 0 {
		t.Errorf("CharsByEvent[compact] = %d, want 0", s.CharsByEvent[EventCompact])
	}
	// First/Last must be min/max, not slice order (rows could be
	// assembled by hand).
	if s.FirstTS != "2026-11-23T00:00:01Z" || s.LastTS != "2026-11-23T00:00:07Z" {
		t.Errorf("window = %s..%s, want 00:00:01..00:00:07", s.FirstTS, s.LastTS)
	}
}

func TestFold_EmptyIsZeroValue(t *testing.T) {
	t.Parallel()
	s := Fold(nil)
	if s.Rows != 0 || s.Intervened != 0 || s.FirstTS != "" {
		t.Errorf("empty fold = %+v, want zero", s)
	}
	// Maps must be non-nil so a JSON consumer sees {} not null.
	if s.ByEvent == nil || s.CharsByEvent == nil || s.TokensByEvent == nil {
		t.Error("fold must return initialised maps")
	}
}

// TestEventNamesAreWireFormat pins the constants: a report or a jq query
// in the wild depends on these exact strings, so renaming one is a
// breaking change dressed as a refactor.
func TestEventNamesAreWireFormat(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"inject":   EventInject,
		"queue":    EventQueue,
		"steer":    EventSteer,
		"deliver":  EventDeliver,
		"withdraw": EventWithdraw,
		"cancel":   EventCancel,
		"compact":  EventCompact,
		"goal":     EventGoal,
	}
	for literal, got := range want {
		if got != literal {
			t.Errorf("constant = %q, wire format documented as %q", got, literal)
		}
	}
}
