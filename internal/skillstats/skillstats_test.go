package skillstats

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// readLines reads a stats file line-by-line, returns the JSON
// objects. Parses each line into a generic map so tests can assert
// individual fields without coupling to the Entry struct's field
// names.
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stats: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("parse line %q: %v", line, err)
		}
		out = append(out, m)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAppend_CreatesFileOnFirstWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats", ".stats.jsonl")
	// Subdir doesn't exist yet — writer must mkdir on first append.

	w := New(path)
	if err := w.Append(Entry{
		TS:        "2026-05-22T13:15:00Z",
		Name:      "go-test-runner",
		SessionID: "sess-1",
		ProjectID: "a3b9f1c2e8d4a7b6",
		Model:     "deepseek-chat",
		Provider:  "deepseek",
	}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	row := lines[0]
	for k, want := range map[string]string{
		"ts":         "2026-05-22T13:15:00Z",
		"name":       "go-test-runner",
		"session_id": "sess-1",
		"project_id": "a3b9f1c2e8d4a7b6",
		"model":      "deepseek-chat",
		"provider":   "deepseek",
	} {
		if row[k] != want {
			t.Errorf("row[%s] = %v, want %v", k, row[k], want)
		}
	}
}

func TestAppend_PreservesOrderWithinProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".stats.jsonl")
	w := New(path)
	for _, n := range []string{"a", "b", "c"} {
		if err := w.Append(Entry{TS: "t", Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	lines := readLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, want := range []string{"a", "b", "c"} {
		if lines[i]["name"] != want {
			t.Errorf("line %d name = %v, want %s", i, lines[i]["name"], want)
		}
	}
}

func TestAppend_ConcurrentWritesDoNotInterleave(t *testing.T) {
	// PRD v2 §9 risk row: lines must be atomic per POSIX O_APPEND
	// when the payload is smaller than PIPE_BUF. We serialise each
	// entry into a single []byte and Write it once; this test
	// hammers the writer with N goroutines and checks every line
	// parses cleanly (proof that no two entries interleaved).
	path := filepath.Join(t.TempDir(), ".stats.jsonl")
	w := New(path)

	const G = 16
	const PerGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < G; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < PerGoroutine; i++ {
				_ = w.Append(Entry{
					TS:   "t",
					Name: "skill",
					// Stuff identifying bytes into a field so a
					// torn line would deserialise as garbage and
					// readLines would fail.
					SessionID: strings.Repeat("x", 32),
					Model:     "m",
					Provider:  "p",
				})
			}
		}(g)
	}
	wg.Wait()

	lines := readLines(t, path)
	if len(lines) != G*PerGoroutine {
		t.Fatalf("got %d lines, want %d (interleaving lost rows)", len(lines), G*PerGoroutine)
	}
	for i, row := range lines {
		if row["session_id"] != strings.Repeat("x", 32) {
			t.Fatalf("line %d corrupted: %v", i, row)
		}
	}
}

func TestAppend_EmptyOptionalFieldsAreOmitted(t *testing.T) {
	// To keep .stats.jsonl scannable, we only emit the fields that
	// were actually set. A skill invoked outside any project should
	// not appear as `"project_id":""` in every row — that's just
	// noise.
	path := filepath.Join(t.TempDir(), ".stats.jsonl")
	w := New(path)
	if err := w.Append(Entry{
		TS:        "t",
		Name:      "x",
		SessionID: "s",
		Model:     "m",
		Provider:  "p",
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "project_id") {
		t.Errorf("empty project_id should be omitted; got: %s", data)
	}
}
