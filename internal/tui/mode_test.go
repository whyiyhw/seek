package tui

import (
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
)

// Tests in this file pin the Plan/Yolo/Ask mode-machine contracts that
// the v0.3.x review flagged (E1, E6) — none of these are tested
// elsewhere, and a regression in any one of them silently corrupts
// the status bar ("PLAN" without ":ANALYZE", or "PLAN:EXEC" on a
// brand-new conversation) without breaking compile or any existing
// assertion. The review found this by reading; these tests make sure
// the next regression breaks `go test` instead.

// substateHostRecorder captures SetPlanSubstate callback firings so
// tests can assert host notifications, not just in-model state.
type substateHostRecorder struct {
	calls []string
}

func (r *substateHostRecorder) capture() func(string) {
	return func(s string) { r.calls = append(r.calls, s) }
}

// --- cycleMode (Shift+Tab) ----------------------------------------------

// TestCycleMode_AskToPlanSetsAnalyzeSubstate pins the entry contract:
// Shift+Tab from Ask into Plan must set PlanSubstate="analyze" AND
// fire the SetPlanSubstate host callback. The review found cycleMode
// previously updated Plan=true but left PlanSubstate empty — the
// status bar rendered bare "PLAN" instead of "PLAN:ANALYZE" and any
// host-side reminder logic gated on substate wouldn't fire.
func TestCycleMode_AskToPlanSetsAnalyzeSubstate(t *testing.T) {
	t.Parallel()

	rec := &substateHostRecorder{}
	m := testModel().WithCustomState(func(m *Model) {
		m.opts.SetPlanSubstate = rec.capture()
	}).BuildPtr()

	m.cycleMode()

	if !m.opts.Plan {
		t.Fatalf("Ask → Plan: Plan must be true, got %v", m.opts.Plan)
	}
	if m.opts.PlanSubstate != "analyze" {
		t.Errorf("PlanSubstate = %q, want %q (cmdPlan / cycleMode parity, PRD §2.1)",
			m.opts.PlanSubstate, "analyze")
	}
	if len(rec.calls) != 1 || rec.calls[0] != "analyze" {
		t.Errorf("SetPlanSubstate host callback should fire once with %q, got %v",
			"analyze", rec.calls)
	}
}

// TestCycleMode_PlanToYoloClearsSubstate pins the exit-via-Yolo
// contract: cycling Plan → Yolo must clear PlanSubstate so a stale
// "analyze"/"execute" value doesn't leak into Yolo mode (where the
// substate is meaningless) or into the next Ask → Plan transition.
func TestCycleMode_PlanToYoloClearsSubstate(t *testing.T) {
	t.Parallel()

	rec := &substateHostRecorder{}
	m := testModel().WithPlan().WithCustomState(func(m *Model) {
		m.opts.PlanSubstate = "execute" // post-propose-tool state
		m.opts.SetPlanSubstate = rec.capture()
	}).BuildPtr()

	m.cycleMode()

	if !m.opts.Yolo || m.opts.Plan {
		t.Fatalf("Plan → Yolo: want Plan=false Yolo=true, got Plan=%v Yolo=%v",
			m.opts.Plan, m.opts.Yolo)
	}
	if m.opts.PlanSubstate != "" {
		t.Errorf("PlanSubstate must be cleared on Plan exit, got %q", m.opts.PlanSubstate)
	}
	// Host must be notified — without this, the host's permission
	// policy / reminder text stays in the execute state even though
	// the in-model field is cleared.
	if len(rec.calls) != 1 || rec.calls[0] != "" {
		t.Errorf("SetPlanSubstate host callback should fire once with %q, got %v",
			"", rec.calls)
	}
}

