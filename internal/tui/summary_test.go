package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// The exit-summary timing is measured at event-arrival in
// applyAgentEvent. These tests drive synthetic event sequences and
// assert the accumulated session stats — including the failure paths
// (cancel mid-stream, stream error, tool-result MessageEnds).

func TestExitTiming_TurnWithTool(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	m.applyAgentEvent(agent.TurnStart{})
	// Sleep so the measured span crosses at least one clock tick —
	// time.Since() returns exactly 0 across sub-tick gaps on Windows.
	time.Sleep(2 * time.Millisecond)
	m.applyAgentEvent(agent.MessageDelta{Delta: "hi"})
	m.applyAgentEvent(agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "hi"}})
	if !m.turnStart.IsZero() {
		t.Fatal("assistant MessageEnd must close the LLM span")
	}
	if m.llmTime <= 0 {
		t.Errorf("llmTime after assistant MessageEnd = %v, want > 0", m.llmTime)
	}

	m.applyAgentEvent(agent.ToolExecStart{CallID: "c1", Name: "read"})
	time.Sleep(2 * time.Millisecond)
	m.applyAgentEvent(agent.ToolExecEnd{CallID: "c1", Name: "read", Result: "x"})
	m.applyAgentEvent(agent.TurnEnd{Usage: deepseek.Usage{CompletionTokens: 100}})

	if m.toolTime <= 0 {
		t.Errorf("toolTime = %v, want > 0", m.toolTime)
	}
	if m.firstTokN != 1 {
		t.Errorf("firstTokN = %d, want 1", m.firstTokN)
	}
	if m.completionTok != 100 {
		t.Errorf("completionTok = %d, want 100", m.completionTok)
	}
	if !m.turnStart.IsZero() {
		t.Error("turnStart must be zero after TurnEnd")
	}
}

func TestExitTiming_ToolLessTurn(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	m.applyAgentEvent(agent.TurnStart{})
	// Same tick-crossing sleep as TestExitTiming_TurnWithTool.
	time.Sleep(2 * time.Millisecond)
	m.applyAgentEvent(agent.MessageDelta{Delta: "answer"})
	m.applyAgentEvent(agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "answer"}})
	m.applyAgentEvent(agent.TurnEnd{})

	if m.llmTime <= 0 {
		t.Errorf("llmTime = %v, want > 0", m.llmTime)
	}
	if m.toolTime != 0 {
		t.Errorf("toolTime = %v, want 0 (no tools)", m.toolTime)
	}
}

func TestExitTiming_FirstTokenCountedOncePerTurn(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	// Two deltas in one turn (a retried stream re-emits under the same
	// TurnStart) must count exactly one first token.
	m.applyAgentEvent(agent.TurnStart{})
	m.applyAgentEvent(agent.MessageDelta{Delta: "a"})
	m.applyAgentEvent(agent.MessageDelta{Delta: "b"})
	m.applyAgentEvent(agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant}})
	m.applyAgentEvent(agent.TurnEnd{})

	m.applyAgentEvent(agent.TurnStart{})
	m.applyAgentEvent(agent.MessageDelta{Delta: "c", Reasoning: true})
	m.applyAgentEvent(agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant}})
	m.applyAgentEvent(agent.TurnEnd{})

	if m.firstTokN != 2 {
		t.Errorf("firstTokN = %d, want 2 (once per turn, incl. reasoning deltas)", m.firstTokN)
	}
}

func TestExitTiming_NoTurnStartNoTiming(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	// Existing tests apply MessageDelta / TurnEnd without a prior
	// TurnStart — must be harmless, not counted.
	m.applyAgentEvent(agent.MessageDelta{Delta: "orphan"})
	m.applyAgentEvent(agent.TurnEnd{})
	m.applyAgentEvent(agent.AgentEnd{})

	if m.firstTokN != 0 || m.llmTime != 0 {
		t.Errorf("orphan deltas counted: firstTokN=%d llmTime=%v, want 0/0", m.firstTokN, m.llmTime)
	}
}

func TestExitTiming_CancelMidStreamClosesSpan(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	// Esc mid-stream: the agent emits AgentEnd without TurnEnd for the
	// in-flight turn. The partial stream time must still count.
	m.applyAgentEvent(agent.TurnStart{})
	time.Sleep(2 * time.Millisecond)
	m.applyAgentEvent(agent.MessageDelta{Delta: "partial"})
	m.applyAgentEvent(agent.AgentEnd{})

	if m.llmTime <= 0 {
		t.Errorf("llmTime after cancel = %v, want > 0", m.llmTime)
	}
	if m.firstTokN != 1 {
		t.Errorf("firstTokN = %d, want 1", m.firstTokN)
	}
	if !m.turnStart.IsZero() {
		t.Error("AgentEnd must close the LLM span")
	}
}

