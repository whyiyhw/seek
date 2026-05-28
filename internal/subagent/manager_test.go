package subagent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// usageWithPrompt is a small helper to build a deepseek.Usage with
// just the prompt token field populated — used by tests that need
// to drive the parent's Cumulative via the child Tracker.
func usageWithPrompt(n int) deepseek.Usage {
	return deepseek.Usage{PromptTokens: n, TotalTokens: n}
}

// currentTier is shorthand for the standard pricing tier used in
// tests where the tier doesn't matter (we're not asserting on
// dollar amounts).
func currentTier() pricing.Tier {
	return pricing.TierStandard
}

// withHome redirects ~/.seek to a tempdir for the duration of a
// test so the index file lands somewhere isolated.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	return home
}

// newManager spins up a Manager wired to a stub Runner the caller
// supplies. parentCwd defaults to a fresh tempdir.
func newManager(t *testing.T, runner Runner) *Manager {
	t.Helper()
	withHome(t)
	parentCwd := t.TempDir()
	policy, err := permission.New(parentCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ManagerOpts{
		ProjectAbsPath:  parentCwd,
		ParentSid:       "20260601-100000-parent",
		ParentTracker:   cache.New(),
		ParentPolicy:    policy,
		ParentToolNames: []string{"read", "grep", "list_dir", "git", "webfetch", "think", "agent", "ask_user", "bash", "write", "edit"},
		MaxConcurrent:   3,
		Runner:          runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

// TestSpawn_HappyPath: a Runner that returns a clean summary
// produces a [agent: completed] result, fires both started and
// completed events, and contributes tokens to the parent Tracker.
func TestSpawn_HappyPath(t *testing.T) {
	runner := func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		// Verify the job bundle is populated.
		if job.SystemPrompt == "" {
			t.Error("Runner: empty SystemPrompt")
		}
		if job.UserPrompt != "find Esc handlers in TUI" {
			t.Errorf("Runner: UserPrompt = %q", job.UserPrompt)
		}
		if !strings.Contains(job.SystemPrompt, "research-only mode") {
			t.Error("Runner: explore template Extra missing from SystemPrompt")
		}
		return RunnerResult{
			Summary: "Found 3 Esc handlers in internal/tui/update_key.go.",
			Tokens:  Tokens{Prompt: 5000, Completion: 100, CacheHit: 4500},
			Turns:   2,
		}, nil
	}
	mgr := newManager(t, runner)

	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "esc audit",
		Prompt:      "find Esc handlers in TUI",
		Type:        TypeExplore,
		ParentTurn:  4,
	})

	if res.Status != StatusCompleted {
		t.Errorf("Status = %s, want completed", res.Status)
	}
	if !strings.HasPrefix(res.Summary, "[agent: completed] ") {
		t.Errorf("Summary missing wire-format prefix:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "Found 3 Esc handlers") {
		t.Errorf("Summary missing body content:\n%s", res.Summary)
	}

	// Cost rollup: parent Tracker did NOT have its own Record
	// calls, but the child's Record (would be invoked by Runner
	// in production — our stub didn't, so the child Tracker is
	// empty). Verify the adoption wiring at least:
	if mgr.opts.ParentTracker.CumulativeCost() < 0 {
		t.Error("CumulativeCost gone negative")
	}

	// Index: must have started + completed events for our sub_sid.
	got, _ := mgr.List(ListFilter{})
	if len(got) != 1 {
		t.Fatalf("List len = %d, want 1", len(got))
	}
	if got[0].Status != StatusCompleted {
		t.Errorf("folded status = %s, want completed", got[0].Status)
	}
	if got[0].Type != TypeExplore {
		t.Errorf("folded type = %s, want explore", got[0].Type)
	}
}

// TestSpawn_RejectsInvalidType: validation fails BEFORE allocating
// a sub_sid or emitting any event.
func TestSpawn_RejectsInvalidType(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		t.Error("Runner called despite invalid type")
		return RunnerResult{}, nil
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: "bogus",
	})
	if res.Status != StatusFailed {
		t.Errorf("Status = %s, want failed", res.Status)
	}
	if !strings.Contains(res.Summary, "spawn_error") {
		t.Errorf("Summary missing reason=spawn_error:\n%s", res.Summary)
	}
	// No index events written.
	got, _ := mgr.List(ListFilter{})
	if len(got) != 0 {
		t.Errorf("index has %d records after rejected spawn, want 0", len(got))
	}
}

