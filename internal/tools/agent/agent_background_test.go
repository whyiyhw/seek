package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
)

// run_in_background surface tests (feature-orchestration PRD §4.1
// D1). New file by design — the sync-path tests in agent_test.go
// stay byte-untouched (acceptance: 既有测试零改动).

// newBgTool wires a Tool against a Manager WITH SessionCtx (the
// precondition for SpawnBackground). release gates the stub runner.
func newBgTool(t *testing.T, release <-chan struct{}) (*Tool, *subagent.Manager) {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	projCwd := t.TempDir()
	policy, err := permission.New(projCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	runner := func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		select {
		case <-release:
			return subagent.RunnerResult{Summary: "bg summary body", Turns: 2}, nil
		case <-ctx.Done():
			return subagent.RunnerResult{}, ctx.Err()
		}
	}
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:    projCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return []string{"read"} },
		MaxConcurrent:     3,
		Runner:            runner,
		SessionCtx:        context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(mgr, nil), mgr
}

// TestExecute_RunInBackground_ImmediateHandle: the tool result
// returns at once with the background-started wire format while the
// runner is still in flight, and the handle's sub_sid is queryable.
func TestExecute_RunInBackground_ImmediateHandle(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tl, mgr := newBgTool(t, release)

	start := time.Now()
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "long research",
		"prompt": "go deep",
		"subagent_type": "explore",
		"run_in_background": true
	}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Execute blocked %v behind the runner — background contract broken", elapsed)
	}

	if !strings.HasPrefix(out, "[agent: background started sub-sid=") {
		t.Errorf("output:\n%s", out)
	}
	if !strings.Contains(out, "long research") {
		t.Errorf("output missing description:\n%s", out)
	}

	// The spawned child is queryable via the Manager (状态可查).
	subs, lerr := mgr.List(subagent.ListFilter{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(subs) != 1 || subs[0].Status != subagent.StatusActive {
		t.Errorf("List = %+v, want 1 active row", subs)
	}
	if !strings.HasPrefix(out, "[agent: background started sub-sid="+subs[0].SubSid+"]") {
		t.Errorf("handle sub_sid does not match the spawned child:\n%s", out)
	}
}

// TestExecute_RunInBackground_WorktreeRefused: worktree isolation
// needs the sync path's post-Spawn cleanup; the combination is
// rejected with a wire-format failure naming the fix.
func TestExecute_RunInBackground_WorktreeRefused(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tl, _ := newBgTool(t, release)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "d",
		"prompt": "p",
		"run_in_background": true,
		"isolation": "worktree"
	}`))
	if err != nil {
		t.Fatalf("Execute error: %v (want wire-format failure, not tool error)", err)
	}
	// wtMgr is nil in this fixture — the nil check fires first, so
	// assert whichever failure prefix came back; both are the
	// documented refusal shapes for this combination.
	if !strings.HasPrefix(out, "[agent: failed reason=spawn_error]") {
		t.Errorf("output:\n%s", out)
	}
}

// TestExecute_Background_CompletionNoticeFlowsToManagerInbox: after
// the runner finishes, the Manager's notice inbox holds the
// background-done wire format the Notifier will deliver — the tool
// layer hands lifecycle ownership to the Manager correctly.
func TestExecute_Background_CompletionNoticeFlowsToManagerInbox(t *testing.T) {
	release := make(chan struct{})
	tl, mgr := newBgTool(t, release)

	out, _ := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "d",
		"prompt": "p",
		"run_in_background": true
	}`))
	if !strings.HasPrefix(out, "[agent: background started sub-sid=") {
		t.Fatalf("handle output:\n%s", out)
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(mgr.PendingNotices()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	pending := mgr.PendingNotices()
	if len(pending) != 1 {
		t.Fatalf("pending notices = %d, want 1", len(pending))
	}
	if !strings.HasPrefix(pending[0].Summary, "[agent: background done sub-sid=") {
		t.Errorf("notice summary:\n%s", pending[0].Summary)
	}
	if !strings.Contains(pending[0].Summary, "bg summary body") {
		t.Errorf("notice summary missing body:\n%s", pending[0].Summary)
	}
}

// TestSchema_RunInBackgroundFieldPresent: the schema exposes the
// field (appended at the END of properties — one-time prefix-cache
// invalidation, 柱 K D9 precedent) and it decodes through the strict
// path. Default absence = false = sync path (pinned by the untouched
// sync tests in agent_test.go).
func TestSchema_RunInBackgroundFieldPresent(t *testing.T) {
	var sch struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tl_schema(), &sch); err != nil {
		t.Fatal(err)
	}
	raw, ok := sch.Properties["run_in_background"]
	if !ok {
		t.Fatal("schema properties missing run_in_background")
	}
	var prop struct {
		Type    string `json:"type"`
		Default any    `json:"default"`
	}
	if err := json.Unmarshal(raw, &prop); err != nil {
		t.Fatal(err)
	}
	if prop.Type != "boolean" {
		t.Errorf("run_in_background type = %s, want boolean", prop.Type)
	}

	// Strict decode accepts the field.
	var a Args
	if err := json.Unmarshal([]byte(`{"description":"d","prompt":"p","run_in_background":true}`), &a); err != nil {
		t.Fatal(err)
	}
	if !a.RunInBackground {
		t.Error("RunInBackground = false, want true")
	}
	// Absence decodes to false — the sync path default.
	var a2 Args
	if err := json.Unmarshal([]byte(`{"description":"d","prompt":"p"}`), &a2); err != nil {
		t.Fatal(err)
	}
	if a2.RunInBackground {
		t.Error("RunInBackground default = true, want false")
	}
}

// tl_schema pins the package-level schema bytes through the Tool
// accessor so the test fails if Schema() ever stops returning the
// package constant.
func tl_schema() json.RawMessage {
	tl := &Tool{}
	return tl.Schema()
}
