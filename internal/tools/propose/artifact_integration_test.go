package propose

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	auser "github.com/whyiyhw/seek/internal/askuser"
)

// artifactSink implements Sink + ContextReceiver + ArtifactReporter
// the way the production planBridge does. Lets us verify the full
// approval-time artifact-handoff flow without spinning up cmd/seek.
type artifactSink struct {
	recordingSink

	// captured from OnProposeStart
	onStartProblem string
	onStartSteps   []string
	onStartWhyNow  string
	onStartCount   int

	// what LastArtifactStatus will report (host pretends to have
	// run a write)
	artifactPath string
	artifactErr  error
	statusCount  int
}

func (s *artifactSink) OnProposeStart(problem string, steps []string, whyNow string) {
	s.onStartProblem = problem
	s.onStartSteps = steps
	s.onStartWhyNow = whyNow
	s.onStartCount++
}

func (s *artifactSink) LastArtifactStatus() (string, error) {
	s.statusCount++
	return s.artifactPath, s.artifactErr
}

func TestArtifact_ContextReceiverFiresBeforePicker(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &artifactSink{}
	raw := `{"problem":"refactor auth","steps":["a","b","c"],"why_now":"unblock mobile"}`
	if _, err := New(policy, sink).Execute(context.Background(), json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	if sink.onStartCount != 1 {
		t.Fatalf("OnProposeStart should fire once per Execute, got %d", sink.onStartCount)
	}
	if sink.onStartProblem != "refactor auth" {
		t.Errorf("OnProposeStart problem = %q, want %q", sink.onStartProblem, "refactor auth")
	}
	if sink.onStartWhyNow != "unblock mobile" {
		t.Errorf("OnProposeStart whyNow = %q, want %q", sink.onStartWhyNow, "unblock mobile")
	}
	if len(sink.onStartSteps) != 3 {
		t.Errorf("OnProposeStart steps len = %d, want 3", len(sink.onStartSteps))
	}
}

func TestArtifact_ApprovePathEmbedsPath(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &artifactSink{artifactPath: "/abs/path/plan.md"}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Plan artifact: /abs/path/plan.md") {
		t.Errorf("approve result missing artifact path line:\n%s", out)
	}
	if sink.statusCount != 1 {
		t.Errorf("LastArtifactStatus should be called once on approve, got %d", sink.statusCount)
	}
}

func TestArtifact_ApproveBatchPathEmbedsPath(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve_batch"}})
	sink := &artifactSink{artifactPath: "/abs/path/batch-plan.md"}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Plan artifact: /abs/path/batch-plan.md") {
		t.Errorf("approve_batch result missing artifact path:\n%s", out)
	}
	if !strings.Contains(out, "[plan: approved]") {
		t.Errorf("batch result must still carry [plan: approved] prefix (reconstruct contract):\n%s", out)
	}
}

func TestArtifact_FailurePathEmbedsNote(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &artifactSink{artifactErr: errors.New("disk full")}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(note: plan artifact write failed: disk full") {
		t.Errorf("approve result missing failure note:\n%s", out)
	}
	if strings.Contains(out, "Plan artifact:") {
		t.Errorf("failure path must not advertise a 'Plan artifact:' success line:\n%s", out)
	}
}

func TestArtifact_NoReporterSinkQuietApprove(t *testing.T) {
	// A sink that doesn't implement ArtifactReporter (e.g. tests, or
	// a host that doesn't care about artifacts) should produce a
	// clean approve result with no artifact-related line.
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"approve"}})
	sink := &recordingSink{}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Plan artifact:") || strings.Contains(out, "plan artifact write failed") {
		t.Errorf("non-reporter sink should produce no artifact line:\n%s", out)
	}
}

func TestArtifact_AdjustPathDoesNotQueryStatus(t *testing.T) {
	// Adjust path never triggers an artifact write, so propose
	// should NOT call LastArtifactStatus — querying it would be
	// noise (and might surface a stale path from a prior approve).
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"adjust"}})
	sink := &artifactSink{artifactPath: "/some/stale/plan.md"}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if sink.statusCount != 0 {
		t.Errorf("adjust path queried LastArtifactStatus %d times, want 0", sink.statusCount)
	}
	if strings.Contains(out, "Plan artifact:") {
		t.Errorf("adjust result must not include a Plan artifact line:\n%s", out)
	}
}

func TestArtifact_CancelPathDoesNotQueryStatus(t *testing.T) {
	policy := newPolicyReturning(auser.Answer{ChosenIDs: []string{"cancel"}})
	sink := &artifactSink{artifactPath: "/some/stale/plan.md"}
	out, err := New(policy, sink).Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if sink.statusCount != 0 {
		t.Errorf("cancel path queried LastArtifactStatus %d times, want 0", sink.statusCount)
	}
	if strings.Contains(out, "Plan artifact:") {
		t.Errorf("cancel result must not include a Plan artifact line:\n%s", out)
	}
}

// duplicateArtifactSink combines DuplicateChecker + ContextReceiver to
// verify the duplicate short-circuit also skips OnProposeStart? Actually
// no — OnProposeStart fires BEFORE the dedup check today. Let me lock
// that ordering: dedup must run FIRST so the host's pending state
// isn't clobbered by a duplicate propose.
type duplicateArtifactSink struct {
	artifactSink
	lastApproved []string
}

func (s *duplicateArtifactSink) Approved(steps []string, batch bool) {
	s.artifactSink.Approved(steps, batch)
	s.lastApproved = append([]string(nil), steps...)
}

func (s *duplicateArtifactSink) IsDuplicateOfLastApproved(steps []string) bool {
	if len(s.lastApproved) != len(steps) {
		return false
	}
	for i, st := range steps {
		if st != s.lastApproved[i] {
			return false
		}
	}
	return true
}

func TestArtifact_DuplicateShortCircuitSkipsBothHooks(t *testing.T) {
	// First call goes through. Second (duplicate) should NOT fire
	// OnProposeStart again (the host's pending context from the
	// in-flight propose would be wrong context) and should NOT
	// query LastArtifactStatus (the duplicate result is its own
	// fixed text).
	askCount := 0
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(auser.Question) auser.Answer {
		askCount++
		return auser.Answer{ChosenIDs: []string{"approve"}}
	})
	sink := &duplicateArtifactSink{}
	sink.artifactPath = "/abs/path/plan.md"
	tool := New(policy, sink)

	// First call — fires both hooks normally.
	if _, err := tool.Execute(context.Background(), json.RawMessage(validArgs)); err != nil {
		t.Fatal(err)
	}
	firstStartCount := sink.onStartCount
	firstStatusCount := sink.statusCount
	if firstStartCount != 1 || firstStatusCount != 1 {
		t.Fatalf("first call: onStart=%d status=%d, want 1/1", firstStartCount, firstStatusCount)
	}

	// Second call with identical args — duplicate short-circuit.
	out, err := tool.Execute(context.Background(), json.RawMessage(validArgs))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "[plan: duplicate]") {
		t.Fatalf("expected duplicate result, got: %s", out)
	}
	if sink.onStartCount != firstStartCount {
		t.Errorf("duplicate path fired OnProposeStart again: now=%d (was %d)", sink.onStartCount, firstStartCount)
	}
	if sink.statusCount != firstStatusCount {
		t.Errorf("duplicate path queried LastArtifactStatus again: now=%d (was %d)", sink.statusCount, firstStatusCount)
	}
}
