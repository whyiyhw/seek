package routinescli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/paths"
)

// withTestHome redirects ~/.seek to a tempdir so tests don't
// touch the user's real cron registry. Matches the pattern
// used in checkpointcli / hookscli / worktreecli tests.
func withTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
}

// ----- arg parsing + help -----

func TestRun_NoArgsPrintsHelp(t *testing.T) {
	var out bytes.Buffer
	if err := Run(nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"seek cron", "create", "list", "delete", "run", "tick", "@every"} {
		if !strings.Contains(out.String(), frag) {
			t.Errorf("help missing %q in:\n%s", frag, out.String())
		}
	}
}

func TestRun_UnknownVerb(t *testing.T) {
	err := Run([]string{"bogus"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("unknown verb should error")
	}
	if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "help") {
		t.Errorf("error should name verb + point at help: %v", err)
	}
}

func TestRun_HelpAliases(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var out bytes.Buffer
		if err := Run([]string{arg}, &out, &bytes.Buffer{}); err != nil {
			t.Errorf("Run(%q): %v", arg, err)
		}
		if !strings.Contains(out.String(), "seek cron") {
			t.Errorf("Run(%q) help empty:\n%s", arg, out.String())
		}
	}
}

// ----- create -----

func TestCreate_HappyPath(t *testing.T) {
	withTestHome(t)
	var out bytes.Buffer
	args := []string{"create", "--name", "morning", "--at", "@daily", "summarise PRs"}
	if err := Run(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "morning") {
		t.Errorf("output missing job name:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "@daily") {
		t.Errorf("output missing schedule:\n%s", out.String())
	}

	// Verify file landed where paths.CronJobs says.
	jobsPath, _ := paths.CronJobs()
	if _, err := os.ReadFile(jobsPath); err != nil {
		t.Errorf("jobs.jsonl not created: %v", err)
	}
}

// TestCreate_MissingPrompt: empty positional args → error
// hinting at the expected shape.
func TestCreate_MissingPrompt(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"create", "--at", "@daily"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
	if !strings.Contains(err.Error(), "<prompt>") {
		t.Errorf("error should mention <prompt>: %v", err)
	}
}

// TestCreate_BadSchedule: invalid --at surfaces ParseSchedule's
// hint verbatim.
func TestCreate_BadSchedule(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"create", "--at", "junk", "prompt"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for bad schedule")
	}
	if !strings.Contains(err.Error(), "schedule") || !strings.Contains(err.Error(), "@every") {
		t.Errorf("error should surface ParseSchedule hint: %v", err)
	}
}

