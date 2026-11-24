package subagent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/permission"
)

// P3-a trigger wiring (feature-orchestration D15): the Manager fires
// SubagentStart right after the `started` index event and
// SubagentStop after every terminal path. These tests use a recording
// in-process hook and assert on the payload, not just "something
// fired".

// lifecycleRecorder captures the two subagent observers in dispatch
// order. Mutex-guarded because background spawns fire from their own
// goroutines.
type lifecycleRecorder struct {
	mu     sync.Mutex
	events []string
	starts []hooks.SubagentStartEvent
	stops  []hooks.SubagentStopEvent
	// onStop, when set, runs inside OnSubagentStop — lets a test
	// assert on Manager state as observed from the hook (the index
	// must already carry the terminal row by then).
	onStop func()
}

func (r *lifecycleRecorder) OnSubagentStart(_ context.Context, ev hooks.SubagentStartEvent) {
	r.mu.Lock()
	r.events = append(r.events, "start")
	r.starts = append(r.starts, ev)
	r.mu.Unlock()
}

func (r *lifecycleRecorder) OnSubagentStop(_ context.Context, ev hooks.SubagentStopEvent) {
	r.mu.Lock()
	r.events = append(r.events, "stop")
	r.stops = append(r.stops, ev)
	cb := r.onStop
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (r *lifecycleRecorder) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

func (r *lifecycleRecorder) Starts() []hooks.SubagentStartEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hooks.SubagentStartEvent(nil), r.starts...)
}

func (r *lifecycleRecorder) Stops() []hooks.SubagentStopEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hooks.SubagentStopEvent(nil), r.stops...)
}

// newHookWiredManager builds a Manager with the recorder registered on
// a live hooks.Registry, optionally with SessionCtx so background
// spawns work.
func newHookWiredManager(t *testing.T, runner Runner, rec *lifecycleRecorder, background bool) (*Manager, context.CancelFunc) {
	t.Helper()
	withHome(t)
	parentCwd := t.TempDir()
	policy, err := permission.New(parentCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	hreg := hooks.NewRegistry()
	hreg.Register(rec)
	cancel := func() {}
	opts := ManagerOpts{
		ProjectAbsPath:   parentCwd,
		ParentSidFn:      func() string { return "20260601-100000-parent" },
		ParentTracker:    cache.New(),
		ParentPolicy:     policy,
		ProjectSectionFn: func() string { return "" },
		SkillManifestFn:  func() string { return "" },
		ParentToolNamesFn: func() []string {
			return []string{"read", "grep", "list_dir", "git", "webfetch", "agent", "ask_user", "bash", "write", "edit"}
		},
		MaxConcurrent: 3,
		Runner:        runner,
		Hooks:         hreg,
	}
	if background {
		var sessionCtx context.Context
		sessionCtx, cancel = context.WithCancel(context.Background())
		opts.SessionCtx = sessionCtx
	}
	mgr, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); mgr.Shutdown() })
	return mgr, cancel
}

// TestSubagentStartStop_FireOnSyncSpawn: one spawn = one start, one
// stop, in that order, with the payload carrying the sub-sid, the
// agent type slug, the description, the terminal status and the token
// total.
func TestSubagentStartStop_FireOnSyncSpawn(t *testing.T) {
	rec := &lifecycleRecorder{}
	mgr, _ := newHookWiredManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{
			Summary: "found things",
			Tokens:  Tokens{Prompt: 700, Completion: 90, CacheHit: 600},
			Turns:   3,
		}, nil
	}, rec, false)

	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "esc audit",
		Prompt:      "find Esc handlers",
		Type:        TypeExplore,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("Status = %s, want completed", res.Status)
	}

	if got := rec.Events(); len(got) != 2 || got[0] != "start" || got[1] != "stop" {
		t.Fatalf("hook order = %v, want [start stop]", got)
	}

	start := rec.Starts()[0]
	if start.SID != res.SubSid {
		t.Errorf("start SID = %q, want %q", start.SID, res.SubSid)
	}
	if start.Type != string(TypeExplore) {
		t.Errorf("start Type = %q, want %q", start.Type, TypeExplore)
	}
	if start.Desc != "esc audit" {
		t.Errorf("start Desc = %q, want %q", start.Desc, "esc audit")
	}

	stop := rec.Stops()[0]
	if stop.SID != res.SubSid {
		t.Errorf("stop SID = %q, want %q", stop.SID, res.SubSid)
	}
	if stop.Status != string(StatusCompleted) {
		t.Errorf("stop Status = %q, want completed", stop.Status)
	}
	if stop.Turns != 3 {
		t.Errorf("stop Turns = %d, want 3", stop.Turns)
	}
	if stop.Tokens != 790 {
		t.Errorf("stop Tokens = %d, want 790 (prompt+completion)", stop.Tokens)
	}
}

