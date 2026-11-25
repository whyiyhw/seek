package routines

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// Autopilot children carry the cron marker; plain jobs must not — a
// human-typed `seek autopilot run` keeps the ungated lane (v8 §9.5③1).
func TestDefaultSubprocess_AutopilotStampsCronMarker(t *testing.T) {
	cmd, err := DefaultSubprocess(context.Background(), Job{Prompt: "g", Autopilot: true}, "run-m")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, kv := range cmd.Env {
		if kv == EnvAutopilotCron+"=1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("autopilot child env should carry exactly one %s=1, got %d", EnvAutopilotCron, count)
	}
}

func TestDefaultSubprocess_PlainJobNoCronMarker(t *testing.T) {
	cmd, err := DefaultSubprocess(context.Background(), Job{Prompt: "g"}, "run-m2")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, EnvAutopilotCron+"=") {
			t.Fatalf("plain job env must not carry the cron marker: %s", kv)
		}
	}
}

// The tick's stamp wins over a marker value smuggled in via the env
// overlay (last-wins cmd.Env semantics) — the spawner, not the
// overlay file, decides that a run is cron-fired.
func TestDefaultSubprocess_MarkerOverridesOverlayValue(t *testing.T) {
	old := envOverlayPath
	envOverlayPath = func() (string, error) { return filepath.Join(t.TempDir(), "env"), nil }
	defer func() { envOverlayPath = old }()
	overlayPath, _ := envOverlayPath()
	if err := os.WriteFile(overlayPath, []byte(EnvAutopilotCron+"=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd, err := DefaultSubprocess(context.Background(), Job{Prompt: "g", Autopilot: true}, "run-m3")
	if err != nil {
		t.Fatal(err)
	}
	last := ""
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, EnvAutopilotCron+"=") {
			last = kv
		}
	}
	if last != EnvAutopilotCron+"=1" {
		t.Fatalf("last marker entry should be the tick's =1, got %q", last)
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
