package autopilot

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/verifier"
)

func sampleReport() Report {
	return Report{
		Goal: "fix the build and add tests",
		Outcomes: []Outcome{
			{Task: Task{Title: "fix build"}, Status: "done", Worktree: "/wt/a", Commit: "abc1234",
				Tokens: 5100, CostUSD: 0.02, Files: 2, Insertions: 10, Deletions: 3},
			{Task: Task{Title: "add tests"}, Status: "done", Worktree: "/wt/b",
				Tokens: 2000, CostUSD: 0.01},
			{Task: Task{Title: "bump deps"}, Status: "failed", Summary: "boom"},
		},
		Done:         2,
		Failed:       1,
		TotalTokens:  7100,
		TotalCostUSD: 0.03,
	}
}

func TestReport_Event(t *testing.T) {
	if got := sampleReport().Event(); got != "autopilot.completed" {
		t.Fatalf("event = %q, want autopilot.completed (some done)", got)
	}
	allFail := Report{Outcomes: []Outcome{{Status: "failed"}}, Failed: 1}
	if got := allFail.Event(); got != "autopilot.failed" {
		t.Fatalf("event = %q, want autopilot.failed (none done)", got)
	}
}

func TestReport_Title(t *testing.T) {
	if got := sampleReport().Title(); got != "autopilot: 2/3 done" {
		t.Fatalf("title = %q", got)
	}
}

func TestReport_Body(t *testing.T) {
	body := sampleReport().Body()
	for _, want := range []string{"fix the build and add tests", "2/3 done", "1 failed", "✓ fix build", "→ /wt/a", "✗ bump deps"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

// TestReport_BodyEvidence: every task line carries the v8 柱 R R1
// suffix "Δ<files>f +<ins>/-<del> · <tokens>tok · $<cost>", the header
// carries the total cost, and the pre-existing fragments (title, SHA,
// worktree) survive on the SAME line as the suffix.
func TestReport_BodyEvidence(t *testing.T) {
	body := sampleReport().Body()
	lines := strings.Split(body, "\n")
	if len(lines) != 4 { // header + 3 tasks
		t.Fatalf("expected 4 lines, got %d:\n%s", len(lines), body)
	}
	if !strings.HasSuffix(lines[0], "$0.030") {
		t.Fatalf("header should end with the total cost, got %q", lines[0])
	}
	cases := []struct {
		line int
		want string
	}{
		{1, "✓ fix build  [abc1234]  → /wt/a  Δ2f +10/-3 · 5100tok · $0.020"},
		{2, "✓ add tests  → /wt/b  Δ0f +0/-0 · 2000tok · $0.010"},
		{3, "✗ bump deps  Δ0f +0/-0 · 0tok · $0"},
	}
	for _, c := range cases {
		if lines[c.line] != "  "+c.want {
			t.Fatalf("line %d =\n  %q\nwant\n  %q", c.line, lines[c.line], c.want)
		}
	}
}

// TestReport_BodyVerdicts: v8 柱 R R2 — tasks carrying a verdict get
// the "· verdict=<v>" suffix on their line, the header carries the
// pass/doubt/fail counts when verification ran, and verdict-less
// reports render exactly as before.
func TestReport_BodyVerdicts(t *testing.T) {
	rep := sampleReport()
	rep.Outcomes[0].Verdict = &verifier.Verdict{Overall: verifier.Pass}
	rep.Outcomes[1].Verdict = &verifier.Verdict{
		Overall:    verifier.Fail,
		Invariants: []verifier.InvariantResult{{ID: "S", Name: "scope", Verdict: verifier.Fail, Detail: "main tree drifted"}},
	}
	rep.VerifiedPass, rep.VerifiedDoubt, rep.VerifiedFail = 1, 0, 1

	body := rep.Body()
	lines := strings.Split(body, "\n")
	if !strings.Contains(lines[0], "verify 1/0/1 pass/doubt/fail") {
		t.Fatalf("header must carry the verdict counts, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "verdict=pass") {
		t.Fatalf("done task line must carry its verdict, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "verdict=fail (scope)") {
		t.Fatalf("failing verdict must name its cause, got %q", lines[2])
	}
	if strings.Contains(lines[3], "verdict=") {
		t.Fatalf("nil-verdict task must render no suffix, got %q", lines[3])
	}

	// Regression guard: a verdict-less report renders byte-identically
	// to the pre-R2 shape (no "verify" in the header).
	if strings.Contains(sampleReport().Body(), "verify") {
		t.Fatal("unverified report must not render verdict chrome")
	}
}
