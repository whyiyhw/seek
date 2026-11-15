package subagent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
)

// Background-spawn lifecycle tests (feature-orchestration PRD §4.1
// D1/D2, card c_bickfj P1-a): immediate return + parent continues,
// completion/failure events on disk, notice inbox + wake, shared
// capacity gate, session-ctx discipline, Shutdown.

// waitFor polls cond until it holds or timeout elapses. Background
// goroutines make every terminal-state assertion asynchronous; a
// 5s ceiling keeps failures loud instead of flaky.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
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

// newBackgroundManager mirrors newManager but wires SessionCtx
// (returned with its cancel) so SpawnBackground is available.
func newBackgroundManager(t *testing.T, runner Runner) (*Manager, context.CancelFunc) {
	t.Helper()
	withHome(t)
	parentCwd := t.TempDir()
	policy, err := permission.New(parentCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	parentTools := []string{"read", "grep", "list_dir", "git", "webfetch", "think", "agent", "ask_user", "bash", "write", "edit"}
	mgr, err := NewManager(ManagerOpts{
		ProjectAbsPath:    parentCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return parentTools },
		MaxConcurrent:     3,
		Runner:            runner,
		SessionCtx:        sessionCtx,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessionCancel(); mgr.Shutdown() }) // 等后台 goroutine 落盘后再放 TempDir 清理（c_bickfj 竞态族修复）
	return mgr, sessionCancel
}

// blockingRunner waits for release (or ctx cancel) then succeeds.
func blockingRunner(release <-chan struct{}) Runner {
	return func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		select {
		case <-release:
			return RunnerResult{
				Summary: "background findings ready",
				Tokens:  Tokens{Prompt: 700, Completion: 90, CacheHit: 600},
				Turns:   3,
			}, nil
		case <-ctx.Done():
			return RunnerResult{}, ctx.Err()
		}
	}
}

// indexEvents reads the project index straight off disk — asserting
// 落盘 (events persisted), not just the in-memory fold.
func indexEvents(t *testing.T, mgr *Manager) []event {
	t.Helper()
	indexPath, err := paths.SubagentsIndex(mgr.opts.ProjectAbsPath)
	if err != nil {
		t.Fatal(err)
	}
	evts, err := readEvents(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	return evts
}

// TestSpawnBackground_ImmediateReturnParentContinues: the tool result
// comes back at once with the handle prefix (PRD 验收 #1), the parent
// keeps spawning while the child runs (主会话继续), and the child
// only reaches terminal state after release.
func TestSpawnBackground_ImmediateReturnParentContinues(t *testing.T) {
	release := make(chan struct{})
	runner := blockingRunner(release)
	var runnerCalls atomic.Int32
	wrapped := func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		runnerCalls.Add(1)
		return runner(ctx, job)
	}
	mgr, _ := newBackgroundManager(t, wrapped)

	start := time.Now()
	res := mgr.SpawnBackground(SpawnArgs{
		Description: "long audit",
		Prompt:      "audit the repo",
		Type:        TypeExplore,
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("SpawnBackground blocked %v — parent turn was dragged by the child", elapsed)
	}

	if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
		t.Errorf("Summary missing background-started prefix:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "long audit") {
		t.Errorf("Summary missing description:\n%s", res.Summary)
	}
	if res.Status != StatusActive {
		t.Errorf("Status = %s, want active (handle; child still running)", res.Status)
	}
	if res.SubSid == "" {
		t.Error("SubSid empty on the handle")
	}

	// 主会话继续: the parent spawns a SECOND background child while
	// the first is still in flight — the whole point of detaching.
	res2 := mgr.SpawnBackground(SpawnArgs{
		Description: "second task",
		Prompt:      "second prompt",
		Type:        TypeExplore,
	})
	if !strings.HasPrefix(res2.Summary, "[agent: background started sub-sid=") {
		t.Errorf("second spawn summary:\n%s", res2.Summary)
	}
	if mgr.ActiveCount() != 2 {
		t.Errorf("ActiveCount = %d, want 2", mgr.ActiveCount())
	}

	// Status query while running (状态可查 mid-flight).
	subs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 || subs[0].Status != StatusActive {
		t.Errorf("List mid-flight = %+v, want 2 active rows", subs)
	}

	close(release)
	waitFor(t, 5*time.Second, func() bool { return mgr.ActiveCount() == 0 })
	if got := runnerCalls.Load(); got != 2 {
		t.Errorf("runner calls = %d, want 2", got)
	}
}

// TestSpawnBackground_CompletionEventOnDiskNoticeAndStatus: the
// acceptance chain — background start → parent continues → completion
// event 落盘 (subagents.jsonl) → notice queued + wake fired → status
// queryable as completed.
func TestSpawnBackground_CompletionEventOnDiskNoticeAndStatus(t *testing.T) {
	release := make(chan struct{})
	mgr, _ := newBackgroundManager(t, blockingRunner(release))

	res := mgr.SpawnBackground(SpawnArgs{
		Description: "deep research",
		Prompt:      "research orchestration",
		Type:        TypeGeneralPurpose,
	})
	if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("bad handle summary:\n%s", res.Summary)
	}

	// Wake channel armed BEFORE completion so the signal can't race past us.
	wake := mgr.WakeCh()

	close(release)

	// The wake signal fires (non-blocking receive with deadline).
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("wake signal never fired on completion")
	}

	// Completion event 落盘: started + completed for our sub_sid.
	waitFor(t, 5*time.Second, func() bool {
		for _, e := range indexEvents(t, mgr) {
			if e.Kind == "completed" && e.SubSid == res.SubSid {
				return true
			}
		}
		return false
	})

	// 状态可查: folded status completed, tokens recorded.
	subs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("List len = %d, want 1", len(subs))
	}
	if subs[0].Status != StatusCompleted {
		t.Errorf("folded status = %s, want completed", subs[0].Status)
	}
	if subs[0].SubSid != res.SubSid {
		t.Errorf("folded sub_sid = %s, want %s", subs[0].SubSid, res.SubSid)
	}
	if subs[0].Tokens.Prompt != 700 {
		t.Errorf("tokens = %+v, want prompt 700 recorded from completed event", subs[0].Tokens)
	}

	// Notice inbox carries the wire-format summary for the Notifier.
	pending := mgr.PendingNotices()
	if len(pending) != 1 {
		t.Fatalf("pending notices = %d, want 1", len(pending))
	}
	if !strings.HasPrefix(pending[0].Summary, "[agent: background done sub-sid="+res.SubSid+"]") {
		t.Errorf("notice summary:\n%s", pending[0].Summary)
	}
	if !strings.Contains(pending[0].Summary, "background findings ready") {
		t.Errorf("notice summary missing body:\n%s", pending[0].Summary)
	}
	if !strings.Contains(pending[0].Summary, "turns: 3") {
		t.Errorf("notice summary missing turns footer:\n%s", pending[0].Summary)
	}
	if pending[0].Status != StatusCompleted {
		t.Errorf("notice status = %s, want completed", pending[0].Status)
	}
}

