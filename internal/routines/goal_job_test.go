package routines

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// DefaultSubprocess builds `seek goal run <condition>` for goal jobs
// (M-goal.4) — never -p, and never --yolo (goal run self-elevates).
func TestDefaultSubprocess_GoalCommand(t *testing.T) {
	cmd, err := DefaultSubprocess(context.Background(), Job{Prompt: "all tests pass", Goal: true}, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "goal run all tests pass") {
		t.Fatalf("goal job should fire `goal run <condition>`, got: %s", got)
	}
	if strings.Contains(got, "-p") || strings.Contains(got, "--yolo") {
		t.Fatalf("goal job must not use -p / --yolo (flags after subcommand aren't parsed): %s", got)
	}
}

// Autopilot wins if both are set (defensive — the CLI rejects the combo,
// but the tick must still be deterministic).
func TestDefaultSubprocess_AutopilotBeatsGoal(t *testing.T) {
	cmd, _ := DefaultSubprocess(context.Background(), Job{Prompt: "x", Autopilot: true, Goal: true}, "r")
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "autopilot run") || strings.Contains(got, "goal run") {
		t.Fatalf("autopilot must take precedence over goal: %s", got)
	}
}

func TestJob_GoalRoundTrip(t *testing.T) {
	sched, _ := ParseSchedule("@daily")
	in := Job{Name: "nightly-goal", Schedule: sched, Prompt: "lint clean", Goal: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"goal":true`) {
		t.Fatalf("goal flag not serialised: %s", b)
	}
	var out Job
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Goal {
		t.Fatal("goal flag lost on round-trip")
	}
}
