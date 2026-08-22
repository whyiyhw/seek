package permission

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// The per-Kind session allowlist ("[a] always: <kind>") replaced the
// pre-v6 "[a] = session yolo" escalation. These tests pin the four
// properties that make the narrower grant safe: it silences askFn for
// exactly one Kind, it never overrides a workflow gate, it dies on
// every posture change, and it never crosses into a subagent.

func TestAlwaysAllow_SkipsAskFnForGrantedKind(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	p, _ := New(root, PrefAsk)
	askCalls := 0
	p.SetAskFn(func(Action) bool { askCalls++; return false })

	p.SetAlwaysAllow(KindEdit)

	// Granted Kind: allowed without consulting askFn — even outside CWD,
	// which is what the user was looking at when they pressed [a].
	if err := p.Check(Action{Kind: KindEdit, Path: filepath.Join(other, "x")}); err != nil {
		t.Errorf("granted edit denied: %v", err)
	}
	if askCalls != 0 {
		t.Errorf("askFn consulted %d times for a granted Kind", askCalls)
	}
}

func TestAlwaysAllow_OtherKindsStillAsk(t *testing.T) {
	p, _ := New(t.TempDir(), PrefAsk)
	askCalls := 0
	p.SetAskFn(func(Action) bool { askCalls++; return false })

	p.SetAlwaysAllow(KindEdit)

	// Non-granted Kind: the grant must not leak — bash still asks (and
	// the askFn's false still denies).
	err := p.Check(Action{Kind: KindBash, Command: "rm -rf /"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("bash err = %v, want ErrDenied", err)
	}
	if askCalls != 1 {
		t.Errorf("askFn calls = %d, want 1", askCalls)
	}
}

func TestAlwaysAllow_WorkflowAnalyzeStillDenies(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, PrefAsk)

	// Grant BEFORE entering the workflow — SetWorkflow clears grants, so
	// re-grant after to test the gate ordering itself.
	p.SetWorkflow(WorkflowPlanAnalyze)
	p.SetAlwaysAllow(KindEdit)

	// Workflow trumps the grant: plan-analyze is read-only, full stop.
	err := p.Check(Action{Kind: KindEdit, Path: filepath.Join(root, "a.go")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze edit err = %v, want ErrDenied (grant must not override workflow)", err)
	}
}

func TestAlwaysAllow_ClearedBySetWorkflow(t *testing.T) {
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetAlwaysAllow(KindBash)
	if !p.AlwaysAllowed(KindBash) {
		t.Fatal("grant did not register")
	}
	p.SetWorkflow(WorkflowPlanAnalyze)
	if p.AlwaysAllowed(KindBash) {
		t.Error("grant survived SetWorkflow — defense-in-depth reset missing")
	}
}

func TestAlwaysAllow_ClearedBySetPref(t *testing.T) {
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetAlwaysAllow(KindBash)
	// /yolo on then off: the off MUST bring prompts back for everything.
	p.SetPref(PrefYolo)
	p.SetPref(PrefAsk)
	if p.AlwaysAllowed(KindBash) {
		t.Error("grant survived SetPref round-trip — /yolo off would silently keep bash unprompted")
	}

	askCalls := 0
	p.SetAskFn(func(Action) bool { askCalls++; return true })
	if err := p.Check(Action{Kind: KindBash, Command: "ls"}); err != nil {
		t.Errorf("bash denied after re-ask: %v", err)
	}
	if askCalls != 1 {
		t.Errorf("askFn calls = %d, want 1 (prompt must be back)", askCalls)
	}
}

func TestAlwaysAllow_NotInheritedBySpawn(t *testing.T) {
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetAlwaysAllow(KindEdit)
	child, err := p.Spawn(t.TempDir(), Restriction{})
	if err != nil {
		t.Fatal(err)
	}
	if child.AlwaysAllowed(KindEdit) {
		t.Error("Spawn inherited the parent's alwaysAllow grant")
	}
}

func TestAlwaysAllow_NilPolicySafe(t *testing.T) {
	var p *Policy
	p.SetAlwaysAllow(KindEdit) // must not panic
	if p.AlwaysAllowed(KindEdit) {
		t.Error("nil policy reported a grant")
	}
}

// Concurrent grant + Check must be race-free (-race pins it): the TUI
// goroutine grants while the agent goroutine is mid-Check.
func TestAlwaysAllow_ConcurrentGrantAndCheck(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, PrefAsk)
	p.SetAskFn(func(Action) bool { return true })

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			p.SetAlwaysAllow(KindEdit)
		}()
		go func() {
			defer wg.Done()
			_ = p.Check(Action{Kind: KindEdit, Path: filepath.Join(root, "f.go")})
		}()
	}
	wg.Wait()
}