// TestCycleMode_YoloToAskClearsSubstate covers the wrap-around case:
// Yolo → Ask transitions through code that previously may have left
// a stale PlanSubstate behind. The substate has no meaning outside
// Plan; any non-empty value here is a leak.
func TestCycleMode_YoloToAskClearsSubstate(t *testing.T) {
	t.Parallel()

	rec := &substateHostRecorder{}
	m := testModel().WithYolo().WithCustomState(func(m *Model) {
		// Synthetic leak: PlanSubstate left non-empty by some earlier
		// path. cycleMode must scrub it regardless of how it got there.
		m.opts.PlanSubstate = "analyze"
		m.opts.SetPlanSubstate = rec.capture()
	}).BuildPtr()

	m.cycleMode()

	if m.opts.Yolo || m.opts.Plan {
		t.Fatalf("Yolo → Ask: want Plan=false Yolo=false, got Plan=%v Yolo=%v",
			m.opts.Plan, m.opts.Yolo)
	}
	if m.opts.PlanSubstate != "" {
		t.Errorf("PlanSubstate must be cleared, got %q", m.opts.PlanSubstate)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "" {
		t.Errorf("SetPlanSubstate host callback should fire once with %q, got %v",
			"", rec.calls)
	}
}

// TestCycleMode_NilHostCallbackTolerated checks the cycle still
// completes (in-model state still flips) when the host hasn't wired
// SetPlanSubstate — e.g. tests, ephemeral runs, third-party hosts.
// A nil-callback NPE here would be a regression.
func TestCycleMode_NilHostCallbackTolerated(t *testing.T) {
	t.Parallel()

	// Default builder leaves SetPlanSubstate nil. Should not panic on
	// any of the three transitions.
	for _, start := range []string{"ask", "plan", "yolo"} {
		b := testModel()
		switch start {
		case "plan":
			b = b.WithPlan().WithCustomState(func(m *Model) { m.opts.PlanSubstate = "analyze" })
		case "yolo":
			b = b.WithYolo()
		}
		m := b.BuildPtr()
		m.cycleMode() // must not panic
	}
}

// --- cmdPlan (/plan toggle) ---------------------------------------------

// TestCmdPlan_EntryStartsInAnalyze pins the /plan toggle contract:
// entering plan mode via /plan starts in the "analyze" substate
// (PRD §2.1). The propose tool's PlanProposalApproved flips it to
// "execute"; user-toggling /plan again exits cleanly.
func TestCmdPlan_EntryStartsInAnalyze(t *testing.T) {
	t.Parallel()

	m := testModel().BuildPtr()

	cmdPlan(m, "")

	if !m.opts.Plan {
		t.Fatalf("cmdPlan should toggle Plan on, got Plan=%v", m.opts.Plan)
	}
	if m.opts.PlanSubstate != "analyze" {
		t.Errorf("PlanSubstate after /plan entry = %q, want %q",
			m.opts.PlanSubstate, "analyze")
	}
}

// TestCmdPlan_ExitClearsSubstate is the symmetry test: toggling /plan
// OFF while in analyze/execute must clear the substate. A leftover
// substate after exit would re-enter Plan with a stale value the
// next time the user toggled /plan back on.
func TestCmdPlan_ExitClearsSubstate(t *testing.T) {
	t.Parallel()

	m := testModel().WithPlan().WithCustomState(func(m *Model) {
		m.opts.PlanSubstate = "execute"
	}).BuildPtr()

	cmdPlan(m, "")

	if m.opts.Plan {
		t.Fatalf("cmdPlan should toggle Plan off, got Plan=%v", m.opts.Plan)
	}
	if m.opts.PlanSubstate != "" {
		t.Errorf("PlanSubstate on /plan exit = %q, want empty", m.opts.PlanSubstate)
	}
}

// --- cmdNew (/clear) resets PlanSubstate --------------------------------

// TestCmdNew_ResetsPlanSubstate pins review finding E6: /clear must
// scrub PlanSubstate. Before the fix, /clear in PLAN:EXECUTE left the
// new (empty) session reading PLAN:EXEC in the status bar even though
// no plan had been proposed.
func TestCmdNew_ResetsPlanSubstate(t *testing.T) {
	t.Parallel()

	// /clear needs RebuildAgent wired to attempt the swap; we hand it
	// a no-op stub that returns nil. The test isn't asserting on the
	// agent — only on the PlanSubstate field.
	m := testModel().WithPlan().WithCustomState(func(m *Model) {
		m.opts.PlanSubstate = "execute"
		m.opts.RebuildAgent = func() (*agent.Agent, error) { return nil, nil }
	}).BuildPtr()

	cmdNew(m, "")

	if m.opts.PlanSubstate != "" {
		t.Errorf("PlanSubstate after /clear = %q, want empty (fresh conversation)",
			m.opts.PlanSubstate)
	}
}
