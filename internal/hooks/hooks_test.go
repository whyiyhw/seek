package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ----- Fakes (one per interface; compose by embedding for multi-slot) -----

type prePromptFake struct {
	out    PrePromptOut
	err    error
	calls  int
	lastIn PrePromptIn
}

func (f *prePromptFake) OnPrePrompt(_ context.Context, in PrePromptIn) (PrePromptOut, error) {
	f.calls++
	f.lastIn = in
	return f.out, f.err
}

type preToolUseFake struct {
	out    PreToolUseOut
	err    error
	calls  int
	lastIn PreToolUseIn
}

func (f *preToolUseFake) OnPreToolUse(_ context.Context, in PreToolUseIn) (PreToolUseOut, error) {
	f.calls++
	f.lastIn = in
	return f.out, f.err
}

type preTurnFake struct{ calls int }

func (f *preTurnFake) OnPreTurn(_ context.Context, _ PreTurnEvent) { f.calls++ }

type postTurnFake struct {
	calls  int
	lastEv PostTurnEvent
}

func (f *postTurnFake) OnPostTurn(_ context.Context, ev PostTurnEvent) {
	f.calls++
	f.lastEv = ev
}

type postToolUseFake struct {
	calls  int
	lastEv PostToolUseEvent
}

func (f *postToolUseFake) OnPostToolUse(_ context.Context, ev PostToolUseEvent) {
	f.calls++
	f.lastEv = ev
}

type sessionStartFake struct{ calls int }

func (f *sessionStartFake) OnSessionStart(_ context.Context, _ SessionStartEvent) { f.calls++ }

type sessionEndFake struct{ calls int }

func (f *sessionEndFake) OnSessionEnd(_ context.Context, _ SessionEndEvent) { f.calls++ }

// ----- P3 observer fakes (orchestration §6 D15) -----

type subagentStartFake struct {
	calls  int
	lastEv SubagentStartEvent
}

func (f *subagentStartFake) OnSubagentStart(_ context.Context, ev SubagentStartEvent) {
	f.calls++
	f.lastEv = ev
}

type subagentStopFake struct {
	calls  int
	lastEv SubagentStopEvent
}

func (f *subagentStopFake) OnSubagentStop(_ context.Context, ev SubagentStopEvent) {
	f.calls++
	f.lastEv = ev
}

type stopFake struct {
	calls  int
	lastEv StopEvent
}

func (f *stopFake) OnStop(_ context.Context, ev StopEvent) {
	f.calls++
	f.lastEv = ev
}

type preCompactFake struct {
	calls  int
	lastEv PreCompactEvent
}

func (f *preCompactFake) OnPreCompact(_ context.Context, ev PreCompactEvent) {
	f.calls++
	f.lastEv = ev
}

type notificationFake struct {
	calls  int
	lastEv NotificationEvent
}

func (f *notificationFake) OnNotification(_ context.Context, ev NotificationEvent) {
	f.calls++
	f.lastEv = ev
}

// allInOneFake satisfies every hook interface; used to assert Register
// fans a single value into every slot.
type allInOneFake struct {
	prePromptFake
	preToolUseFake
	preTurnFake
	postTurnFake
	postToolUseFake
	sessionStartFake
	sessionEndFake
	subagentStartFake
	subagentStopFake
	stopFake
	preCompactFake
	notificationFake
}

// panickingObserver always panics in OnPostTurn — used to test recovery.
type panickingObserver struct{}

func (panickingObserver) OnPostTurn(_ context.Context, _ PostTurnEvent) {
	panic("boom")
}

// panickingStopObserver panics in OnStop — recovery must hold for the
// P3 points exactly as it does for the v3 ones.
type panickingStopObserver struct{}

func (panickingStopObserver) OnStop(_ context.Context, _ StopEvent) {
	panic("boom-stop")
}

// ----- Tests -----

