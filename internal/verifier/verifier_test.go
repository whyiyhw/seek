package verifier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeJudge is the Judge seam: canned rows, canned note, canned
// error. The regression test's "gullible" judge (all pass, citing
// only the green tests) is the adversarial configuration.
type fakeJudge struct {
	rows []JudgedAssertion
	note string
	err  error
	// bundles records every bundle handed in — tests assert the
	// judge never saw anything beyond the evidence bundle.
	bundles []string
}

func (f *fakeJudge) Judge(ctx context.Context, model, bundle string) (Judgement, error) {
	f.bundles = append(f.bundles, bundle)
	if f.err != nil {
		return Judgement{}, f.err
	}
	return Judgement{Assertions: f.rows, Note: f.note, Tokens: 4321}, nil
}

// allPassJudge returns a judge that marks EVERY assertion pass,
// citing only the test evidence — the "tests are green, ship it"
// posture the verifier must survive.
func allPassJudge() *fakeJudge {
	return &fakeJudge{note: "tests green, looks done"}
}

func (f *fakeJudge) passAll(n int, evidence ...string) *fakeJudge {
	for i := 1; i <= n; i++ {
		f.rows = append(f.rows, JudgedAssertion{Index: i, Verdict: Pass, Evidence: evidence, Note: "green"})
	}
	return f
}

// TestVerify_GullibleJudgeCannotOverrideMechanicalFail: the clamp.
// Judge says everything passed; the tests ARE green; the scope
// invariant says the main tree moved. Overall must be fail.
func TestVerify_GullibleJudgeCannotOverrideMechanicalFail(t *testing.T) {
	vd := &Verdict{
		Invariants: []InvariantResult{{ID: "S", Name: "scope", Verdict: Fail, Detail: "main tree drifted"}},
		Assertions: []AssertionResult{{Index: 1, Text: "x", Verdict: Pass, Evidence: []string{"T1"}}},
	}
	if got := composeOverall(vd); got != Fail {
		t.Fatalf("composeOverall = %q, want fail (mechanical fail must clamp)", got)
	}
}

