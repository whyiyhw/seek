package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/subagent"
)

// Background-notification presentation tests (feature-orchestration
// PRD §4.1 D2 / M-O1.2 TUI half, card c_zdjkgf): the status-bar
// badge's visible state migration (在跑 → 已完成待注入 → cleared on
// ingest) and the /agents panel's MODE column (bg rows + terminal
// states + the notified delivery tick). Both are driven by fake
// completion/notified events — a stub-Runner Manager for the
// lifecycle, a hand-appended `notified` index line for delivery (its
// writer is P1-b's resume path, not this card).

// newPanelManager builds a real Manager with SessionCtx wired so
// SpawnBackground works, plus the project dir (for direct index
// access when faking notified events).
func newPanelManager(t *testing.T, runner subagent.Runner) (*subagent.Manager, string) {
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
		Runner:            runner,
		SessionCtx:        context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr, projCwd
}

// pollUntil spins until cond holds or the timeout fires (the
// subagent package's waitFor, local copy for this test file).
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestStatusBarBadge_BackgroundLifecycle drives the badge through
// its full visible state migration with a fake completion event:
// running ("agents 1") → terminal notice in the inbox ("bg done 1",
// running badge gone) → Notifier drain at the next Prompt (both
// badges cleared).
func TestStatusBarBadge_BackgroundLifecycle(t *testing.T) {
	release := make(chan struct{})
	mgr, _ := newPanelManager(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		select {
		case <-release:
			return subagent.RunnerResult{Summary: "findings ready", Turns: 1}, nil
		case <-ctx.Done():
			return subagent.RunnerResult{}, ctx.Err()
		}
	})
	m := testModel().Build()
	m.opts.Subagents = mgr
	m.opts.SubagentWake = mgr.WakeCh()

	mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "long research",
		Prompt:      "p",
		Type:        subagent.TypeExplore,
	})
	pollUntil(t, 5*time.Second, func() bool { return mgr.ActiveCount() == 1 })

	// 在跑: running badge only, no done badge yet.
	bar := stripANSI(m.renderStatusBar())
	if !strings.Contains(bar, "agents 1") {
		t.Errorf("running state missing 'agents 1': %q", bar)
	}
	if strings.Contains(bar, "bg done") {
		t.Errorf("done badge leaked while still running: %q", bar)
	}

	// Fake completion event: release the runner — the terminal
	// notice lands in the inbox and the wake fires.
	close(release)
	pollUntil(t, 5*time.Second, func() bool { return len(mgr.PendingNotices()) == 1 })

	// 已完成待注入: done badge migrated in, running badge gone.
	bar = stripANSI(m.renderStatusBar())
	if !strings.Contains(bar, "bg done 1") {
		t.Errorf("post-completion state missing 'bg done 1': %q", bar)
	}
	if strings.Contains(bar, "agents 1") {
		t.Errorf("running badge outlived the spawn: %q", bar)
	}

	// Next Prompt: the Notifier (P1-a delivery path) drains the
	// inbox — the badge must clear on the following redraw.
	mgr.DrainNotices()
	bar = stripANSI(m.renderStatusBar())
	if strings.Contains(bar, "bg done") || strings.Contains(bar, "agents 1") {
		t.Errorf("badges survived the ingest drain: %q", bar)
	}
}

