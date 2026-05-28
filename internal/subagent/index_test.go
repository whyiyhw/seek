package subagent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeIndex returns a temp file path for the subagents.jsonl index
// + a helper that reads it back as a slice of events.
func makeIndex(t *testing.T) (string, func() []event) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	read := func() []event {
		es, err := readEvents(path)
		if err != nil {
			t.Fatalf("readEvents: %v", err)
		}
		return es
	}
	return path, read
}

func TestAppendEvent_WritesOneLine(t *testing.T) {
	path, read := makeIndex(t)
	err := appendEvent(path, event{
		Kind: "started", SubSid: "sub1", ParentSid: "parent1",
		Type: TypeExplore, Description: "find handlers",
		ParentTurn: 4,
	})
	if err != nil {
		t.Fatalf("appendEvent: %v", err)
	}
	events := read()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Kind != "started" || events[0].SubSid != "sub1" {
		t.Errorf("event roundtrip lost data: %+v", events[0])
	}
	// TS auto-filled when omitted.
	if events[0].TS.IsZero() {
		t.Error("TS not auto-filled by appendEvent")
	}
}

func TestAppendEvent_LazyMkdir(t *testing.T) {
	// Index path nested under a non-existent parent — appendEvent
	// must MkdirAll automatically, not return ENOENT.
	root := t.TempDir()
	path := filepath.Join(root, "missing", "subdir", "subagents.jsonl")
	err := appendEvent(path, event{Kind: "started", SubSid: "x"})
	if err != nil {
		t.Fatalf("appendEvent on nested missing dir: %v", err)
	}
}

func TestAppendEvent_RejectsEmptyKindOrSid(t *testing.T) {
	path, _ := makeIndex(t)
	for _, e := range []event{
		{Kind: "", SubSid: "s"},
		{Kind: "started", SubSid: ""},
	} {
		if err := appendEvent(path, e); err == nil {
			t.Errorf("appendEvent accepted invalid event: %+v", e)
		}
	}
}

func TestReadEvents_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.jsonl")
	events, err := readEvents(path)
	if err != nil {
		t.Fatalf("readEvents on missing file: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from missing file, want 0", len(events))
	}
}

func TestReadEvents_SkipsMalformedLines(t *testing.T) {
	path, read := makeIndex(t)
	if err := appendEvent(path, event{Kind: "started", SubSid: "good"}); err != nil {
		t.Fatal(err)
	}
	// Manually append a garbage line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("this is not json\n")
	f.Close()
	if err := appendEvent(path, event{Kind: "completed", SubSid: "good"}); err != nil {
		t.Fatal(err)
	}

	got := read()
	if len(got) != 2 {
		t.Fatalf("malformed line not skipped: got %d events, want 2", len(got))
	}
}

// TestFold_HappyPath: started → completed gives Completed status
// with token rollup.
func TestFold_HappyPath(t *testing.T) {
	tokens := &Tokens{Prompt: 100, Completion: 20, CacheHit: 80}
	events := []event{
		{Kind: "started", SubSid: "s1", ParentSid: "p1", Type: TypeExplore,
			Description: "find X", TS: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)},
		{Kind: "completed", SubSid: "s1", Tokens: tokens,
			TS: time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)},
	}
	folded := foldEvents(events)
	if len(folded) != 1 {
		t.Fatalf("len = %d", len(folded))
	}
	s := folded[0]
	if s.Status != StatusCompleted {
		t.Errorf("Status = %s, want completed", s.Status)
	}
	if s.Tokens != *tokens {
		t.Errorf("Tokens = %+v, want %+v", s.Tokens, *tokens)
	}
	if s.EndedAt.IsZero() {
		t.Error("EndedAt zero on completed subagent")
	}
}

// TestFold_TerminalKinds: each terminal event kind maps to the
// correct Status.
func TestFold_TerminalKinds(t *testing.T) {
	cases := []struct {
		kind string
		want Status
	}{
		{"completed", StatusCompleted},
		{"failed", StatusFailed},
		{"killed", StatusKilled},
		{"orphaned", StatusOrphaned},
		{"promoted", StatusPromoted},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			events := []event{
				{Kind: "started", SubSid: "x", Type: TypeGeneralPurpose, TS: time.Now()},
				{Kind: c.kind, SubSid: "x", TS: time.Now(), Reason: "test"},
			}
			folded := foldEvents(events)
			if len(folded) != 1 || folded[0].Status != c.want {
				t.Errorf("kind=%s → folded=%+v, want Status=%s", c.kind, folded, c.want)
			}
		})
	}
}

