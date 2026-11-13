package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
)

// Background-completion wake tests (feature-orchestration PRD §4.1
// D2): the wake signal fires subagentWakeMsg, the handler re-arms,
// and the UI hint PEEKS the inbox instead of draining it (delivery
// belongs to the Notifier PrePrompt hook).

// newWakeModel builds a Model with a real Manager (SessionCtx
// wired, instant runner) and its wake channel attached.
func newWakeModel(t *testing.T) (*Model, *subagent.Manager) {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	projCwd := t.TempDir()
	policy, err := permission.New(projCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
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
		Runner: func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
			return subagent.RunnerResult{Summary: "s", Turns: 1}, nil
		},
		SessionCtx: context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := testModel().Build()
	m.opts.Subagents = mgr
	m.opts.SubagentWake = mgr.WakeCh()
	return &m, mgr
}

// TestSubagentWake_CmdReceivesSignal: a completed background spawn
// leaves a pending wake; waitForSubagentWake converts it into
// subagentWakeMsg. A closed channel converts to nil (no spin).
func TestSubagentWake_CmdReceivesSignal(t *testing.T) {
	_, mgr := newWakeModel(t)

	res := mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "nightly digest",
		Prompt:      "p",
		Type:        subagent.TypeExplore,
	})
	if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("handle summary:\n%s", res.Summary)
	}

	// Wait for the terminal notice + pending wake signal.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(mgr.PendingNotices()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if len(mgr.PendingNotices()) != 1 {
		t.Fatal("notice never reached the inbox")
	}

	msg := waitForSubagentWake(mgr.WakeCh())()
	if _, ok := msg.(subagentWakeMsg); !ok {
		t.Fatalf("waitForSubagentWake produced %T, want subagentWakeMsg", msg)
	}

	closed := make(chan struct{})
	close(closed)
	if out := waitForSubagentWake(closed)(); out != nil {
		t.Errorf("closed-channel wake produced %T, want nil", out)
	}
}

// TestSubagentWake_UpdateHandlesAndRearms: Update routes
// subagentWakeMsg to handleSubagentWake — hint Cmd + re-arm Cmd
// while the notice stays in the inbox for the Notifier (peek, not
// drain).
func TestSubagentWake_UpdateHandlesAndRearms(t *testing.T) {
	m, mgr := newWakeModel(t)

	mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "digest",
		Prompt:      "p",
		Type:        subagent.TypeExplore,
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(mgr.PendingNotices()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// Drain the pending wake so the re-arm Cmd is the only live one.
	select {
	case <-mgr.WakeCh():
	default:
	}

	next, cmd := m.Update(subagentWakeMsg{})
	_ = next.(Model)
	if cmd == nil {
		t.Fatal("Update(subagentWakeMsg) returned nil Cmd — wake handler did not re-arm")
	}

	// Peek semantics: the notice is STILL pending after the handler
	// ran — the TUI must not steal from the Notifier's delivery.
	if pending := mgr.PendingNotices(); len(pending) != 1 {
		t.Fatalf("inbox after wake handler = %d, want 1 (drained by UI?)", len(pending))
	}
}

// TestSubagentWake_NilGate: with no wake channel wired, the handler
// no-ops instead of crashing (--no-save mode).
func TestSubagentWake_NilGate(t *testing.T) {
	m := testModel().Build()
	if got := (&m).handleSubagentWake(); got != nil {
		t.Errorf("nil-channel handler returned %d cmds, want nil", len(got))
	}
}
