package propose

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	auser "github.com/whyiyhw/seek/internal/askuser"
)

// validArgs is the canonical "well-formed args" fixture reused by
// happy-path tests. Sized at 3 steps so it's also covered by the
// approved-result step echoing checks.
const validArgs = `{
	"problem": "Refactor auth middleware for per-request token store",
	"steps": ["Inventory call sites", "Define interface", "Migrate store"]
}`

func newPolicyReturning(ans auser.Answer) *auser.Policy {
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(_ auser.Question) auser.Answer { return ans })
	return p
}

type recordingSink struct {
	approved       bool
	approvedSteps  []string
	adjusted       bool
	adjustFeedback string
	cancelled      bool
	callCount      int
}

func (s *recordingSink) Approved(steps []string) {
	s.approved = true
	s.approvedSteps = steps
	s.callCount++
}
func (s *recordingSink) AdjustRequested(feedback string) {
	s.adjusted = true
	s.adjustFeedback = feedback
	s.callCount++
}
func (s *recordingSink) Cancelled() {
	s.cancelled = true
	s.callCount++
}

// --- Happy paths ---------------------------------------------------

func TestExecute_Approve(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &recordingSink{}

	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[plan: approved]") {
		t.Errorf("result should start with [plan: approved], got: %s", out)
	}
	if !strings.Contains(out, "Inventory call sites") {
		t.Errorf("approved result should echo steps verbatim, got: %s", out)
	}
	if !sink.approved {
		t.Error("sink.Approved should have been called")
	}
	if len(sink.approvedSteps) != 3 || sink.approvedSteps[0] != "Inventory call sites" {
		t.Errorf("sink.Approved got steps=%v, want 3 steps starting with 'Inventory call sites'", sink.approvedSteps)
	}
	if sink.callCount != 1 {
		t.Errorf("sink should have been called exactly once, got %d", sink.callCount)
	}
}

func TestExecute_AdjustWithoutFeedback(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"adjust"}})
	sink := &recordingSink{}

	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[plan: adjust requested]") {
		t.Errorf("result should start with [plan: adjust requested], got: %s", out)
	}
	if !strings.Contains(out, "No specific feedback") {
		t.Errorf("empty-feedback adjust should prompt to ask user what to change, got: %s", out)
	}
	if !sink.adjusted || sink.adjustFeedback != "" {
		t.Errorf("sink: adjusted=%v feedback=%q, want adjusted=true feedback=\"\"", sink.adjusted, sink.adjustFeedback)
	}
}

func TestExecute_AdjustWithFreeText(t *testing.T) {
	feedback := "step 3 should be split — interface first, store change separate"
	policy := newPolicyReturning(auser.Answer{FreeText: feedback})
	sink := &recordingSink{}

	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[plan: adjust requested]") {
		t.Errorf("free-text response should produce adjust result, got: %s", out)
	}
	if !strings.Contains(out, feedback) {
		t.Errorf("adjust result should echo user feedback verbatim, got: %s", out)
	}
	if sink.adjustFeedback != feedback {
		t.Errorf("sink got feedback=%q, want %q", sink.adjustFeedback, feedback)
	}
}

func TestExecute_CancelById(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"cancel"}})
	sink := &recordingSink{}

	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[plan: cancelled]") {
		t.Errorf("cancel pick should produce cancel result, got: %s", out)
	}
	if !sink.cancelled {
		t.Error("sink.Cancelled should have been called")
	}
}

func TestExecute_CancelByEsc(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{Cancelled: true})
	sink := &recordingSink{}

	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[plan: cancelled]") {
		t.Errorf("Esc cancellation should produce cancel result, got: %s", out)
	}
	if !sink.cancelled {
		t.Error("sink.Cancelled should have been called for Esc")
	}
}

// --- Sink optionality ----------------------------------------------

func TestExecute_NilSinkOK(t *testing.T) {
	// P1 tool must work without a sink — P2 wires the real one.
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	out, err := New(policy, nil).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatalf("nil sink should not error: %v", err)
	}
	if !strings.HasPrefix(out, "[plan: approved]") {
		t.Errorf("nil-sink path should still produce normal result, got: %s", out)
	}
}

func TestExecute_NilSinkAllBranches(t *testing.T) {
	cases := []struct {
		name string
		ans  auser.Answer
	}{
		{"approve", auser.Answer{ChosenIDs: []string{"approve"}}},
		{"adjust", auser.Answer{ChosenIDs: []string{"adjust"}}},
		{"freetext", auser.Answer{FreeText: "x"}},
		{"cancel-id", auser.Answer{ChosenIDs: []string{"cancel"}}},
		{"cancel-esc", auser.Answer{Cancelled: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy := newPolicyReturning(c.ans)
			if _, err := New(policy, nil).Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
				t.Errorf("nil sink + %s should not error: %v", c.name, err)
			}
		})
	}
}

// --- Schema validation ---------------------------------------------

