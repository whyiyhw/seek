package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/goal"
)

type tuiFakeJudge struct {
	v   goal.Verdict
	err error
}

func (j tuiFakeJudge) Judge(context.Context, string, goal.TurnResult) (goal.Verdict, error) {
	return j.v, j.err
}

// goalModel builds a *Model wired for /goal: a fake agent (so the continue
// path's submit works), a non-nil ctx (submit derives a cancel ctx), and
// default caps.
func goalModel(t *testing.T, j goal.Judge) (*Model, *fakeAgent) {
	t.Helper()
	a := newFakeAgent()
	m := testModel().WithAgent(a).BuildPtr()
	m.opts.GoalJudge = j
	m.opts.Ctx = context.Background()
	m.goalCaps = goal.Caps{}.WithDefaults()
	return m, a
}

func TestCmdGoal_StartsLoop(t *testing.T) {
	m, _ := goalModel(t, tuiFakeJudge{})
	res := runHandler(t, m, "/goal make all tests pass")
	if !m.goalActive || m.goalCond != "make all tests pass" {
		t.Fatalf("goal not armed: active=%v cond=%q", m.goalActive, m.goalCond)
	}
	if res.extra == nil {
		t.Fatal("starting a goal must return a goalStartMsg cmd")
	}
	if msg := res.extra(); msg == nil {
		t.Fatal("goalStartMsg cmd produced no msg")
	} else if _, ok := msg.(goalStartMsg); !ok {
		t.Fatalf("extra cmd should emit goalStartMsg, got %T", msg)
	}
}

func TestCmdGoal_NoJudgeUnavailable(t *testing.T) {
	m, _ := goalModel(t, nil) // no judge
	res := runHandler(t, m, "/goal do the thing")
	if m.goalActive {
		t.Fatal("goal must NOT arm without a judge")
	}
	if !strings.Contains(res.text, "unavailable") {
		t.Fatalf("expected an 'unavailable' message, got %q", res.text)
	}
}

func TestCmdGoal_RejectedMidStream(t *testing.T) {
	m, _ := goalModel(t, tuiFakeJudge{})
	m.streaming = true
	res := runHandler(t, m, "/goal do it")
	if m.goalActive || !strings.Contains(res.text, "mid-turn") {
		t.Fatalf("starting a goal mid-stream should be refused: active=%v text=%q", m.goalActive, res.text)
	}
}

func TestCmdGoal_StatusAndClear(t *testing.T) {
	m, _ := goalModel(t, tuiFakeJudge{})
	// no active goal
	if res := runHandler(t, m, "/goal"); !strings.Contains(res.text, "no active goal") {
		t.Fatalf("status with no goal = %q", res.text)
	}
	// armed → status shows the condition
	m.goalActive = true
	m.goalCond = "ship it"
	if res := runHandler(t, m, "/goal"); !strings.Contains(res.text, "ship it") {
		t.Fatalf("status should show condition: %q", res.text)
	}
	// clear
	if res := runHandler(t, m, "/goal clear"); !strings.Contains(res.text, "cleared") || m.goalActive {
		t.Fatalf("clear failed: text=%q active=%v", res.text, m.goalActive)
	}
}

func TestHandleGoalVerdict_MetStops(t *testing.T) {
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = true
	m.goalCond = "c"
	m.goalTurns = 2
	nm, _ := m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: true, Reason: "all green"}})
	newM := nm.(Model)
	if newM.goalActive {
		t.Fatal("met verdict must clear the goal")
	}
	if len(a.PromptCalls) != 0 {
		t.Fatalf("met verdict must NOT submit another turn, got %v", a.PromptCalls)
	}
}

func TestHandleGoalVerdict_NotMetContinues(t *testing.T) {
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = true
	m.goalCond = "make tests pass"
	m.goalTurns = 1
	m.goalLastTurnTools = 2 // progress → no stall
	nm, _ := m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: false, Reason: "1 failing", Hint: "fix it"}})
	newM := nm.(Model)
	if !newM.goalActive || !newM.streaming {
		t.Fatalf("not-met should continue the loop: active=%v streaming=%v", newM.goalActive, newM.streaming)
	}
	if len(a.PromptCalls) != 1 {
		t.Fatalf("not-met should submit a continuation turn, got %v", a.PromptCalls)
	}
	if !strings.Contains(a.PromptCalls[0], "make tests pass") || !strings.Contains(a.PromptCalls[0], "fix it") {
		t.Fatalf("continuation must carry condition + hint: %q", a.PromptCalls[0])
	}
}

