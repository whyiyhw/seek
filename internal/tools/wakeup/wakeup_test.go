package wakeup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/routines"
)

// newTestTool returns a Tool wired to an isolated Store under
// a tempdir. Pinned clock so wakeup IDs / NextRun are
// reproducible across test runs.
func newTestTool(t *testing.T) (*Tool, *routines.Store, time.Time) {
	t.Helper()
	dir := t.TempDir()
	store := routines.OpenStoreAt(filepath.Join(dir, "jobs.jsonl"))
	now := time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC)
	tool := New(store).WithNow(func() time.Time { return now })
	return tool, store, now
}

func TestTool_Name(t *testing.T) {
	tool, _, _ := newTestTool(t)
	if tool.Name() != "schedule_wakeup" {
		t.Errorf("Name = %q", tool.Name())
	}
}

// TestTool_SchemaIdempotent: schema bytes stable across calls
// (load-bearing for prefix cache).
func TestTool_SchemaIdempotent(t *testing.T) {
	tool, _, _ := newTestTool(t)
	a := string(tool.Schema())
	b := string(tool.Schema())
	if a != b {
		t.Error("Schema() not idempotent")
	}
	var probe map[string]any
	if err := json.Unmarshal(tool.Schema(), &probe); err != nil {
		t.Errorf("schema bytes not valid JSON: %v", err)
	}
}

// TestExecute_HappyPath: valid delay + prompt → wire-format
// success + Store has a max_runs=1 job with the expected
// NextRun pinned to now+delay.
func TestExecute_HappyPath(t *testing.T) {
	tool, store, now := newTestTool(t)
	args := json.RawMessage(`{"delay_seconds": 300, "prompt": "check CI build #42"}`)

	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[schedule: waking at ") {
		t.Errorf("missing wire-format prefix:\n%s", out)
	}
	if !strings.Contains(out, "(job wakeup-") {
		t.Errorf("missing job name with wakeup- prefix:\n%s", out)
	}

	// Store inspection: one job, max_runs=1, NextRun =
	// now+300s exactly.
	jobs, _ := store.List()
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if j.MaxRuns != 1 {
		t.Errorf("MaxRuns = %d, want 1", j.MaxRuns)
	}
	if !j.Yolo {
		t.Error("Yolo should be true for wakeup jobs (no human to ask)")
	}
	want := now.Add(300 * time.Second)
	if !j.NextRun.Equal(want) {
		t.Errorf("NextRun = %v, want %v", j.NextRun, want)
	}
	if j.Prompt != "check CI build #42" {
		t.Errorf("Prompt round-trip: %q", j.Prompt)
	}
}

