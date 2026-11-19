package propose

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	auser "github.com/whyiyhw/seek/internal/askuser"
)

// done_when / reviewed_by landed as the "plan is closeable" gate.
// These tests lock the three things that make it a gate rather than a
// suggestion: the field is REQUIRED, the caps are enforced with
// recovery-shaped errors, and the approved result hands the
// assertions back to the model (the whole point is that they stay in
// working attention during execution).

func TestExecute_RejectsMissingDoneWhen(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})

	raw := json.RawMessage(`{"problem":"x","steps":["a"]}`)
	_, err := New(policy, nil).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected done_when-required error")
	}
	msg := err.Error()
	// The error has to teach the recovery, not just refuse: name the
	// field, say what a good entry looks like, and list valid fields.
	for _, want := range []string{"done_when", "required", "Valid fields"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q for in-turn recovery; got: %v", want, err)
		}
	}
	if !strings.Contains(msg, "steps, why_now") && !strings.Contains(msg, "done_when") {
		t.Errorf("error should list the valid fields; got: %v", err)
	}
}

func TestExecute_RejectsEmptyStringDoneWhen(t *testing.T) {
	// An explicitly-empty array is as unusable as an absent one — the
	// gate must not be satisfiable by `"done_when": []`.
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	raw := json.RawMessage(`{"problem":"x","done_when":[],"steps":["a"]}`)
	if _, err := New(policy, nil).Execute(context.Background(), raw); err == nil {
		t.Fatal("empty done_when array must be rejected")
	}
}

func TestExecute_RejectsBlankDoneWhenEntry(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	raw := json.RawMessage(`{"problem":"x","done_when":["ok","  "],"steps":["a"]}`)
	_, err := New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "done_when[1]") {
		t.Errorf("expected blank-entry error naming the index, got: %v", err)
	}
}