func TestHandleGoalVerdict_MaxTurnsStops(t *testing.T) {
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = true
	m.goalCond = "c"
	m.goalCaps.MaxTurns = 3
	m.goalTurns = 3 // already at the cap
	m.goalLastTurnTools = 1
	nm, _ := m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: false}})
	if nm.(Model).goalActive || len(a.PromptCalls) != 0 {
		t.Fatal("hitting max turns must stop without another turn")
	}
}

func TestHandleGoalVerdict_StallStops(t *testing.T) {
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = true
	m.goalCond = "c"
	m.goalCaps.StallLimit = 3
	m.goalStalls = 2        // two prior no-progress turns
	m.goalLastTurnTools = 0 // this turn also made no progress → 3rd
	nm, _ := m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: false}})
	if nm.(Model).goalActive || len(a.PromptCalls) != 0 {
		t.Fatal("hitting the stall limit must stop")
	}
}

func TestHandleGoalVerdict_StaleDropped(t *testing.T) {
	// Verdict arrives but the goal was cleared, or the user took over (a
	// new stream is in flight) → drop it, don't submit.
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = false
	if _, cmd := m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: false}}); cmd != nil {
		t.Fatal("verdict with no active goal must be a no-op")
	}
	m.goalActive = true
	m.streaming = true // user already started another turn
	m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: false}})
	if len(a.PromptCalls) != 0 {
		t.Fatal("verdict while streaming must not submit")
	}
}

func TestHandleGoalVerdict_JudgeErrorTreatedNotMet(t *testing.T) {
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = true
	m.goalCond = "c"
	m.goalTurns = 1
	m.goalLastTurnTools = 1
	nm, _ := m.handleGoalVerdict(goalVerdictMsg{err: context.DeadlineExceeded})
	if !nm.(Model).goalActive || len(a.PromptCalls) != 1 {
		t.Fatal("a judge error must be treated as not-met and continue")
	}
}

func TestGoalJudgeCmd_SurfacesVerdict(t *testing.T) {
	m, _ := goalModel(t, tuiFakeJudge{v: goal.Verdict{Met: true, Reason: "done"}})
	m.goalCond = "c"
	msg := m.goalJudgeCmd()()
	gv, ok := msg.(goalVerdictMsg)
	if !ok {
		t.Fatalf("goalJudgeCmd should emit goalVerdictMsg, got %T", msg)
	}
	if !gv.v.Met || gv.v.Reason != "done" {
		t.Fatalf("verdict not surfaced: %+v", gv)
	}
}

func TestStatusBar_GoalBadge(t *testing.T) {
	out := RenderStatusBar(StatusSnapshot{Model: "m", GoalActive: true, GoalTurns: 2, GoalMaxTurns: 25})
	if !strings.Contains(out, "goal") || !strings.Contains(out, "2/25") {
		t.Fatalf("goal badge missing/wrong: %q", out)
	}
	if strings.Contains(RenderStatusBar(StatusSnapshot{Model: "m"}), "🎯") {
		t.Fatal("goal badge must be suppressed when no goal is active")
	}
}

// builder helpers for resume tests (ResumeGoal/GoalJudge are read by the
// constructor, so they must be set before Build).
func (b *testModelBuilder) WithGoalJudge(j goal.Judge) *testModelBuilder {
	b.opts.GoalJudge = j
	return b
}
func (b *testModelBuilder) WithResumeGoal(c string) *testModelBuilder {
	b.opts.ResumeGoal = c
	return b
}

func TestResumeGoal_ReArms(t *testing.T) {
	m := testModel().WithAgent(newFakeAgent()).
		WithGoalJudge(tuiFakeJudge{}).WithResumeGoal("finish the migration").BuildPtr()
	if !m.goalActive || m.goalCond != "finish the migration" {
		t.Fatalf("resume should re-arm the goal: active=%v cond=%q", m.goalActive, m.goalCond)
	}
	if m.goalCaps.MaxTurns == 0 {
		t.Fatal("resumed goal must get default caps")
	}
}

func TestResumeGoal_GatedOnJudge(t *testing.T) {
	// No judge → don't re-arm (the loop would be unable to assess anyway).
	m := testModel().WithAgent(newFakeAgent()).WithResumeGoal("do it").BuildPtr()
	if m.goalActive {
		t.Fatal("resume must NOT re-arm without a judge")
	}
}

func TestUpdate_GoalStartSubmits(t *testing.T) {
	m, a := goalModel(t, tuiFakeJudge{})
	m.goalActive = true
	m.goalCond = "make it pass"
	nm, _ := m.Update(goalStartMsg{})
	if !nm.(Model).streaming || len(a.PromptCalls) != 1 || a.PromptCalls[0] != "make it pass" {
		t.Fatalf("goalStartMsg should submit the condition as the first turn: streaming=%v calls=%v",
			nm.(Model).streaming, a.PromptCalls)
	}
}