// TestVerify_ComposeOverallPrecedence: fail > doubt > pass, and a
// judge error floors at doubt.
func TestVerify_ComposeOverallPrecedence(t *testing.T) {
	cases := []struct {
		name string
		vd   Verdict
		want string
	}{
		{"all pass", Verdict{Assertions: []AssertionResult{{Verdict: Pass}}}, Pass},
		{"doubt beats pass", Verdict{
			Assertions: []AssertionResult{{Verdict: Pass}, {Verdict: Doubt}}}, Doubt},
		{"fail beats doubt", Verdict{
			Assertions: []AssertionResult{{Verdict: Doubt}},
			Invariants: []InvariantResult{{Verdict: Fail}}}, Fail},
		{"judge error floors at doubt", Verdict{
			Assertions: []AssertionResult{{Verdict: Pass}},
			JudgeError: "judge call: 401"}, Doubt},
		{"empty verdict is not pass by accident", Verdict{}, Pass}, // nothing checked, nothing failed — but see TestVerify_NoAssertions
	}
	for _, c := range cases {
		if got := composeOverall(&c.vd); got != c.want {
			t.Errorf("%s: composeOverall = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestVerify_NoAssertionsIsDoubt: a task without done_when cannot be
// verified — the invariant row lands as doubt, never pass.
func TestVerify_NoAssertionsIsDoubt(t *testing.T) {
	ver := NewWithJudge(allPassJudge().passAll(0), "m")
	vd := ver.Verify(context.Background(), Request{TaskTitle: "t", WorkDir: t.TempDir()})
	if vd.Overall != Doubt {
		t.Fatalf("overall = %q, want doubt for an assertion-less task", vd.Overall)
	}
	found := false
	for _, inv := range vd.Invariants {
		if inv.ID == "A" && inv.Verdict == Doubt {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an assertions invariant row, got %+v", vd.Invariants)
	}
}

// TestVerify_PassWithoutGroundedEvidenceDowngrades: a judge "pass"
// citing nothing (or citing ids that don't exist in the bundle)
// becomes doubt. A pass must point at something real.
func TestVerify_PassWithoutGroundedEvidenceDowngrades(t *testing.T) {
	rows := []JudgedAssertion{
		{Index: 1, Verdict: Pass, Evidence: nil, Note: "trust me"},
		{Index: 2, Verdict: Pass, Evidence: []string{"T9", "X7"}, Note: "hallucinated ids"},
		{Index: 3, Verdict: Pass, Evidence: []string{"D"}, Note: "cites the diff"},
	}
	known := map[string]bool{"D": true, "T1": true}
	got := clampAssertions([]string{"a", "b", "c"}, rows, known, "")
	if got[0].Verdict != Doubt || got[1].Verdict != Doubt {
		t.Fatalf("unevidenced passes must downgrade to doubt, got %+v", got)
	}
	if got[2].Verdict != Pass || len(got[2].Evidence) != 1 || got[2].Evidence[0] != "D" {
		t.Fatalf("grounded pass must survive, got %+v", got[2])
	}
	// Hallucinated ids are dropped from the record, not carried along.
	if len(got[1].Evidence) != 0 {
		t.Fatalf("ungrounded ids must be stripped, got %+v", got[1].Evidence)
	}
}

// TestVerify_JudgeSkippedAssertionBecomesDoubt: every assertion gets
// a row even when the judge ignored it.
func TestVerify_JudgeSkippedAssertionBecomesDoubt(t *testing.T) {
	rows := []JudgedAssertion{{Index: 1, Verdict: Pass, Evidence: []string{"D"}}}
	got := clampAssertions([]string{"a", "b"}, rows, map[string]bool{"D": true}, "")
	if len(got) != 2 {
		t.Fatalf("expected one row per assertion, got %d", len(got))
	}
	if got[1].Verdict != Doubt || !strings.Contains(got[1].Note, "did not address") {
		t.Fatalf("skipped assertion must be doubt with a reason, got %+v", got[1])
	}
}

// TestVerify_JudgeErrorIsDoubtNeverPass: judge session fails (401,
// network, unparseable) → doubt with the reason recorded, and every
// assertion row carries it.
func TestVerify_JudgeErrorIsDoubtNeverPass(t *testing.T) {
	j := &fakeJudge{err: errors.New("judge call: 401 unauthorized")}
	ver := NewWithJudge(j, "m")
	vd := ver.Verify(context.Background(), Request{
		TaskTitle:  "t",
		Assertions: []string{"a"},
		WorkDir:    t.TempDir(),
	})
	if vd.Overall != Doubt {
		t.Fatalf("overall = %q, want doubt on judge error", vd.Overall)
	}
	if !strings.Contains(vd.JudgeError, "401") {
		t.Fatalf("judge error must be recorded, got %q", vd.JudgeError)
	}
	if len(vd.Assertions) != 1 || vd.Assertions[0].Verdict != Doubt {
		t.Fatalf("assertion rows must exist and be doubt, got %+v", vd.Assertions)
	}
	if vd.FinishedAt.Before(vd.StartedAt) {
		t.Fatal("verdict timestamps must bracket the run")
	}
}

// TestVerify_ContextCanceled: ctx dead on entry → the verdict still
// resolves (doubt), never hangs, never passes.
func TestVerify_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := &fakeJudge{err: context.Canceled}
	ver := NewWithJudge(j, "m")
	done := make(chan *Verdict, 1)
	go func() {
		done <- ver.Verify(ctx, Request{TaskTitle: "t", Assertions: []string{"a"}, WorkDir: t.TempDir()})
	}()
	select {
	case vd := <-done:
		if vd.Overall != Doubt {
			t.Fatalf("overall = %q, want doubt under cancellation", vd.Overall)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Verify did not return after ctx cancel")
	}
}

// TestVerify_PerAssertionRowsCarryMechanicalEvidence: acceptance ② —
// the verdict is per-assertion rows (index, text, conclusion, cited
// evidence), not a prose blob.
func TestVerify_PerAssertionRowsCarryMechanicalEvidence(t *testing.T) {
	j := allPassJudge()
	j.passAll(2, "T1", "D")
	ver := NewWithJudge(j, "m")
	vd := ver.Verify(context.Background(), Request{
		TaskTitle:  "append a line",
		Assertions: []string{"README gains a line", "tests pass"},
		WorkDir:    t.TempDir(),
	})
	if len(vd.Assertions) != 2 {
		t.Fatalf("expected 2 assertion rows, got %d", len(vd.Assertions))
	}
	for i, a := range vd.Assertions {
		if a.Index != i+1 {
			t.Errorf("row %d has index %d", i, a.Index)
		}
		if a.Text == "" {
			t.Errorf("row %d has no assertion text", i)
		}
		if a.Verdict != Pass || len(a.Evidence) == 0 {
			t.Errorf("row %d = %+v, want evidenced pass", i, a)
		}
	}
	// The bundle the judge saw contains the numbered assertions —
	// the mapping material, not a summary.
	if len(j.bundles) != 1 || !strings.Contains(j.bundles[0], "1. README gains a line") {
		t.Fatalf("bundle must list the numbered assertions, got:\n%s", j.bundles[0])
	}
	if vd.JudgeTokens != 4321 {
		t.Fatalf("judge token burn must be recorded, got %d", vd.JudgeTokens)
	}
}

// TestVerdict_Summary: one-line rendering for the report body.
func TestVerdict_Summary(t *testing.T) {
	nilV := (*Verdict)(nil)
	if got := nilV.Summary(); got != "" {
		t.Fatalf("nil verdict summary = %q, want empty", got)
	}
	pass := &Verdict{Overall: Pass}
	if got := pass.Summary(); got != "verdict=pass" {
		t.Fatalf("pass summary = %q", got)
	}
	scopeFail := &Verdict{Overall: Fail, Invariants: []InvariantResult{{ID: "S", Name: "scope", Verdict: Fail}}}
	if got := scopeFail.Summary(); got != "verdict=fail (scope)" {
		t.Fatalf("scope-fail summary = %q", got)
	}
	assertFail := &Verdict{Overall: Fail, Assertions: []AssertionResult{{Index: 2, Verdict: Fail}}}
	if got := assertFail.Summary(); got != "verdict=fail (assertion 2)" {
		t.Fatalf("assertion-fail summary = %q", got)
	}
}

// TestVerify_RequestedTestsThatCannotRunAreDoubt: TestCmds without a
// WorkDir (or any layer-level shortfall) must not read as a green
// suite — partial evidence is doubt.
func TestVerify_RequestedTestsThatCannotRunAreDoubt(t *testing.T) {
	ver := NewWithJudge(allPassJudge().passAll(1, "T1"), "m")
	vd := ver.Verify(context.Background(), Request{
		TaskTitle:  "t",
		Assertions: []string{"a"},
		TestCmds:   []string{"go test ./..."},
		// WorkDir deliberately empty.
	})
	if vd.Overall != Doubt {
		t.Fatalf("overall = %q, want doubt when requested tests could not run", vd.Overall)
	}
	found := false
	for _, inv := range vd.Invariants {
		if inv.ID == "T" && inv.Verdict == Doubt && strings.Contains(inv.Detail, "0 of 1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a doubt tests invariant spelling the shortfall, got %+v", vd.Invariants)
	}
}