// TestSpawnMode_IndexMarker: SpawnBackground stamps background:true
// on the `started` event (so the fold → /agents MODE column can
// present bg rows), sync Spawn leaves it unset. Terminal events stay
// identical for both modes.
func TestSpawnMode_IndexMarker(t *testing.T) {
	mgr, _ := newBackgroundManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})

	bg := mgr.SpawnBackground(SpawnArgs{Description: "bg", Prompt: "p", Type: TypeExplore})
	waitFor(t, 5*time.Second, func() bool { return mgr.ActiveCount() == 0 })

	sync := mgr.Spawn(context.Background(), SpawnArgs{Description: "sync", Prompt: "p", Type: TypeExplore})
	if sync.Status != StatusCompleted {
		t.Fatalf("sync status = %s, want completed", sync.Status)
	}

	startedBg, startedSync := false, true
	for _, e := range indexEvents(t, mgr) {
		if e.Kind == "started" && e.SubSid == bg.SubSid {
			startedBg = e.Background
		}
		if e.Kind == "started" && e.SubSid == sync.SubSid {
			startedSync = e.Background
		}
	}
	if !startedBg {
		t.Error("SpawnBackground started event missing background:true marker")
	}
	if startedSync {
		t.Error("sync Spawn started event carries background:true, want unset")
	}

	subs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range subs {
		switch s.SubSid {
		case bg.SubSid:
			if !s.Background {
				t.Error("folded background row missing Background=true")
			}
		case sync.SubSid:
			if s.Background {
				t.Error("folded sync row Background=true, want false")
			}
		}
	}
}

