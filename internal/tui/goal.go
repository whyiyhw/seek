package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/whyiyhw/seek/internal/goal"
)

// cmdGoal handles `/goal [<condition> | clear]` (M-goal.2). With a
// condition it starts the autonomous loop (first turn fired from Update via
// goalStartMsg, so submit()'s streaming-Model isn't lost); no args reports
// status; `clear` stops. Esc during a turn also stops (handleStreamEnd).
func cmdGoal(m *Model, args string) cmdResult {
	args = strings.TrimSpace(args)
	switch {
	case args == "":
		if !m.goalActive {
			return cmdResult{text: styleMuted.Render("  no active goal — usage: /goal <condition>")}
		}
		return cmdResult{text: styleMuted.Render(m.goalStatusLine())}

	case args == "clear":
		if !m.goalActive {
			return cmdResult{text: styleMuted.Render("  no active goal")}
		}
		m.clearGoal()
		return cmdResult{text: styleMuted.Render("  ■ goal cleared")}

	default:
		if m.opts.GoalJudge == nil {
			return cmdResult{text: styleErr.Render("  ! /goal needs a judge model (unavailable in this session)")}
		}
		if m.streaming {
			return cmdResult{text: styleErr.Render("  ! can't start a goal mid-turn — wait for it to finish or press Esc first")}
		}
		m.goalActive = true
		m.goalCond = args
		m.goalTurns = 0
		m.goalStalls = 0
		m.goalStart = time.Now()
		m.goalCaps = goal.Caps{}.WithDefaults()
		return cmdResult{
			text:  styleMuted.Render("  ▶ goal: " + truncateOneLine(args, 70) + "   (Esc or /goal clear to stop)"),
			extra: func() tea.Msg { return goalStartMsg{} },
		}
	}
}

// goalJudgeCmd runs the judge off the UI thread on the just-finished turn,
// surfacing a goalVerdictMsg. The judge call is independent of the main
// conversation (its own deepseek call) so it never perturbs the worker's
// prefix cache.
func (m *Model) goalJudgeCmd() tea.Cmd {
	judge := m.opts.GoalJudge
	cond := m.goalCond
	last := goal.TurnResult{Text: m.lastAssistantText, ToolCalls: m.goalLastTurnTools}
	ctx := m.opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		v, err := judge.Judge(ctx, cond, last)
		return goalVerdictMsg{v: v, err: err}
	}
}

// handleGoalVerdict applies the judge's verdict: met → stop; otherwise
// enforce caps/stall and either continue (submit the Continuation) or stop.
// Mirrors goal.Driver's decision order (met → max-turns → stall) so the TUI
// and headless paths behave identically.
func (m Model) handleGoalVerdict(msg goalVerdictMsg) (tea.Model, tea.Cmd) {
	// Stale verdict: the goal was cleared, or the user took over with a
	// manual turn while the judge was in flight. Drop it — the user's turn
	// will produce its own verdict at its stream-end.
	if !m.goalActive || m.streaming {
		return m, nil
	}

	v := msg.v
	if msg.err != nil {
		v = goal.Verdict{Met: false, Reason: "judge error: " + msg.err.Error()}
	}

	stop := func(line string) (tea.Model, tea.Cmd) {
		(&m).clearGoal()
		(&m).persistSession() // clear Goal on disk so -resume won't re-arm
		return m, m.appendHistory(styleMuted.Render(line))
	}

	switch {
	case v.Met:
		return stop(fmt.Sprintf("  ✓ goal met after %d turn(s): %s", m.goalTurns, v.Reason))
	case m.goalTurns >= m.goalCaps.MaxTurns:
		return stop(fmt.Sprintf("  ■ goal stopped: hit max turns (%d). last: %s", m.goalCaps.MaxTurns, v.Reason))
	}

	if m.goalLastTurnTools == 0 {
		m.goalStalls++
		if m.goalStalls >= m.goalCaps.StallLimit {
			return stop(fmt.Sprintf("  ■ goal stopped: no progress for %d turns. last: %s", m.goalStalls, v.Reason))
		}
	} else {
		m.goalStalls = 0
	}

	// Continue: append-only continuation as the next prompt (same byte
	// stability rules as the headless Driver via the shared Continuation).
	m.goalToolsBase = m.toolCalls
	return m.submit(goal.Continuation(m.goalCond, v))
}

// clearGoal resets all /goal loop state.
func (m *Model) clearGoal() {
	m.goalActive = false
	m.goalCond = ""
	m.goalTurns = 0
	m.goalStalls = 0
	m.goalToolsBase = 0
	m.goalLastTurnTools = 0
}

// goalStatusLine is the one-line readout for `/goal` (no args) and the
// status bar indicator.
func (m *Model) goalStatusLine() string {
	return fmt.Sprintf("  🎯 goal: %s · turn %d/%d · %s",
		truncateOneLine(m.goalCond, 50), m.goalTurns, m.goalCaps.MaxTurns,
		time.Since(m.goalStart).Round(time.Second))
}
