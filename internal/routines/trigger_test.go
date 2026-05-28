package routines

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeTrigger drops a JSON trigger file under triggersDir +
// rewinds its mtime to be older than triggerMtimeQuiet
// RELATIVE TO `now` so the quiescence check passes when
// processTriggers is called with that same `now`. Tests using
// a fixture clock (newTickFixture) must pass f.now here;
// passing time.Now() works for tests not using a fixture.
func writeTrigger(t *testing.T, triggersDir string, now time.Time, trig Trigger) string {
	t.Helper()
	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(trig)
	path := filepath.Join(triggersDir, trig.TriggerID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * triggerMtimeQuiet)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRawTrigger drops arbitrary bytes (used for malformed
// JSON tests). Same now-aware backdating as writeTrigger.
func writeRawTrigger(t *testing.T, triggersDir string, now time.Time, id string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(triggersDir, id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * triggerMtimeQuiet)
	_ = os.Chtimes(path, old, old)
	return path
}

// TestProcessTriggers_HappyPath: valid trigger fires the
// supplied SubprocessFn, deletes the file, increments
// dispatched count.
func TestProcessTriggers_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh stub not available on Windows")
	}
	root := t.TempDir()
	triggersDir := filepath.Join(root, "triggers")
	runsDir := filepath.Join(root, "runs")

	now := time.Now().UTC()
	path := writeTrigger(t, triggersDir, now, Trigger{
		TriggerID: "ci-build-42",
		Prompt:    "summarise CI #42",
	})

	calls := 0
	sub := shellStub(t, "echo handled CI #42")
	wrapped := SubprocessFn(func(ctx context.Context, j Job, runID string) (*exec.Cmd, error) {
		calls++
		return sub(ctx, j, runID)
	})

	n, err := processTriggers(context.Background(), triggersDir, runsDir, now, wrapped, noopNotifier)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("dispatched count = %d, want 1", n)
	}
	if calls != 1 {
		t.Errorf("SubprocessFn calls = %d, want 1", calls)
	}
	// Trigger file consumed.
	if _, err := os.Stat(path); err == nil {
		t.Error("trigger file should be deleted after dispatch")
	}
	// Run record exists.
	entries, _ := os.ReadDir(runsDir)
	if len(entries) == 0 {
		t.Error("no run record written under runsDir")
	}
}

// TestProcessTriggers_TTLExpired: trigger with TTLMinutes past
// expiry is deleted without firing.
func TestProcessTriggers_TTLExpired(t *testing.T) {
	root := t.TempDir()
	triggersDir := filepath.Join(root, "triggers")
	runsDir := filepath.Join(root, "runs")

	now := time.Now().UTC()
	created := now.Add(-2 * time.Hour)
	path := writeTrigger(t, triggersDir, now, Trigger{
		TriggerID:  "stale",
		Prompt:     "p",
		CreatedAt:  created,
		TTLMinutes: 60, // 1h TTL, but 2h old → expired
	})

	fired := false
	sub := SubprocessFn(func(ctx context.Context, j Job, runID string) (*exec.Cmd, error) {
		fired = true
		return nil, nil // shouldn't be reached
	})

	n, err := processTriggers(context.Background(), triggersDir, runsDir, now, sub, noopNotifier)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("dispatched = %d, want 0 (TTL expired)", n)
	}
	if fired {
		t.Error("SubprocessFn fired despite expired TTL")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("expired trigger file should be deleted")
	}
}

// TestProcessTriggers_MalformedQuarantined: invalid JSON gets
// moved to triggers/.malformed/ so a human can inspect.
func TestProcessTriggers_MalformedQuarantined(t *testing.T) {
	root := t.TempDir()
	triggersDir := filepath.Join(root, "triggers")
	runsDir := filepath.Join(root, "runs")

	now := time.Now().UTC()
	path := writeRawTrigger(t, triggersDir, now, "bad-syntax", []byte(`{this is not json}`))

	n, _ := processTriggers(context.Background(), triggersDir, runsDir, now, nil, noopNotifier)
	if n != 0 {
		t.Errorf("dispatched = %d, want 0 (malformed)", n)
	}
	// Original gone, quarantined copy present.
	if _, err := os.Stat(path); err == nil {
		t.Error("malformed trigger file should be moved out of inbox")
	}
	quarantined := filepath.Join(triggersDir, ".malformed", "bad-syntax.json")
	if _, err := os.Stat(quarantined); err != nil {
		t.Errorf("expected quarantined copy at %s: %v", quarantined, err)
	}
}