// TestSpawnBackground_FailureNotice: a Runner error produces the
// same failed index event as the sync path plus the background
// wire-format notice.
func TestSpawnBackground_FailureNotice(t *testing.T) {
	runner := func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{}, context.DeadlineExceeded
	}
	mgr, _ := newBackgroundManager(t, runner)

	res := mgr.SpawnBackground(SpawnArgs{
		Description: "doomed",
		Prompt:      "will time out",
		Type:        TypeExplore,
	})

	waitFor(t, 5*time.Second, func() bool { return mgr.ActiveCount() == 0 })

	for _, e := range indexEvents(t, mgr) {
		if e.Kind == "failed" && e.SubSid == res.SubSid && e.Reason == "timeout" {
			goto found
		}
	}
	t.Fatal("no failed reason=timeout event on disk")
found:
	pending := mgr.PendingNotices()
	if len(pending) != 1 {
		t.Fatalf("pending notices = %d, want 1", len(pending))
	}
	if !strings.HasPrefix(pending[0].Summary, "[agent: background failed reason=timeout]") {
		t.Errorf("failure notice:\n%s", pending[0].Summary)
	}
}

// TestSpawnBackground_TurnCtxCancelDoesNotKillChild: cancelling the
// CALLER's ctx (the parent turn, e.g. Esc) must not touch a
// background child — its ctx is the session ctx (PRD 验收 #4, the
// 柱 K D5 lesson). Killing the session ctx DOES cancel it.
func TestSpawnBackground_TurnCtxCancelDoesNotKillChild(t *testing.T) {
	release := make(chan struct{})
	mgr, sessionCancel := newBackgroundManager(t, blockingRunner(release))

	// A turn ctx the "caller" would have passed in the sync world.
	// SpawnBackground takes no turn ctx by DESIGN (the whole point):
	// simulate the parent turn dying immediately after the handle
	// came back.
	_, turnCancel := context.WithCancel(context.Background())
	turnCancel()

	res := mgr.SpawnBackground(SpawnArgs{
		Description: "survivor",
		Prompt:      "outlive the turn",
		Type:        TypeGeneralPurpose,
	})

	// Turn dies; child must still be active.
	turnCancel()
	time.Sleep(20 * time.Millisecond)
	if mgr.ActiveCount() != 1 {
		t.Fatal("background child died with the parent turn — ctx discipline broken")
	}

	// Session dies; child cancels and writes its terminal event.
	sessionCancel()
	waitFor(t, 5*time.Second, func() bool { return mgr.ActiveCount() == 0 })
	for _, e := range indexEvents(t, mgr) {
		if e.Kind == "failed" && e.SubSid == res.SubSid && e.Reason == "canceled" {
			return
		}
	}
	t.Fatal("no failed reason=canceled event after session ctx cancel")
}

// TestSpawnBackground_SharedCapacityGate: background and sync spawns
// share one MaxConcurrent door (PRD D4).
func TestSpawnBackground_SharedCapacityGate(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	mgr, _ := newBackgroundManager(t, blockingRunner(release))

	for i := 0; i < 3; i++ {
		res := mgr.SpawnBackground(SpawnArgs{
			Description: "filler",
			Prompt:      "p",
			Type:        TypeExplore,
		})
		if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
			t.Fatalf("filler spawn %d rejected:\n%s", i, res.Summary)
		}
	}

	// 4th spawn — background — hits the shared gate.
	res := mgr.SpawnBackground(SpawnArgs{Description: "over", Prompt: "p", Type: TypeExplore})
	if !strings.HasPrefix(res.Summary, "[agent: failed reason=too_many_subagents]") {
		t.Errorf("background over-capacity summary:\n%s", res.Summary)
	}

	// And the SYNC path sees the same door held by background children.
	syncRes := mgr.Spawn(context.Background(), SpawnArgs{Description: "sync over", Prompt: "p", Type: TypeExplore})
	if !strings.HasPrefix(syncRes.Summary, "[agent: failed reason=too_many_subagents]") {
		t.Errorf("sync over-capacity summary:\n%s", syncRes.Summary)
	}
}

// TestSpawnBackground_SessionCtxRequired: a Manager wired without
// SessionCtx refuses background spawns with a wire-format failure
// (host-programmer error surfaced to the model, not a panic).
func TestSpawnBackground_SessionCtxRequired(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "unused"}, nil
	})
	res := mgr.SpawnBackground(SpawnArgs{Description: "d", Prompt: "p", Type: TypeExplore})
	if !strings.HasPrefix(res.Summary, "[agent: failed reason=spawn_error]") {
		t.Errorf("no-SessionCtx summary:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "SessionCtx") {
		t.Errorf("no-SessionCtx failure should name the missing wiring:\n%s", res.Summary)
	}
}