func TestExitTiming_ErrorEventClosesSpan(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	// Non-retryable stream death: ErrorEvent without MessageEnd/AgentEnd.
	m.applyAgentEvent(agent.TurnStart{})
	time.Sleep(2 * time.Millisecond)
	m.applyAgentEvent(agent.ErrorEvent{Err: errors.New("stream died")})

	if m.llmTime <= 0 {
		t.Errorf("llmTime after ErrorEvent = %v, want > 0", m.llmTime)
	}
	if !m.turnStart.IsZero() {
		t.Error("ErrorEvent must close the LLM span")
	}
}

func TestExitTiming_ToolResultMessageEndKeepsSpanOpen(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	// Only the ASSISTANT MessageEnd closes the LLM span — a tool-result
	// MessageEnd (which fires after every tool) must not.
	m.applyAgentEvent(agent.TurnStart{})
	time.Sleep(2 * time.Millisecond)
	m.applyAgentEvent(agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleTool, ToolCallID: "c1"}})
	if m.turnStart.IsZero() {
		t.Fatal("tool-result MessageEnd must NOT close the LLM span")
	}
	m.applyAgentEvent(agent.TurnEnd{})

	if m.llmTime <= 0 {
		t.Errorf("llmTime = %v, want > 0 (span closed at TurnEnd)", m.llmTime)
	}
}

func TestExitTiming_ResetZeroesTiming(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()
	m.llmTime = time.Minute
	m.toolTime = time.Minute
	m.firstTokSum = time.Second
	m.firstTokN = 3
	m.completionTok = 99
	m.turnStart = time.Now()
	m.turnFirstTok = true

	m.resetSessionCounters()

	if m.llmTime != 0 || m.toolTime != 0 || m.firstTokSum != 0 || m.firstTokN != 0 ||
		m.completionTok != 0 || !m.turnStart.IsZero() || m.turnFirstTok {
		t.Errorf("resetSessionCounters left timing state: %+v", m)
	}
}

func TestRenderExitSummary_Format(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()
	m.turns = 12
	m.toolCalls = 277
	m.llmTime = 46*time.Minute + 53*time.Second
	m.toolTime = 28*time.Minute + 8*time.Second
	m.firstTokSum = 12 * (3*time.Second + 300*time.Millisecond)
	m.firstTokN = 12
	m.completionTok = 456000

	out := renderExitSummary(*m)
	for _, want := range []string{
		"12 turns", "277 tools",
		"llm 46m53s", "tools 28m8s",
		"first-token avg 3.3s",
		"162 tok/s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "resumed") {
		t.Errorf("fresh session must not be marked resumed:\n%s", out)
	}
}

func TestRenderExitSummary_ResumedMarker(t *testing.T) {
	t.Parallel()
	m := testModel().WithTurns(12).BuildPtr()
	m.resumed = true

	out := renderExitSummary(*m)
	if !strings.Contains(out, "12 turns") || !strings.Contains(out, "resumed") {
		t.Errorf("resumed session summary must carry the marker:\n%s", out)
	}
	// No this-run timing data → timing line omitted entirely.
	if strings.Contains(out, "llm ") || strings.Contains(out, "tok/s") {
		t.Errorf("resumed-then-quit must not show a zeroed timing line:\n%s", out)
	}
}

func TestRenderExitSummary_IdleEmpty(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()

	if out := renderExitSummary(*m); out != "" {
		t.Errorf("idle session summary = %q, want empty", out)
	}
}

func TestRenderExitSummary_TrackerSegments(t *testing.T) {
	t.Parallel()
	m := testModel().BuildPtr()
	m.turns = 1
	m.opts.Tracker.Record(deepseek.Usage{
		PromptTokens:          1200000,
		PromptCacheHitTokens:  960000,
		PromptCacheMissTokens: 240000,
		CompletionTokens:      456000,
		TotalTokens:           1656000,
	}, "deepseek-v4-flash", pricing.CurrentTier(time.Now()))

	out := renderExitSummary(*m)
	// formatTokensK renders k-style ("1200.0k"), never M — same helper
	// the per-turn footer uses.
	for _, want := range []string{"↑1200.0k prompt", "80.0% cache", "↓456.0k", "total 1656.0k", "$"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestCmdNew_ClearsResumed(t *testing.T) {
	t.Parallel()

	// /clear replaces the session AND the tracker — totals no longer
	// include prior runs, so the exit summary must not claim "resumed".
	m := testModel().WithCustomState(func(m *Model) {
		m.resumed = true
		m.opts.RebuildAgent = func() (*agent.Agent, error) { return nil, nil }
	}).BuildPtr()

	cmdNew(m, "")

	if m.resumed {
		t.Error("resumed after /clear = true, want false (fresh session + fresh tracker)")
	}
}

func TestResumeReconstruction_SetsResumed(t *testing.T) {
	t.Parallel()
	sess := &session.Session{Messages: []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "q"},
		{Role: deepseek.RoleAssistant, Content: "a"},
		{Role: deepseek.RoleAssistant, Content: "a2"},
	}}
	m := testModel().WithSession(sess).Build()
	if !m.resumed {
		t.Error("session with assistant history must set resumed=true")
	}

	fresh := testModel().Build()
	if fresh.resumed {
		t.Error("fresh session must leave resumed=false")
	}
}
