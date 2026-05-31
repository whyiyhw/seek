package routines

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// DefaultSubprocess builds `seek autopilot run <goal>` for autopilot jobs
// and `seek -p <prompt> --no-save` otherwise (v7 柱 N).
func TestDefaultSubprocess_AutopilotCommand(t *testing.T) {
	cmd, err := DefaultSubprocess(context.Background(), Job{Prompt: "fix the build", Autopilot: true}, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "autopilot run fix the build") {
		t.Fatalf("autopilot job should fire `autopilot run <goal>`, got: %s", got)
	}
	if strings.Contains(got, "-p") {
		t.Fatalf("autopilot job must not use -p print mode: %s", got)
	}
}

func TestDefaultSubprocess_PlainCommandUnchanged(t *testing.T) {
	cmd, err := DefaultSubprocess(context.Background(), Job{Prompt: "summarise PRs", Yolo: true}, "run-2")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "-p summarise PRs") || !strings.Contains(got, "--no-save") || !strings.Contains(got, "--yolo") {
		t.Fatalf("plain job should fire `-p <prompt> --no-save --yolo`, got: %s", got)
	}
}

// The Autopilot flag survives the Job JSON round-trip (jobs.jsonl persist).
func TestJob_AutopilotRoundTrip(t *testing.T) {
	sched, _ := ParseSchedule("@daily")
	in := Job{Name: "nightly", Schedule: sched, Prompt: "tidy", Autopilot: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"autopilot":true`) {
		t.Fatalf("autopilot not serialised: %s", b)
	}
	var out Job
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Autopilot {
		t.Fatal("autopilot flag lost on round-trip")
	}
}
