package tui

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
)

// TestRenderPlanTaskList_AllStates asserts the four status markers
// render with the right glyphs. The model's narration is the source
// of truth for step status, so getting the wrong marker would lie
// about completion.
func TestRenderPlanTaskList_AllStates(t *testing.T) {
	SetTheme("dark")
	steps := []agent.PlanStep{
		{Text: "first", Status: "completed"},
		{Text: "second", Status: "in_progress"},
		{Text: "third", Status: "pending"},
		{Text: "fourth", Status: "skipped"},
	}
	got := renderPlanTaskList(steps, 1)

	if !strings.Contains(got, "✓ 1. first") {
		t.Errorf("missing completed marker:\n%s", got)
	}
	if !strings.Contains(got, "▸ 2. second") {
		t.Errorf("missing in_progress marker:\n%s", got)
	}
	if !strings.Contains(got, "· 3. third") {
		t.Errorf("missing pending marker:\n%s", got)
	}
	if !strings.Contains(got, "— 4. fourth (skipped)") {
		t.Errorf("missing skipped marker:\n%s", got)
	}
	if !strings.Contains(got, "plan") {
		t.Errorf("missing 'plan' header:\n%s", got)
	}
}

func TestRenderPlanTaskList_EmptyReturnsEmpty(t *testing.T) {
	if got := renderPlanTaskList(nil, -1); got != "" {
		t.Fatalf("empty steps should render empty, got %q", got)
	}
}

// TestApplyAgentEvent_PlanStepUpdated covers the TUI-side event
// handler: receiving PlanStepUpdated mirrors Steps + CurrentIdx into
// Options without committing a scrollback line (avoids spamming
// history on every step change).
func TestApplyAgentEvent_PlanStepUpdated(t *testing.T) {
	SetTheme("dark")
	m := renderTestModel(t)
	steps := []agent.PlanStep{
		{Text: "alpha", Status: "in_progress"},
		{Text: "beta", Status: "pending"},
	}
	cmds := m.applyAgentEvent(agent.PlanStepUpdated{Steps: steps, CurrentIdx: 0})
	if len(cmds) != 0 {
		t.Fatalf("PlanStepUpdated should not emit scrollback cmds, got %d", len(cmds))
	}
	if len(m.opts.PlanSteps) != 2 {
		t.Fatalf("PlanSteps len = %d, want 2", len(m.opts.PlanSteps))
	}
	if m.opts.PlanCurrentIdx != 0 {
		t.Fatalf("PlanCurrentIdx = %d, want 0", m.opts.PlanCurrentIdx)
	}
}

// TestApplyAgentEvent_PlanProposalCancelled_ClearsTaskList ensures the
// task list is dropped when the user cancels /plan — otherwise stale
// rows would persist after the workflow exits.
func TestApplyAgentEvent_PlanProposalCancelled_ClearsTaskList(t *testing.T) {
	SetTheme("dark")
	m := renderTestModel(t)
	m.opts.PlanSteps = []agent.PlanStep{{Text: "x", Status: "in_progress"}}
	m.opts.PlanCurrentIdx = 0
	m.opts.Plan = true
	m.opts.PlanSubstate = "execute"
	m.applyAgentEvent(agent.PlanProposalCancelled{})
	if m.opts.PlanSteps != nil {
		t.Errorf("PlanSteps should be nil after cancel, got %+v", m.opts.PlanSteps)
	}
	if m.opts.PlanCurrentIdx != -1 {
		t.Errorf("PlanCurrentIdx should be -1 after cancel, got %d", m.opts.PlanCurrentIdx)
	}
	if m.opts.Plan {
		t.Errorf("Plan should be false after cancel")
	}
	if m.opts.PlanSubstate != "" {
		t.Errorf("PlanSubstate should be empty after cancel, got %q", m.opts.PlanSubstate)
	}
}

// TestStatusBar_PlanExecWithCounter verifies the status bar appends
// "done/total" to the PLAN:EXEC badge when PlanStepsTotal > 0.
func TestStatusBar_PlanExecWithCounter(t *testing.T) {
	SetTheme("dark")
	out := RenderStatusBar(StatusSnapshot{
		Model:          "deepseek-chat",
		Plan:           true,
		PlanSubstate:   "execute",
		PlanStepsTotal: 5,
		PlanStepsDone:  2,
	})
	if !strings.Contains(out, "PLAN:EXEC 2/5") {
		t.Fatalf("status bar missing counter:\n%s", out)
	}
}

func TestStatusBar_PlanExecWithoutStepsOmitsCounter(t *testing.T) {
	SetTheme("dark")
	out := RenderStatusBar(StatusSnapshot{
		Model:        "deepseek-chat",
		Plan:         true,
		PlanSubstate: "execute",
	})
	if !strings.Contains(out, "PLAN:EXEC") {
		t.Fatalf("missing PLAN:EXEC label:\n%s", out)
	}
	if strings.Contains(out, "0/0") {
		t.Fatalf("should omit 0/0 counter when no plan loaded:\n%s", out)
	}
}