// TestShutdown_CancelsBackgroundAndWritesTerminalEvents: clean exit
// cancels every active background child, waits for the terminal
// events to land (no started-without-terminal residue), and the
// notices carry the canceled classification (PRD D3, 验收 #5).
func TestShutdown_CancelsBackgroundAndWritesTerminalEvents(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	mgr, _ := newBackgroundManager(t, blockingRunner(release))

	sids := make([]string, 2)
	for i := range sids {
		res := mgr.SpawnBackground(SpawnArgs{
			Description: "bg",
			Prompt:      "p",
			Type:        TypeExplore,
		})
		sids[i] = res.SubSid
	}

	done := make(chan struct{})
	go func() {
		mgr.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return — background goroutines never wrote terminal events")
	}

	if mgr.ActiveCount() != 0 {
		t.Errorf("ActiveCount after Shutdown = %d, want 0", mgr.ActiveCount())
	}

	evts := indexEvents(t, mgr)
	for _, sid := range sids {
		terminal := false
		for _, e := range evts {
			if e.SubSid == sid && e.Kind == "failed" && e.Reason == "canceled" {
				terminal = true
			}
		}
		if !terminal {
			t.Errorf("sub %s has no failed reason=canceled terminal event (residue for the orphan scanner)", sid)
		}
	}

	pending := mgr.PendingNotices()
	if len(pending) != 2 {
		t.Fatalf("pending notices after Shutdown = %d, want 2", len(pending))
	}
	for _, n := range pending {
		if !strings.HasPrefix(n.Summary, "[agent: background failed reason=canceled]") {
			t.Errorf("shutdown notice:\n%s", n.Summary)
		}
	}
}

// TestNotifier: the PrePrompt delivery hook drains the inbox into
// <context> Prepend messages (the main-session transcript seam) and
// only the delivery path consumes.
func TestNotifier(t *testing.T) {
	release := make(chan struct{})
	mgr, _ := newBackgroundManager(t, blockingRunner(release))

	notifier := NewNotifier(mgr)

	// Nothing pending → zero out ("" UserText = no change).
	out, err := notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "next turn"})
	if err != nil {
		t.Fatal(err)
	}
	if out.UserText != "" || len(out.Prepend) != 0 {
		t.Errorf("empty-inbox out = %+v, want zero value", out)
	}

	res := mgr.SpawnBackground(SpawnArgs{Description: "n1", Prompt: "p", Type: TypeExplore})
	close(release)
	waitFor(t, 5*time.Second, func() bool { return len(mgr.PendingNotices()) == 1 })

	// Peek doesn't drain.
	if got := len(mgr.PendingNotices()); got != 1 {
		t.Fatalf("peek count = %d, want 1", got)
	}

	out, err = notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "next turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Prepend) != 1 {
		t.Fatalf("Prepend len = %d, want 1", len(out.Prepend))
	}
	msg := out.Prepend[0]
	if msg.Role != "user" {
		t.Errorf("prepend role = %s, want user (cache-volatile seam)", msg.Role)
	}
	if !strings.HasPrefix(msg.Content, "<context>\n[agent: background done sub-sid="+res.SubSid+"]") {
		t.Errorf("prepend content:\n%s", msg.Content)
	}
	if !strings.HasSuffix(msg.Content, "</context>") {
		t.Errorf("prepend content missing closing tag:\n%s", msg.Content)
	}

	// Drained — the next Prompt sees nothing (no double delivery).
	out2, _ := notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "next turn"})
	if len(out2.Prepend) != 0 {
		t.Errorf("second drain returned %d messages, want 0", len(out2.Prepend))
	}
}

// Serialisation check on notice ordering (PRD D2 FIFO): completions
// are delivered in terminal order.
func TestNotifier_FIFOOrder(t *testing.T) {
	mgr, _ := newBackgroundManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "s"}, nil
	})
	mgr.enqueueNotice(BackgroundNotice{SubSid: "first", Summary: "[agent: background done sub-sid=first]"})
	mgr.enqueueNotice(BackgroundNotice{SubSid: "second", Summary: "[agent: background done sub-sid=second]"})

	notifier := NewNotifier(mgr)
	out, err := notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "next turn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Prepend) != 2 {
		t.Fatalf("Prepend len = %d, want 2", len(out.Prepend))
	}
	if !strings.Contains(out.Prepend[0].Content, "sub-sid=first") ||
		!strings.Contains(out.Prepend[1].Content, "sub-sid=second") {
		t.Errorf("FIFO broken:\n%s\n%s", out.Prepend[0].Content, out.Prepend[1].Content)
	}
}