// TestSpawn_RejectsEmptyArgs covers description / prompt fast-fail.
func TestSpawn_RejectsEmptyArgs(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		t.Error("Runner called despite empty args")
		return RunnerResult{}, nil
	})
	for _, args := range []SpawnArgs{
		{Description: "", Prompt: "y", Type: TypeGeneralPurpose},
		{Description: "x", Prompt: "", Type: TypeGeneralPurpose},
	} {
		res := mgr.Spawn(context.Background(), args)
		if res.Status != StatusFailed {
			t.Errorf("Spawn(%+v) Status = %s, want failed", args, res.Status)
		}
	}
}

// TestSpawn_TooManySubagents: MaxConcurrent gate produces a clean
// failed result with reason=too_many_subagents.
func TestSpawn_TooManySubagents(t *testing.T) {
	// Use a blocking Runner so the first N spawns occupy the slots
	// while we test the N+1th.
	release := make(chan struct{})
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		<-release // block until test releases
		return RunnerResult{Summary: "done", Turns: 1}, nil
	}
	mgr := newManager(t, runner)
	mgr.opts.MaxConcurrent = 2

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.Spawn(context.Background(), SpawnArgs{
				Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
			})
		}()
	}
	// Wait for the goroutines to register in active.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mgr.ActiveCount() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if mgr.ActiveCount() != 2 {
		close(release)
		wg.Wait()
		t.Fatalf("ActiveCount stuck at %d before testing N+1", mgr.ActiveCount())
	}

	// N+1th call must fail immediately with too_many_subagents.
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "extra", Prompt: "extra", Type: TypeGeneralPurpose,
	})
	if !strings.Contains(res.Summary, "too_many_subagents") {
		t.Errorf("Summary missing too_many_subagents:\n%s", res.Summary)
	}
	if res.Status != StatusFailed {
		t.Errorf("Status = %s, want failed", res.Status)
	}

	close(release)
	wg.Wait()
}

// TestSpawn_KillProducesKilledEvent: Manager.Kill during a running
// Spawn produces StatusKilled, not StatusFailed reason=canceled.
func TestSpawn_KillProducesKilledEvent(t *testing.T) {
	started := make(chan string, 1) // ships sub-sid as soon as Runner is invoked
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		started <- j.SubSid
		<-ctx.Done()
		return RunnerResult{}, ctx.Err()
	}
	mgr := newManager(t, runner)

	resCh := make(chan Result, 1)
	go func() {
		resCh <- mgr.Spawn(context.Background(), SpawnArgs{
			Description: "infinite", Prompt: "loop", Type: TypeGeneralPurpose,
		})
	}()

	var subSid string
	select {
	case subSid = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Runner never invoked")
	}
	if err := mgr.Kill(subSid); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	res := <-resCh
	if res.Status != StatusKilled {
		t.Errorf("Status = %s, want killed", res.Status)
	}
	if !strings.Contains(res.Summary, "reason=killed") {
		t.Errorf("Summary missing reason=killed:\n%s", res.Summary)
	}

	// Index reflects killed.
	folded, _ := mgr.List(ListFilter{})
	if len(folded) != 1 || folded[0].Status != StatusKilled {
		t.Errorf("folded after kill = %+v", folded)
	}
}

// TestSpawn_CtxCanceledByParent: ctx cancellation from the caller
// side (parent turn died) produces StatusFailed reason=canceled,
// distinguishing it from explicit Kill.
func TestSpawn_CtxCanceledByParent(t *testing.T) {
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		<-ctx.Done()
		return RunnerResult{}, ctx.Err()
	}
	mgr := newManager(t, runner)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res := mgr.Spawn(ctx, SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
	})
	if res.Status != StatusFailed {
		t.Errorf("Status = %s, want failed", res.Status)
	}
	if !strings.Contains(res.Summary, "reason=canceled") {
		t.Errorf("Summary missing reason=canceled:\n%s", res.Summary)
	}
}

// TestSpawn_RunnerErrorClassifiedAsSpawnError: arbitrary Runner
// errors fall through to spawn_error.
func TestSpawn_RunnerErrorClassifiedAsSpawnError(t *testing.T) {
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		return RunnerResult{}, errors.New("simulated LLM 500")
	}
	mgr := newManager(t, runner)
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
	})
	if !strings.Contains(res.Summary, "reason=spawn_error") {
		t.Errorf("Summary missing spawn_error:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "simulated LLM 500") {
		t.Errorf("Summary missing runner err detail:\n%s", res.Summary)
	}
}

// TestSpawn_PanicInRunnerBecomesSpawnError: Runner panics convert
// to failed events, not process crashes.
func TestSpawn_PanicInRunnerBecomesSpawnError(t *testing.T) {
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		panic("boom")
	}
	mgr := newManager(t, runner)
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
	})
	if res.Status != StatusFailed {
		t.Errorf("Status = %s, want failed after panic", res.Status)
	}
	if !strings.Contains(res.Summary, "panic") {
		t.Errorf("Summary missing panic detail:\n%s", res.Summary)
	}
}