func TestRegistry_NilReceiverIsSafe(t *testing.T) {
	var r *Registry

	out, err := r.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "hi"})
	if err != nil {
		t.Fatalf("ApplyPrePrompt on nil: %v", err)
	}
	if out.UserText != "hi" {
		t.Errorf("nil registry should pass UserText through, got %q", out.UserText)
	}
	if out.Prepend != nil {
		t.Errorf("nil registry should not produce Prepend, got %v", out.Prepend)
	}

	pto, err := r.ApplyPreToolUse(context.Background(), PreToolUseIn{Name: "x"})
	if err != nil || pto.Deny != "" || pto.Args != nil {
		t.Errorf("nil registry ApplyPreToolUse should be zero-value, got %#v err=%v", pto, err)
	}

	// Notify* must not panic.
	r.NotifyPreTurn(context.Background(), PreTurnEvent{})
	r.NotifyPostTurn(context.Background(), PostTurnEvent{})
	r.NotifyPostToolUse(context.Background(), PostToolUseEvent{})
	r.NotifySessionStart(context.Background(), SessionStartEvent{})
	r.NotifySessionEnd(context.Background(), SessionEndEvent{})
	r.NotifySubagentStart(context.Background(), SubagentStartEvent{})
	r.NotifySubagentStop(context.Background(), SubagentStopEvent{})
	r.NotifyStop(context.Background(), StopEvent{})
	r.NotifyPreCompact(context.Background(), PreCompactEvent{})
	r.NotifyNotification(context.Background(), NotificationEvent{})

	if s := r.Stack(); len(s.PrePrompt)+len(s.PreToolUse)+len(s.PreTurn)+len(s.PostTurn)+len(s.PostToolUse)+len(s.SessionStart)+len(s.SessionEnd)+
		len(s.SubagentStart)+len(s.SubagentStop)+len(s.Stop)+len(s.PreCompact)+len(s.Notification) != 0 {
		t.Errorf("nil registry Stack() should be empty, got %#v", s)
	}
}

func TestRegister_FansIntoEverySatisfiedSlot(t *testing.T) {
	r := NewRegistry()
	r.Register(&allInOneFake{})

	s := r.Stack()
	if len(s.PrePrompt) != 1 || len(s.PreToolUse) != 1 ||
		len(s.PreTurn) != 1 || len(s.PostTurn) != 1 || len(s.PostToolUse) != 1 ||
		len(s.SessionStart) != 1 || len(s.SessionEnd) != 1 ||
		len(s.SubagentStart) != 1 || len(s.SubagentStop) != 1 ||
		len(s.Stop) != 1 || len(s.PreCompact) != 1 || len(s.Notification) != 1 {
		t.Errorf("expected one entry in every slot, got %#v", s)
	}
}

func TestRegister_PanicsOnNonHook(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if v := recover(); v == nil {
			t.Errorf("expected Register(non-hook) to panic")
		}
	}()
	r.Register(struct{ X int }{X: 1})
}

func TestPrePrompt_ChainsInRegistrationOrder(t *testing.T) {
	r := NewRegistry()
	h1 := &prePromptFake{out: PrePromptOut{UserText: "from-h1"}}
	h2 := &prePromptFake{} // empty UserText → should preserve h1's output
	r.Register(h1)
	r.Register(h2)

	out, err := r.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "orig"})
	if err != nil {
		t.Fatalf("ApplyPrePrompt: %v", err)
	}
	if out.UserText != "from-h1" {
		t.Errorf("expected chained UserText %q, got %q", "from-h1", out.UserText)
	}
	if h1.calls != 1 || h2.calls != 1 {
		t.Errorf("expected each hook called once, got h1=%d h2=%d", h1.calls, h2.calls)
	}
	if h2.lastIn.UserText != "from-h1" {
		t.Errorf("h2 should see h1's modified UserText, got %q", h2.lastIn.UserText)
	}
}