// TestSubagentStop_FiresAfterTerminalEventLands: the index must
// already carry the terminal row when the hook runs — that is the
// contract that makes "run a lint after every subagent" work (the
// hook can read the settled state). Asserted from INSIDE the hook so
// it can't pass by accident on a later read.
func TestSubagentStop_FiresAfterTerminalEventLands(t *testing.T) {
	rec := &lifecycleRecorder{}
	var mgrRef *Manager
	var subSid string
	rec.onStop = func() {
		recs, err := mgrRef.List(ListFilter{})
		if err != nil {
			t.Errorf("List from hook: %v", err)
			return
		}
		for _, s := range recs {
			if s.SubSid != subSid {
				continue
			}
			if s.Status != StatusCompleted {
				t.Errorf("index status at hook time = %s, want completed (terminal event must land first)", s.Status)
			}
			return
		}
		t.Errorf("sub_sid %q not in index when the Stop hook ran", subSid)
	}

	mgr, _ := newHookWiredManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		subSid = job.SubSid
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	}, rec, false)
	mgrRef = mgr

	mgr.Spawn(context.Background(), SpawnArgs{Description: "d", Prompt: "p", Type: TypeExplore})
	if len(rec.Stops()) != 1 {
		t.Fatalf("stops = %d, want 1", len(rec.Stops()))
	}
}

// TestSubagentStop_FailureAndKillStatuses: every terminal class
// reports its own status — a hook filtering "only failures" depends on
// this vocabulary being honest.
func TestSubagentStop_FailureAndKillStatuses(t *testing.T) {
	cases := []struct {
		name  string
		run   Runner
		kill  bool
		want  Status
		cause string
	}{
		{
			name: "runner error",
			run: func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
				return RunnerResult{}, fmt.Errorf("boom")
			},
			want: StatusFailed,
		},
		{
			name: "user kill",
			run: func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
				<-ctx.Done()
				return RunnerResult{}, ctx.Err()
			},
			kill: true,
			want: StatusKilled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &lifecycleRecorder{}
			started := make(chan struct{})
			runner := tc.run
			if tc.kill {
				inner := tc.run
				runner = func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
					close(started)
					return inner(ctx, job)
				}
			}
			mgr, _ := newHookWiredManager(t, runner, rec, false)

			done := make(chan Result, 1)
			var subSid string
			go func() {
				res := mgr.Spawn(context.Background(), SpawnArgs{Description: "d", Prompt: "p", Type: TypeExplore})
				subSid = res.SubSid
				done <- res
			}()
			if tc.kill {
				<-started
				// Kill needs the sub-sid; read it off the start event.
				waitFor(t, 5*time.Second, func() bool { return len(rec.Starts()) == 1 })
				if err := mgr.Kill(rec.Starts()[0].SID); err != nil {
					t.Fatalf("Kill: %v", err)
				}
			}
			<-done

			stops := rec.Stops()
			if len(stops) != 1 {
				t.Fatalf("stops = %d, want 1", len(stops))
			}
			if stops[0].Status != string(tc.want) {
				t.Errorf("stop Status = %q, want %q", stops[0].Status, tc.want)
			}
			if stops[0].SID != subSid {
				t.Errorf("stop SID = %q, want %q", stops[0].SID, subSid)
			}
		})
	}
}