// TestExecute_AutoNamePrefix: name carries "wakeup-" so
// `seek cron list` immediately reveals wakeup entries.
func TestExecute_AutoNamePrefix(t *testing.T) {
	tool, store, _ := newTestTool(t)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"delay_seconds": 60, "prompt": "p"}`))
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := store.List()
	if !strings.HasPrefix(jobs[0].Name, "wakeup-") {
		t.Errorf("auto name should start with wakeup-: %q", jobs[0].Name)
	}
}

// TestExecute_DelayBelowMinimum: < 60s returns wire-format
// failure naming the floor; no job persisted.
func TestExecute_DelayBelowMinimum(t *testing.T) {
	tool, store, _ := newTestTool(t)
	for _, sec := range []int{1, 30, 59} {
		args := json.RawMessage(`{"delay_seconds": ` + intStr(sec) + `, "prompt": "p"}`)
		out, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("delay=%d: unexpected err: %v", sec, err)
		}
		if !strings.HasPrefix(out, "[schedule: failed reason=delay_too_short]") {
			t.Errorf("delay=%d: expected delay_too_short failure, got:\n%s", sec, out)
		}
	}
	if jobs, _ := store.List(); len(jobs) != 0 {
		t.Errorf("Store has %d jobs; below-minimum should have written nothing", len(jobs))
	}
}

// TestExecute_DelayAboveMaximum: > 86400s returns
// delay_too_long with hint about splitting.
func TestExecute_DelayAboveMaximum(t *testing.T) {
	tool, _, _ := newTestTool(t)
	args := json.RawMessage(`{"delay_seconds": 86401, "prompt": "p"}`)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "[schedule: failed reason=delay_too_long]") {
		t.Errorf("expected delay_too_long, got:\n%s", out)
	}
	if !strings.Contains(out, "24h") {
		t.Errorf("hint should mention the 24h cap; got:\n%s", out)
	}
}

// TestExecute_MissingFieldsErrorAtParser: empty prompt or
// delay=0 surfaces as tool-level error (NOT wire-format
// failure), so the agent loop's MissingField path handles it.
func TestExecute_MissingFieldsErrorAtParser(t *testing.T) {
	tool, _, _ := newTestTool(t)
	for _, raw := range []string{
		`{"delay_seconds": 60}`,                 // missing prompt
		`{"prompt": "p"}`,                       // missing delay
		`{"delay_seconds": 0, "prompt": "p"}`,   // explicit zero
	} {
		_, err := tool.Execute(context.Background(), json.RawMessage(raw))
		if err == nil {
			t.Errorf("expected error for %q", raw)
		}
	}
}

// TestExecute_UnknownFieldRejected: strict unmarshal blocks
// typos. Critical: schedule_wakeup is in production callers
// outside seek's control; a typo'd "delay_minutes" would
// silently default to 0 and produce confusing failures.
func TestExecute_UnknownFieldRejected(t *testing.T) {
	tool, _, _ := newTestTool(t)
	args := json.RawMessage(`{"delay_seconds": 60, "prompt": "p", "delay_minutes": 5}`)
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected strict-unmarshal error for unknown field")
	}
}

// TestExecute_PromptTooLong: 33 KB prompt → wire-format
// prompt_too_long failure, no job written.
func TestExecute_PromptTooLong(t *testing.T) {
	tool, store, _ := newTestTool(t)
	bigPrompt := strings.Repeat("x", 33*1024)
	args := json.RawMessage(`{"delay_seconds": 60, "prompt": ` + jsonString(bigPrompt) + `}`)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "[schedule: failed reason=prompt_too_long]") {
		t.Errorf("expected prompt_too_long; got:\n%s", out)
	}
	if jobs, _ := store.List(); len(jobs) != 0 {
		t.Errorf("Store has %d jobs; over-budget prompt should write nothing", len(jobs))
	}
}

// TestNew_PanicsOnNilStore: misuse fails loud. New(nil) is a
// programmer error — host should skip registration if Store
// can't be opened.
func TestNew_PanicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(nil) must panic")
		}
	}()
	_ = New(nil)
}

// TestExecute_StoreErrorReturnsWireFailure: if Store.Create
// fails (e.g. disk full, permission), the tool returns
// store_error wire format rather than a Go error. The LLM
// gets a structured signal it can react to (retry, escalate).
func TestExecute_StoreErrorReturnsWireFailure(t *testing.T) {
	// Parent of jobs.jsonl must be a directory; if a regular file
	// occupies that path, MkdirAll fails on every OS (unlike
	// /dev/null/... which only blocks writes on Unix).
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := routines.OpenStoreAt(filepath.Join(blocker, "jobs.jsonl"))
	tool := New(store).WithNow(func() time.Time { return time.Now() })

	args := json.RawMessage(`{"delay_seconds": 60, "prompt": "p"}`)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("expected wire failure, got err: %v", err)
	}
	if !strings.HasPrefix(out, "[schedule: failed reason=store_error]") {
		t.Errorf("expected store_error wire format, got:\n%s", out)
	}
}

// ----- small helpers -----

func intStr(n int) string { return strconv.Itoa(n) }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