func TestPrePrompt_EmptyUserTextMeansNoChange(t *testing.T) {
	r := NewRegistry()
	r.Register(&prePromptFake{}) // returns empty UserText
	r.Register(&prePromptFake{}) // returns empty UserText

	out, _ := r.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "orig"})
	if out.UserText != "orig" {
		t.Errorf("expected original to pass through when no hook changes UserText, got %q", out.UserText)
	}
}

func TestPrePrompt_PrependAccumulatesAcrossHooks(t *testing.T) {
	r := NewRegistry()
	r.Register(&prePromptFake{out: PrePromptOut{Prepend: []deepseek.Message{{Role: "user", Content: "a"}}}})
	r.Register(&prePromptFake{out: PrePromptOut{Prepend: []deepseek.Message{{Role: "user", Content: "b"}}}})

	out, _ := r.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "orig"})
	if len(out.Prepend) != 2 || out.Prepend[0].Content != "a" || out.Prepend[1].Content != "b" {
		t.Errorf("expected Prepend=[a,b] in registration order, got %v", out.Prepend)
	}
}

func TestPrePrompt_ErrorAbortsChain(t *testing.T) {
	r := NewRegistry()
	sentinel := errors.New("boom")
	r.Register(&prePromptFake{err: sentinel})
	later := &prePromptFake{}
	r.Register(later)

	_, err := r.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "orig"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel, got %v", err)
	}
	if later.calls != 0 {
		t.Errorf("expected later hook to be skipped after error, got calls=%d", later.calls)
	}
}

func TestPreToolUse_DenyShortCircuits(t *testing.T) {
	r := NewRegistry()
	r.Register(&preToolUseFake{out: PreToolUseOut{Deny: "policy: nope"}})
	later := &preToolUseFake{}
	r.Register(later)

	out, err := r.ApplyPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	if err != nil {
		t.Fatalf("ApplyPreToolUse: %v", err)
	}
	if out.Deny != "policy: nope" {
		t.Errorf("expected Deny propagated, got %q", out.Deny)
	}
	if later.calls != 0 {
		t.Errorf("expected later hook skipped after Deny, got calls=%d", later.calls)
	}
}

func TestPreToolUse_ArgsRewriteChains(t *testing.T) {
	r := NewRegistry()
	first := &preToolUseFake{out: PreToolUseOut{Args: json.RawMessage(`{"cmd":"redacted-1"}`)}}
	second := &preToolUseFake{out: PreToolUseOut{Args: json.RawMessage(`{"cmd":"redacted-2"}`)}}
	r.Register(first)
	r.Register(second)

	out, err := r.ApplyPreToolUse(context.Background(), PreToolUseIn{
		Name: "bash",
		Args: json.RawMessage(`{"cmd":"echo hi"}`),
	})
	if err != nil {
		t.Fatalf("ApplyPreToolUse: %v", err)
	}
	if string(out.Args) != `{"cmd":"redacted-2"}` {
		t.Errorf("expected second hook's Args to win, got %s", out.Args)
	}
	if string(second.lastIn.Args) != `{"cmd":"redacted-1"}` {
		t.Errorf("second hook should see first's redacted Args, got %s", second.lastIn.Args)
	}
}

func TestPreToolUse_NoArgsRewriteMeansNil(t *testing.T) {
	r := NewRegistry()
	r.Register(&preToolUseFake{}) // returns zero PreToolUseOut

	out, _ := r.ApplyPreToolUse(context.Background(), PreToolUseIn{
		Name: "bash",
		Args: json.RawMessage(`{"cmd":"echo hi"}`),
	})
	if out.Args != nil {
		t.Errorf("expected nil Args when no hook rewrites, got %s", out.Args)
	}
}

func TestObservers_AllRunOnceInOrder(t *testing.T) {
	r := NewRegistry()
	a := &postTurnFake{}
	b := &postTurnFake{}
	r.Register(a)
	r.Register(b)

	ev := PostTurnEvent{Index: 7, ToolCalls: 3, Finish: "stop"}
	r.NotifyPostTurn(context.Background(), ev)

	if a.calls != 1 || b.calls != 1 {
		t.Errorf("expected each observer called once, got a=%d b=%d", a.calls, b.calls)
	}
	if a.lastEv != ev || b.lastEv != ev {
		t.Errorf("expected both observers to receive the same event")
	}
}

