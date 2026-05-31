package autopilot

import (
	"strings"
	"testing"
)

func sampleReport() Report {
	return Report{
		Goal: "fix the build and add tests",
		Outcomes: []Outcome{
			{Task: Task{Title: "fix build"}, Status: "done", Worktree: "/wt/a"},
			{Task: Task{Title: "add tests"}, Status: "done", Worktree: "/wt/b"},
			{Task: Task{Title: "bump deps"}, Status: "failed", Summary: "boom"},
		},
		Done:   2,
		Failed: 1,
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
