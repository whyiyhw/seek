package plan

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// recordingSink captures every StepChanged call for assertion. Safe
// for concurrent Execute calls — the mutex serialises observations.
type recordingSink struct {
	mu    sync.Mutex
	calls []sinkCall
}

type sinkCall struct {
	steps      []Step
	currentIdx int
}

func (s *recordingSink) StepChanged(snapshot []Step, currentIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, sinkCall{steps: snapshot, currentIdx: currentIdx})
}

func (s *recordingSink) last() (sinkCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return sinkCall{}, false
	}
	return s.calls[len(s.calls)-1], true
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func mustRaw(t *testing.T, action string, index int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(struct {
		Action string `json:"action"`
		Index  int    `json:"index"`
	}{Action: action, Index: index})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return raw
}

func TestSeedAndExecute_StartComplete(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tool := New(sink)
	tool.Seed([]string{"step one", "step two", "step three"})

	// Seed emits one StepChanged with all pending.
	if got := sink.count(); got != 1 {
		t.Fatalf("after seed, sink calls = %d, want 1", got)
	}
	last, _ := sink.last()
	if len(last.steps) != 3 || last.currentIdx != -1 {
		t.Fatalf("seed snapshot: %+v", last)
	}
	for i, s := range last.steps {
		if s.Status != StatusPending {
			t.Fatalf("step %d status = %q, want pending", i, s.Status)
		}
	}

	// Start step 1.
	res, err := tool.Execute(context.Background(), mustRaw(t, "start", 1))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(res, "step 1 started") {
		t.Fatalf("start result: %q", res)
	}
	last, _ = sink.last()
	if last.currentIdx != 0 || last.steps[0].Status != StatusInProgress {
		t.Fatalf("after start: %+v", last)
	}

	// Complete step 1.
	res, err = tool.Execute(context.Background(), mustRaw(t, "complete", 1))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(res, "step 1 complete, 1/3 done") {
		t.Fatalf("complete result: %q", res)
	}
	last, _ = sink.last()
	if last.currentIdx != -1 || last.steps[0].Status != StatusCompleted {
		t.Fatalf("after complete: %+v", last)
	}
}

func TestExecute_StartReplacesPreviousInProgress(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a", "b"})

	if _, err := tool.Execute(context.Background(), mustRaw(t, "start", 1)); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), mustRaw(t, "start", 2))
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	if !strings.Contains(res, "step 1 was still in_progress") {
		t.Fatalf("expected demotion note, got: %q", res)
	}

	steps, cur := tool.Snapshot()
	if cur != 1 {
		t.Fatalf("currentIdx = %d, want 1", cur)
	}
	if steps[0].Status != StatusPending {
		t.Fatalf("step 0 status = %q, want pending", steps[0].Status)
	}
	if steps[1].Status != StatusInProgress {
		t.Fatalf("step 1 status = %q, want in_progress", steps[1].Status)
	}
}

func TestExecute_Skip(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a", "b"})
	if _, err := tool.Execute(context.Background(), mustRaw(t, "start", 1)); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), mustRaw(t, "skip", 1))
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if !strings.Contains(res, "step 1 skipped") {
		t.Fatalf("skip result: %q", res)
	}
	steps, cur := tool.Snapshot()
	if cur != -1 {
		t.Fatalf("currentIdx after skip = %d, want -1", cur)
	}
	if steps[0].Status != StatusSkipped {
		t.Fatalf("step 0 status = %q, want skipped", steps[0].Status)
	}
}

func TestExecute_NoActivePlan(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	_, err := tool.Execute(context.Background(), mustRaw(t, "start", 1))
	if err == nil || !strings.Contains(err.Error(), "no active plan") {
		t.Fatalf("expected 'no active plan' error, got: %v", err)
	}
}

func TestExecute_OutOfRange(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a"})
	_, err := tool.Execute(context.Background(), mustRaw(t, "complete", 5))
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("expected 'out of range' error, got: %v", err)
	}
}

func TestExecute_IndexBelowOne(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a"})
	_, err := tool.Execute(context.Background(), mustRaw(t, "complete", 0))
	if err == nil || !strings.Contains(err.Error(), "1-based") {
		t.Fatalf("expected '1-based' error, got: %v", err)
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a"})
	_, err := tool.Execute(context.Background(), mustRaw(t, "delete", 1))
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("expected 'unknown action' error, got: %v", err)
	}
}

