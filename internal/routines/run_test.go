package routines

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

// TestNewRunID_FormatAndUniqueness pins the canonical ID shape.
// Matches session.generateID / subagent.newSubSid so visual
// inspection of paths immediately tells the reader what kind of
// ID they're looking at.
func TestNewRunID_FormatAndUniqueness(t *testing.T) {
	now := time.Date(2026, 5, 28, 14, 23, 5, 0, time.UTC)
	id := NewRunID(now)
	if len(id) != 22 {
		t.Errorf("run id length = %d, want 22 — got %q", len(id), id)
	}
	if !strings.HasPrefix(id, "20260528-142305-") {
		t.Errorf("run id missing timestamp prefix: %q", id)
	}

	seen := make(map[string]bool, 500)
	for range 500 {
		s := NewRunID(now)
		if seen[s] {
			t.Errorf("duplicate run id: %s", s)
		}
		seen[s] = true
	}
}

// TestRecorder_HeaderAndEvents covers the full lifecycle:
// header written on construction, stdout/stderr events
// interleaved, terminal event closes the stream.
func TestRecorder_HeaderAndEvents(t *testing.T) {
	dir := t.TempDir()
	rr, err := NewRecorder(dir, "20260528-142305-aaaa", "morning-report", "/proj",
		[]string{"seek", "-p", "summarise"}, true, time.Date(2026, 5, 28, 14, 23, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	if err := rr.WriteStdout("line 1\n"); err != nil {
		t.Errorf("WriteStdout: %v", err)
	}
	if err := rr.WriteStderr("warning\n"); err != nil {
		t.Errorf("WriteStderr: %v", err)
	}
	if err := rr.WriteCompleted(0, 8432*time.Millisecond, "line 1"); err != nil {
		t.Errorf("WriteCompleted: %v", err)
	}
	if err := rr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	lines := readJSONLines(t, filepath.Join(dir, "20260528-142305-aaaa.jsonl"))
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (header + 3 events), got %d", len(lines))
	}

	// Header.
	if lines[0]["job_name"] != "morning-report" {
		t.Errorf("header.job_name = %v", lines[0]["job_name"])
	}
	if lines[0]["yolo"] != true {
		t.Errorf("header.yolo = %v, want true", lines[0]["yolo"])
	}
	cmd, ok := lines[0]["command"].([]any)
	if !ok || len(cmd) != 3 {
		t.Errorf("header.command shape = %T %v", lines[0]["command"], lines[0]["command"])
	}

	// Events have an "event" discriminator + a ts.
	for i, want := range []string{"stdout", "stderr", "completed"} {
		got := lines[i+1]["event"]
		if got != want {
			t.Errorf("event[%d] = %v, want %s", i, got, want)
		}
		if lines[i+1]["ts"] == nil {
			t.Errorf("event[%d] missing ts", i)
		}
	}
	// Completed carries summary + exit_code + duration_ms.
	if lines[3]["summary"] != "line 1" {
		t.Errorf("completed.summary = %v", lines[3]["summary"])
	}
}

// TestRecorder_FailedAndKilledShapes covers the two non-success
// terminal events.
func TestRecorder_FailedAndKilledShapes(t *testing.T) {
	dir := t.TempDir()

	rrFail, _ := NewRecorder(dir, "id-fail", "j", "", []string{"seek"}, false, time.Now())
	_ = rrFail.WriteFailed(1, 200*time.Millisecond, "exit status 1: bad api key")
	_ = rrFail.Close()
	failLines := readJSONLines(t, filepath.Join(dir, "id-fail.jsonl"))
	if failLines[1]["event"] != "failed" {
		t.Errorf("failed event missing")
	}
	if !strings.Contains(failLines[1]["error"].(string), "bad api key") {
		t.Errorf("failed.error truncated: %v", failLines[1]["error"])
	}

	rrKill, _ := NewRecorder(dir, "id-kill", "j", "", []string{"seek"}, false, time.Now())
	_ = rrKill.WriteKilled("timeout", 30*time.Minute)
	_ = rrKill.Close()
	killLines := readJSONLines(t, filepath.Join(dir, "id-kill.jsonl"))
	if killLines[1]["event"] != "killed" {
		t.Errorf("killed event missing")
	}
	if killLines[1]["reason"] != "timeout" {
		t.Errorf("killed.reason = %v", killLines[1]["reason"])
	}
}

// TestRecorder_ConcurrentStdoutStderrSerialised: streamer
// goroutines call WriteStdout + WriteStderr concurrently; the
// recorder's mu must serialise so no two events share a line.
// readJSONLines failing on bad JSON would catch interleave.
func TestRecorder_ConcurrentStdoutStderrSerialised(t *testing.T) {
	dir := t.TempDir()
	rr, _ := NewRecorder(dir, "id-conc", "j", "", []string{"seek"}, false, time.Now())

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_ = rr.WriteStdout("stdout chunk")
		}(i)
		go func(n int) {
			defer wg.Done()
			_ = rr.WriteStderr("stderr chunk")
		}(i)
	}
	wg.Wait()
	_ = rr.WriteCompleted(0, time.Millisecond, "")
	_ = rr.Close()

	lines := readJSONLines(t, filepath.Join(dir, "id-conc.jsonl"))
	// header + 2N events + completed
	want := 1 + 2*N + 1
	if len(lines) != want {
		t.Errorf("got %d lines, want %d (interleave would break JSON parse)", len(lines), want)
	}
}

// TestRecorder_CloseIsIdempotent: defer paths often double-
// close. Close on a closed recorder must not panic / not error.
func TestRecorder_CloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	rr, _ := NewRecorder(dir, "id-cl", "j", "", []string{"seek"}, false, time.Now())
	if err := rr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rr.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	// Writes after close are silent no-ops (caller had nothing
	// to do anyway; failing here would surprise unrelated test
	// code paths).
	if err := rr.WriteStdout("late"); err != nil {
		t.Errorf("WriteStdout after Close = %v, want nil", err)
	}
}

// TestRecorder_NilSafeMethods defends a nil-receiver path —
// callers might defer Close on a recorder that failed to
// initialise.
func TestRecorder_NilSafeMethods(t *testing.T) {
	var rr *RunRecorder
	if err := rr.Close(); err != nil {
		t.Errorf("nil Close = %v", err)
	}
	if err := rr.WriteStdout("x"); err != nil {
		t.Errorf("nil WriteStdout = %v", err)
	}
}

// TestRecorder_ExclusiveOpen rejects a re-open of an existing
// run file. Duplicate run IDs would corrupt the JSONL stream;
// O_EXCL catches it.
func TestRecorder_ExclusiveOpen(t *testing.T) {
	dir := t.TempDir()
	rr, err := NewRecorder(dir, "dup", "j", "", []string{"seek"}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Close()
	_, err2 := NewRecorder(dir, "dup", "j", "", []string{"seek"}, false, time.Now())
	if err2 == nil {
		t.Error("second NewRecorder on same ID should fail (O_EXCL)")
	}
}

// readJSONLines parses every line of path as JSON object.
// Fails the test on first parse error.
func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad JSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}
