package permission

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// pp/wp are convenience helpers so the table below stays terse.
func pp(p Preference) *Preference { return &p }
func wp(w Workflow) *Workflow     { return &w }

// TestSpawn_PreferenceTransitions is the table-driven gate for the
// monotonic-only Preference axis. Each row is one (parent, restriction)
// combination; wantOK indicates whether Spawn should accept it.
func TestSpawn_PreferenceTransitions(t *testing.T) {
	cases := []struct {
		name       string
		parent     Preference
		childPref  *Preference // nil = inherit
		wantPref   Preference  // only checked when wantOK
		wantOK     bool
		wantSubErr string // substring of error message when !wantOK
	}{
		// Inherit by leaving childPref nil.
		{"deny inherit", PrefDeny, nil, PrefDeny, true, ""},
		{"ask inherit", PrefAsk, nil, PrefAsk, true, ""},
		{"yolo inherit", PrefYolo, nil, PrefYolo, true, ""},
		// Same-level explicit pin (idempotent tighten).
		{"deny→deny", PrefDeny, pp(PrefDeny), PrefDeny, true, ""},
		{"ask→ask", PrefAsk, pp(PrefAsk), PrefAsk, true, ""},
		{"yolo→yolo", PrefYolo, pp(PrefYolo), PrefYolo, true, ""},
		// Valid tightening (parent looser, child stricter).
		{"ask→deny", PrefAsk, pp(PrefDeny), PrefDeny, true, ""},
		{"yolo→ask", PrefYolo, pp(PrefAsk), PrefAsk, true, ""},
		{"yolo→deny", PrefYolo, pp(PrefDeny), PrefDeny, true, ""},
		// Loosening — must reject.
		{"deny→ask (loosen)", PrefDeny, pp(PrefAsk), 0, false, "loosen"},
		{"deny→yolo (loosen)", PrefDeny, pp(PrefYolo), 0, false, "loosen"},
		{"ask→yolo (loosen)", PrefAsk, pp(PrefYolo), 0, false, "loosen"},
	}
	cwd := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent, err := New(cwd, c.parent)
			if err != nil {
				t.Fatalf("New parent: %v", err)
			}
			child, err := parent.Spawn(cwd, Restriction{Pref: c.childPref})
			if c.wantOK {
				if err != nil {
					t.Fatalf("Spawn rejected valid transition: %v", err)
				}
				if got := child.Pref(); got != c.wantPref {
					t.Errorf("child pref = %s, want %s", got, c.wantPref)
				}
			} else {
				if err == nil {
					t.Fatalf("Spawn accepted invalid transition; child = %+v", child)
				}
				if !strings.Contains(err.Error(), c.wantSubErr) {
					t.Errorf("error %q does not contain %q", err, c.wantSubErr)
				}
			}
		})
	}
}

// TestSpawn_WorkflowTransitions is the table-driven gate for the
// Workflow axis. The axis is not totally ordered, so the cases below
// enumerate every (parent × child) pair explicitly.
func TestSpawn_WorkflowTransitions(t *testing.T) {
	cases := []struct {
		name      string
		parent    Workflow
		childWf   *Workflow
		wantWf    Workflow
		wantOK    bool
	}{
		// Parent None → any child workflow OK.
		{"none→inherit (none)", WorkflowNone, nil, WorkflowNone, true},
		{"none→plan-analyze", WorkflowNone, wp(WorkflowPlanAnalyze), WorkflowPlanAnalyze, true},
		{"none→plan-execute", WorkflowNone, wp(WorkflowPlanExecute), WorkflowPlanExecute, true},

		// Parent PlanAnalyze → child MUST stay PlanAnalyze (terminal).
		{"plan-analyze→inherit", WorkflowPlanAnalyze, nil, WorkflowPlanAnalyze, true},
		{"plan-analyze→plan-analyze", WorkflowPlanAnalyze, wp(WorkflowPlanAnalyze), WorkflowPlanAnalyze, true},
		{"plan-analyze→plan-execute (escape)", WorkflowPlanAnalyze, wp(WorkflowPlanExecute), 0, false},
		{"plan-analyze→none (escape)", WorkflowPlanAnalyze, wp(WorkflowNone), 0, false},

		// Parent PlanExecute → child PlanExecute or PlanAnalyze; not None.
		{"plan-execute→inherit", WorkflowPlanExecute, nil, WorkflowPlanExecute, true},
		{"plan-execute→plan-execute", WorkflowPlanExecute, wp(WorkflowPlanExecute), WorkflowPlanExecute, true},
		{"plan-execute→plan-analyze (tighten)", WorkflowPlanExecute, wp(WorkflowPlanAnalyze), WorkflowPlanAnalyze, true},
		{"plan-execute→none (escape)", WorkflowPlanExecute, wp(WorkflowNone), 0, false},
	}
	cwd := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent, _ := New(cwd, PrefYolo) // pref axis decoupled from workflow tests
			parent.SetWorkflow(c.parent)
			child, err := parent.Spawn(cwd, Restriction{Workflow: c.childWf})
			if c.wantOK {
				if err != nil {
					t.Fatalf("Spawn rejected valid transition: %v", err)
				}
				if got := child.Workflow(); got != c.wantWf {
					t.Errorf("child workflow = %s, want %s", got, c.wantWf)
				}
			} else {
				if err == nil {
					t.Fatalf("Spawn accepted invalid workflow transition; child = %+v", child)
				}
			}
		})
	}
}