func TestExecute_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a"})
	raw := json.RawMessage(`{"action":"start","index":1,"extra":true}`)
	_, err := tool.Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected 'unknown field' error, got: %v", err)
	}
}

func TestExecute_RejectsEmptyAction(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a"})
	raw := json.RawMessage(`{"action":"","index":1}`)
	_, err := tool.Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "action is required") {
		t.Fatalf("expected 'action is required', got: %v", err)
	}
}

func TestExecute_AllStepsCompleteHintsExitRamp(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a", "b"})
	if _, err := tool.Execute(context.Background(), mustRaw(t, "complete", 1)); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), mustRaw(t, "complete", 2))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "All steps complete") {
		t.Fatalf("expected exit-ramp hint, got: %q", res)
	}
}

func TestSeed_ClearsPreviousState(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a", "b"})
	if _, err := tool.Execute(context.Background(), mustRaw(t, "complete", 1)); err != nil {
		t.Fatal(err)
	}
	tool.Seed([]string{"x", "y", "z"})
	steps, cur := tool.Snapshot()
	if len(steps) != 3 {
		t.Fatalf("len = %d, want 3", len(steps))
	}
	if cur != -1 {
		t.Fatalf("currentIdx = %d, want -1", cur)
	}
	for i, s := range steps {
		if s.Status != StatusPending {
			t.Fatalf("step %d status = %q, want pending", i, s.Status)
		}
	}
}

func TestClear_DropsActivePlan(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tool := New(sink)
	tool.Seed([]string{"a"})
	tool.Clear()
	steps, cur := tool.Snapshot()
	if steps != nil || cur != -1 {
		t.Fatalf("after Clear: steps=%v cur=%d, want nil/-1", steps, cur)
	}
	if got := sink.count(); got != 2 {
		t.Fatalf("sink calls after seed+clear = %d, want 2", got)
	}
}

func TestNilSink_NoCrash(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a"})
	if _, err := tool.Execute(context.Background(), mustRaw(t, "start", 1)); err != nil {
		t.Fatal(err)
	}
	tool.Clear()
}

// TestConcurrentExecute exercises the mutex under -race. Multiple
// goroutines hammer Execute + Snapshot concurrently; the only
// invariant we assert is "no panic, final state internally
// consistent" — the model's contract is "single Prompt thread", but
// this catches the race-detector class of bug from accidental shared
// mutation.
func TestConcurrentExecute(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	steps := make([]string, 8)
	for i := range steps {
		steps[i] = "s"
	}
	tool.Seed(steps)

	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = tool.Execute(context.Background(), mustRaw(t, "start", i))
		}()
		go func() {
			defer wg.Done()
			_, _ = tool.Snapshot()
		}()
	}
	wg.Wait()

	finalSteps, _ := tool.Snapshot()
	if len(finalSteps) != 8 {
		t.Fatalf("steps len = %d, want 8", len(finalSteps))
	}
}

func TestSnapshot_IsDefensiveCopy(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a", "b"})
	s1, _ := tool.Snapshot()
	s1[0].Text = "MUTATED"
	s2, _ := tool.Snapshot()
	if s2[0].Text != "a" {
		t.Fatalf("snapshot leaked internal slice: got text=%q", s2[0].Text)
	}
}

func TestSchema_IsValidJSON(t *testing.T) {
	t.Parallel()
	var v any
	if err := json.Unmarshal(schemaBytes, &v); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
}

func TestSchema_DeterministicBytes(t *testing.T) {
	t.Parallel()
	// CLAUDE.md / PRD §4.8.1: schemas are package-level []byte
	// constants so the wire bytes don't churn. Two Schema() calls
	// must return the same backing array (same address).
	tool := New(nil)
	a := tool.Schema()
	b := tool.Schema()
	if &a[0] != &b[0] {
		t.Fatalf("Schema() returned a different backing array on the second call — wire bytes would diverge")
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if got := (&Tool{}).Name(); got != "plan" {
		t.Fatalf("Name() = %q, want plan", got)
	}
}