func TestObserver_PanicIsRecovered(t *testing.T) {
	r := NewRegistry()
	var mu sync.Mutex
	var stages []string
	r.SetPanicHandler(func(stage string, _ any) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, stage)
	})
	r.Register(panickingObserver{})
	later := &postTurnFake{}
	r.Register(later)

	// Must not panic out.
	r.NotifyPostTurn(context.Background(), PostTurnEvent{})

	if len(stages) != 1 || stages[0] != "PostTurn" {
		t.Errorf("expected onPanic('PostTurn', ...) once, got %v", stages)
	}
	if later.calls != 1 {
		t.Errorf("expected later observer still called after sibling panic, got calls=%d", later.calls)
	}
}

func TestStack_OrderMatchesRegistration(t *testing.T) {
	r := NewRegistry()
	r.Register(&prePromptFake{})
	r.Register(&prePromptFake{})
	s := r.Stack()
	if len(s.PrePrompt) != 2 {
		t.Fatalf("expected 2 PrePrompt entries, got %d", len(s.PrePrompt))
	}
	for _, name := range s.PrePrompt {
		if name != "*hooks.prePromptFake" {
			t.Errorf("expected *hooks.prePromptFake, got %s", name)
		}
	}
}

// ----- P3 observer dispatch (orchestration §6 D15) -----

// TestP3Observers_TriggeredAndNot: every new point fires its own slot
// exactly once with the event verbatim, and does NOT fire any other
// slot's observers (an allInOneFake registered alongside would catch
// cross-slot leakage).
func TestP3Observers_TriggeredAndNot(t *testing.T) {
	cases := []struct {
		name   string
		fire   func(r *Registry)
		wanted func(f *allInOneFake) int
	}{
		{
			name: "SubagentStart",
			fire: func(r *Registry) {
				r.NotifySubagentStart(context.Background(), SubagentStartEvent{SID: "sub-1", Type: "code-reviewer", Desc: "review pkg"})
			},
			wanted: func(f *allInOneFake) int { return f.subagentStartFake.calls },
		},
		{
			name: "SubagentStop",
			fire: func(r *Registry) {
				r.NotifySubagentStop(context.Background(), SubagentStopEvent{SID: "sub-1", Status: "completed", Turns: 3, Tokens: 1234})
			},
			wanted: func(f *allInOneFake) int { return f.subagentStopFake.calls },
		},
		{
			name: "Stop",
			fire: func(r *Registry) {
				r.NotifyStop(context.Background(), StopEvent{Turns: 4, Finish: "stop"})
			},
			wanted: func(f *allInOneFake) int { return f.stopFake.calls },
		},
		{
			name: "PreCompact",
			fire: func(r *Registry) {
				r.NotifyPreCompact(context.Background(), PreCompactEvent{Trigger: "manual"})
			},
			wanted: func(f *allInOneFake) int { return f.preCompactFake.calls },
		},
		{
			name: "Notification",
			fire: func(r *Registry) {
				r.NotifyNotification(context.Background(), NotificationEvent{Kind: "os", Title: "done"})
			},
			wanted: func(f *allInOneFake) int { return f.notificationFake.calls },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			f := &allInOneFake{}
			r.Register(f)
			tc.fire(r)
			if got := tc.wanted(f); got != 1 {
				t.Errorf("%s: wanted slot called once, got %d", tc.name, got)
			}
			// Not-triggered side: every OTHER slot (old and new) stays
			// at zero calls.
			other := map[string]int{
				"PrePrompt":     f.prePromptFake.calls,
				"PreToolUse":    f.preToolUseFake.calls,
				"PreTurn":       f.preTurnFake.calls,
				"PostTurn":      f.postTurnFake.calls,
				"PostToolUse":   f.postToolUseFake.calls,
				"SessionStart":  f.sessionStartFake.calls,
				"SessionEnd":    f.sessionEndFake.calls,
				"SubagentStart": f.subagentStartFake.calls,
				"SubagentStop":  f.subagentStopFake.calls,
				"Stop":          f.stopFake.calls,
				"PreCompact":    f.preCompactFake.calls,
				"Notification":  f.notificationFake.calls,
			}
			for slot, calls := range other {
				if slot == tc.name && calls == 1 {
					continue
				}
				if calls != 0 {
					t.Errorf("%s: slot %s unexpectedly fired %d times", tc.name, slot, calls)
				}
			}
		})
	}
}

