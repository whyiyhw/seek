package uitracecli

// CLI tests. The load-bearing behaviours:
//
//  1. A missing trace file is a UX state ("no interactions recorded"),
//     not an error and not an empty success that looks broken.
//  2. --since filters by row timestamp and keeps rows it can't parse
//     (dropping them would undercount silently).
//  3. --rows prints newest-first; --limit keeps the NEWEST rows, which
//     is what an operator tailing a live session wants.
//  4. --json is stable enough for jq: it carries the path and either a
//     summary or a rows array, never null.
//  5. The summary's percentages don't divide by zero on an empty window.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/uitrace"
)

// withTrace seeds a trace file at $SEEK_HOME/ui-trace.jsonl and returns
// the home dir.
func withTrace(t *testing.T, rows ...uitrace.Event) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	path := filepath.Join(home, "ui-trace.jsonl")
	if len(rows) == 0 {
		return home
	}
	w := uitrace.New(path)
	for _, r := range rows {
		if err := w.Append(r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return home
}

func run(t *testing.T, args ...string) (string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	if err := Run(args, &out, &errb); err != nil {
		t.Fatalf("Run(%v): %v", args, err)
	}
	return out.String(), errb.String()
}

func TestRun_NoFileIsAUXStateNotAnError(t *testing.T) {
	withTrace(t) // creates SEEK_HOME but no file
	out, _ := run(t)
	if !strings.Contains(out, "no interactions recorded") {
		t.Errorf("out = %q, want the empty-state message", out)
	}
	// The hint must name how rows appear, otherwise the message reads
	// like a bug.
	if !strings.Contains(out, "streaming") {
		t.Errorf("empty state should hint how rows appear, got %q", out)
	}
}

func TestRun_SummaryCounts(t *testing.T) {
	withTrace(t,
		uitrace.Event{Event: uitrace.EventInject, Chars: 10, Turn: 2},
		uitrace.Event{Event: uitrace.EventSteer, Chars: 20, Turn: 3},
		uitrace.Event{Event: uitrace.EventDeliver, Detail: "steer", Chars: 20},
		uitrace.Event{Event: uitrace.EventCancel},
	)
	out, _ := run(t)
	for _, want := range []string{
		"intervened 2", "delivered  1", "withdrawn  0", "cancelled  1",
		"reached the model: 50%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out missing %q:\n%s", want, out)
		}
	}
}

func TestRun_EmptyWindowDoesNotDivideByZero(t *testing.T) {
	// Rows exist but all fall outside the --since window: the summary
	// must print without a NaN%.
	withTrace(t, uitrace.Event{TS: "2020-01-01T00:00:00Z", Event: uitrace.EventInject})
	out, _ := run(t, "--since", "1h")
	if strings.Contains(out, "NaN") || strings.Contains(out, "+Inf") {
		t.Errorf("percentage divided by zero:\n%s", out)
	}
	if !strings.Contains(out, "no interactions recorded") {
		t.Errorf("out = %q, want the empty-state message for an empty window", out)
	}
}

func TestRun_SinceFilters(t *testing.T) {
	now := time.Now().UTC()
	withTrace(t,
		uitrace.Event{TS: now.Add(-48 * time.Hour).Format(time.RFC3339), Event: uitrace.EventCancel},
		uitrace.Event{TS: now.Add(-1 * time.Minute).Format(time.RFC3339), Event: uitrace.EventInject},
	)
	out, _ := run(t, "--since", "24h")
	if !strings.Contains(out, "intervened 1") {
		t.Errorf("old row should be excluded:\n%s", out)
	}
	if !strings.Contains(out, "cancelled  0") {
		t.Errorf("the 48h-old cancel must be filtered out:\n%s", out)
	}
}

func TestRun_SinceKeepsUnparsableTS(t *testing.T) {
	now := time.Now().UTC()
	withTrace(t,
		uitrace.Event{TS: "not-a-timestamp", Event: uitrace.EventCancel},
		uitrace.Event{TS: now.Format(time.RFC3339), Event: uitrace.EventInject},
	)
	out, _ := run(t, "--since", "24h")
	// Dropping an unreadable row would undercount the ledger; the window
	// is a convenience filter, not an integrity check.
	if !strings.Contains(out, "cancelled  1") {
		t.Errorf("unparsable-ts row must be kept:\n%s", out)
	}
}

func TestRun_RowsNewestFirstAndLimitKeepsNewest(t *testing.T) {
	base := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	var rows []uitrace.Event
	for i := 0; i < 5; i++ {
		rows = append(rows, uitrace.Event{
			TS:     base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			Event:  uitrace.EventInject,
			Detail: fmt.Sprintf("row-%c", 'a'+i),
		})
	}
	withTrace(t, rows...)

	out, _ := run(t, "--rows", "--limit", "2")
	ie, id := strings.Index(out, "row-e"), strings.Index(out, "row-d")
	if ie < 0 || id < 0 {
		t.Fatalf("rows missing: %s", out)
	}
	if ie > id {
		t.Errorf("newest (row-e) must print before older (row-d):\n%s", out)
	}
	if strings.Contains(out, "row-a") || strings.Contains(out, "row-c") {
		t.Errorf("--limit 2 must keep only the newest two rows:\n%s", out)
	}
}

func TestRun_JSONSummaryShape(t *testing.T) {
	withTrace(t, uitrace.Event{Event: uitrace.EventInject, Chars: 7})
	out, _ := run(t, "--json")

	var got struct {
		Path    string          `json:"path"`
		Summary uitrace.Summary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if !strings.HasSuffix(got.Path, "ui-trace.jsonl") {
		t.Errorf("path = %q", got.Path)
	}
	if got.Summary.Intervened != 1 || got.Summary.Rows != 1 {
		t.Errorf("summary = %+v", got.Summary)
	}
	// A JSON consumer must see {} rather than null for the maps.
	if got.Summary.ByEvent == nil {
		t.Error("by_event must not be null")
	}
}

func TestRun_JSONRowsShape(t *testing.T) {
	withTrace(t,
		uitrace.Event{Event: uitrace.EventInject, Chars: 1},
		uitrace.Event{Event: uitrace.EventSteer, Chars: 2},
	)
	out, _ := run(t, "--json", "--rows")

	var got struct {
		Path string          `json:"path"`
		Rows []uitrace.Event `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(got.Rows))
	}
	if got.Rows[0].Event != uitrace.EventSteer {
		t.Errorf("rows must be newest-first, got %q first", got.Rows[0].Event)
	}
}

func TestRun_JSONEmptyWindowHasEmptyArray(t *testing.T) {
	withTrace(t) // no file at all
	out, _ := run(t, "--json", "--rows")
	if !strings.Contains(out, `"rows": []`) {
		t.Errorf("empty rows must marshal as [], not null:\n%s", out)
	}
}

func TestRun_BadFlagReturnsError(t *testing.T) {
	withTrace(t)
	var out, errb bytes.Buffer
	if err := Run([]string{"--nope"}, &out, &errb); err == nil {
		t.Error("unknown flag must return an error so the shell sees a non-zero exit")
	}
}

// TestRun_FileIsReadOnly guards the read path: querying must never
// create or modify the trace file.
func TestRun_FileIsReadOnly(t *testing.T) {
	home := withTrace(t, uitrace.Event{Event: uitrace.EventInject})
	path := filepath.Join(home, "ui-trace.jsonl")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	run(t, "--rows")
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Error("reading the trace modified the file")
	}
}
