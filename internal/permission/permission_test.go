package permission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestYoloAllowsEverything(t *testing.T) {
	p, err := New(t.TempDir(), PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Action{
		{Kind: KindBash, Command: "rm -rf /"},
		{Kind: KindWrite, Path: "/etc/hosts"},
		{Kind: KindEdit, Path: "/no/such/place/file"},
	} {
		if err := p.Check(a); err != nil {
			t.Errorf("yolo denied %v: %v", a, err)
		}
	}
}

func TestBashRequiresYolo(t *testing.T) {
	p, _ := New(t.TempDir(), PrefDeny)
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestWriteInsideCWD(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, PrefDeny)
	for _, path := range []string{
		filepath.Join(root, "a.txt"),
		filepath.Join(root, "deep", "nested", "b.txt"),
		root, // the root itself
	} {
		if err := p.Check(Action{Kind: KindWrite, Path: path}); err != nil {
			t.Errorf("denied %q: %v", path, err)
		}
	}
}

func TestWriteOutsideCWD(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir() // different dir
	p, _ := New(root, PrefDeny)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(other, "x")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestEditAlsoCWDGated(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, PrefDeny)
	err := p.Check(Action{Kind: KindEdit, Path: "/etc/hosts"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestUnknownKind(t *testing.T) {
	p, _ := New(t.TempDir(), PrefDeny)
	err := p.Check(Action{Kind: "voodoo"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

// --- ModeAsk + askFn paths ------------------------------------------
//
// The interactive approval flow is the production path used by every
// TUI session that isn't --yolo. Pre-this-commit it was 0% covered:
// SetAskFn never called by any test, so denial attribution / fallback
// behaviour / safe-action skipping were all untested guesses.

func TestModeAsk_AllowsWhenAskFnReturnsTrue(t *testing.T) {
	p, _ := New(t.TempDir(), PrefAsk)
	var (
		calls int
		seen  Action
	)
	p.SetAskFn(func(a Action) bool {
		calls++
		seen = a
		return true
	})
	if err := p.Check(Action{Kind: KindBash, Command: "ls -la"}); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
	if calls != 1 {
		t.Errorf("askFn called %d times, want 1", calls)
	}
	if seen.Command != "ls -la" {
		t.Errorf("askFn saw command=%q, want ls -la", seen.Command)
	}
}

func TestModeAsk_DeniesWhenAskFnReturnsFalse(t *testing.T) {
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetAskFn(func(_ Action) bool { return false })
	err := p.Check(Action{Kind: KindBash, Command: "rm -rf /"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	// The denial must attribute to the user — that's how the LLM
	// knows to ask for clarification rather than retry.
	if !strings.Contains(err.Error(), "user declined") {
		t.Errorf("denial should mention user choice, got %q", err.Error())
	}
}

func TestModeAsk_NoAskFnFallsBackToDeny(t *testing.T) {
	// If the host forgot SetAskFn — the policy must NEVER silently
	// allow. Failing closed is non-negotiable for a permission gate.
	p, _ := New(t.TempDir(), PrefAsk)
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("ask-without-askFn should deny, got %v", err)
	}
	// The error should still surface the --yolo escape hatch.
	if !strings.Contains(err.Error(), "--yolo") {
		t.Errorf("denial message should suggest --yolo: %q", err.Error())
	}
}

func TestModeAsk_SafeActionsBypassAskFn(t *testing.T) {
	// Writes inside CWD are safe; the askFn must NOT be consulted —
	// every safe action that nags the user is a UX regression.
	root := t.TempDir()
	p, _ := New(root, PrefAsk)
	var calls int
	p.SetAskFn(func(_ Action) bool { calls++; return true })

	target := filepath.Join(root, "f.txt")
	if err := p.Check(Action{Kind: KindWrite, Path: target}); err != nil {
		t.Fatalf("safe write denied: %v", err)
	}
	if calls != 0 {
		t.Errorf("askFn consulted %d times for safe write; want 0", calls)
	}
}

// --- SetMode runtime transitions ------------------------------------

func TestSetMode_TransitionFromAskToYoloTakesEffectImmediately(t *testing.T) {
	// /yolo in the TUI uses SetMode for live policy updates. The
	// next Check after the flip must see the new mode without any
	// agent / registry rebuild.
	p, _ := New(t.TempDir(), PrefAsk)
	p.SetAskFn(func(_ Action) bool { return false }) // would deny

	if err := p.Check(Action{Kind: KindBash, Command: "ls"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("pre-flip should deny, got %v", err)
	}

	p.SetPref(PrefYolo)

	if err := p.Check(Action{Kind: KindBash, Command: "ls"}); err != nil {
		t.Errorf("post-flip should allow, got %v", err)
	}
	// Getter sanity — also bumps coverage on Mode / Yolo / CWD,
	// which are trivial but part of the public API.
	if p.Pref() != PrefYolo {
		t.Errorf("Mode() = %v, want PrefYolo", p.Pref())
	}
	if !p.Yolo() {
		t.Errorf("Yolo() = false, want true after flip")
	}
	if p.CWD() == "" {
		t.Errorf("CWD() returned empty string")
	}
}

func TestSetMode_NilPolicySafe(t *testing.T) {
	// Defensive: every public method on *Policy guards against nil
	// receivers. A test that exercises this catches future refactors
	// that accidentally remove the guard (or worse, introduce a
	// dependency on Policy being non-nil).
	var p *Policy
	p.SetPref(PrefYolo)
	p.SetAskFn(func(_ Action) bool { return true })
	if p.Pref() != PrefDeny {
		t.Errorf("nil policy Mode() = %v, want PrefDeny", p.Pref())
	}
	if p.Yolo() {
		t.Errorf("nil policy Yolo() = true, want false")
	}
	// Check on nil policy must DENY, never panic, never allow.
	if err := p.Check(Action{Kind: KindBash}); !errors.Is(err, ErrDenied) {
		t.Errorf("nil policy Check should deny, got %v", err)
	}
}

// --- Concurrent Check -----------------------------------------------

func TestCheck_ConcurrentCallsRaceFree(t *testing.T) {
	// Dispatch runs tool goroutines concurrently (partitioned batches),
	// so N tools in flight means N concurrent Check calls — each
	// possibly blocking in askFn, which the TUI serialises through its
	// single armed listener. This contract was pinned back when
	// dispatch was still sequential, which is exactly why landing
	// parallelism found "policy is already safe" instead of a mutex
	// retrofit.
	//
	// Mixes reads (Check/Mode/Yolo) and writes (SetMode) to stress
	// the mode field specifically.
	p, _ := New(t.TempDir(), PrefYolo)
	p.SetAskFn(func(_ Action) bool { return true })

	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = p.Check(Action{Kind: KindBash, Command: "ls"})
			_ = p.Pref()
			_ = p.Yolo()
		}()
		go func(flip int) {
			defer wg.Done()
			if flip%2 == 0 {
				p.SetPref(PrefYolo)
			} else {
				p.SetPref(PrefAsk)
			}
		}(i)
	}
	wg.Wait()
}

// --- Symlink resolution (security) ------------------------------------
//
// permission.isWithin now resolves symlinks (filepath.EvalSymlinks) so a
// symlink INSIDE cwd that points OUTSIDE cwd is correctly caught.
func TestIsWithin_SymlinkInsideCWDPointingOutsideIsDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	p, _ := New(root, PrefDeny)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(link, "x")})
	if err == nil {
		t.Error("symlink-in-cwd write allowed — symlink resolution not working")
	}
}

// --- WorkflowPlanAnalyze tests --------------------------------------
// These exercise the workflow gate's read-only constraints. The pref
// axis is held at PrefAsk to isolate workflow behaviour from pref
// (see TestWorkflowAnalyze_TrumpsYoloPref for the cross-axis case).

func newPlanAnalyze(t *testing.T, root string) *Policy {
	t.Helper()
	p, err := New(root, PrefAsk)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	p.SetWorkflow(WorkflowPlanAnalyze)
	return p
}

func TestWorkflowAnalyze_AllowsReadInsideCWD(t *testing.T) {
	root := t.TempDir()
	p := newPlanAnalyze(t, root)
	err := p.Check(Action{Kind: KindRead, Path: filepath.Join(root, "foo.go")})
	if err != nil {
		t.Errorf("plan-analyze should allow read inside CWD, got %v", err)
	}
}

func TestWorkflowAnalyze_DeniesReadOutsideCWD(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	p := newPlanAnalyze(t, root)
	err := p.Check(Action{Kind: KindRead, Path: filepath.Join(other, "secret")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze should deny read outside CWD, got %v", err)
	}
}

func TestWorkflowAnalyze_DeniesBash(t *testing.T) {
	p := newPlanAnalyze(t, t.TempDir())
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze should deny bash, got %v", err)
	}
}

func TestWorkflowAnalyze_AllowsReadOnlyBash(t *testing.T) {
	// ReadOnly flag (set by bash tool's whitelist + metachar check)
	// punches through the plan-analyze blanket deny.
	p := newPlanAnalyze(t, t.TempDir())
	err := p.Check(Action{Kind: KindBash, Command: "go vet ./...", ReadOnly: true})
	if err != nil {
		t.Errorf("ReadOnly bash should be allowed in plan-analyze, got %v", err)
	}
}

func TestWorkflowAnalyze_DeniesWriteInsideCWD(t *testing.T) {
	root := t.TempDir()
	p := newPlanAnalyze(t, root)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(root, "x.go")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze should deny write even inside CWD, got %v", err)
	}
}

func TestWorkflowAnalyze_DeniesEdit(t *testing.T) {
	p := newPlanAnalyze(t, t.TempDir())
	err := p.Check(Action{Kind: KindEdit, Path: "/some/file"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze should deny edit, got %v", err)
	}
}

func TestWorkflowAnalyze_DeniesMemoryRemember(t *testing.T) {
	p := newPlanAnalyze(t, t.TempDir())
	err := p.Check(Action{Kind: KindMemoryRemember, Display: Display{MemoryName: "test"}})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze should deny memory_remember, got %v", err)
	}
}

func TestWorkflowAnalyze_DeniesUnknownKind(t *testing.T) {
	p := newPlanAnalyze(t, t.TempDir())
	err := p.Check(Action{Kind: "voodoo"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan-analyze should deny unknown kind, got %v", err)
	}
}

func TestPlan_Method(t *testing.T) {
	p := newPlanAnalyze(t, t.TempDir())
	if !p.Plan() {
		t.Error("Plan() should return true under any plan workflow")
	}
	if p.Yolo() {
		t.Error("Yolo() should return false under PrefAsk")
	}
	if p.Workflow() != WorkflowPlanAnalyze {
		t.Errorf("Workflow() = %v, want WorkflowPlanAnalyze", p.Workflow())
	}
}

// --- Cross-axis matrix (PRD §6.1) -----------------------------------
// Workflow trumps pref where workflow imposes a hard constraint.
// PrefYolo + WorkflowPlanAnalyze MUST still be read-only — that's the
// load-bearing invariant of plan mode existing at all.

func TestWorkflowAnalyze_TrumpsYoloPref(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, PrefYolo)
	p.SetWorkflow(WorkflowPlanAnalyze)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(root, "x.go")})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("write under Yolo+PlanAnalyze should be denied — workflow trumps pref. Got: %v", err)
	}
}

func TestWorkflowExecute_FallsBackToPref(t *testing.T) {
	// PlanExecute is just plan-analyze unlocked; pref takes over.
	// Yolo + PlanExecute = allow writes; Ask + PlanExecute (no
	// preApproved) = askFn consulted.
	root := t.TempDir()
	p, _ := New(root, PrefYolo)
	p.SetWorkflow(WorkflowPlanExecute)
	if err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(root, "x.go")}); err != nil {
		t.Errorf("Yolo + PlanExecute should allow writes (pref takes over), got %v", err)
	}
}

func TestSetWorkflow_ResetsPreApproved(t *testing.T) {
	// Any workflow transition wipes preApproved — the plan-execute
	// step state must not survive workflow boundaries.
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
