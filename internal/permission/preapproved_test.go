package permission

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPreApproved_BypassesAskFnForWrites: when the flag is set, the
// per-call y/N callback is never consulted for bash/write/edit. This
// is the load-bearing property of plan-execute batch mode.
func TestPreApproved_BypassesAskFnForWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p, _ := New(root, PrefAsk)
	var askCalls int32
	p.SetAskFn(func(Action) bool {
		atomic.AddInt32(&askCalls, 1)
		return false // would deny, but we expect not to be called
	})
	// preApproved is only consulted inside WorkflowPlanExecute — the
	// flag's "owner" workflow. Outside that workflow, setting it is
	// a no-op (Check ignores it). This explicit setup makes the
	// workflow-scoping contract part of the test.
	p.SetWorkflow(WorkflowPlanExecute)
	p.SetPreApproved(true)

	outside := filepath.Join(t.TempDir(), "x.txt") // outside-CWD → dangerous
	for _, a := range []Action{
		{Kind: KindBash, Command: "go test ./..."},
		{Kind: KindWrite, Path: outside},
		{Kind: KindEdit, Path: outside},
	} {
		if err := p.Check(a); err != nil {
			t.Errorf("preApproved should allow %v, got: %v", a.Kind, err)
		}
	}
	if got := atomic.LoadInt32(&askCalls); got != 0 {
		t.Fatalf("askFn called %d times, want 0", got)
	}
}

// TestPreApproved_DoesNotBypassMemoryOrSkillInstall: only bash / write
// / edit are pre-approved. Memory writes and skill installs still go
// through the gate even in batch mode — those are durable side
// effects outside the project repo that the user should always see.
func TestPreApproved_DoesNotBypassMemoryOrSkillInstall(t *testing.T) {
	t.Parallel()
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetWorkflow(WorkflowPlanExecute)
	p.SetPreApproved(true)
	p.SetAskFn(func(Action) bool { return false })

	err := p.Check(Action{Kind: KindMemoryRemember, Display: Display{MemoryName: "x"}})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("memory_remember should still ask, got: %v", err)
	}
	err = p.Check(Action{Kind: KindSkillInstall, Display: Display{SkillName: "y"}})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("skill_install should still ask, got: %v", err)
	}
}

// TestPreApproved_SetWorkflowResetsFlag: workflow transitions clear
// preApproved so a /plan off / propose cancel / mode rebuild never
// leaves the gate half-open. SetPref does NOT reset (pref and
// workflow are independent axes; only workflow owns step state).
func TestPreApproved_SetWorkflowResetsFlag(t *testing.T) {
	t.Parallel()
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetWorkflow(WorkflowPlanExecute)
	p.SetPreApproved(true)
	if !p.PreApproved() {
		t.Fatal("setup: PreApproved should be true")
	}
	p.SetWorkflow(WorkflowNone)
	if p.PreApproved() {
		t.Fatal("SetWorkflow should clear PreApproved")
	}
}

// TestPreApproved_SetPrefDoesNotResetFlag: pref changes (e.g. /yolo
// mid-execute to skip y/N) must NOT wipe preApproved. The two axes
// are independent — pref is "how strict", workflow owns step state.
func TestPreApproved_SetPrefDoesNotResetFlag(t *testing.T) {
	t.Parallel()
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetWorkflow(WorkflowPlanExecute)
	p.SetPreApproved(true)
	p.SetPref(PrefYolo)
	if !p.PreApproved() {
		t.Fatal("SetPref must NOT clear PreApproved — workflow owns step state")
	}
}

// TestPreApproved_ToggleFalse: explicit SetPreApproved(false) returns
// us to per-call gating. Step boundaries rely on this.
func TestPreApproved_ToggleFalse(t *testing.T) {
	t.Parallel()
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetWorkflow(WorkflowPlanExecute)
	var asked int32
	p.SetAskFn(func(Action) bool {
		atomic.AddInt32(&asked, 1)
		return true
	})
	outside := filepath.Join(t.TempDir(), "x.txt")

	p.SetPreApproved(true)
	if err := p.Check(Action{Kind: KindWrite, Path: outside}); err != nil {
		t.Fatalf("during pre-approval: %v", err)
	}
	if got := atomic.LoadInt32(&asked); got != 0 {
		t.Fatalf("askFn called during pre-approval: %d", got)
	}

	p.SetPreApproved(false)
	if err := p.Check(Action{Kind: KindWrite, Path: outside}); err != nil {
		t.Fatalf("post-revoke (askFn=true): %v", err)
	}
	if got := atomic.LoadInt32(&asked); got != 1 {
		t.Fatalf("askFn calls = %d, want 1 after revoke", got)
	}
}

// TestPreApproved_PlanAnalyzeStillReadOnly: WorkflowPlanAnalyze
// denies writes unconditionally, even with preApproved=true. The
// flag is only meant to operate inside WorkflowPlanExecute. This
// defensive test guards against the future when someone wires
// preApproved into the wrong workflow by mistake.
func TestPreApproved_PlanAnalyzeStillReadOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p, _ := New(root, PrefAsk)
	p.SetWorkflow(WorkflowPlanAnalyze)
	p.SetPreApproved(true)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(root, "x.txt")})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("plan-analyze write should be denied even with preApproved, got: %v", err)
	}
}

// TestPlanAnalyze_AllowsReadOnlyBash: the ReadOnly flag (set by the
// bash tool when the command matches the inspector whitelist) lets
// `go vet ./...` and friends through plan-analyze. Without the flag,
// the same command is still denied.
func TestPlanAnalyze_AllowsReadOnlyBash(t *testing.T) {
	t.Parallel()
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetWorkflow(WorkflowPlanAnalyze)
	if err := p.Check(Action{Kind: KindBash, Command: "go vet ./...", ReadOnly: true}); err != nil {
		t.Errorf("ReadOnly bash should be allowed in plan-analyze, got: %v", err)
	}
	if err := p.Check(Action{Kind: KindBash, Command: "rm -rf /", ReadOnly: false}); !errors.Is(err, ErrDenied) {
		t.Errorf("non-ReadOnly bash should still be denied in plan-analyze, got: %v", err)
	}
}

// TestPreApproved_ConcurrentCheckAndToggle exercises the mutex under
// -race. Mirrors the goroutine pattern in plan-execute: tool dispatch
// calls Check on the agent goroutine while the TUI cancellation path
// calls SetPreApproved(false) from the bubbletea goroutine.
func TestPreApproved_ConcurrentCheckAndToggle(t *testing.T) {
	t.Parallel()
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetAskFn(func(Action) bool { return true })

	var wg sync.WaitGroup
	for range 64 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = p.Check(Action{Kind: KindBash, Command: "echo ok"})
		}()
		go func() {
			defer wg.Done()
			p.SetPreApproved(true)
			p.SetPreApproved(false)
		}()
	}
	wg.Wait()
}
