package tui

// Mid-turn injection, TUI half (pkg/agent/inject.go + the
// midTurnInjector seam). What these pin:
//
//  1. Enter / Alt+Enter during a stream try the agent's boundary queue
//     FIRST, and only fall back to queuedText / steerStream when the
//     agent says no. The fallback must stay byte-identical to the
//     pre-feature behaviour — that is the compatibility promise for
//     every fake agent and every lane that hasn't opted in.
//  2. A rejected injection (caps, no round in flight) does not lose the
//     user's text: it lands in queuedText, exactly as before.
//  3. Esc clears injectedPreview — "Esc stops everything" includes the
//     UI echo of a queued injection.
//  4. agent.InjectedMessage clears the preview and commits the block to
//     scrollback, so the user can see where their text went in.
//  5. renderQueueHint distinguishes injected from queued/steering.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// injectableAgent is fakeAgent + the optional midTurnInjector seam.
// accept controls what QueueUserMessage reports so both branches of the
// TUI's fallback are reachable without wiring caps.
type injectableAgent struct {
	*fakeAgent
	accept bool
	calls  []string
}

func newInjectableAgent(accept bool) *injectableAgent {
	return &injectableAgent{fakeAgent: newFakeAgent(), accept: accept}
}

func (a *injectableAgent) QueueUserMessage(text string) bool {
	a.calls = append(a.calls, text)
	return a.accept
}

// Compile-time: the seam is satisfied structurally, and the base still
// satisfies the main contract.
var (
	_ midTurnInjector = (*injectableAgent)(nil)
	_ AgentClient     = (*injectableAgent)(nil)
)

// TestStreamingEnter_InjectsWhenAgentAccepts: Enter during a stream with
// an injectable agent hands the text to the agent and does NOT park it in
// queuedText (which would re-send it at stream end).
func TestStreamingEnter_InjectsWhenAgentAccepts(t *testing.T) {
	t.Parallel()

	fa := newInjectableAgent(true)
	m := testModel().WithAgent(fa).Streaming().Build()
	m.input.SetValue("  use the other file  ")

	out, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)

	if len(fa.calls) != 1 || fa.calls[0] != "use the other file" {
		t.Fatalf("QueueUserMessage calls = %v, want one trimmed entry", fa.calls)
	}
	if got.queuedText != "" {
		t.Errorf("queuedText = %q, want empty (the text is already accepted)", got.queuedText)
	}
	if got.injectedPreview != "use the other file" {
		t.Errorf("injectedPreview = %q, want the accepted text", got.injectedPreview)
	}
	if got.input.Value() != "" {
		t.Errorf("textarea should be reset after injection, got %q", got.input.Value())
	}
	if cmd == nil {
		t.Error("injection should commit a scrollback line")
	}
}

// TestStreamingEnter_FallsBackWhenAgentRejects: a cap (or no round in
// flight) must degrade to the pre-feature queue, not lose the text.
func TestStreamingEnter_FallsBackWhenAgentRejects(t *testing.T) {
	t.Parallel()

	fa := newInjectableAgent(false)
	m := testModel().WithAgent(fa).Streaming().Build()
	m.input.SetValue("follow-up")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)

	if len(fa.calls) != 1 {
		t.Fatalf("QueueUserMessage should have been attempted once, calls = %v", fa.calls)
	}
	if got.queuedText != "follow-up" {
		t.Errorf("queuedText = %q, want the fallback to hold the text", got.queuedText)
	}
	if got.injectedPreview != "" {
		t.Errorf("injectedPreview = %q, want empty on the fallback path", got.injectedPreview)
	}
}

// TestStreamingEnter_FallsBackWhenAgentHasNoInjection: the plain fake
// agent (and every lane that never opted in) keeps the old behaviour
// verbatim.
func TestStreamingEnter_FallsBackWhenAgentHasNoInjection(t *testing.T) {
	t.Parallel()

	fa := newFakeAgent() // no midTurnInjector
	m := testModel().WithAgent(fa).Streaming().Build()
	m.input.SetValue("follow-up")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)

	if got.queuedText != "follow-up" {
		t.Errorf("queuedText = %q, want the pre-feature queue path", got.queuedText)
	}
	if got.injectedPreview != "" {
		t.Errorf("injectedPreview = %q, want empty", got.injectedPreview)
	}
}

// TestStreamingAltEnter_InjectsWithoutCancelling: the whole point of the
// feature — an interrupt that no longer interrupts. cancelStream must
// not fire.
func TestStreamingAltEnter_InjectsWithoutCancelling(t *testing.T) {
	t.Parallel()

	fa := newInjectableAgent(true)
	m := testModel().WithAgent(fa).Streaming().Build()
	m.input.SetValue("no wait, undo that")
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	got := out.(Model)

	if canceled {
		t.Error("injection must not cancel the in-flight round")
	}
	if got.pendingSteerText != "" {
		t.Errorf("pendingSteerText = %q, want empty (no steer needed)", got.pendingSteerText)
	}
	if got.injectedPreview != "no wait, undo that" {
		t.Errorf("injectedPreview = %q, want the accepted text", got.injectedPreview)
	}
	if len(fa.calls) != 1 {
		t.Errorf("QueueUserMessage calls = %v, want exactly one", fa.calls)
	}
}

// TestStreamingAltEnter_SteersWhenAgentRejects: the fallback keeps the
// expensive meaning available when injection isn't possible.
func TestStreamingAltEnter_SteersWhenAgentRejects(t *testing.T) {
	t.Parallel()

	fa := newInjectableAgent(false)
	m := testModel().WithAgent(fa).Streaming().Build()
	m.input.SetValue("stop, do X")
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	got := out.(Model)

	if !canceled {
		t.Error("steer fallback must cancel the in-flight round")
	}
	if got.pendingSteerText != "stop, do X" {
		t.Errorf("pendingSteerText = %q, want the steer fallback", got.pendingSteerText)
	}
}