// TestKill_UnknownReturnsErr: calling Kill on a non-existent sub_sid
// returns ErrUnknownSubagent (not a panic).
func TestKill_UnknownReturnsErr(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	err := mgr.Kill("nope")
	if !errors.Is(err, ErrUnknownSubagent) {
		t.Errorf("Kill(unknown) = %v, want ErrUnknownSubagent", err)
	}
}

// TestKill_IsIdempotent: a completed subagent's sub_sid is no
// longer in active; a second Kill is a benign Unknown.
func TestKill_IsIdempotent(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
	})
	if err := mgr.Kill(res.SubSid); !errors.Is(err, ErrUnknownSubagent) {
		t.Errorf("Kill on completed sub = %v, want ErrUnknownSubagent", err)
	}
}

// TestSpawn_PolicyMonotonicTighten: parent Yolo spawning explore
// gives a child with Workflow=PlanAnalyze (forced by template).
// Probe via the child Policy's behaviour on a destructive action.
func TestSpawn_PolicyMonotonicTighten(t *testing.T) {
	var captured *permission.Policy
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		captured = j.Policy
		return RunnerResult{Summary: "noop", Turns: 1}, nil
	}
	mgr := newManager(t, runner)
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeExplore,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("spawn failed: %s", res.Summary)
	}
	if captured == nil {
		t.Fatal("Runner did not capture child Policy")
	}
	if got := captured.Workflow(); got != permission.WorkflowPlanAnalyze {
		t.Errorf("child Workflow = %s, want plan-analyze (explore template)", got)
	}
	if got := captured.Pref(); got != permission.PrefYolo {
		t.Errorf("child Pref = %s, want yolo (inherited from parent)", got)
	}
}

// TestSpawn_ChildTrackerAdopted: parent's Cumulative usage picks
// up the child Tracker's Record calls.
func TestSpawn_ChildTrackerAdopted(t *testing.T) {
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		// Simulate the LLM turn by Recording usage on the child Tracker.
		j.Tracker.Record(usageWithPrompt(5_000), "deepseek-v4-flash", currentTier())
		return RunnerResult{Summary: "done", Turns: 1, Tokens: Tokens{Prompt: 5_000}}, nil
	}
	mgr := newManager(t, runner)
	_ = mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
	})
	if got := mgr.opts.ParentTracker.Cumulative().PromptTokens; got != 5_000 {
		t.Errorf("parent Cumulative.PromptTokens = %d, want 5000 (rolled up from child)", got)
	}
}

// TestSpawn_ConcurrentN: spawn N subagents in parallel goroutines;
// all complete, index has 2*N events (started + completed each),
// no -race violations.
func TestSpawn_ConcurrentN(t *testing.T) {
	const N = 10
	var completed int32
	runner := func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		atomic.AddInt32(&completed, 1)
		return RunnerResult{Summary: "ok " + j.SubSid, Turns: 1}, nil
	}
	mgr := newManager(t, runner)
	mgr.opts.MaxConcurrent = N // ensure none fail on capacity gate

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := mgr.Spawn(context.Background(), SpawnArgs{
				Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
			})
			if res.Status != StatusCompleted {
				t.Errorf("concurrent spawn status = %s", res.Status)
			}
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&completed) != int32(N) {
		t.Errorf("completed runners = %d, want %d", completed, N)
	}
	folded, _ := mgr.List(ListFilter{})
	if len(folded) != N {
		t.Errorf("List len = %d, want %d", len(folded), N)
	}
}

// TestSpawn_IndexPathUsesPathsHelper: lock in that Manager writes
// to the path returned by paths.SubagentsIndex, NOT some other
// location. Catches refactors that drift the path.
func TestSpawn_IndexPathUsesPathsHelper(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	_ = mgr.Spawn(context.Background(), SpawnArgs{
		Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
	})
	wantPath, err := paths.SubagentsIndex(mgr.opts.ProjectAbsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected index at %q, stat err: %v", wantPath, err)
	}
}

// TestList_FilterByStatus: List honours the Status filter.
func TestList_FilterByStatus(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	for i := 0; i < 3; i++ {
		_ = mgr.Spawn(context.Background(), SpawnArgs{
			Description: "x", Prompt: "y", Type: TypeGeneralPurpose,
		})
	}
	completed, _ := mgr.List(ListFilter{Status: StatusCompleted})
	if len(completed) != 3 {
		t.Errorf("List(Completed) len = %d, want 3", len(completed))
	}
	active, _ := mgr.List(ListFilter{Status: StatusActive})
	if len(active) != 0 {
		t.Errorf("List(Active) len = %d, want 0", len(active))
	}
}
