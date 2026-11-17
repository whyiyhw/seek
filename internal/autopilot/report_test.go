package autopilot

import (
	"strings"
	"testing"
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