// TestStreamingEsc_ClearsInjectedPreview: Esc stops everything, and the
// injected echo is part of "everything" (the agent clears its own queue
// per round; this is the UI half).
func TestStreamingEsc_ClearsInjectedPreview(t *testing.T) {
	t.Parallel()

	m := testModel().Streaming().Build()
	m.injectedPreview = "queued for the boundary"
	m.cancelStream = func() {}

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	got := out.(Model)

	if got.injectedPreview != "" {
		t.Errorf("injectedPreview = %q, want cleared on Esc", got.injectedPreview)
	}
}

// TestInjectedMessageEvent_CommitsAndClears: the agent's confirmation is
// what turns the hint into transcript. Both halves matter — a stale hint
// would keep claiming text is pending after it landed.
func TestInjectedMessageEvent_CommitsAndClears(t *testing.T) {
	t.Parallel()

	m := testModel().Streaming().Build()
	m.injectedPreview = "use the other file"

	cmds := m.applyAgentEvent(agent.InjectedMessage{
		Index:   1,
		Message: deepseek.Message{Role: deepseek.RoleUser, Content: "use the other file"},
	})

	if m.injectedPreview != "" {
		t.Errorf("injectedPreview = %q, want cleared once committed", m.injectedPreview)
	}
	if len(cmds) != 2 {
		t.Fatalf("cmds = %d, want 2 (user block + boundary note)", len(cmds))
	}
}

// TestRenderQueueHint_InjectedState: three states, three different words.
// The hint is the only place the user can tell "queued for round end"
// from "already accepted for the next boundary".
func TestRenderQueueHint_InjectedState(t *testing.T) {
	t.Parallel()

	m := testModel().Streaming().Build()
	m.injectedPreview = "use the other file"
	got := m.renderQueueHint()
	if got == "" {
		t.Fatal("injected state must render a hint")
	}
	queued := (&Model{opts: m.opts}).renderQueueHint()
	if queued != "" {
		t.Errorf("empty state rendered %q", queued)
	}

	// Precedence: a held queue/steer describes text the user still owns,
	// so it outranks the accepted-injection echo.
	m.queuedText = "later"
	if hint := m.renderQueueHint(); !strings.Contains(hint, "later") {
		t.Errorf("queued text should outrank the injected echo, got %q", hint)
	}
	m.queuedText = ""
	m.pendingSteerText = "urgent"
	if hint := m.renderQueueHint(); !strings.Contains(hint, "urgent") {
		t.Errorf("pending steer should outrank the injected echo, got %q", hint)
	}
}

// TestSubmitOrSteer_InjectsWhenStreaming covers the slash-command path
// (/skill use, /review …) which reaches the same seam through
// submitOrSteer rather than handleKey.
func TestSubmitOrSteer_InjectsWhenStreaming(t *testing.T) {
	t.Parallel()

	fa := newInjectableAgent(true)
	m := testModel().WithAgent(fa).Streaming().Build()

	res := submitOrSteer(&m, "please use dual-model")
	if len(fa.calls) != 1 || fa.calls[0] != "please use dual-model" {
		t.Fatalf("QueueUserMessage calls = %v, want the prompt", fa.calls)
	}
	if m.pendingSteerText != "" {
		t.Errorf("pendingSteerText = %q, want empty (injected, not steered)", m.pendingSteerText)
	}
	if res.extra == nil {
		t.Error("injection should surface a scrollback line through cmdResult.extra")
	}
}

// TestSubmitOrSteer_SteersWhenAgentRejects is the mirror fallback.
func TestSubmitOrSteer_SteersWhenAgentRejects(t *testing.T) {
	t.Parallel()

	fa := newInjectableAgent(false)
	m := testModel().WithAgent(fa).Streaming().Build()
	m.cancelStream = func() {}

	submitOrSteer(&m, "please use dual-model")
	if m.pendingSteerText != "please use dual-model" {
		t.Errorf("pendingSteerText = %q, want the steer fallback", m.pendingSteerText)
	}
}

// TestMidTurnInject_NoAgentIsSafe: a Model without an agent (early
// wiring, tests) must report "can't", not panic.
func TestMidTurnInject_NoAgentIsSafe(t *testing.T) {
	t.Parallel()
	m := Model{}
	if m.tryMidTurnInject("x") {
		t.Error("no agent must report false")
	}
}

// TestInjectionThenStreamEnd_ClearsPreview: a round that ends before the
// boundary arrives drops the echo (the agent's endTurnSession already
// dropped the text).
func TestInjectionThenStreamEnd_ClearsPreview(t *testing.T) {
	t.Parallel()

	m := testModel().Streaming().Build()
	m.injectedPreview = "never landed"
	m.cancelStream = func() {}

	out, _ := m.handleStreamEnd(streamEndMsg{})
	got := out.(Model)

	if got.injectedPreview != "" {
		t.Errorf("injectedPreview = %q, want cleared at stream end", got.injectedPreview)
	}
}

// TestRealAgentSatisfiesMidTurnInjector guards the structural claim the
// production wiring relies on: *agent.Agent implements the optional
// interface, so no adapter is needed between cmd/seek and the TUI.
func TestRealAgentSatisfiesMidTurnInjector(t *testing.T) {
	t.Parallel()
	var _ midTurnInjector = (*agent.Agent)(nil)
}