// TestCmdAgents_BackgroundRowsTerminalStatesAndDelivery: the panel
// lists background subs with their terminal states and the delivery
// tick. Three rows via public API + one fake notified event:
//
//	bg + completed + notified  → MODE "bg✓"
//	bg + failed (no notified)  → MODE "bg"
//	sync + completed           → MODE "-"
func TestCmdAgents_BackgroundRowsTerminalStatesAndDelivery(t *testing.T) {
	mgr, projCwd := newPanelManager(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		if j.UserPrompt == "boom" {
			return subagent.RunnerResult{}, errors.New("upstream 500")
		}
		return subagent.RunnerResult{Summary: "ok", Turns: 1}, nil
	})

	bgDone := mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "nightly digest", Prompt: "ok", Type: subagent.TypeExplore,
	})
	bgFail := mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "flaky crawl", Prompt: "boom", Type: subagent.TypeExplore,
	})
	if !strings.HasPrefix(bgFail.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("bg-fail handle summary:\n%s", bgFail.Summary)
	}
	sync := mgr.Spawn(context.Background(), subagent.SpawnArgs{
		Description: "sync scan", Prompt: "ok", Type: subagent.TypeExplore,
	})
	if sync.Status != subagent.StatusCompleted {
		t.Fatalf("sync spawn status = %s, want completed", sync.Status)
	}
	// Both background goroutines reach their terminal state.
	pollUntil(t, 5*time.Second, func() bool { return len(mgr.PendingNotices()) == 2 })

	// Fold contract before rendering: the started marker and the
	// fake notified event land on the folded rows.
	indexPath, err := paths.SubagentsIndex(projCwd)
	if err != nil {
		t.Fatal(err)
	}
	notifiedLine := fmt.Sprintf("{\"event\":\"notified\",\"sub_sid\":%q,\"ts\":\"2026-11-15T00:00:00Z\"}\n", bgDone.SubSid)
	f, err := os.OpenFile(indexPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(notifiedLine); err != nil {
		t.Fatal(err)
	}
	f.Close()

	subs, err := mgr.List(subagent.ListFilter{ParentSid: "20260601-100000-parent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("List len = %d, want 3", len(subs))
	}
	byDesc := map[string]subagent.Subagent{}
	for _, s := range subs {
		byDesc[s.Description] = s
	}
	if s := byDesc["nightly digest"]; !s.Background || !s.Notified || s.Status != subagent.StatusCompleted {
		t.Errorf("bg+notified fold = %+v, want Background+Notified+completed", s)
	}
	if s := byDesc["flaky crawl"]; !s.Background || s.Notified || s.Status != subagent.StatusFailed {
		t.Errorf("bg-failed fold = %+v, want Background, no Notified, failed", s)
	}
	if s := byDesc["sync scan"]; s.Background || s.Notified || s.Status != subagent.StatusCompleted {
		t.Errorf("sync fold = %+v, want no markers, completed", s)
	}

	// Panel rendering: MODE column present, per-row labels right.
	m := testModel().Build()
	m.opts.Subagents = mgr
	m.opts.Session = &session.Session{ID: "20260601-100000-parent"}
	res := runHandler(t, &m, "/agents")

	if !strings.Contains(res.text, "MODE") {
		t.Errorf("MODE header missing:\n%s", res.text)
	}
	rowFor := func(desc string) string {
		for _, line := range strings.Split(res.text, "\n") {
			if strings.Contains(line, desc) {
				return line
			}
		}
		t.Fatalf("no row for %q in:\n%s", desc, res.text)
		return ""
	}
	if row := rowFor("nightly digest"); !strings.Contains(row, "bg✓") || !strings.Contains(row, "completed") {
		t.Errorf("delivered bg row = %q, want bg✓ + completed", row)
	}
	if row := rowFor("flaky crawl"); !strings.Contains(row, "bg") || strings.Contains(row, "bg✓") || !strings.Contains(row, "failed") {
		t.Errorf("failed bg row = %q, want plain bg + failed (no delivery tick)", row)
	}
	if row := rowFor("sync scan"); !strings.Contains(row, "-") || strings.Contains(row, "bg") {
		t.Errorf("sync row = %q, want '-' mode cell and no bg label", row)
	}
}

// TestSubagentModeLabel pins the pure MODE-cell renderer.
func TestSubagentModeLabel(t *testing.T) {
	cases := []struct {
		name string
		sub  subagent.Subagent
		want string
	}{
		{"sync", subagent.Subagent{}, "-"},
		{"bg active/terminal undelivered", subagent.Subagent{Background: true}, "bg"},
		{"bg delivered", subagent.Subagent{Background: true, Notified: true}, "bg✓"},
		{"notified without background (corrupt index)", subagent.Subagent{Notified: true}, "-"},
	}
	for _, c := range cases {
		if got := subagentModeLabel(c.sub); got != c.want {
			t.Errorf("%s: subagentModeLabel = %q, want %q", c.name, got, c.want)
		}
	}
}