func TestExecute_RejectsTooManyDoneWhen(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	entries := make([]string, maxDoneWhen+1)
	for i := range entries {
		entries[i] = "a test"
	}
	raw, err := json.Marshal(map[string]any{
		"problem":   "x",
		"done_when": entries,
		"steps":     []string{"a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "too many done_when") {
		t.Errorf("expected too-many-done_when error, got: %v", err)
	}
}

func TestExecute_RejectsLongDoneWhenEntry(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	long := strings.Repeat("a", maxDoneWhenLength+1)
	raw, err := json.Marshal(map[string]any{
		"problem":   "x",
		"done_when": []string{long},
		"steps":     []string{"a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "done_when[0]") {
		t.Errorf("expected long-entry error naming the index, got: %v", err)
	}
}

func TestExecute_RejectsLongReviewedBy(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	long := strings.Repeat("a", maxReviewedByLength+1)
	raw, err := json.Marshal(map[string]any{
		"problem":     "x",
		"done_when":   []string{"a test"},
		"steps":       []string{"a"},
		"reviewed_by": long,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(policy, nil).Execute(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "reviewed_by") {
		t.Errorf("expected reviewed_by-too-long error, got: %v", err)
	}
}

func TestExecute_AcceptsMinimalDoneWhen(t *testing.T) {
	// One assertion is enough to pass the gate — the tool enforces
	// presence and shape, not taste. (The skill recommends 3-5.)
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	raw := json.RawMessage(`{"problem":"x","done_when":["go test ./... exits 0"],"steps":["a"]}`)
	if _, err := New(policy, nil).Execute(context.Background(), raw); err != nil {
		t.Fatalf("single-assertion plan should be accepted: %v", err)
	}
}

// --- gate placement: the approved result must echo done_when ---------

func TestApproveResult_EchoesDoneWhenAndReviewedBy(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	raw := json.RawMessage(`{
		"problem":"p",
		"done_when":["go test -race ./... exits 0","grep finds loadMeta in AGENTS.md"],
		"steps":["step one","step two"],
		"reviewed_by":"a new agent session given plan + diff; human decides"
	}`)
	out, err := New(policy, nil).Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "[plan: approved]") {
		t.Fatalf("approve result must keep the [plan: approved] prefix (reconstruct contract); got: %s", out)
	}
	for _, want := range []string{
		"Done when",
		"go test -race ./... exits 0",
		"grep finds loadMeta in AGENTS.md",
		"Reviewed by:",
		"a new agent session given plan + diff; human decides",
		"acceptance criteria",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("approve result should carry %q so it stays in working attention:\n%s", want, out)
		}
	}
}

func TestApproveResult_OmitsReviewedByWhenAbsent(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	out, err := New(policy, nil).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Reviewed by:") {
		t.Errorf("absent reviewed_by must not render a Reviewed by section:\n%s", out)
	}
	// done_when is required, so it is always echoed.
	if !strings.Contains(out, "Done when") {
		t.Errorf("done_when must always be echoed:\n%s", out)
	}
}

// --- picker header --------------------------------------------------

func TestQuestion_IncludesDoneWhenBeforeSteps(t *testing.T) {
	var captured auser.Question
	p := auser.New(auser.ModeAsk)
	p.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})
	raw := json.RawMessage(`{"problem":"p","done_when":["assertion A"],"steps":["step B"]}`)
	if _, err := New(p, nil).Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	hdr := captured.Question
	if !strings.Contains(hdr, "Done when:") {
		t.Errorf("picker header should label the completion conditions, got: %s", hdr)
	}
	// The user decides whether to approve; the completion conditions
	// must be visible before the step list, not buried after it.
	if strings.Index(hdr, "Done when:") > strings.Index(hdr, "Plan:") {
		t.Errorf("Done when must come before Plan: in the picker (user reads it first); got:\n%s", hdr)
	}
	if !strings.Contains(hdr, "assertion A") {
		t.Errorf("picker header must show the assertion text, got: %s", hdr)
	}
}

// --- CompletionReceiver sibling interface ---------------------------

type completionSink struct {
	recordingSink
	doneWhen     []string
	reviewedBy   string
	completionCh int
}

func (s *completionSink) OnProposeCompletion(doneWhen []string, reviewedBy string) {
	s.doneWhen = doneWhen
	s.reviewedBy = reviewedBy
	s.completionCh++
}

func TestCompletion_ReceiverFiresBeforePicker(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &completionSink{}
	raw := json.RawMessage(`{"problem":"p","done_when":["a","b"],"steps":["s"],"reviewed_by":"glm lane"}`)
	if _, err := New(policy, sink).Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if sink.completionCh != 1 {
		t.Fatalf("OnProposeCompletion should fire once per Execute, got %d", sink.completionCh)
	}
	if len(sink.doneWhen) != 2 || sink.doneWhen[0] != "a" {
		t.Errorf("doneWhen not handed over verbatim: %#v", sink.doneWhen)
	}
	if sink.reviewedBy != "glm lane" {
		t.Errorf("reviewedBy = %q, want %q", sink.reviewedBy, "glm lane")
	}
}

func TestCompletion_SinkWithoutInterfaceStillWorks(t *testing.T) {
	// recordingSink implements Sink only — the sibling interface must
	// stay optional, exactly like ContextReceiver / ArtifactReporter.
	// This is the compile-time + runtime guarantee that adding
	// done_when didn't widen the existing contract.
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &recordingSink{}
	if _, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatalf("plain Sink should still work: %v", err)
	}
	if !sink.approved {
		t.Error("Approved should still fire for a plain Sink")
	}
}

func TestSchema_DeclaresDoneWhenRequired(t *testing.T) {
	var s struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &s); err != nil {
		t.Fatalf("schema must parse: %v", err)
	}
	var hasDoneWhen bool
	for _, r := range s.Required {
		if r == "done_when" {
			hasDoneWhen = true
		}
	}
	if !hasDoneWhen {
		t.Errorf("done_when must be in the schema's required list (the model reads the schema, not this test); required=%v", s.Required)
	}
	for _, f := range []string{"done_when", "reviewed_by"} {
		if _, ok := s.Properties[f]; !ok {
			t.Errorf("schema missing property %q", f)
		}
	}
}