func TestExecute_RejectsUnknownField(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	raw := json.RawMessage(`{"problem":"x","steps":["a"],"foo":"bar"}`)
	_, err := New(policy, nil).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected unknown-field error")
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error should mention unknown field 'foo', got: %v", err)
	}
}

func TestExecute_RejectsEmptyProblem(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	raw := json.RawMessage(`{"problem":"   ","steps":["a"]}`)
	_, err := New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "problem") {
		t.Errorf("expected missing-problem error, got: %v", err)
	}
}

func TestExecute_RejectsEmptySteps(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	raw := json.RawMessage(`{"problem":"x","steps":[]}`)
	_, err := New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "steps") {
		t.Errorf("expected missing-steps error, got: %v", err)
	}
}

func TestExecute_RejectsTooManySteps(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	steps := make([]string, 13)
	for i := range steps {
		steps[i] = "step"
	}
	raw, err := json.Marshal(map[string]any{"problem": "x", "steps": steps})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Errorf("expected too-many-steps error, got: %v", err)
	}
}

func TestExecute_RejectsLongStep(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	longStep := strings.Repeat("a", maxStepLength+1)
	raw, err := json.Marshal(map[string]any{"problem": "x", "steps": []string{longStep}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Errorf("expected step-too-long error, got: %v", err)
	}
}

func TestExecute_RejectsEmptyStepInList(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	raw := json.RawMessage(`{"problem":"x","steps":["a","  ","b"]}`)
	_, err := New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "step 1") {
		t.Errorf("expected empty-step-1 error, got: %v", err)
	}
}

// --- Policy plumbing -----------------------------------------------

func TestExecute_NilPolicyErrors(t *testing.T) {
	_, err := New(nil, nil).Execute(context.Background(), json.RawMessage(validArgs))
	if err == nil {
		t.Fatal("expected nil-policy error")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("error should mention policy, got: %v", err)
	}
}

func TestExecute_PolicyDisabledPropagates(t *testing.T) {
	p := auser.New(auser.ModeDisabled)
	_, err := New(p, nil).Execute(context.Background(), json.RawMessage(validArgs))
	if err == nil {
		t.Fatal("expected error from disabled policy")
	}
	if !errors.Is(err, auser.ErrDisabled) {
		t.Errorf("expected ErrDisabled, got: %v", err)
	}
}

// --- Picker question shape -----------------------------------------

func TestQuestion_IncludesSteps(t *testing.T) {
	var captured auser.Question
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})

	if _, err := New(p, nil).Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"Inventory call sites", "1. Inventory", "Plan:"} {
		if !strings.Contains(captured.Question, want) {
			t.Errorf("picker header missing %q, got: %s", want, captured.Question)
		}
	}
}

func TestQuestion_IncludesWhyNow(t *testing.T) {
	var captured auser.Question
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})

	raw := json.RawMessage(`{"problem":"x","steps":["a"],"why_now":"because the bug is breaking prod"}`)
	if _, err := New(p, nil).Execute(context.Background(), raw); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(captured.Question, "because the bug is breaking prod") {
		t.Errorf("picker header should include why_now, got: %s", captured.Question)
	}
	if !strings.Contains(captured.Question, "Why now:") {
		t.Errorf("picker header should label why_now, got: %s", captured.Question)
	}
}

func TestQuestion_OmitsWhyNowWhenEmpty(t *testing.T) {
	var captured auser.Question
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})

	if _, err := New(p, nil).Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(captured.Question, "Why now") {
		t.Errorf("picker header should NOT have Why now: label when omitted, got: %s", captured.Question)
	}
}

func TestOptions_AreApproveAdjustCancel(t *testing.T) {
	var captured auser.Question
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})

	if _, err := New(p, nil).Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(captured.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(captured.Options))
	}
	for i, want := range []string{"approve", "adjust", "cancel"} {
		if captured.Options[i].ID != want {
			t.Errorf("option %d: id=%q, want %q", i, captured.Options[i].ID, want)
		}
	}
	if captured.MultiSelect {
		t.Error("propose picker must be single-select; approve/adjust/cancel are mutually exclusive")
	}
}

// --- Misc ----------------------------------------------------------

func TestSchema_IsValidJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal(schemaBytes, &v); err != nil {
		t.Fatalf("schemaBytes must be valid JSON: %v", err)
	}
}

func TestSchema_DeterministicBytes(t *testing.T) {
	// Per CLAUDE.md / PRD §4.8.1: schemas are package-level []byte
	// constants so identical bytes appear in every turn (DeepSeek's
	// prefix cache requirement). Calling Schema() twice must return
	// the same slice header pointing at the same bytes.
	tool := New(nil, nil)
	a := tool.Schema()
	b := tool.Schema()
	if string(a) != string(b) {
		t.Errorf("Schema() must be byte-identical across calls")
	}
}

func TestName(t *testing.T) {
	if New(nil, nil).Name() != "propose" {
		t.Errorf("Name() = %q, want %q", New(nil, nil).Name(), "propose")
	}
}