// TestSubagentStartStop_BackgroundSpawn: the background path fires the
// same pair (the Manager, not the child, is the single firing point),
// and Stop still fires even though the terminal event happens on a
// detached goroutine.
func TestSubagentStartStop_BackgroundSpawn(t *testing.T) {
	release := make(chan struct{})
	rec := &lifecycleRecorder{}
	mgr, _ := newHookWiredManager(t, blockingRunner(release), rec, true)

	res := mgr.SpawnBackground(SpawnArgs{Description: "nightly digest", Prompt: "p", Type: TypeExplore})
	if res.Status != StatusActive {
		t.Fatalf("SpawnBackground status = %s, want active", res.Status)
	}
	// Start fires synchronously inside SpawnBackground (before the
	// detached goroutine exists).
	if got := rec.Events(); len(got) != 1 || got[0] != "start" {
		t.Fatalf("after spawn, events = %v, want [start]", got)
	}

	close(release)
	waitFor(t, 5*time.Second, func() bool { return len(rec.Stops()) == 1 })
	stop := rec.Stops()[0]
	if stop.SID != res.SubSid {
		t.Errorf("stop SID = %q, want %q", stop.SID, res.SubSid)
	}
	if stop.Status != string(StatusCompleted) {
		t.Errorf("stop Status = %q, want completed", stop.Status)
	}
}

// TestSubagentStartStop_ContinuationInheritsType: a continuation has
// no args.Type (it inherits the prior run's) — the start event must
// still name the effective type rather than reporting "".
func TestSubagentStartStop_ContinuationInheritsType(t *testing.T) {
	rec := &lifecycleRecorder{}
	// writeTranscriptAndSummarise (manager_continue_bg_test.go) is the
	// stub that persists a transcript, which continuation requires.
	mgr, _ := newHookWiredManager(t, writeTranscriptAndSummarise, rec, false)

	first := mgr.Spawn(context.Background(), SpawnArgs{Description: "first", Prompt: "p", Type: TypeExplore})
	if first.Status != StatusCompleted {
		t.Fatalf("seed spawn status = %s", first.Status)
	}
	res := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid:  first.SubSid,
		Description: "second",
		Prompt:      "more",
	})
	if res.Status != StatusCompleted {
		t.Fatalf("continuation status = %s (summary: %s)", res.Status, res.Summary)
	}

	starts := rec.Starts()
	if len(starts) != 2 {
		t.Fatalf("start events = %d, want 2", len(starts))
	}
	cont := starts[1]
	if cont.Type != string(TypeExplore) {
		t.Errorf("continuation start Type = %q, want %q (inherited from the prior run)", cont.Type, TypeExplore)
	}
	if cont.Desc != "second" {
		t.Errorf("continuation start Desc = %q, want %q", cont.Desc, "second")
	}
	if cont.SID != res.SubSid {
		t.Errorf("continuation start SID = %q, want %q", cont.SID, res.SubSid)
	}
	// And the continuation gets its own Stop, paired with the same SID.
	stops := rec.Stops()
	if len(stops) != 2 {
		t.Fatalf("stop events = %d, want 2", len(stops))
	}
	if stops[1].SID != res.SubSid || stops[1].Status != string(StatusCompleted) {
		t.Errorf("continuation stop = %+v, want SID=%s status=completed", stops[1], res.SubSid)
	}
}

// TestSubagentHooks_NilRegistryIsSafe: no Hooks wired (the pre-P3-a
// callers, most tests) must not panic — nil-receiver dispatch.
func TestSubagentHooks_NilRegistryIsSafe(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{Description: "d", Prompt: "p", Type: TypeExplore})
	if res.Status != StatusCompleted {
		t.Errorf("Status = %s, want completed with nil Hooks", res.Status)
	}
}
