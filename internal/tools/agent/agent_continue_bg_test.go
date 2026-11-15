package agent

// Tool-level tests for the P1-b × P1-a composition (card c_u4ugnu):
// session_id + run_in_background dispatches to
// Manager.ContinueBackground. New file by design — the sync
// continuation tests (agent_test.go) and the background spawn tests
// (agent_background_test.go) stay byte-untouched.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// newContinueBgTool wires a Tool whose Manager has SessionCtx (the
// precondition for ContinueBackground) and a runner that persists
// transcripts (the precondition for continuation) — fresh spawns
// complete immediately, continuations block on release so the
// in-flight handle is observable.
func newContinueBgTool(t *testing.T, release <-chan struct{}) *Tool {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	projCwd := t.TempDir()
	policy, err := permission.New(projCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	runner := func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		if len(job.PriorMessages) > 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return subagent.RunnerResult{}, ctx.Err()
			}
		}
		msgs := make([]deepseek.Message, 0, len(job.PriorMessages)+2)
		msgs = append(msgs, job.PriorMessages...)
		msgs = append(msgs,
			deepseek.Message{Role: deepseek.RoleUser, Content: job.UserPrompt},
			deepseek.Message{Role: deepseek.RoleAssistant, Content: "summary for " + job.UserPrompt},
		)
		sess := session.New("test-model", job.Policy.Cwd(), job.SystemPrompt, true, false)
		sess.ID = job.SubSid
		sess.Messages = msgs
		if err := session.SaveTo(filepath.Join(job.SessionDir, "transcript.jsonl"), sess); err != nil {
			t.Errorf("stub runner: save transcript %s: %v", job.SubSid, err)
		}
		return subagent.RunnerResult{
			Summary: "summary for " + job.UserPrompt,
			Turns:   1,
			Tokens:  subagent.Tokens{Prompt: 10, Completion: 2},
		}, nil
	}
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:    projCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return nil },
		MaxConcurrent:     3,
		Runner:            runner,
		SessionCtx:        sessionCtx,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessionCancel(); mgr.Shutdown() })
	return New(mgr, nil)
}

// TestTool_Execute_ContinueInBackground drives the model's composed
// workflow: fresh spawn → completed footer id → session_id +
// run_in_background follow-up → immediate background handle →
// release → the completion notice lands in the Manager inbox (the
// main-session delivery payload) with the lineage footer.
func TestTool_Execute_ContinueInBackground(t *testing.T) {
	release := make(chan struct{})
	tl := newContinueBgTool(t, release)
	mgr := tl.mgr

	first, err := tl.Execute(context.Background(), json.RawMessage(
		`{"description": "research x", "prompt": "survey the adapters", "subagent_type": "explore"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "[agent: completed] ") {
		t.Fatalf("first call not completed:\n%s", first)
	}
	short := footerShortSid(t, first)

	start := time.Now()
	handle, err := tl.Execute(context.Background(), json.RawMessage(
		`{"description": "follow up async", "prompt": "now audit the edge cases", "session_id": "`+short+`", "run_in_background": true}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Execute blocked %v behind the continuation — background contract broken", elapsed)
	}
	if !strings.HasPrefix(handle, "[agent: background started sub-sid=") {
		t.Fatalf("handle missing background-started prefix:\n%s", handle)
	}

	// Release the gated continuation; its completion then flows into
	// the notice inbox.
	close(release)

	// The notice (delivery payload for the parent session's next
	// turn) carries the lineage footer.
	deadline := time.Now().Add(5 * time.Second)
	for {
		notices := mgr.PendingNotices()
		if len(notices) == 1 {
			if !strings.HasPrefix(notices[0].Summary, "[agent: background done sub-sid=") {
				t.Fatalf("notice missing background-done prefix:\n%s", notices[0].Summary)
			}
			if !strings.Contains(notices[0].Summary, "resumed-from: "+short) {
				t.Fatalf("notice missing lineage footer:\n%s", notices[0].Summary)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completion notice never reached the Manager inbox")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestTool_Execute_ContinueBackgroundWorktreeRefused: the
// background × worktree refusal covers continuations too — cleanup
// rides the sync path either way.
func TestTool_Execute_ContinueBackgroundWorktreeRefused(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tl := newContinueBgTool(t, release)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "d",
		"prompt": "p",
		"session_id": "whatever",
		"run_in_background": true,
		"isolation": "worktree"
	}`))
	if err != nil {
		t.Fatalf("refusal must be a tool result, not error: %v", err)
	}
	if !strings.HasPrefix(out, "[agent: failed reason=spawn_error]") {
		t.Errorf("unexpected refusal wire format:\n%s", out)
	}
	// wtMgr is nil in this fixture, so the isolation switch refuses
	// first ("requires a git working tree"); with a worktree Manager
	// wired it would be the background×worktree refusal. Either way
	// the combination must die as a tool result before any spawn.
	if !strings.Contains(out, "worktree") {
		t.Errorf("refusal does not name worktree:\n%s", out)
	}
}
