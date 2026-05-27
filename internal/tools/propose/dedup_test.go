package propose

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	auser "github.com/whyiyhw/seek/internal/askuser"
)

// dupSink implements Sink + DuplicateChecker so we can verify
// propose short-circuits on the second call. lastApproved is set by
// Approved exactly the way the production bridge does it.
type dupSink struct {
	recordingSink
	lastApproved []string
}

func (s *dupSink) Approved(steps []string, batch bool) {
	s.recordingSink.Approved(steps, batch)
	s.lastApproved = append([]string(nil), steps...)
}

func (s *dupSink) IsDuplicateOfLastApproved(steps []string) bool {
	if len(s.lastApproved) != len(steps) {
		return false
	}
	for i, st := range steps {
		if strings.TrimSpace(st) != strings.TrimSpace(s.lastApproved[i]) {
			return false
		}
	}
	return true
}

func TestDedup_FirstCallShowsPicker(t *testing.T) {
	asked := false
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(auser.Question) auser.Answer {
		asked = true
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})
	sink := &dupSink{}
	if _, err := New(p, sink).Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Error("first propose should show the picker")
	}
}

func TestDedup_DuplicateShortCircuits(t *testing.T) {
	askCount := 0
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(auser.Question) auser.Answer {
		askCount++
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})
	sink := &dupSink{}
	tool := New(p, sink)

	if _, err := tool.Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if askCount != 1 {
		t.Fatalf("first call ask count = %d, want 1", askCount)
	}

	// Second call with byte-identical args should NOT pop the picker.
	out, err := tool.Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("duplicate call: %v", err)
	}
	if askCount != 1 {
		t.Errorf("duplicate ask count = %d, want still 1 (picker should not re-open)", askCount)
	}
	if !strings.HasPrefix(out, "[plan: duplicate]") {
		t.Errorf("duplicate result must start with [plan: duplicate], got: %s", out)
	}
	if sink.callCount != 1 {
		t.Errorf("sink should be called once (the original approve), got %d", sink.callCount)
	}
}

func TestDedup_DifferentStepsStillShowsPicker(t *testing.T) {
	askCount := 0
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(auser.Question) auser.Answer {
		askCount++
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})
	sink := &dupSink{}
	tool := New(p, sink)

	if _, err := tool.Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatal(err)
	}

	// Different steps → picker shows again.
	otherArgs := `{"problem":"p","steps":["wholly different step"]}`
	if _, err := tool.Execute(context.Background(), json.RawMessage(otherArgs)); err != nil {
		t.Fatal(err)
	}
	if askCount != 2 {
		t.Errorf("ask count = %d, want 2 (different steps re-open the picker)", askCount)
	}
}

func TestDedup_NoCheckerSinkAlwaysShowsPicker(t *testing.T) {
	// A Sink that does NOT implement DuplicateChecker should never
	// trigger the short-circuit even if the model re-proposes
	// verbatim — the propose tool can't know there's a duplicate
	// without help from the host.
	askCount := 0
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(auser.Question) auser.Answer {
		askCount++
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})
	sink := &recordingSink{} // no DuplicateChecker
	tool := New(p, sink)

	if _, err := tool.Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatal(err)
	}
	if askCount != 2 {
		t.Fatalf("recording-only sink: ask count = %d, want 2", askCount)
	}
}

// progressSink implements Sink + ProgressReporter; lets us assert
// the adjust-path result text carries the progress line.
type progressSink struct {
	recordingSink
	summary string
}

func (s *progressSink) ProgressSummary() string { return s.summary }

func TestProgress_InjectedIntoAdjustResult(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"adjust"}})
	sink := &progressSink{summary: "completed: 1,2. in_progress: 3. pending: 4,5."}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "completed: 1,2") {
		t.Errorf("adjust result should embed progress summary, got: %s", out)
	}
	if !strings.Contains(out, "Progress on the previous") {
		t.Errorf("adjust result should announce the progress section, got: %s", out)
	}
}

func TestProgress_InjectedIntoFreeTextAdjustResult(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{FreeText: "step 4 should come before step 3"})
	sink := &progressSink{summary: "completed: 1,2."}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "completed: 1,2") {
		t.Errorf("free-text adjust should also embed progress, got: %s", out)
	}
	if !strings.Contains(out, "step 4 should come before step 3") {
		t.Errorf("free-text adjust must still echo user feedback verbatim, got: %s", out)
	}
}

func TestProgress_EmptyOmitsSection(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"adjust"}})
	sink := &progressSink{summary: ""}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Progress on the previous") {
		t.Errorf("empty progress should not render the progress section, got: %s", out)
	}
}

func TestProgress_NoReporterSinkOmitsSection(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"adjust"}})
	sink := &recordingSink{} // no ProgressReporter
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Progress on the previous") {
		t.Errorf("sink without ProgressReporter should not render progress, got: %s", out)
	}
}