// TestCreate_DuplicateRequiresForce: second create with same
// --name without --force fails with the explicit message
// (PRD §8 risk row #11 — protect against silent prompt
// overwrite).
func TestCreate_DuplicateRequiresForce(t *testing.T) {
	withTestHome(t)
	_ = Run([]string{"create", "--name", "x", "--at", "@hourly", "first"}, &bytes.Buffer{}, &bytes.Buffer{})
	err := Run([]string{"create", "--name", "x", "--at", "@hourly", "second"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected ErrJobExists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

// TestCreate_ForceOverwrites: same as above but with --force →
// the new prompt lands; second list reflects it.
func TestCreate_ForceOverwrites(t *testing.T) {
	withTestHome(t)
	_ = Run([]string{"create", "--name", "x", "--at", "@hourly", "first"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := Run([]string{"create", "--name", "x", "--at", "@hourly", "--force", "second"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("--force should overwrite: %v", err)
	}
	// Verify second prompt is what's persisted.
	var listOut bytes.Buffer
	_ = Run([]string{"list", "--json"}, &listOut, &bytes.Buffer{})
	var jobs []map[string]any
	if err := json.Unmarshal(listOut.Bytes(), &jobs); err != nil {
		t.Fatalf("list --json: %v / output:\n%s", err, listOut.String())
	}
	if len(jobs) != 1 || jobs[0]["prompt"] != "second" {
		t.Errorf("force did not overwrite prompt; jobs[0] = %+v", jobs[0])
	}
}

// TestCreate_AutoGenName: omitting --name produces a `cron-`
// prefixed auto name that passes ValidateName.
func TestCreate_AutoGenName(t *testing.T) {
	withTestHome(t)
	_ = Run([]string{"create", "--at", "@hourly", "do something"}, &bytes.Buffer{}, &bytes.Buffer{})

	var listOut bytes.Buffer
	_ = Run([]string{"list", "--json"}, &listOut, &bytes.Buffer{})
	var jobs []map[string]any
	_ = json.Unmarshal(listOut.Bytes(), &jobs)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job; got %d", len(jobs))
	}
	name, _ := jobs[0]["name"].(string)
	if !strings.HasPrefix(name, "cron-") {
		t.Errorf("auto name should start with cron-: %q", name)
	}
}

// ----- list -----

func TestList_EmptyStateHint(t *testing.T) {
	withTestHome(t)
	var out bytes.Buffer
	if err := Run([]string{"list"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no cron jobs") {
		t.Errorf("empty-state hint missing:\n%s", out.String())
	}
}

func TestList_RendersTable(t *testing.T) {
	withTestHome(t)
	_ = Run([]string{"create", "--name", "alpha", "--at", "@hourly", "A"}, &bytes.Buffer{}, &bytes.Buffer{})
	_ = Run([]string{"create", "--name", "beta", "--at", "@daily", "B"}, &bytes.Buffer{}, &bytes.Buffer{})

	var out bytes.Buffer
	if err := Run([]string{"list"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"NAME", "SCHEDULE", "NEXT_RUN", "alpha", "beta", "@hourly", "@daily"} {
		if !strings.Contains(out.String(), frag) {
			t.Errorf("table missing %q:\n%s", frag, out.String())
		}
	}
}

func TestList_JSONEmitsArray(t *testing.T) {
	withTestHome(t)
	_ = Run([]string{"create", "--name", "x", "--at", "@hourly", "p"}, &bytes.Buffer{}, &bytes.Buffer{})

	var out bytes.Buffer
	if err := Run([]string{"list", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var jobs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &jobs); err != nil {
		t.Fatalf("--json output should be JSON array: %v / got:\n%s", err, out.String())
	}
	if len(jobs) != 1 || jobs[0]["name"] != "x" {
		t.Errorf("unexpected JSON shape: %+v", jobs)
	}
}

// ----- delete -----

func TestDelete_HappyPath(t *testing.T) {
	withTestHome(t)
	_ = Run([]string{"create", "--name", "x", "--at", "@hourly", "p"}, &bytes.Buffer{}, &bytes.Buffer{})
	var out bytes.Buffer
	if err := Run([]string{"delete", "x"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `deleted cron job "x"`) {
		t.Errorf("output missing deletion message:\n%s", out.String())
	}

	// Verify list shows empty again.
	var listOut bytes.Buffer
	_ = Run([]string{"list"}, &listOut, &bytes.Buffer{})
	if !strings.Contains(listOut.String(), "no cron jobs") {
		t.Errorf("delete didn't actually remove the job:\n%s", listOut.String())
	}
}

func TestDelete_MissingArgErrors(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"delete"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for missing <name>")
	}
}

func TestDelete_NonexistentJob(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"delete", "nope"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
}

// ----- run -----

// TestRun_MissingArgErrors covers the CLI surface; the actual
// subprocess execution is tested at internal/routines/tick_test.go.
func TestRun_MissingArgErrors(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"run"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for missing <name>")
	}
}

func TestRun_NonexistentJob(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"run", "nope"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
}

// ----- tick -----

// TestTick_EmptyStoreExits: tick on an empty cron registry exits
// 0 with no output (idle path; OS scheduler logs stay clean).
func TestTick_EmptyStoreExits(t *testing.T) {
	withTestHome(t)
	var out bytes.Buffer
	if err := Run([]string{"tick"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Errorf("idle tick should print nothing; got:\n%s", out.String())
	}
}

// TestTick_VerboseEmitsIdleHint: --verbose flips the silent
// default for diagnostics.
func TestTick_VerboseEmitsIdleHint(t *testing.T) {
	withTestHome(t)
	var out bytes.Buffer
	if err := Run([]string{"tick", "--verbose"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "considered") {
		t.Errorf("verbose tick should mention 'considered':\n%s", out.String())
	}
}

// TestCreate_GoalFlag: --goal persists Job.Goal=true (M-goal.4).
func TestCreate_GoalFlag(t *testing.T) {
	withTestHome(t)
	if err := Run([]string{"create", "--name", "ng", "--at", "@daily", "--goal", "all tests pass"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	jobsPath, _ := paths.CronJobs()
	raw, err := os.ReadFile(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"goal":true`) {
		t.Fatalf("--goal not persisted into jobs.jsonl: %s", raw)
	}
}

// TestCreate_GoalAutopilotMutuallyExclusive: can't be both.
func TestCreate_GoalAutopilotMutuallyExclusive(t *testing.T) {
	withTestHome(t)
	err := Run([]string{"create", "--at", "@daily", "--autopilot", "--goal", "x"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}