// TestP3Observers_EventCarriesThrough: the dispatch hands observers
// the event verbatim (field-level check on the two richest payloads).
func TestP3Observers_EventCarriesThrough(t *testing.T) {
	r := NewRegistry()
	ss := &subagentStopFake{}
	r.Register(ss)

	ev := SubagentStopEvent{SID: "sub-9", Type: "explorer", Desc: "map the repo", Status: "orphaned", Turns: 7, Tokens: 99_876}
	r.NotifySubagentStop(context.Background(), ev)
	if ss.lastEv != ev {
		t.Errorf("SubagentStop event mutated in dispatch: %+v", ss.lastEv)
	}
}

// TestP3Observers_NoObserverRegistered: dispatch on a registry with no
// matching observer is a silent no-op (the "not configured" steady
// state — the wiring layer must not need nil checks).
func TestP3Observers_NoObserverRegistered(t *testing.T) {
	r := NewRegistry()
	r.Register(&postTurnFake{}) // unrelated slot only
	r.NotifySubagentStart(context.Background(), SubagentStartEvent{})
	r.NotifySubagentStop(context.Background(), SubagentStopEvent{})
	r.NotifyStop(context.Background(), StopEvent{})
	r.NotifyPreCompact(context.Background(), PreCompactEvent{})
	r.NotifyNotification(context.Background(), NotificationEvent{})
}

// TestP3Observer_PanicIsRecovered: a panicking P3 observer must not
// kill the dispatch loop, same contract as the v3 observers.
func TestP3Observer_PanicIsRecovered(t *testing.T) {
	r := NewRegistry()
	var stages []string
	r.SetPanicHandler(func(stage string, _ any) { stages = append(stages, stage) })
	r.Register(panickingStopObserver{})
	later := &stopFake{}
	r.Register(later)

	r.NotifyStop(context.Background(), StopEvent{})

	if len(stages) != 1 || stages[0] != "Stop" {
		t.Errorf("expected onPanic('Stop', ...) once, got %v", stages)
	}
	if later.calls != 1 {
		t.Errorf("expected later observer still called after sibling panic, got calls=%d", later.calls)
	}
}

// TestCloneWithout_ExcludesP3Slots: scoped dispatch (subagent
// children) must be able to drop a P3 observer the same way it drops
// parent-bound v3 ones.
func TestCloneWithout_ExcludesP3Slots(t *testing.T) {
	r := NewRegistry()
	notifier := &notificationFake{}
	keep := &stopFake{}
	r.Register(notifier)
	r.Register(keep)

	clone := r.CloneWithout(notifier)
	clone.NotifyNotification(context.Background(), NotificationEvent{})
	clone.NotifyStop(context.Background(), StopEvent{})

	if notifier.calls != 0 {
		t.Errorf("excluded observer fired on clone: calls=%d", notifier.calls)
	}
	if keep.calls != 1 {
		t.Errorf("kept observer missing from clone: calls=%d", keep.calls)
	}
	// Original unaffected by clone registration state.
	r.NotifyNotification(context.Background(), NotificationEvent{})
	if notifier.calls != 1 {
		t.Errorf("original registry lost its observer: calls=%d", notifier.calls)
	}
}