// TestProcessTriggers_MissingRequiredFields: trigger_id or
// prompt empty → quarantined with the missing-fields reason.
func TestProcessTriggers_MissingRequiredFields(t *testing.T) {
	root := t.TempDir()
	triggersDir := filepath.Join(root, "triggers")
	runsDir := filepath.Join(root, "runs")

	// trigger_id present but prompt empty.
	now := time.Now().UTC()
	data, _ := json.Marshal(Trigger{TriggerID: "no-prompt", Prompt: ""})
	path := writeRawTrigger(t, triggersDir, now, "no-prompt", data)

	n, _ := processTriggers(context.Background(), triggersDir, runsDir, now, nil, noopNotifier)
	if n != 0 {
		t.Errorf("dispatched = %d, want 0 (missing prompt)", n)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("incomplete trigger file should be quarantined out of inbox")
	}
}

// TestProcessTriggers_FreshFileSkipped: a file with mtime
// inside the quiescence window is skipped this tick; remains
// in the inbox for next tick. Defends against half-written
// files per PRD §8.
func TestProcessTriggers_FreshFileSkipped(t *testing.T) {
	root := t.TempDir()
	triggersDir := filepath.Join(root, "triggers")
	runsDir := filepath.Join(root, "runs")

	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(triggersDir, "fresh.json")
	data, _ := json.Marshal(Trigger{TriggerID: "fresh", Prompt: "p"})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// NO Chtimes — file mtime is now, well within quiescence.

	fired := false
	sub := SubprocessFn(func(ctx context.Context, j Job, runID string) (*exec.Cmd, error) {
		fired = true
		return nil, nil
	})

	n, _ := processTriggers(context.Background(), triggersDir, runsDir, time.Now().UTC(), sub, noopNotifier)
	if n != 0 {
		t.Errorf("dispatched = %d, want 0 (file too fresh)", n)
	}
	if fired {
		t.Error("SubprocessFn fired on too-fresh file")
	}
	// File preserved for next tick.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("fresh file should be preserved for next tick: %v", err)
	}
}

// TestProcessTriggers_MissingDirIsNotError: never-seen-before
// trigger inbox shouldn't trigger spurious errors at startup.
// Fresh installations look like "no triggers ever".
func TestProcessTriggers_MissingDirIsNotError(t *testing.T) {
	root := t.TempDir()
	nonexistent := filepath.Join(root, "no-such-triggers")
	n, err := processTriggers(context.Background(), nonexistent, filepath.Join(root, "runs"), time.Now().UTC(), nil, noopNotifier)
	if err != nil {
		t.Fatalf("missing triggers dir should not error: %v", err)
	}
	if n != 0 {
		t.Errorf("dispatched = %d, want 0", n)
	}
}

// TestProcessTriggers_IgnoresNonJSON: a .txt or no-extension
// file in the inbox is silently ignored — external producers
// might drop README.txt etc next to the .json files, that
// shouldn't crash tick.
func TestProcessTriggers_IgnoresNonJSON(t *testing.T) {
	root := t.TempDir()
	triggersDir := filepath.Join(root, "triggers")
	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.txt", "notes", "config.yaml"} {
		_ = os.WriteFile(filepath.Join(triggersDir, name), []byte("hi"), 0o644)
	}
	n, err := processTriggers(context.Background(), triggersDir, filepath.Join(root, "runs"), time.Now().UTC(), nil, noopNotifier)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("dispatched = %d, want 0", n)
	}
	// Non-JSON files NOT touched.
	for _, name := range []string{"README.txt", "notes", "config.yaml"} {
		if _, err := os.Stat(filepath.Join(triggersDir, name)); err != nil {
			t.Errorf("non-JSON file %s was unexpectedly removed: %v", name, err)
		}
	}
}

// TestTick_TriggerDispatchedAlongsideJobs: end-to-end through
// Tick — a job + a trigger both fire in one invocation.
func TestTick_TriggerDispatchedAlongsideJobs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh stub not available on Windows")
	}
	f := newTickFixture(t)
	f.createDueJob(t, "j1", "job prompt")
	writeTrigger(t, filepath.Join(f.dir, "triggers"), f.now, Trigger{
		TriggerID: "trig1", Prompt: "trigger prompt",
	})

	sub := shellStub(t, "echo handled")
	opts := f.opts(sub)
	opts.Notifier = noopNotifier

	res, err := Tick(context.Background(), f.store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fired != 1 {
		t.Errorf("Fired = %d, want 1", res.Fired)
	}
	if res.TriggersDispatched != 1 {
		t.Errorf("TriggersDispatched = %d, want 1", res.TriggersDispatched)
	}
	// Trigger file consumed; job advanced.
	if _, err := os.Stat(filepath.Join(f.dir, "triggers", "trig1.json")); err == nil {
		t.Error("trigger file not consumed")
	}
	got, _ := f.store.Get("j1")
	if got.RunCount != 1 {
		t.Errorf("job RunCount = %d, want 1", got.RunCount)
	}
}

// ----- helpers -----

// We need exec.Cmd for the test stub typings; tick_test.go's
// shellStub returns a SubprocessFn that returns *exec.Cmd, so
// we just need the import here.
//
// (Importing in main file is fine; trigger.go already imports.)
var _ = strings.Contains // keep strings import if test mutates
