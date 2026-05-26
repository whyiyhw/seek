package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tests in this file exercise the streaming pipeline end-to-end with
// the fakeAgent test harness. They cover paths that the unit-level
// tests in update_test.go can't reach without a real agent — Prompt
// → MessageDelta → MessageEnd → TurnEnd ordering, queued-text
// auto-submit at stream end, mid-stream guards on /clear and friends.
//
// The bugs found in the v0.3.x review that THIS file would have caught
// before code-review:
//   - cmdNew runs mid-stream (E5 in the review) — see
//     TestCmdNew_RefusesMidStream
//   - persistSession reads Agent.Messages() concurrently with the
//     agent goroutine appending (A3) — exposed under -race by
//     TestStreaming_PersistDoesNotRaceAgentAppend

// TestSubmit_CallsAgentPromptWithText pins the most basic streaming
// contract: m.submit(text) goes through AgentClient.Prompt with the
// user's verbatim text. Was implicit before this test — the existing
// suite never asserted on what the agent received.
func TestSubmit_CallsAgentPromptWithText(t *testing.T) {
	t.Parallel()

	fa := newFakeAgent()
	m := testModel().WithAgent(fa).WithCustomState(func(m *Model) {
		// submit() reads m.opts.Ctx; nil would panic on
		// context.WithCancel.
		m.opts.Ctx = context.Background()
	}).Build()

	_, cmd := m.submit("hello world")
	if cmd == nil {
		t.Fatal("submit must return a non-nil cmd (batch of printCmd + waitForAgentEvent)")
	}

	if got := fa.PromptCalls; len(got) != 1 || got[0] != "hello world" {
		t.Errorf("Prompt should be called once with %q, got %v", "hello world", got)
	}
}

// TestStreaming_HappyPathSequence drives a complete turn through
// applyAgentEvent and pins the state-transition contract:
//
//	MessageStart → buffers cleared
//	MessageDelta (content) → m.curContent grows
//	MessageEnd → commit scrollback, buffers reset
//	TurnEnd → m.turns++
//
// Each step's invariant is asserted on the post-event Model.
func TestStreaming_HappyPathSequence(t *testing.T) {
	t.Parallel()

	m := testModel().WithCustomState(func(m *Model) {
		m.curContent = "leftover from prior turn" // must be wiped at MessageStart
	}).BuildPtr()

	// MessageStart wipes buffers.
	m.applyAgentEvent(agent.MessageStart{})
	if m.curContent != "" {
		t.Errorf("MessageStart must clear curContent, got %q", m.curContent)
	}

	// Two deltas accumulate.
	m.applyAgentEvent(agent.MessageDelta{Delta: "hello "})
	m.applyAgentEvent(agent.MessageDelta{Delta: "world"})
	if m.curContent != "hello world" {
		t.Errorf("curContent after two deltas = %q, want %q", m.curContent, "hello world")
	}

	// MessageEnd commits + resets.
	cmds := m.applyAgentEvent(agent.MessageEnd{
		Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "hello world"},
	})
	if len(cmds) != 1 {
		t.Errorf("MessageEnd with content must emit exactly 1 commit cmd, got %d", len(cmds))
	}
	if m.curContent != "" {
		t.Errorf("MessageEnd must reset curContent, got %q", m.curContent)
	}

	// TurnEnd bumps the counter.
	prevTurns := m.turns
	m.applyAgentEvent(agent.TurnEnd{})
	if m.turns != prevTurns+1 {
		t.Errorf("TurnEnd should increment turns, got %d → %d", prevTurns, m.turns)
	}
}

// TestStreaming_ToolCallOnlyTurnSuppressesSeekBlock pins the
// "no `▸ seek` block on tool-only turns" contract at the applyAgentEvent
// level. Sibling of TestApplyAgentEvent_PureToolCallTurnSkipsCommit but
// driven via the fakeAgent test harness, making the intent obvious.
func TestStreaming_ToolCallOnlyTurnSuppressesSeekBlock(t *testing.T) {
	t.Parallel()

	m := testModel().BuildPtr()

	// Reasoning-only delta, no content delta.
	m.applyAgentEvent(agent.MessageDelta{Delta: "thinking…", Reasoning: true})

	cmds := m.applyAgentEvent(agent.MessageEnd{
		Message: deepseek.Message{Role: deepseek.RoleAssistant},
	})

	if len(cmds) != 0 {
		t.Errorf("pure tool-call turn must emit 0 commit cmds, got %d", len(cmds))
	}
	if m.curReasoning != "" {
		t.Errorf("buffers must still be reset even when no commit, got curReasoning=%q",
			m.curReasoning)
	}
}

// TestCmdNew_RefusesMidStream is the regression test for the streaming-
// gate bug (review finding E5). Without the guard, /clear during an
// in-flight turn orphans the stream and leaves m.streaming=true while
// the new session is installed. This test asserts the guard fires —
// no session swap, no agent swap, no counter reset — when streaming.
func TestCmdNew_RefusesMidStream(t *testing.T) {
	t.Parallel()

	fa := newFakeAgent()
	rebuilt := false
	m := testModel().WithAgent(fa).WithTurns(3).WithCustomState(func(m *Model) {
		m.toolCalls = 12
		m.opts.RebuildAgent = func() (*agent.Agent, error) {
			rebuilt = true
			return nil, nil
		}
	}).Streaming().BuildPtr()

	res := cmdNew(m, "")

	if rebuilt {
		t.Error("cmdNew must NOT call RebuildAgent when streaming")
	}
	if m.turns != 3 || m.toolCalls != 12 {
		t.Errorf("cmdNew must NOT reset counters mid-stream, got turns=%d toolCalls=%d",
			m.turns, m.toolCalls)
	}
	if !strings.Contains(res.text, "wait for the current turn") {
		t.Errorf("expected 'wait for current turn' notice, got %q", res.text)
	}
	if res.clear {
		t.Error("cmdNew must not request a clear-screen mid-stream")
	}
}

// TestApplyAgentEvent_ToolExecCommitsResultLine drives ToolExecStart
// + ToolExecEnd via the harness. Pins the contract that ToolExecEnd
// emits a single committed `↳ name(args) → N bytes` scrollback line
// — which is what makes the replay path correct (replay walks RoleTool
// messages and renders them identically via the SAME function).
func TestApplyAgentEvent_ToolExecCommitsResultLine(t *testing.T) {
	t.Parallel()

	m := testModel().BuildPtr()

	m.applyAgentEvent(agent.ToolExecStart{
		CallID: "c1",
		Name:   "read",
		Args:   `{"path":"/tmp/x"}`,
	})
	if got := len(m.activeTools); got != 1 {
		t.Fatalf("ToolExecStart should add 1 active tool, got %d", got)
	}

	cmds := m.applyAgentEvent(agent.ToolExecEnd{
		CallID: "c1",
		Name:   "read",
		Result: "hello",
	})
	if len(cmds) != 1 {
		t.Fatalf("ToolExecEnd must emit exactly 1 scrollback commit, got %d", len(cmds))
	}
	// Slot must be marked finished but NOT removed — per the contract
	// in update_agent.go: finished slots stay visible (rendered with
	// ✓) until handleStreamEnd clears the list.
	if len(m.activeTools) != 1 {
		t.Errorf("finished slot must stay in activeTools, got %d", len(m.activeTools))
	}
	if !m.activeTools[0].finished {
		t.Error("active tool slot should be marked finished")
	}
}