// TestSpawn_PreApprovedNeverInherits is load-bearing for the
// PlanExecute-under-PlanExecute case: even though the child stays in
// PlanExecute, the parent's per-step auto-approve window MUST NOT
// silently extend into the child. The child must re-establish its own
// preApproved via its own plan flow.
func TestSpawn_PreApprovedNeverInherits(t *testing.T) {
	cwd := t.TempDir()
	parent, _ := New(cwd, PrefYolo)
	parent.SetWorkflow(WorkflowPlanExecute)
	parent.SetPreApproved(true)

	child, err := parent.Spawn(cwd, Restriction{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if child.PreApproved() {
		t.Error("child.PreApproved == true; parent's preApproved must NOT inherit even when workflow stays PlanExecute")
	}
	// Sanity: parent unchanged.
	if !parent.PreApproved() {
		t.Error("parent.PreApproved flipped to false by Spawn — Spawn must not mutate parent")
	}
}

// TestSpawn_CwdIsResolvedToAbs verifies that cwd handling matches the
// New constructor's invariant: stored cwd is absolute, regardless of
// whether the caller passes relative or absolute.
func TestSpawn_CwdIsResolvedToAbs(t *testing.T) {
	parentCwd := t.TempDir()
	parent, _ := New(parentCwd, PrefAsk)

	// Pass a different cwd (simulating isolation:"worktree" landing
	// the child in a worktree path).
	worktree := t.TempDir()
	child, err := parent.Spawn(worktree, Restriction{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	got := child.Cwd()
	want, _ := filepath.Abs(worktree)
	if got != want {
		t.Errorf("child Cwd = %q, want %q", got, want)
	}
	// Parent Cwd untouched.
	if pc := parent.Cwd(); pc != parentCwd {
		// Note: parent's Cwd may already be absolute (TempDir returns abs);
		// compare via filepath.Abs to be robust.
		absParent, _ := filepath.Abs(parentCwd)
		if pc != absParent {
			t.Errorf("parent Cwd mutated by Spawn: %q vs %q", pc, parentCwd)
		}
	}
}

// TestSpawn_NilParentReturnsErr defends the nil-receiver path — Spawn
// must return an error rather than panic so the orchestrator can
// surface a clean failure to the LLM.
func TestSpawn_NilParentReturnsErr(t *testing.T) {
	var p *Policy
	child, err := p.Spawn(t.TempDir(), Restriction{})
	if err == nil {
		t.Fatalf("Spawn on nil parent must error, got child=%+v", child)
	}
}

// TestSpawn_ChildHasIndependentLock: concurrent ops on parent and
// child must not block each other. Run a long burst of SetPref +
// Pref reads on both in parallel goroutines; verify the test does
// not hang and -race stays clean.
func TestSpawn_ChildHasIndependentLock(t *testing.T) {
	cwd := t.TempDir()
	parent, _ := New(cwd, PrefYolo)
	child, err := parent.Spawn(cwd, Restriction{Pref: pp(PrefAsk)})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			parent.SetPref(PrefYolo)
			_ = parent.Pref()
		}()
		go func() {
			defer wg.Done()
			child.SetPref(PrefAsk)
			_ = child.Pref()
		}()
	}
	wg.Wait()
	// Final state is whatever the last-writer happened to be; the test
	// only cares that there's no deadlock or data race.
	_ = parent.Pref()
	_ = child.Pref()
}

// TestSpawn_AskFnAndOnDestructiveNotCarried documents that the child
// starts with zero askFn and zero onDestructive — the orchestrator
// wires them after Spawn. Observable via Check: with no askFn set, a
// child in PrefAsk + unconditional-dangerous action (KindBash) takes
// the "ask-but-no-askFn → deny with re-run hint" branch in prefGate.
// (KindWrite is path-gated and only goes dangerous outside cwd, so it
// would let a same-cwd write pass without consulting askFn at all —
// not a useful probe for askFn-inheritance.)
func TestSpawn_AskFnAndOnDestructiveNotCarried(t *testing.T) {
	cwd := t.TempDir()
	parent, _ := New(cwd, PrefAsk)
	// Wire parent's askFn to always allow — we want to prove the child
	// does NOT inherit this.
	parent.SetAskFn(func(Action) bool { return true })

	child, err := parent.Spawn(cwd, Restriction{})
	if err != nil {
		t.Fatal(err)
	}
	// KindBash is unconditionally dangerous under prefGate. Child in
	// PrefAsk + nil askFn must deny.
	err = child.Check(Action{Kind: KindBash, Command: "echo hi"})
	if err == nil {
		t.Error("child inherited askFn from parent; expected denial when child has no askFn wired")
	}

	// Parent still allows (its askFn returns true).
	if err := parent.Check(Action{Kind: KindBash, Command: "echo hi"}); err != nil {
		t.Errorf("parent's Check unexpectedly denied: %v", err)
	}
}