// TestFold_ActiveWhenNoTerminal: started with no terminal event →
// StatusActive (the only non-terminal state).
func TestFold_ActiveWhenNoTerminal(t *testing.T) {
	events := []event{{Kind: "started", SubSid: "live", TS: time.Now()}}
	folded := foldEvents(events)
	if len(folded) != 1 || folded[0].Status != StatusActive {
		t.Errorf("expected single Active subagent, got %+v", folded)
	}
}

// TestFold_SortsByStartedAtDescending: newest-first ordering for
// /agents panel default presentation.
func TestFold_SortsByStartedAtDescending(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(1 * time.Minute)
	t3 := t1.Add(2 * time.Minute)
	events := []event{
		{Kind: "started", SubSid: "first", TS: t1},
		{Kind: "started", SubSid: "second", TS: t2},
		{Kind: "started", SubSid: "third", TS: t3},
	}
	folded := foldEvents(events)
	got := []string{folded[0].SubSid, folded[1].SubSid, folded[2].SubSid}
	want := []string{"third", "second", "first"}
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("sort order = %v, want %v (newest first)", got, want)
	}
}

// TestFold_TerminalForUnknownSidSkipped: a terminal event for a
// sub_sid we never saw started must NOT create a phantom record.
func TestFold_TerminalForUnknownSidSkipped(t *testing.T) {
	events := []event{
		{Kind: "completed", SubSid: "ghost", TS: time.Now()},
	}
	folded := foldEvents(events)
	if len(folded) != 0 {
		t.Errorf("phantom subagent from orphan terminal: %+v", folded)
	}
}

// TestOrphanRecover_AppendsOrphanedEvents: the seek-startup hook
// marks `started`-without-terminal sub_sids as orphaned.
func TestOrphanRecover_AppendsOrphanedEvents(t *testing.T) {
	path, read := makeIndex(t)

	// Three subagents: one completed, one still active, one
	// failed-but-active-looking (started + failed in different order
	// — but since failed is terminal, this should NOT be orphaned).
	for _, e := range []event{
		{Kind: "started", SubSid: "a", TS: time.Now()},
		{Kind: "completed", SubSid: "a", TS: time.Now()},
		{Kind: "started", SubSid: "b", TS: time.Now()}, // active
		{Kind: "started", SubSid: "c", TS: time.Now()},
		{Kind: "failed", SubSid: "c", Reason: "boom", TS: time.Now()},
	} {
		if err := appendEvent(path, e); err != nil {
			t.Fatal(err)
		}
	}

	orphaned, err := OrphanRecover(path)
	if err != nil {
		t.Fatalf("OrphanRecover: %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != "b" {
		t.Errorf("orphaned = %v, want [b]", orphaned)
	}

	// After recovery, b's status must be Orphaned.
	folded := foldEvents(read())
	statusBy := map[string]Status{}
	for _, s := range folded {
		statusBy[s.SubSid] = s.Status
	}
	if statusBy["b"] != StatusOrphaned {
		t.Errorf("after recover, b status = %s, want orphaned", statusBy["b"])
	}
	// Idempotent: a second recovery does nothing.
	again, err := OrphanRecover(path)
	if err != nil {
		t.Fatalf("OrphanRecover (2nd): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second OrphanRecover produced %v, want []", again)
	}
}

// TestAppendEvent_ConcurrentDoesNotCorrupt: two goroutines append
// 100 events each; readback finds 200 well-formed lines with no
// interleave artifacts.
func TestAppendEvent_ConcurrentDoesNotCorrupt(t *testing.T) {
	path, _ := makeIndex(t)
	const N = 100

	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				_ = appendEvent(path, event{
					Kind:   "started",
					SubSid: idFor(worker, i),
				})
			}
		}(w)
	}
	wg.Wait()

	// Raw line count, byte-faithful (don't go through readEvents
	// which would mask interleaved garbage).
	f, _ := os.Open(path)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	lines := 0
	for scanner.Scan() {
		var e event
		line := scanner.Text()
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("corrupted line: %q (%v)", line, err)
		}
		lines++
	}
	if lines != 2*N {
		t.Errorf("got %d lines, want %d", lines, 2*N)
	}
}

func idFor(worker, n int) string {
	return strings.Repeat("w", worker+1) + "-" + strings.Repeat("0", 0) + fmtInt(n)
}

func fmtInt(n int) string {
	return string(rune('a'+n%26)) + string(rune('a'+(n/26)%26))
}
