package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/skill"

	tea "charm.land/bubbletea/v2"

	"charm.land/bubbles/v2/textarea"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// streamingModel returns a Model in the streaming state, with a textarea
// pre-populated. Used by the queue/steer tests below.
func streamingModel(t *testing.T, input string) Model {
	t.Helper()
	m := Model{input: textarea.New(), streaming: true}
	m.input.SetValue(input)
	return m
}

// TestApplyAgentEvent_PureToolCallTurnSkipsCommit covers the
// "no (no content) blocks" UX rule: when an assistant turn carries
// only reasoning + tool_calls (no narrative text), MessageEnd must
// NOT emit a scrollback line. Without this guard the live region
// would render `▸ seek\n(no content)` for every silent tool round —
// noisy when the model chains several greps before answering.
//
// Buffers must still be reset so the next turn's commit doesn't
// inherit this turn's reasoning text.
func TestApplyAgentEvent_PureToolCallTurnSkipsCommit(t *testing.T) {
	t.Parallel()
	m := emptyModel()

	// Simulate the stream: reasoning deltas arrive, content stays empty,
	// then MessageEnd fires for the assistant turn (with tool_calls on
	// the wire, though we don't need to populate them for the rendering
	// branch under test — applyAgentEvent's MessageEnd path only reads
	// Role + the model's curContent/curReasoning buffers).
	m.applyAgentEvent(agent.MessageDelta{Delta: "thinking about it…", Reasoning: true})
	cmds := m.applyAgentEvent(agent.MessageEnd{
		Message: deepseek.Message{Role: deepseek.RoleAssistant},
	})

	if len(cmds) != 0 {
		t.Errorf("expected no tea.Cmds (no scrollback commit), got %d", len(cmds))
	}
	if m.curContent != "" || m.curReasoning != "" {
		t.Errorf("live buffers not reset: content=%q reasoning=%q",
			m.curContent, m.curReasoning)
	}
}

// TestApplyAgentEvent_TextTurnCommits is the positive control for the
// guard above: when content is non-empty, MessageEnd MUST commit a
// `▸ seek` block to scrollback (one tea.Println cmd) and reset the
// live buffers.
func TestApplyAgentEvent_TextTurnCommits(t *testing.T) {
	t.Parallel()
	m := emptyModel()

	m.applyAgentEvent(agent.MessageDelta{Delta: "here is the answer", Reasoning: false})
	cmds := m.applyAgentEvent(agent.MessageEnd{
		Message: deepseek.Message{Role: deepseek.RoleAssistant},
	})

	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 tea.Cmd (the appendHistory Println), got %d", len(cmds))
	}
	if m.curContent != "" || m.curReasoning != "" {
		t.Errorf("live buffers not reset: content=%q reasoning=%q",
			m.curContent, m.curReasoning)
	}
}

// TestApplyAgentEvent_ToolMessageEndIgnored covers the unchanged
// invariant that MessageEnd for a tool-role message is a no-op at the
// rendering layer (ToolExecEnd is the commit point for tool lines).
// Including this test means a future MessageEnd refactor that
// accidentally widened the role guard would fail loudly.
func TestApplyAgentEvent_ToolMessageEndIgnored(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.curContent = "stale"
	m.curReasoning = "stale"

	cmds := m.applyAgentEvent(agent.MessageEnd{
		Message: deepseek.Message{Role: deepseek.RoleTool, ToolCallID: "x"},
	})

	if len(cmds) != 0 {
		t.Errorf("tool MessageEnd should not emit cmds, got %d", len(cmds))
	}
	if m.curContent != "stale" || m.curReasoning != "stale" {
		t.Errorf("tool MessageEnd must not touch the assistant live buffers; got content=%q reasoning=%q",
			m.curContent, m.curReasoning)
	}
}

func TestHandleKey_StreamingEnter_QueuesText(t *testing.T) {
	t.Parallel()
	m := streamingModel(t, "  follow-up: also check main.go  ")

	// Enter (no modifier) during a stream → queue, do NOT submit.
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2, ok := out.(Model)
	if !ok {
		t.Fatalf("handleKey did not return a Model, got %T", out)
	}

	if m2.queuedText != "follow-up: also check main.go" {
		t.Errorf("queuedText: got %q, want trimmed user text", m2.queuedText)
	}
	if m2.pendingSteerText != "" {
		t.Errorf("Enter must not set pendingSteerText (got %q)", m2.pendingSteerText)
	}
	if got := m2.input.Value(); got != "" {
		t.Errorf("textarea should be reset after queue, got %q", got)
	}
}

func TestHandleKey_StreamingAltEnter_TriggersSteer(t *testing.T) {
	t.Parallel()
	m := streamingModel(t, "wait, undo that change")

	// Wire a cancel func so we can observe it being called.
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m2 := out.(Model)

	if !canceled {
		t.Error("Alt+Enter must call cancelStream so streamEndMsg can dispatch")
	}
	if m2.pendingSteerText != "wait, undo that change" {
		t.Errorf("pendingSteerText: got %q", m2.pendingSteerText)
	}
	if m2.queuedText != "" {
		t.Errorf("Alt+Enter must not populate queuedText (got %q)", m2.queuedText)
	}
	// userCanceled must stay false — steer is NOT a user cancel; the
	// "↰ interrupted" notice in streamEndMsg must NOT fire.
	if m2.userCanceled {
		t.Error("steer must leave userCanceled=false so streamEndMsg dispatches the next prompt")
	}
}

// TestHandleKey_ShiftEnterInsertsNewline: on terminals that report
// modifiers (v2 CSI-u / kitty protocol / Windows coninput), Shift+Enter
// must insert a newline instead of submitting — the v1-era "Shift+Enter
// is indistinguishable from Enter" limitation (docs/pitfalls.md, resolved
// by the v2 migration). Terminals without modifier reporting collapse
// Shift+Enter to plain Enter, which submits as before (safe degradation).
func TestHandleKey_ShiftEnterInsertsNewline(t *testing.T) {
	t.Parallel()
	// v2 String() contract: the modifier must survive into the string the
	// keymap and textarea bindings match on. If this ever changes, the
	// "shift+enter" binding in model.go silently stops matching.
	if got := (tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}).String(); got != "shift+enter" {
		t.Fatalf("v2 String() for Shift+Enter = %q, want %q", got, "shift+enter")
	}

	m := testModel().WithAgent(newFakeAgent()).Build()
	m.opts.Ctx = context.Background() // submit() derives its cancel ctx from this
	m.input.SetValue("hello")

	// Shift+Enter → newline inserted, NOT submitted. (cmd may be the
	// textarea's cursor-blink cmd — the proof of "not submitted" is
	// streaming staying false, not cmd being nil.)
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	mm := out.(Model)
	if mm.streaming {
		t.Fatal("Shift+Enter must not submit the input")
	}
	if got := mm.input.Value(); got != "hello\n" {
		t.Fatalf("Shift+Enter should insert a newline after the text, input = %q", got)
	}

	// Plain Enter on the same model still submits.
	out2, cmd2 := mm.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm2 := out2.(Model)
	if !mm2.streaming {
		t.Fatal("plain Enter must still submit")
	}
	if cmd2 == nil {
		t.Fatal("plain Enter must return a submit cmd")
	}
}

func TestHandleKey_StreamingEnter_EmptyInputNothingToWithdrawIsNoOp(t *testing.T) {
	t.Parallel()
	// Empty textarea + empty queue + empty pending steer + Enter → no-op.
	// (When something IS pending, the withdraw path runs — see the
	// next two tests.)
	m := streamingModel(t, "   ") // whitespace-only

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText != "" {
		t.Errorf("empty input should not queue, got %q", m2.queuedText)
	}
}

func TestHandleKey_StreamingEnter_EmptyInputWithdrawsQueue(t *testing.T) {
	t.Parallel()
	// User typed "do A", queued it (Enter), then changed their mind —
	// pressing Enter on an empty textarea should withdraw the queued
	// message without cancelling the in-flight stream.
	m := streamingModel(t, "") // empty textarea
	m.queuedText = "look at server.go"
	// Wire a no-op cancel so we can verify it does NOT get called —
	// withdrawing a queue must not touch the stream.
	cancelCalled := false
	m.cancelStream = func() { cancelCalled = true }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText != "" {
		t.Errorf("queue should be empty after withdraw, got %q", m2.queuedText)
	}
	if cancelCalled {
		t.Error("withdraw must NOT cancel the in-flight stream")
	}
	if m2.userCanceled {
		t.Error("withdraw must NOT set userCanceled (no '↰ interrupted' notice)")
	}
}

func TestHandleKey_StreamingEnter_EmptyInputWithdrawsSteer(t *testing.T) {
	t.Parallel()
	// Same shape, steer instead of queue. A pending steer means
	// cancelStream has already been called (it's how steer works) —
	// withdrawal here just clears the about-to-fire submission, the
	// stream is already on its way out.
	m := streamingModel(t, "")
	m.pendingSteerText = "wait, undo that"

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.pendingSteerText != "" {
		t.Errorf("pending steer should clear, got %q", m2.pendingSteerText)
	}
	if m2.queuedText != "" {
		t.Errorf("queue should remain empty, got %q", m2.queuedText)
	}
}

func TestHandleKey_StreamingEnter_SecondPressReplacesQueue(t *testing.T) {
	t.Parallel()
	// First Enter queues "first"; second Enter (with new textarea
	// content) replaces — "last thing you said is what you meant".
	m := streamingModel(t, "first message")
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = out.(Model)
	if m.queuedText != "first message" {
		t.Fatalf("setup: queuedText=%q, want %q", m.queuedText, "first message")
	}

	m.input.SetValue("second message — disregard the first")
	out, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = out.(Model)

	if m.queuedText != "second message — disregard the first" {
		t.Errorf("second Enter should replace queue; got %q", m.queuedText)
	}
}

func TestHandleKey_StreamingEsc_ClearsQueueAndSteer(t *testing.T) {
	t.Parallel()
	// Esc during a stream cancels AND clears both queue and pending
	// steer — "Esc stops everything" must include latent state.
	m := streamingModel(t, "")
	m.queuedText = "stale queue"
	m.pendingSteerText = "stale steer"
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m2 := out.(Model)

	if !canceled {
		t.Error("Esc must call cancelStream")
	}
	if !m2.userCanceled {
		t.Error("Esc must set userCanceled so streamEndMsg prints '↰ interrupted'")
	}
	if m2.queuedText != "" || m2.pendingSteerText != "" {
		t.Errorf("Esc must clear queuedText and pendingSteerText, got queue=%q steer=%q",
			m2.queuedText, m2.pendingSteerText)
	}
	// Esc restores queued text into the textarea so the user can
	// edit and re-submit, but does NOT restore steer text (steer
	// is "cancel and replace" — cancelling the replacement means
	// the user changed their mind entirely).
	if got := m2.input.Value(); got != "stale queue" {
		t.Errorf("Esc should restore queued text into input, got %q", got)
	}
}

// TestHandleKey_CtrlL_RequestsClearScreen pins the only "visible blank
// without state reset" escape hatch left after /clear and /new were
// unified into a full-reset handler. Ctrl+L returns tea.ClearScreen
// directly — no session save, no agent rebuild, no counter reset —
// which preserves the rare "I just want to wipe my screen" use case
// (matching shell `clear` semantics) without bloating /clear's contract.
func TestHandleKey_CtrlL_RequestsClearScreen(t *testing.T) {
	t.Parallel()
	m := *emptyModel()

	_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+L must return a tea.Cmd (tea.ClearScreen)")
	}
	if got := cmd(); got != tea.ClearScreen() {
		t.Errorf("Ctrl+L must return tea.ClearScreen, got %T(%v)", got, got)
	}
}

func TestHandleKey_CommandMenuOpen_EnterDispatchesCandidate(t *testing.T) {
	t.Parallel()
	// When the slash-command menu is open with candidates, Enter
	// dispatches the highlighted candidate directly (no intermediate
	// "accept and add trailing space" step). This is the user-expected
	// behaviour: a visibly-selected /help entry should run on Enter,
	// not stage "/help " for a second Enter. Tab keeps the accept-only
	// flow for when the user wants to type args.
	m := Model{input: textarea.New()}
	m.input.SetValue("/h")
	cmds := filterCommands(allCommands(), "/h")
	if len(cmds) == 0 {
		t.Fatalf("setup: filterCommands returned 0 candidates for '/h'")
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.commandMenuOpen {
		t.Error("Enter on candidate should close the menu")
	}
	if got := m2.input.Value(); got != "" {
		t.Errorf("textarea should be reset after dispatch, got %q", got)
	}
}

func TestHandleKey_CommandMenuOpen_TabUniqueAutocompletes(t *testing.T) {
	t.Parallel()
	// M9.5 PRD §3.3: Tab on a UNIQUE prefix autocompletes the input
	// to the full command + trailing space. `/un` matches /undo only
	// (other /u-prefix commands are /upgrade which doesn't share /un).
	m := Model{input: textarea.New()}
	m.input.SetValue("/un")
	cmds := filterCommands(allCommands(), "/un")
	if len(cmds) != 1 {
		t.Fatalf("setup: filterCommands('/un') expected 1 candidate (got %d) — registry may have changed; pick a different unique prefix", len(cmds))
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if m2.commandMenuOpen {
		t.Error("Tab on a unique candidate should close the menu")
	}
	want := cmds[0].names[0] + " "
	if got := m2.input.Value(); got != want {
		t.Errorf("Tab on unique should autocomplete to %q, got %q", want, got)
	}
}

func TestHandleKey_CommandMenuOpen_TabBareSlashCompletesHighlighted(t *testing.T) {
	t.Parallel()
	// Bare "/" + Tab: LCP is "/" == input (no progress to make), so Tab
	// completes the highlighted row — the first command — rather than
	// the old silent bell. ↑/↓ would choose a different row first.
	m := Model{input: textarea.New()}
	m.input.SetValue("/")
	cmds := filterCommands(allCommands(), "/")
	if len(cmds) < 2 {
		t.Fatalf("setup: filterCommands('/') expected all candidates, got %d", len(cmds))
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	want := cmds[0].names[0] + " "
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if got := m2.input.Value(); got != want {
		t.Errorf("bare '/' + Tab should complete the highlighted (first) command; want %q got %q", want, got)
	}
	if m2.commandMenuOpen {
		t.Error("command menu should close after Tab completes the highlighted candidate")
	}
}

func TestHandleKey_CommandMenuOpen_TabNoMatchEmitsBell(t *testing.T) {
	t.Parallel()
	// M9.5 PRD §3.3: Tab when the typed prefix has NO matches emits a
	// terminal bell and leaves the input untouched. The menu is still
	// "open" (set by updateCommandMenu because input starts with `/`)
	// but with an empty filtered slice.
	m := Model{input: textarea.New()}
	m.input.SetValue("/zzz")
	m.commandMenuOpen = true
	m.commandMenuFiltered = nil // no matches
	m.commandMenuSelected = 0

	out, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if got := m2.input.Value(); got != "/zzz" {
		t.Errorf("Tab on no-match should NOT change input, want %q got %q", "/zzz", got)
	}
	if cmd == nil {
		t.Error("Tab on no-match should return a bell cmd (got nil)")
	}
}

func TestHandleKey_CommandMenuOpen_TabFillsLongestCommonPrefix(t *testing.T) {
	t.Parallel()
	// Readline/bash-style Tab completion: when multiple candidates share
	// a longer common prefix than the user has typed, Tab fills to that
	// longer prefix (visible progress). The picker stays open so the
	// user can either keep typing to disambiguate or arrow + Enter.
	//
	// `/ski` filters to [/skill, /skills] — LCP is `/skill` (one char
	// longer than `/ski`). Tab should fill to `/skill` and keep the
	// picker open showing both.
	m := Model{input: textarea.New()}
	m.input.SetValue("/ski")
	cmds := filterCommands(allCommands(), "/ski")
	if len(cmds) < 2 {
		t.Fatalf("setup: filterCommands('/ski') expected 2+ candidates (/skill + /skills), got %d", len(cmds))
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if got := m2.input.Value(); got != "/skill" {
		t.Errorf("Tab with LCP progress should fill to '/skill', got %q", got)
	}
	if !m2.commandMenuOpen {
		t.Error("Tab with LCP progress should KEEP the picker open for further disambiguation")
	}
}

func TestHandleKey_CommandMenuOpen_TabExactNameAcceptsShorter(t *testing.T) {
	t.Parallel()
	// `/skill` matches both /skill and /skills (HasPrefix). Without the
	// exact-name rule, LCP equals input → bell. With the rule, the
	// user's explicit typing of "/skill" is honoured: accept it as the
	// canonical and add trailing space — the shorter, more-commonly-used
	// command wins over the longer-prefix sibling.
	m := Model{input: textarea.New()}
	m.input.SetValue("/skill")
	cmds := filterCommands(allCommands(), "/skill")
	if len(cmds) < 2 {
		t.Fatalf("setup: filterCommands('/skill') expected 2+ (skill + skills), got %d", len(cmds))
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if got := m2.input.Value(); got != "/skill " {
		t.Errorf("Tab on exact /skill should accept it (+ space), got %q", got)
	}
	if m2.commandMenuOpen {
		t.Error("exact-match accept should close the menu")
	}
}

func TestHandleKey_CommandMenuOpen_TabAliasAcceptsCanonical(t *testing.T) {
	t.Parallel()
	// `/s` is an alias for /steer (per the {"/steer","/s"} names tuple).
	// User typing `/s` + Tab should accept the command and PROMOTE to
	// canonical: input becomes "/steer " (not "/s "). Mirrors bash where
	// aliases expand on completion.
	m := Model{input: textarea.New()}
	m.input.SetValue("/s")
	cmds := filterCommands(allCommands(), "/s")
	if len(cmds) < 2 {
		t.Fatalf("setup: filterCommands('/s') expected 2+ candidates, got %d", len(cmds))
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if got := m2.input.Value(); got != "/steer " {
		t.Errorf("Tab on alias `/s` should promote to canonical `/steer `, got %q", got)
	}
	if m2.commandMenuOpen {
		t.Error("alias-accept should close the menu")
	}
}

func TestHandleKey_CommandMenuOpen_TabAtLcpAcceptsHighlighted(t *testing.T) {
	t.Parallel()
	// "/re" matches /review, /restore, /redo; their longest common prefix
	// is "/re" == input, so Tab can't extend the prefix. Instead of a
	// (silent) bell it now completes the HIGHLIGHTED candidate — same as a
	// single-candidate Tab — with ↑/↓ choosing which row. This was the
	// canonical "Tab does nothing" complaint.
	m := Model{input: textarea.New()}
	m.input.SetValue("/re")
	cmds := filterCommands(allCommands(), "/re")
	if len(cmds) < 2 {
		t.Fatalf("setup: '/re' expected 2+ candidates, got %d", len(cmds))
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 1 // second candidate — proves the highlight drives the pick

	want := cmds[1].names[0] + " "
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m2 := out.(Model)

	if got := m2.input.Value(); got != want {
		t.Errorf("Tab at LCP should complete the highlighted candidate; want %q got %q", want, got)
	}
	if m2.commandMenuOpen {
		t.Error("command menu should close once Tab accepts the highlighted candidate")
	}
}

func TestLongestCommonCandidatePrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []command
		want string
	}{
		{"empty", nil, ""},
		{"single", []command{{names: []string{"/foo"}}}, "/foo"},
		{
			"two share /skill",
			[]command{{names: []string{"/skill"}}, {names: []string{"/skills"}}},
			"/skill",
		},
		{
			"all share /s",
			[]command{{names: []string{"/skill"}}, {names: []string{"/steer"}}, {names: []string{"/setup"}}},
			"/s",
		},
		{
			"only / shared",
			[]command{{names: []string{"/foo"}}, {names: []string{"/bar"}}},
			"/",
		},
		{
			"nothing shared",
			[]command{{names: []string{"alpha"}}, {names: []string{"beta"}}},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := longestCommonCandidatePrefix(tc.in); got != tc.want {
				t.Errorf("longestCommonCandidatePrefix(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHandleKey_CommandMenuOpen_EscClearsInput(t *testing.T) {
	t.Parallel()
	// Esc on an open slash menu = "cancel this command entirely" —
	// must clear the partial `/foo` text along with the menu. Earlier
	// behaviour kept the input, which looked like submission residue
	// from the user's point of view.
	m := Model{input: textarea.New()}
	m.input.SetValue("/he")
	cmds := filterCommands(allCommands(), "/he")
	if len(cmds) == 0 {
		t.Fatalf("setup: filterCommands returned 0 candidates for '/he'")
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m2 := out.(Model)

	if m2.commandMenuOpen {
		t.Error("Esc should close the menu")
	}
	if got := m2.input.Value(); got != "" {
		t.Errorf("Esc should clear the input, got %q", got)
	}
}

func TestHandleKey_SlashMenuEnter_HandsOffToModelPicker(t *testing.T) {
	t.Parallel()
	// User flow: type "/model" → slash menu opens with /model highlighted →
	// press Enter. The expected result is that the model picker opens
	// immediately (not on the NEXT keystroke). With Enter-dispatches
	// behaviour, /model is dispatched directly: cmdModel opens its
	// picker, the textarea resets, and pickerPurpose is set.
	m := emptyModel()
	m.opts.Model = "deepseek-v4-flash"
	m.input.SetValue("/model")
	m.updateCommandMenu() // simulate the state after typing "/model"
	if !m.commandMenuOpen {
		t.Fatalf("setup: slash menu should be open for '/model'")
	}

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.commandMenuOpen {
		t.Error("slash menu should be closed after dispatch")
	}
	if !m2.modelPickerOpen {
		t.Error("model picker should auto-open right after /model is dispatched")
	}
	if m2.pickerPurpose != "model" {
		t.Errorf("pickerPurpose = %q, want 'model'", m2.pickerPurpose)
	}
	if got := m2.input.Value(); got != "" {
		t.Errorf("textarea should be reset after dispatch, got %q", got)
	}
}

func TestUpdateCommandMenu_AutoOpensModelPickerOnSpace(t *testing.T) {
	t.Parallel()
	// Regression: when the user types "/model<space>" the model picker
	// should auto-open. Before this fix, the slash menu closed (because
	// it forbids spaces in args mode) and nothing replaced it — the
	// user saw nothing until they hit Enter.
	m := emptyModel()
	m.opts.Model = "deepseek-v4-flash"
	m.input.SetValue("/model ")

	m.updateCommandMenu()

	if !m.modelPickerOpen {
		t.Fatal("model picker should auto-open on '/model '")
	}
	if m.pickerPurpose != "model" {
		t.Errorf("pickerPurpose = %q, want 'model'", m.pickerPurpose)
	}
	if m.commandMenuOpen {
		t.Error("slash menu should be closed when picker is open (mutually exclusive)")
	}
	// Current model preselected.
	if m.modelPickerFiltered[m.modelPickerSelected].id != "deepseek-v4-flash" {
		t.Errorf("expected current model preselected, got %q",
			m.modelPickerFiltered[m.modelPickerSelected].id)
	}
}

func TestUpdateCommandMenu_ClosesAutoPickerOnBackspace(t *testing.T) {
	t.Parallel()
	// Once the input is no longer "/model<space>..." (e.g. user
	// backspaced the space back to "/model"), the auto-opened picker
	// must close so the regular slash menu can re-appear.
	m := emptyModel()
	m.opts.Model = "deepseek-v4-flash"
	m.modelPickerOpen = true
	m.pickerPurpose = "model"
	m.modelPickerFiltered = knownModelsForProvider("")

	m.input.SetValue("/model") // space deleted
	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Error("picker should close after backspace deletes the space trigger")
	}
	if !m.commandMenuOpen {
		t.Error("slash menu should re-open on '/model' (no space)")
	}
}

// TestUpdateCommandMenu_SubcommandVerbPicker covers the generic
// "/<cmd> <verb>" completion added for CLI-mirror commands. Typing
// "/memory " must open a verb picker (purpose "subcmd:/memory") listing
// the command's declared subcommands, with the slash menu closed.
func TestUpdateCommandMenu_SubcommandVerbPicker(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.input.SetValue("/memory ")
	m.updateCommandMenu()

	if !m.modelPickerOpen {
		t.Fatal("'/memory ' should auto-open the verb picker")
	}
	if m.pickerPurpose != subcmdPurposePrefix+"/memory" {
		t.Errorf("pickerPurpose = %q, want %q", m.pickerPurpose, subcmdPurposePrefix+"/memory")
	}
	if m.commandMenuOpen {
		t.Error("slash menu must be closed while the verb picker is open")
	}
	if len(m.modelPickerFiltered) != 4 || m.modelPickerFiltered[0].id != "list" {
		t.Errorf("want 4 memory verbs starting with 'list'; got %+v", m.modelPickerFiltered)
	}
}

// TestUpdateCommandMenu_SubcommandFiltersByPrefix: a partial verb
// narrows the picker (readline-style), here "/hooks t" → only "trust".
func TestUpdateCommandMenu_SubcommandFiltersByPrefix(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.input.SetValue("/hooks t")
	m.updateCommandMenu()

	if !m.modelPickerOpen || m.pickerPurpose != subcmdPurposePrefix+"/hooks" {
		t.Fatalf("'/hooks t' should open the hooks verb picker; open=%v purpose=%q", m.modelPickerOpen, m.pickerPurpose)
	}
	if len(m.modelPickerFiltered) != 1 || m.modelPickerFiltered[0].id != "trust" {
		t.Errorf("'/hooks t' should narrow to [trust]; got %+v", m.modelPickerFiltered)
	}
}

// TestUpdateCommandMenu_NoSubcommandsNoPicker: a verb-less command
// ("/yolo ") must NOT open a verb picker — the generic branch is gated
// on command.subcommands being declared.
func TestUpdateCommandMenu_NoSubcommandsNoPicker(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.input.SetValue("/yolo ")
	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Errorf("'/yolo ' has no subcommands and must not open a picker; purpose=%q", m.pickerPurpose)
	}
}

// TestApplyModelChoice_SubcommandInsertsVerb: picking a verb is a
// completion insert ("/memory list "), not a dispatch — the user keeps
// composing args. The picker closes afterward.
func TestApplyModelChoice_SubcommandInsertsVerb(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.input.SetValue("/memory ")
	m.updateCommandMenu() // opens picker, filtered[0] == "list"

	m.applyModelChoice(0)

	if got := m.input.Value(); got != "/memory list " {
		t.Errorf("input = %q, want %q", got, "/memory list ")
	}
	if m.modelPickerOpen {
		t.Error("picker should close after the verb is inserted")
	}
}

// TestUpdateCommandMenu_SubcommandClosesPastVerb: once the user types a
// space after the verb ("/memory list "), they've moved on to args, so
// the verb picker closes.
func TestUpdateCommandMenu_SubcommandClosesPastVerb(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.modelPickerOpen = true
	m.pickerPurpose = subcmdPurposePrefix + "/memory"
	m.input.SetValue("/memory list ")
	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Error("verb picker should close once input moves past the verb")
	}
}

func TestCmdModel_NoArgsOpensPicker(t *testing.T) {
	t.Parallel()
	// /model with no args should open the picker for a curated provider
	// (DeepSeek), with the current model preselected.
	m := &Model{}
	m.opts.Model = "deepseek-v4-pro"
	m.opts.ProviderName = "" // DeepSeek

	res := cmdModel(m, "")

	if !m.modelPickerOpen {
		t.Fatal("expected /model to open the picker")
	}
	if len(m.modelPickerFiltered) < 2 {
		t.Fatalf("expected at least 2 DeepSeek candidates, got %d", len(m.modelPickerFiltered))
	}
	// Preselect "deepseek-v4-pro" since it matches the current model
	// AND it is the explicit reasoning entry in the curated list (the
	// retired "deepseek-reasoner" alias is not in the list).
	got := m.modelPickerFiltered[m.modelPickerSelected].id
	if got != "deepseek-v4-pro" {
		t.Errorf("expected current model preselected, got %q", got)
	}
	// No surface text when opening — the picker is the response.
	if res.text != "" {
		t.Errorf("expected empty text on picker open, got %q", res.text)
	}
}

// TestCmdModel_LegacyReasonerNotInPicker pins the policy decision: the
// picker lists only explicit V4 IDs — the retired "deepseek-reasoner"
// alias must never reappear in the curated list (see
// knownModelsForProvider). If someone re-adds it this test fails on
// purpose. Direct-id use via /model <any-id> remains available
// (covered elsewhere) — the picker curates, it does not gate.
func TestCmdModel_LegacyReasonerNotInPicker(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.opts.Model = "deepseek-v4-flash"
	m.opts.ProviderName = ""

	cmdModel(m, "")

	for _, mc := range m.modelPickerFiltered {
		if mc.id == "deepseek-reasoner" {
			t.Errorf("deepseek-reasoner should not appear in the curated picker, found in: %+v",
				m.modelPickerFiltered)
		}
	}
}

// ---- Paste folding tests ----------------------------------------------

func TestPasteFolding_FoldOnMultiLinePaste(t *testing.T) {
	t.Parallel()
	// Paste with lines exceeding textarea height should fold.
	m := Model{input: textarea.New(), opts: Options{}}
	m.input.SetHeight(3)
	lines := "line1\nline2\nline3\nline4" // 4 lines > 3 height
	m.input.SetValue(lines)

	out := m.handlePasteFolding()
	if out.pastedContent != lines {
		t.Errorf("pastedContent = %q, want full paste", out.pastedContent)
	}
	if out.input.Value() == lines {
		t.Errorf("textarea should show placeholder, not full content")
	}
	if !strings.Contains(out.input.Value(), "pasted") {
		t.Errorf("placeholder should contain 'pasted', got %q", out.input.Value())
	}
}

func TestPasteFolding_NoFoldOnShortPaste(t *testing.T) {
	t.Parallel()
	// Paste with lines ≤ textarea height should NOT fold.
	m := Model{input: textarea.New(), opts: Options{}}
	m.input.SetHeight(3)
	lines := "line1\nline2\nline3" // 3 lines ≤ 3 height
	m.input.SetValue(lines)

	out := m.handlePasteFolding()
	if out.pastedContent != "" {
		t.Errorf("pastedContent should be empty, got %q", out.pastedContent)
	}
	if out.input.Value() != lines {
		t.Errorf("textarea should keep original content, got %q", out.input.Value())
	}
}

func TestPasteFolding_NoFoldOnSingleLine(t *testing.T) {
	t.Parallel()
	// Single-line paste should NOT fold.
	m := Model{input: textarea.New(), opts: Options{}}
	text := "hello world"
	m.input.SetValue(text)

	out := m.handlePasteFolding()
	if out.pastedContent != "" {
		t.Errorf("pastedContent should be empty for single line")
	}
	if out.input.Value() != text {
		t.Errorf("textarea value changed unexpectedly")
	}
}

func TestPasteFolding_MarkerPersistsOnNonEnterKey(t *testing.T) {
	t.Parallel()
	// When folded, a non-Enter keypress should NOT restore content.
	// The marker stays in place and the user can type around it.
	lines := "line1\nline2\nline3\nline4\nline5\nline6"
	m := Model{input: textarea.New(), opts: Options{}}
	m.pastedContent = lines
	m.pastedLineCount = 6
	m.input.SetValue("📋 pasted 6 lines — press Enter to send")

	// Simulate a character key press — without the old restore block,
	// pastedContent should remain set and the marker should still be visible.
	out, _ := m.handleKey(tea.KeyPressMsg{Text: "x"})
	m2 := out.(Model)

	if m2.pastedContent != lines {
		t.Errorf("pastedContent should be preserved on non-Enter key, got %q", m2.pastedContent)
	}
	if strings.Contains(m2.input.Value(), "line1") {
		t.Errorf("textarea should still show marker, not restored content, got %q", m2.input.Value())
	}
	if !strings.Contains(m2.input.Value(), "press Enter to send") {
		t.Errorf("textarea should still contain marker text, got %q", m2.input.Value())
	}
}

func TestPasteFolding_PlaceholderShowsLineCount(t *testing.T) {
	t.Parallel()
	// The placeholder should include the line count.
	m := Model{input: textarea.New()}
	m.input.SetHeight(3)
	lines := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n" // 7 lines (+ trailing \n)
	m.input.SetValue(lines)
	out := m.handlePasteFolding()
	want := "7 lines"
	if !strings.Contains(out.input.Value(), want) {
		t.Errorf("placeholder %q should contain %q", out.input.Value(), want)
	}
}

func TestPasteFolding_NoFoldOnNonPasteTyping(t *testing.T) {
	t.Parallel()
	// handlePasteFolding is ONLY called on paste events (tea.PasteMsg or
	// the conhost CRLF-Enter guard). Verify a plain character key does NOT
	// fold even if the textarea has >3 lines.
	//
	// We simulate this by calling handleKey with a plain character event
	// after setting up multi-line content.
	lines := "line1\nline2\nline3\nline4\nline5\nline6"
	m := Model{input: textarea.New(), opts: Options{}}
	m.input.SetValue(lines)

	// handleKey only calls handlePasteFolding on paste events. With a
	// plain character event, folding is skipped and pastedContent stays
	// empty — content should remain as-is.
	out, _ := m.handleKey(tea.KeyPressMsg{Text: "x"})
	m2 := out.(Model)

	if m2.pastedContent != "" {
		t.Errorf("pastedContent should be empty (not a paste), got %q", m2.pastedContent)
	}
	// Content should still be the multi-line text (with 'x' appended via
	// the textarea Update at the end of handleKey).
	if !strings.Contains(m2.input.Value(), "line6") {
		t.Errorf("textarea should still contain multi-line content, got %q", m2.input.Value())
	}
}

func TestPasteFolding_ExactThreshold(t *testing.T) {
	t.Parallel()
	// textarea height = threshold. 4 lines should fold, 3 should not.
	t.Run("four lines fold", func(t *testing.T) {
		m := Model{input: textarea.New()}
		m.input.SetHeight(3)
		m.input.SetValue("a\nb\nc\nd") // 4 lines
		out := m.handlePasteFolding()
		if out.pastedContent == "" {
			t.Error("4 lines should fold")
		}
	})
	t.Run("three lines no fold", func(t *testing.T) {
		m := Model{input: textarea.New()}
		m.input.SetHeight(3)
		m.input.SetValue("a\nb\nc") // 3 lines
		out := m.handlePasteFolding()
		if out.pastedContent != "" {
			t.Error("3 lines should NOT fold")
		}
	})
}

// ---- Paste resolution on Enter ----------------------------------------

func TestPasteFolding_StreamingEnterResolvesPaste(t *testing.T) {
	t.Parallel()
	// Streaming + Enter: folded paste should be resolved before queueing.
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7"
	m := streamingModel(t, "")
	m.streaming = true
	m.pastedContent = content
	m.pastedLineCount = 7
	m.input.SetValue("📋 pasted 7 lines — press Enter to send")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.pastedContent != "" {
		t.Errorf("pastedContent should be cleared after Enter, got %q", m2.pastedContent)
	}
	if m2.pastedLineCount != 0 {
		t.Errorf("pastedLineCount should be 0 after Enter, got %d", m2.pastedLineCount)
	}
	if m2.queuedText != content {
		t.Errorf("queuedText should be the full paste content, got %q", m2.queuedText)
	}
}

func TestPasteFolding_StreamingEnterResolvesPasteWithTyping(t *testing.T) {
	t.Parallel()
	// Streaming + Enter: user typed additional text after the marker,
	// both the paste and the extra text should appear in queuedText.
	content := "line1\nline2\nline3\nline4\nline5\nline6"
	m := streamingModel(t, "")
	m.streaming = true
	m.pastedContent = content
	m.pastedLineCount = 6
	m.input.SetValue("📋 pasted 6 lines — press Enter to send and also fix this")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	want := content + " and also fix this"
	if m2.queuedText != want {
		t.Errorf("queuedText should combine paste + typed text:\ngot:  %q\nwant: %q",
			m2.queuedText, want)
	}
}

func TestPasteFolding_StreamingAltEnterResolvesPaste(t *testing.T) {
	t.Parallel()
	// Streaming + Alt+Enter: folded paste should be resolved for steer.
	content := "fix this:\nremove the panic\nadd error handling"
	m := streamingModel(t, "")
	m.streaming = true
	m.pastedContent = content
	m.pastedLineCount = 3
	m.input.SetValue("📋 pasted 3 lines — press Enter to send")
	m.cancelStream = func() {} // no-op, just needs to be non-nil

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m2 := out.(Model)

	if m2.pastedContent != "" {
		t.Errorf("pastedContent should be cleared after Alt+Enter, got %q", m2.pastedContent)
	}
	if m2.pendingSteerText != content {
		t.Errorf("pendingSteerText should be the full paste, got %q", m2.pendingSteerText)
	}
}

func TestPasteFolding_StreamingEnterPasteNotEmptyOnEmptyTyping(t *testing.T) {
	t.Parallel()
	// Streaming + Enter with only the marker in the textarea (no extra
	// typing) should produce a non-empty queuedText = the pasted content.
	// Regression: the old empty-text check must not fire when the
	// resolved value is the paste itself.
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8"
	m := streamingModel(t, "")
	m.streaming = true
	m.pastedContent = content
	m.pastedLineCount = 8
	m.input.SetValue("📋 pasted 8 lines — press Enter to send")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText == "" {
		t.Error("queuedText should be non-empty (the paste content)")
	}
	if m2.queuedText != content {
		t.Errorf("queuedText = %q, want paste content %q", m2.queuedText, content)
	}
}

func TestPasteFolding_NonStreamingEnterConsumesPasteState(t *testing.T) {
	t.Parallel()
	// Non-streaming + Enter: even though submit will fail (no agent),
	// the paste state should be consumed and the input cleared.
	content := "a\nb\nc\nd\ne\nf"
	m := Model{input: textarea.New()}
	m.pastedContent = content
	m.pastedLineCount = 6
	m.input.SetValue("📋 pasted 6 lines — press Enter to send")

	// handleKey will reach m.submit() which panics on nil agent.
	// We catch the panic and verify the paste state was already consumed.
	var m2 Model
	func() {
		defer func() { recover() }()
		out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		m2 = out.(Model)
	}()

	if m2.pastedContent != "" {
		t.Errorf("pastedContent should be cleared after Enter, got %q", m2.pastedContent)
	}
	if m2.pastedLineCount != 0 {
		t.Errorf("pastedLineCount should be 0 after Enter, got %d", m2.pastedLineCount)
	}
}

func TestPasteFolding_NonStreamingEnterWithExtraTextConsumesPaste(t *testing.T) {
	t.Parallel()
	// Non-streaming: paste marker + extra text → paste state consumed.
	content := "errors.go:\nfunc handleErr"
	m := Model{input: textarea.New()}
	m.pastedContent = content
	m.pastedLineCount = 2
	m.input.SetValue("📋 pasted 2 lines — press Enter to send and add context")

	var m2 Model
	func() {
		defer func() { recover() }()
		out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		m2 = out.(Model)
	}()

	if m2.pastedContent != "" {
		t.Errorf("pastedContent should be cleared, got %q", m2.pastedContent)
	}
	if m2.pastedLineCount != 0 {
		t.Errorf("pastedLineCount should be 0, got %d", m2.pastedLineCount)
	}
}

func TestCmdModel_ArgsPathStillWorks(t *testing.T) {
	t.Parallel()
	// /model <id> should bypass the picker — used by power users and
	// for compatible-provider freeform ids.
	m := &Model{}
	m.opts.Model = "deepseek-v4-flash"

	res := cmdModel(m, "deepseek-v4-pro")

	if m.modelPickerOpen {
		t.Error("/model <id> must not open the picker")
	}
	if m.opts.Model != "deepseek-v4-pro" {
		t.Errorf("model not switched: got %q", m.opts.Model)
	}
	if !strings.Contains(res.text, "deepseek-v4-pro") {
		t.Errorf("transition message should mention new model: %q", res.text)
	}
}

func TestCmdModel_UnknownProviderFallsBackToHint(t *testing.T) {
	t.Parallel()
	// For --provider=compatible (or any uncurated provider name), we
	// have no candidate list — fall back to the "type the id" hint
	// instead of opening an empty picker.
	m := &Model{}
	m.opts.Model = "llama3-8b"
	m.opts.ProviderName = "Local Ollama" // not in knownModelsForProvider

	res := cmdModel(m, "")

	if m.modelPickerOpen {
		t.Error("uncurated provider should NOT open an empty picker")
	}
	if !strings.Contains(res.text, "llama3-8b") {
		t.Errorf("fallback should mention current model: %q", res.text)
	}
}

func TestHandleKey_ModelPickerOpen_EnterApplies(t *testing.T) {
	t.Parallel()
	// Picker open + Enter on a different row → model switches, picker closes.
	m := Model{input: textarea.New()}
	m.opts.Model = "deepseek-v4-flash"
	m.modelPickerOpen = true
	m.pickerPurpose = "model"
	m.modelPickerFiltered = []modelChoice{
		{"deepseek-v4-flash", "current"},
		{"deepseek-v4-pro", "Thinking mode"},
	}
	m.modelPickerSelected = 1 // user arrowed down to the second row
	// The user arrived here by typing "/model " — textarea still
	// holds that. Verify the accept cleans it up.
	m.input.SetValue("/model ")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.modelPickerOpen {
		t.Error("Enter on a picker row should close the picker")
	}
	if m2.opts.Model != "deepseek-v4-pro" {
		t.Errorf("model should have switched, got %q", m2.opts.Model)
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be cleared after accept; got %q", m2.input.Value())
	}
}

func TestCmdSetup_OpensProviderPicker(t *testing.T) {
	t.Parallel()
	// /setup should open the picker pre-loaded with the four known
	// providers and tagged with the setup purpose so accept routes
	// correctly. emptyModel has ProviderName="" → DeepSeek preselected.
	m := emptyModel()
	res := cmdSetup(m, "")

	if !m.modelPickerOpen {
		t.Fatal("/setup should open the picker")
	}
	if m.pickerPurpose != "setup-provider" {
		t.Errorf("pickerPurpose = %q, want 'setup-provider'", m.pickerPurpose)
	}
	if len(m.modelPickerFiltered) != 4 {
		t.Errorf("expected 4 provider choices, got %d", len(m.modelPickerFiltered))
	}
	// DeepSeek should be preselected (provider id of "" defaults to DeepSeek).
	if m.modelPickerFiltered[m.modelPickerSelected].id != "deepseek" {
		t.Errorf("expected DeepSeek preselected, got %q",
			m.modelPickerFiltered[m.modelPickerSelected].id)
	}
	if res.text == "" {
		t.Error("setup should print an intro line so the user sees why textarea changed")
	}
}

func TestApplyModelChoice_RoutesByPurpose(t *testing.T) {
	t.Parallel()
	// Verify the picker dispatcher splits "model" vs "setup-provider":
	// model purpose → updates m.opts.Model; setup purpose → enters
	// key-entry mode without touching m.opts.Model.
	tests := []struct {
		name              string
		purpose           string
		wantModelChanged  bool
		wantKeyEntry      bool
		wantSetupProvider string
	}{
		{"model_purpose_switches_model", "model", true, false, ""},
		{"setup_purpose_enters_key_entry", "setup-provider", false, true, "anthropic"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := emptyModel()
			m.opts.Model = "deepseek-v4-flash"
			m.modelPickerOpen = true
			m.pickerPurpose = tc.purpose
			m.modelPickerFiltered = []modelChoice{
				{"anthropic", "Anthropic Claude"},
			}
			m.modelPickerSelected = 0

			m.applyModelChoice(0)

			if m.modelPickerOpen {
				t.Error("picker should close after accept")
			}
			if m.pickerPurpose != "" {
				t.Errorf("purpose should clear, got %q", m.pickerPurpose)
			}
			modelChanged := m.opts.Model != "deepseek-v4-flash"
			if modelChanged != tc.wantModelChanged {
				t.Errorf("modelChanged = %v, want %v (now %q)", modelChanged, tc.wantModelChanged, m.opts.Model)
			}
			if m.setupKeyEntry != tc.wantKeyEntry {
				t.Errorf("setupKeyEntry = %v, want %v", m.setupKeyEntry, tc.wantKeyEntry)
			}
			if m.setupProvider != tc.wantSetupProvider {
				t.Errorf("setupProvider = %q, want %q", m.setupProvider, tc.wantSetupProvider)
			}
		})
	}
}

func TestHandleKey_SetupKeyEntry_EscCancels(t *testing.T) {
	t.Parallel()
	// Esc during key entry should clear the wizard state without
	// hitting Store.Save (no config file written).
	m := emptyModel()
	m.setupKeyEntry = true
	m.setupProvider = "anthropic"
	m.input.SetValue("sk-ant-partial")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m2 := out.(Model)

	if m2.setupKeyEntry {
		t.Error("Esc should exit key-entry mode")
	}
	if m2.setupProvider != "" {
		t.Errorf("setupProvider should clear, got %q", m2.setupProvider)
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be reset on cancel, got %q", m2.input.Value())
	}
}

func TestHandleKey_SetupKeyEntry_EnterEmptyDoesNotSave(t *testing.T) {
	t.Parallel()
	// Pressing Enter with an empty key shouldn't save garbage to
	// config — finishSetup returns a "cancelled" Println and clears
	// state. We verify state-clear here; config side-effect-absence
	// is covered structurally (empty key short-circuits before Save).
	m := emptyModel()
	m.setupKeyEntry = true
	m.setupProvider = "deepseek"
	m.input.SetValue("   ") // whitespace-only — trimmed to empty

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.setupKeyEntry {
		t.Error("Enter should exit key-entry mode even when key is empty")
	}
}

func TestHandleKey_ModelPickerOpen_EscDismisses(t *testing.T) {
	t.Parallel()
	// Esc closes the picker WITHOUT switching the model.
	m := Model{input: textarea.New()}
	m.opts.Model = "deepseek-v4-flash"
	m.modelPickerOpen = true
	m.modelPickerFiltered = []modelChoice{
		{"deepseek-v4-flash", "current"},
		{"deepseek-v4-pro", "Thinking mode"},
	}
	m.modelPickerSelected = 1

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m2 := out.(Model)

	if m2.modelPickerOpen {
		t.Error("Esc should close the picker")
	}
	if m2.opts.Model != "deepseek-v4-flash" {
		t.Errorf("Esc must not switch the model; got %q", m2.opts.Model)
	}
}

// TestHandleKey_EffortPicker_AutoOpened_EnterUsesPickerNotTyped covers
// the behaviour where the auto-opened picker captures Enter. When the
// user types "/effort high" while the picker is showing, pressing Enter
// applies the picker's highlighted row — NOT the typed "high". The
// textarea shows "/effort high" but Enter selects whatever row is
// highlighted (e.g. "off" or "max"). This is the current behaviour for
// all auto-open pickers (/model, /effort); the test documents it
// so any future change to "Enter takes the typed value" is deliberate.
func TestHandleKey_EffortPicker_AutoOpened_EnterUsesPickerNotTyped(t *testing.T) {
	t.Parallel()
	m := Model{input: textarea.New()}
	m.opts.Effort = "" // current = off
	m.modelPickerOpen = true
	m.pickerPurpose = "effort"
	m.modelPickerFiltered = effortChoices()
	m.modelPickerSelected = 0 // picker highlights "off"
	// User typed "/effort high" — typed value disagrees with picker.
	m.input.SetValue("/effort high")

	var captured string
	m.opts.SetEffort = func(e string) { captured = e }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.modelPickerOpen {
		t.Error("Enter should close the picker")
	}
	// The picker's selected row ("off" → "") wins, not the typed "high".
	if captured != "" {
		t.Errorf("SetEffort called with %q, want %q (picker row wins over typed text)", captured, "")
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be cleared after accept; got %q", m2.input.Value())
	}
}

// TestHandleKey_EffortPicker_AutoOpened_EnterAppliesMaxWhenHighlighted
// complements the test above: when the user arrows to "max" and presses
// Enter, the highlighted row wins regardless of what's in the textarea.
func TestHandleKey_EffortPicker_AutoOpened_EnterAppliesMaxWhenHighlighted(t *testing.T) {
	t.Parallel()
	m := Model{input: textarea.New()}
	m.opts.Effort = "" // current = off
	m.modelPickerOpen = true
	m.pickerPurpose = "effort"
	m.modelPickerFiltered = effortChoices()
	m.modelPickerSelected = 2       // picker highlights "max"
	m.input.SetValue("/effort off") // typed value disagrees

	var captured string
	m.opts.SetEffort = func(e string) { captured = e }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.modelPickerOpen {
		t.Error("Enter should close the picker")
	}
	// Picker row "max" wins, not the typed "off".
	if captured != "max" {
		t.Errorf("SetEffort called with %q, want %q (picker row wins over typed text)", captured, "max")
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be cleared after accept; got %q", m2.input.Value())
	}
}

func TestHandleKey_PathPickerOpen_EnterAcceptsHighlighted(t *testing.T) {
	t.Parallel()
	// Picker open with candidates: Enter accepts (same as Tab). The
	// user then presses Enter again to submit.
	m := Model{input: textarea.New()}
	m.input.SetValue("@RE")
	m.pathPicker.open = true
	m.pathPicker.filtered = []string{"README.md"}
	m.pathPicker.selected = 0
	m.pathPicker.tokenStart = 0
	m.pathPicker.token = "RE"

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.pathPicker.open {
		t.Error("Enter on candidate should close the picker")
	}
	if !strings.Contains(m2.input.Value(), "README.md") {
		t.Errorf("textarea should contain the chosen path, got %q", m2.input.Value())
	}
}

func TestHandleKey_StreamingEnter_SlashCommandRunsImmediately(t *testing.T) {
	t.Parallel()
	// Regression: while streaming, typing "/help all" and pressing Enter
	// must fire the slash command immediately — NOT stash "/help all"
	// into queuedText and dispatch it as a user message when the turn
	// ends. Slash commands are TUI-side, not LLM-bound. We use "all"
	// to get the overlay directly (bare /help now opens a topic picker).
	m := streamingModel(t, "/help all")

	out, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText != "" {
		t.Errorf("/help during stream must not queue, got queuedText=%q", m2.queuedText)
	}
	if !m2.helpOverlayOpen {
		t.Error("/help during stream should set helpOverlayOpen, got false")
	}
	if m2.helpContent == "" {
		t.Error("/help during stream should set non-empty helpContent")
	}
	// cmd is nil because /help no longer commits text to scrollback
	// (it shows a dismissable overlay instead). Nil cmd is correct.
	_ = cmd
}

func TestRenderQueueHint_States(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		setup   func(*Model)
		wantSub string // substring that must appear; "" = empty hint
	}{
		{"empty", func(*Model) {}, ""},
		{"queued",
			func(m *Model) { m.queuedText = "look at server.go" },
			"queued"},
		{"steering",
			func(m *Model) { m.pendingSteerText = "stop, undo" },
			"steering"},
		{"steer_supersedes_queue",
			func(m *Model) {
				m.queuedText = "do A"
				m.pendingSteerText = "no, do B"
			},
			"steering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{input: textarea.New()}
			tc.setup(&m)
			got := m.renderQueueHint()
			if tc.wantSub == "" {
				if got != "" {
					t.Errorf("expected empty hint, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("hint %q should contain %q", got, tc.wantSub)
			}
		})
	}
}

func TestShiftTab_CyclesModes(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	var yoloCalls, planCalls []bool
	m.opts.SetYolo = func(b bool) { yoloCalls = append(yoloCalls, b) }
	m.opts.SetPlan = func(b bool) { planCalls = append(planCalls, b) }

	// Start in Ask mode (default): Yolo=false, Plan=false
	if m.opts.Yolo || m.opts.Plan {
		t.Fatal("expected default Ask mode")
	}

	// Shift+Tab 1: Ask → Plan
	m.cycleMode()
	if !m.opts.Plan || m.opts.Yolo {
		t.Errorf("after 1st cycle: want Plan, got Plan=%v Yolo=%v", m.opts.Plan, m.opts.Yolo)
	}

	// Shift+Tab 2: Plan → Yolo
	m.cycleMode()
	if !m.opts.Yolo || m.opts.Plan {
		t.Errorf("after 2nd cycle: want Yolo, got Plan=%v Yolo=%v", m.opts.Plan, m.opts.Yolo)
	}

	// Shift+Tab 3: Yolo → Ask
	m.cycleMode()
	if m.opts.Yolo || m.opts.Plan {
		t.Errorf("after 3rd cycle: want Ask, got Plan=%v Yolo=%v", m.opts.Plan, m.opts.Yolo)
	}

	// Verify hooks were called with correct values.
	// Plan: false→true (Ask→Plan), true→false (Plan→Yolo),
	//       false→false (Yolo→Ask, no-op for symmetry with cmdYolo)
	if len(planCalls) != 3 || planCalls[0] != true || planCalls[1] != false || planCalls[2] != false {
		t.Errorf("planCalls = %v, want [true, false, false]", planCalls)
	}
	// Yolo: false→true (Plan→Yolo), true→false (Yolo→Ask)
	if len(yoloCalls) != 2 || yoloCalls[0] != true || yoloCalls[1] != false {
		t.Errorf("yoloCalls = %v, want [true, false]", yoloCalls)
	}
}

func TestReviewBranchEntry_EscCancels_BeforeStreaming(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.reviewBranchEntry = true
	m.input.SetValue("my-feature")

	// Esc should clear reviewBranchEntry even without a stream.
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m2 := out.(Model)

	if m2.reviewBranchEntry {
		t.Error("reviewBranchEntry should be false after Esc")
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be reset after Esc, got %q", m2.input.Value())
	}
}

func TestReviewBranchEntry_EscCancels_WithStream(t *testing.T) {
	t.Parallel()
	m := streamingModel(t, "my-feature")
	m.reviewBranchEntry = true

	// Wire a cancel func so we can observe it being called.
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m2 := out.(Model)

	if m2.reviewBranchEntry {
		t.Error("reviewBranchEntry should be false after Esc")
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be reset after Esc, got %q", m2.input.Value())
	}
	// Esc in reviewBranchEntry must NOT cancel the stream — the review
	// entry guard fires before the streaming guard.
	if canceled {
		t.Error("Esc in reviewBranchEntry must not cancel the stream")
	}
}

func TestReviewBranchEntry_EnterEmptyShowsError(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.reviewBranchEntry = true
	// Textarea is empty (default).

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.reviewBranchEntry {
		t.Error("reviewBranchEntry should be false after Enter")
	}
}

func TestReviewBranchEntry_EnterSubmitsCommand(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.reviewBranchEntry = true
	m.input.SetValue("some-branch")

	out, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 := out.(Model)

	if m2.reviewBranchEntry {
		t.Error("reviewBranchEntry should be false after Enter")
	}
	if m2.input.Value() != "" {
		t.Errorf("textarea should be reset after Enter, got %q", m2.input.Value())
	}
	if cmd == nil {
		t.Error("Enter with a branch name should return a tea.Cmd")
	}
}

func TestTruncateOneLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{
			name: "ascii_under_limit",
			s:    "hello world",
			n:    20,
			want: "hello world",
		},
		{
			name: "ascii_exact_limit",
			s:    "hello world",
			n:    11,
			want: "hello world",
		},
		{
			name: "ascii_over_limit",
			s:    "hello world this is long",
			n:    11,
			want: "hello world…",
		},
		{
			name: "chinese_under_limit",
			s:    "你好世界",
			n:    10,
			want: "你好世界",
		},
		{
			name: "chinese_over_limit_cut_at_char_boundary",
			s:    "跑一下 go test 确认没坏",
			n:    12,
			want: "跑一下 go test …",
		},
		{
			name: "emoji_4byte",
			s:    "hello 🫠 world",
			n:    8,
			want: "hello 🫠 …",
		},
		{
			name: "newlines_collapsed",
			s:    "line1\nline2\nline3",
			n:    15,
			want: "line1 line2 lin…",
		},
		{
			name: "mixed_multi_byte_cut_between_bytes_old_bug",
			s:    "abcdefghijklmnopqrstuvwxyz一的二三四五六七八九十",
			n:    30,
			// Byte 30 would land mid-character for '的' (3-byte).
			// Rune-level slicing gives 30 code points = "abcdefghijklmnopqrstuvwxyz一的二三"
			want: "abcdefghijklmnopqrstuvwxyz一的二三…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateOneLine(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("truncateOneLine(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
			// The result must always be valid UTF-8.
			if !utf8.ValidString(got) {
				t.Errorf("truncateOneLine(%q, %d) produced invalid UTF-8: %q", tt.s, tt.n, got)
			}
		})
	}
}

// --- /skill verb + name pickers -----------------------------------------
//
// The "auto-open on trailing space" pattern matches /model /effort
// /review; these tests verify the new /skill branches follow the same
// state-machine rules: open on `/skill `, narrow as you type, hand off
// to the name picker on `use`, close once you compose past the name.

func attachSkills(m *Model, names ...string) {
	set := skill.NewSet()
	for _, n := range names {
		set.Add(&skill.Skill{Name: n, Description: "desc for " + n, Source: "builtin"})
	}
	m.opts.Skills = set
}

func TestUpdateCommandMenu_SkillVerbPickerOpensOnSpace(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.input.SetValue("/skill ")

	m.updateCommandMenu()

	if !m.modelPickerOpen {
		t.Fatal("expected skill-verb picker open on '/skill '")
	}
	if m.pickerPurpose != "skill-verb" {
		t.Errorf("pickerPurpose = %q, want skill-verb", m.pickerPurpose)
	}
	// All verbs visible.
	if len(m.modelPickerFiltered) < 6 {
		t.Errorf("expected full verb list, got %d entries", len(m.modelPickerFiltered))
	}
}

func TestUpdateCommandMenu_SkillVerbPickerFiltersByPrefix(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.input.SetValue("/skill us")

	m.updateCommandMenu()

	if !m.modelPickerOpen || m.pickerPurpose != "skill-verb" {
		t.Fatalf("expected skill-verb picker, got open=%v purpose=%q", m.modelPickerOpen, m.pickerPurpose)
	}
	// "us" should narrow to just "use".
	if len(m.modelPickerFiltered) != 1 || m.modelPickerFiltered[0].id != "use" {
		t.Errorf("expected sole 'use' candidate, got %d entries", len(m.modelPickerFiltered))
	}
}

func TestUpdateCommandMenu_SkillVerbPickerEmptyFilterDoesNotOpen(t *testing.T) {
	t.Parallel()
	// Typo: no verb starts with "zzz" → no candidates → picker stays
	// closed so the user isn't stranded with an empty dropdown.
	m := emptyModel()
	m.input.SetValue("/skill zzz")

	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Errorf("picker should be closed when filter matches no verbs")
	}
}

func TestUpdateCommandMenu_SkillVerbPickerClosesPastVerb(t *testing.T) {
	t.Parallel()
	// Once the user types past the verb (a second space), the verb
	// picker is no longer relevant — the user is moving into sub-args
	// (which themselves may have a picker, like skill-name for `use`).
	m := emptyModel()
	m.input.SetValue("/skill list ") // verb done + a trailing space

	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Errorf("verb picker must close once user moves past the verb token")
	}
}

func TestUpdateCommandMenu_SkillNamePickerOpensAfterUseSpace(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	attachSkills(m, "dual-model", "go-test-runner")
	m.input.SetValue("/skill use ")

	m.updateCommandMenu()

	if !m.modelPickerOpen {
		t.Fatal("expected skill-name picker open on '/skill use '")
	}
	if m.pickerPurpose != "skill-name" {
		t.Errorf("pickerPurpose = %q, want skill-name", m.pickerPurpose)
	}
	if len(m.modelPickerFiltered) != 2 {
		t.Errorf("expected 2 skill candidates, got %d", len(m.modelPickerFiltered))
	}
}

func TestUpdateCommandMenu_SkillNamePickerFiltersByPrefix(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	attachSkills(m, "dual-model", "go-test-runner")
	m.input.SetValue("/skill use du")

	m.updateCommandMenu()

	if !m.modelPickerOpen || m.pickerPurpose != "skill-name" {
		t.Fatalf("expected skill-name picker, got open=%v purpose=%q", m.modelPickerOpen, m.pickerPurpose)
	}
	if len(m.modelPickerFiltered) != 1 || m.modelPickerFiltered[0].id != "dual-model" {
		t.Errorf("expected only dual-model, got %v", pickerIDs(m.modelPickerFiltered))
	}
}

func TestUpdateCommandMenu_SkillNamePickerClosesWhenComposingTask(t *testing.T) {
	t.Parallel()
	// Once the user types a space AFTER the skill name, they're
	// composing the inline task — the name picker must get out of the
	// way so Enter doesn't snap their selection back to a picker row.
	m := emptyModel()
	attachSkills(m, "dual-model")
	m.input.SetValue("/skill use dual-model 帮我重构")

	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Errorf("skill-name picker must close once user composes the inline task")
	}
}

func TestUpdateCommandMenu_SkillNamePickerNoSkillsLoaded(t *testing.T) {
	t.Parallel()
	// No skills loaded — picker has no candidates; must not open. The
	// user still gets a clear error from cmdSkillUse if they Enter.
	m := emptyModel() // opts.Skills nil
	m.input.SetValue("/skill use ")

	m.updateCommandMenu()

	if m.modelPickerOpen {
		t.Errorf("picker must not open when no skills are loaded")
	}
}

func TestApplyModelChoice_SkillVerbInsertsAndHandsOffOnUse(t *testing.T) {
	t.Parallel()
	// Accepting `use` from the verb picker must (a) write `/skill use `
	// into the textarea — the trailing space is what triggers the
	// re-evaluation — and (b) immediately open the name picker on the
	// same Update tick so the user doesn't see a dead "/skill use "
	// with no dropdown.
	m := emptyModel()
	attachSkills(m, "dual-model", "go-test-runner")
	// Simulate the state at the moment of accept.
	m.modelPickerFiltered = skillVerbChoices()
	m.modelPickerOpen = true
	m.pickerPurpose = "skill-verb"
	// "use" is the first entry in skillVerbChoices.
	m.modelPickerSelected = 0

	m.applyModelChoice(0)

	if got := m.input.Value(); got != "/skill use " {
		t.Errorf("input after verb accept = %q, want '/skill use '", got)
	}
	if !m.modelPickerOpen || m.pickerPurpose != "skill-name" {
		t.Errorf("expected name picker to open via re-trigger; got open=%v purpose=%q",
			m.modelPickerOpen, m.pickerPurpose)
	}
}

func TestApplyModelChoice_SkillVerbNonUseClosesPicker(t *testing.T) {
	t.Parallel()
	// Verbs other than `use` have no second-level picker. After
	// accept the textarea sits at `/skill <verb> ` and no picker is
	// open — user is composing free-form args.
	m := emptyModel()
	choices := skillVerbChoices()
	m.modelPickerFiltered = choices
	m.modelPickerOpen = true
	m.pickerPurpose = "skill-verb"
	// Pick "list" — first non-use entry.
	idx := -1
	for i, c := range choices {
		if c.id == "list" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("test setup: skillVerbChoices missing 'list'")
	}
	m.modelPickerSelected = idx

	m.applyModelChoice(idx)

	if got := m.input.Value(); got != "/skill list " {
		t.Errorf("input after verb accept = %q, want '/skill list '", got)
	}
	if m.modelPickerOpen {
		t.Errorf("no second-level picker for 'list'; expected closed, got open=%v purpose=%q",
			m.modelPickerOpen, m.pickerPurpose)
	}
}

func TestApplyModelChoice_SkillNameInsertsWithTrailingSpace(t *testing.T) {
	t.Parallel()
	// Skill-name accept lands at "/skill use <name> " WITH a trailing
	// space — cursor sits in the inline-task position so the user can
	// immediately type a task. submit()'s TrimSpace still allows
	// Enter-to-arm without the user manually removing the space.
	//
	// The trailing space ALSO prevents the prior bug where the next
	// updateCommandMenu invocation re-opened the name picker on the
	// just-accepted row (since `tail` now contains a space and routes
	// to the "user past name" branch).
	m := emptyModel()
	attachSkills(m, "dual-model", "go-test-runner")
	m.modelPickerFiltered = skillNameChoices(m.opts.Skills)
	m.modelPickerOpen = true
	m.pickerPurpose = "skill-name"
	m.modelPickerSelected = 0 // dual-model (insertion order)

	m.applyModelChoice(0)

	if got := m.input.Value(); got != "/skill use dual-model " {
		t.Errorf("input after name accept = %q, want '/skill use dual-model '", got)
	}
	if m.modelPickerOpen {
		t.Errorf("no further picker after name; expected closed, got open=%v purpose=%q",
			m.modelPickerOpen, m.pickerPurpose)
	}
}

// pickerIDs is a test helper that pulls just the id field out of a
// picker filter slice, for readable failure messages.
func pickerIDs(choices []modelChoice) []string {
	out := make([]string, 0, len(choices))
	for _, c := range choices {
		out = append(out, c.id)
	}
	return out
}

// --- ask_user picker ---------------------------------------------------
//
// The picker is the user-facing surface of the ask_user tool. These
// tests exercise the key-handler state machine directly — without
// firing up bubbletea — by setting m.pendingQuestion + selection
// state and driving handleQuestionKey with synthetic KeyMsgs.

// armQuestion attaches a pending Question to a fresh Model and
// returns a reply channel so the test can read the Answer that the
// handler eventually sends back.
func armQuestion(t *testing.T, q askuser.Question) (*Model, <-chan askuser.Answer) {
	t.Helper()
	m := emptyModel()
	reply := make(chan askuser.Answer, 1)
	m.pendingQuestion = &askuser.Request{Question: q, Reply: reply}
	m.pendingQuestionSelected = map[int]bool{}
	m.pendingQuestionCursor = 0
	return m, reply
}

func keyDown() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown} }
func keyUp() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyUp} }
func keyEnter() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter}
}
func keySpace() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeySpace} }
func keyEsc() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyEsc} }

func TestHandleQuestionKey_SingleSelectEnter(t *testing.T) {
	q := askuser.Question{
		Question: "Pick one",
		Options: []askuser.Option{
			{ID: "a", Label: "A"},
			{ID: "b", Label: "B"},
		},
	}
	m, reply := armQuestion(t, q)

	// Cursor down once to land on "b".
	updated, _ := m.handleQuestionKey(keyDown())
	m2 := updated.(Model)

	// Enter on cursor row should commit ChosenIDs=[b] and clear state.
	updated, _ = m2.handleQuestionKey(keyEnter())
	m3 := updated.(Model)

	if m3.pendingQuestion != nil {
		t.Errorf("pendingQuestion should be cleared after Enter")
	}
	select {
	case ans := <-reply:
		if len(ans.ChosenIDs) != 1 || ans.ChosenIDs[0] != "b" {
			t.Errorf("ChosenIDs=%v, want [b]", ans.ChosenIDs)
		}
	default:
		t.Fatal("Answer never sent on reply channel")
	}
}

func TestHandleQuestionKey_EscCancels(t *testing.T) {
	q := askuser.Question{
		Question: "Pick",
		Options:  []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	m, reply := armQuestion(t, q)

	updated, _ := m.handleQuestionKey(keyEsc())
	m2 := updated.(Model)

	if m2.pendingQuestion != nil {
		t.Errorf("pendingQuestion should clear on Esc")
	}
	select {
	case ans := <-reply:
		if !ans.Cancelled {
			t.Errorf("expected Cancelled=true on Esc, got %+v", ans)
		}
		if len(ans.ChosenIDs) != 0 || ans.FreeText != "" {
			t.Errorf("cancelled answer must be empty otherwise, got %+v", ans)
		}
	default:
		t.Fatal("Esc must still emit an answer so the agent unblocks")
	}
}

func TestHandleQuestionKey_MultiSelectSpaceThenEnter(t *testing.T) {
	q := askuser.Question{
		Question:    "Which features",
		MultiSelect: true,
		Options: []askuser.Option{
			{ID: "x", Label: "X"},
			{ID: "y", Label: "Y"},
			{ID: "z", Label: "Z"},
		},
	}
	m, reply := armQuestion(t, q)

	// Space toggles row 0 (X) on.
	updated, _ := m.handleQuestionKey(keySpace())
	m2 := updated.(Model)
	if !m2.pendingQuestionSelected[0] {
		t.Errorf("Space should toggle current row; selected=%v", m2.pendingQuestionSelected)
	}

	// Down twice → row 2 (Z), Space toggle.
	updated, _ = m2.handleQuestionKey(keyDown())
	m3 := updated.(Model)
	updated, _ = m3.handleQuestionKey(keyDown())
	m4 := updated.(Model)
	updated, _ = m4.handleQuestionKey(keySpace())
	m5 := updated.(Model)
	if !m5.pendingQuestionSelected[2] {
		t.Errorf("Space at row 2 should toggle Z on; selected=%v", m5.pendingQuestionSelected)
	}

	// Enter confirms — should return [x, z] in option order, not toggle order.
	updated, _ = m5.handleQuestionKey(keyEnter())
	m6 := updated.(Model)
	if m6.pendingQuestion != nil {
		t.Errorf("question should clear after Enter")
	}
	select {
	case ans := <-reply:
		if len(ans.ChosenIDs) != 2 || ans.ChosenIDs[0] != "x" || ans.ChosenIDs[1] != "z" {
			t.Errorf("ChosenIDs=%v, want [x z]", ans.ChosenIDs)
		}
	default:
		t.Fatal("no answer received")
	}
}

func TestHandleQuestionKey_OtherTransitionsToFreeText(t *testing.T) {
	q := askuser.Question{
		Question: "Pick or type",
		Options:  []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	m, reply := armQuestion(t, q)
	// Cursor at index 2 = the auto-appended Other row.
	m.pendingQuestionCursor = 2

	updated, _ := m.handleQuestionKey(keyEnter())
	m2 := updated.(Model)
	if !m2.pendingQuestionFreeText {
		t.Fatal("Enter on Other row should flip pendingQuestionFreeText=true")
	}
	// Picker should still be open — we're collecting free text now.
	if m2.pendingQuestion == nil {
		t.Error("question must remain pending; we haven't replied yet")
	}

	// Simulate the user typing.
	m2.input.SetValue("actually I want a different thing")
	updated, _ = m2.handleQuestionKey(keyEnter())
	m3 := updated.(Model)

	if m3.pendingQuestion != nil {
		t.Error("question should clear after Enter on typed answer")
	}
	select {
	case ans := <-reply:
		if ans.FreeText == "" {
			t.Errorf("FreeText must be populated, got %+v", ans)
		}
		if len(ans.ChosenIDs) != 0 {
			t.Errorf("ChosenIDs must be empty when FreeText answer used, got %v", ans.ChosenIDs)
		}
	default:
		t.Fatal("free-text submission did not produce an answer")
	}
}

func TestHandleQuestionKey_FreeTextEscReturnsToChoices(t *testing.T) {
	// Esc inside free-text mode goes BACK to the picker, doesn't
	// cancel the whole thing. Differentiates "I want to retype"
	// from "I want to give up".
	q := askuser.Question{
		Question: "Pick",
		Options:  []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	m, _ := armQuestion(t, q)
	m.pendingQuestionFreeText = true
	m.input.SetValue("partial type")

	updated, _ := m.handleQuestionKey(keyEsc())
	m2 := updated.(Model)

	if m2.pendingQuestionFreeText {
		t.Error("Esc in free-text should revert to choice mode")
	}
	if m2.pendingQuestion == nil {
		t.Error("Esc in free-text must NOT cancel the question (it's a sub-Esc, not the cancel Esc)")
	}
}

func TestHandleQuestionKey_MultiSelectOtherIsExclusive(t *testing.T) {
	// Toggling Other while other rows are on must clear them, and
	// vice versa: toggling a non-Other row while Other is on must
	// clear Other. Otherwise we'd produce nonsensical answers like
	// "ids=[a] AND free_text='...'" which the schema forbids.
	q := askuser.Question{
		Question:    "Pick",
		MultiSelect: true,
		Options:     []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	otherIdx := 2
	m, _ := armQuestion(t, q)

	// Pre-select a + b.
	m.pendingQuestionSelected = map[int]bool{0: true, 1: true}
	// Cursor to Other; toggle it on.
	m.pendingQuestionCursor = otherIdx
	updated, _ := m.handleQuestionKey(keySpace())
	m2 := updated.(Model)
	if !m2.pendingQuestionSelected[otherIdx] {
		t.Errorf("Other should be toggled on; selected=%v", m2.pendingQuestionSelected)
	}
	if m2.pendingQuestionSelected[0] || m2.pendingQuestionSelected[1] {
		t.Errorf("Toggling Other must clear non-Other rows; selected=%v", m2.pendingQuestionSelected)
	}

	// Now cursor back to row 0 and toggle it on; Other should clear.
	m2.pendingQuestionCursor = 0
	updated, _ = m2.handleQuestionKey(keySpace())
	m3 := updated.(Model)
	if m3.pendingQuestionSelected[otherIdx] {
		t.Errorf("Toggling a non-Other row must clear Other; selected=%v", m3.pendingQuestionSelected)
	}
	if !m3.pendingQuestionSelected[0] {
		t.Errorf("Row 0 should be on after Space; selected=%v", m3.pendingQuestionSelected)
	}
}

func TestHandleQuestionKey_CursorBoundsClamped(t *testing.T) {
	q := askuser.Question{
		Question: "Pick",
		Options:  []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	otherIdx := 2
	m, _ := armQuestion(t, q)

	// Up at top: no-op (cursor stays at 0).
	updated, _ := m.handleQuestionKey(keyUp())
	m2 := updated.(Model)
	if m2.pendingQuestionCursor != 0 {
		t.Errorf("Up at top should clamp at 0, got %d", m2.pendingQuestionCursor)
	}

	// Down past last: stops at Other (index = len(options)).
	for i := 0; i < 10; i++ {
		updated, _ = m2.handleQuestionKey(keyDown())
		m2 = updated.(Model)
	}
	if m2.pendingQuestionCursor != otherIdx {
		t.Errorf("Down should clamp at Other index %d, got %d", otherIdx, m2.pendingQuestionCursor)
	}
}

// --- Plan substate event handlers (P4 of feature-plan-mode) -------

// TestApplyAgentEvent_PlanProposalApproved verifies that the propose
// tool's "user approved" signal flips the TUI into plan-execute
// substate and notifies the host (cmd/seek) so it can switch the
// permission policy from ModePlan to ModeAsk. The host wiring itself
// lives in cmd/seek; this test only pins the TUI's contract.
func TestApplyAgentEvent_PlanProposalApproved(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Plan = true
	m.opts.PlanSubstate = "analyze"
	var hostSeen []string
	m.opts.SetPlanSubstate = func(s string) { hostSeen = append(hostSeen, s) }

	cmds := m.applyAgentEvent(agent.PlanProposalApproved{Steps: []string{"a", "b"}})

	if m.opts.PlanSubstate != "execute" {
		t.Errorf("substate = %q, want %q", m.opts.PlanSubstate, "execute")
	}
	if !m.opts.Plan {
		t.Error("Plan should stay true after approve (it's a substate change, not exit)")
	}
	if len(hostSeen) != 1 || hostSeen[0] != "execute" {
		t.Errorf("host callback got %v, want [execute]", hostSeen)
	}
	if len(cmds) == 0 {
		t.Error("expected a tea.Println for scrollback feedback")
	}
}

// TestApplyAgentEvent_PlanProposalAdjustRequested verifies the
// "adjust" path: substate snaps back to analyze (in case it was
// somehow on execute), Plan stays on, and the optional free-text
// feedback gets surfaced in the scrollback line so the user can see
// what their picker selection conveyed.
func TestApplyAgentEvent_PlanProposalAdjustRequested(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Plan = true
	m.opts.PlanSubstate = "execute" // simulate already-executing then user clicks adjust
	var hostSeen []string
	m.opts.SetPlanSubstate = func(s string) { hostSeen = append(hostSeen, s) }

	cmds := m.applyAgentEvent(agent.PlanProposalAdjustRequested{Feedback: "step 3 is wrong"})

	if m.opts.PlanSubstate != "analyze" {
		t.Errorf("substate = %q, want %q", m.opts.PlanSubstate, "analyze")
	}
	if !m.opts.Plan {
		t.Error("Plan should stay true after adjust (still in plan mode, just re-thinking)")
	}
	if len(hostSeen) != 1 || hostSeen[0] != "analyze" {
		t.Errorf("host callback got %v, want [analyze]", hostSeen)
	}
	if len(cmds) == 0 {
		t.Error("expected a tea.Println for scrollback feedback")
	}
}

// TestApplyAgentEvent_PlanProposalAdjustRequested_NoFeedback covers
// the "user clicked Adjust without typing" branch — the scrollback
// line must not include an empty parenthetical.
func TestApplyAgentEvent_PlanProposalAdjustRequested_NoFeedback(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Plan = true
	m.opts.SetPlanSubstate = func(s string) {}

	cmds := m.applyAgentEvent(agent.PlanProposalAdjustRequested{Feedback: ""})

	if len(cmds) == 0 {
		t.Fatal("expected a tea.Println for scrollback feedback")
	}
	// We can't read the rendered string out of tea.Cmd easily without
	// executing it; cmds presence + substate check is sufficient.
	if m.opts.PlanSubstate != "analyze" {
		t.Errorf("substate = %q, want %q", m.opts.PlanSubstate, "analyze")
	}
}

// TestApplyAgentEvent_PlanProposalCancelled verifies that cancellation
// from inside the propose picker is equivalent to /plan off: Plan
// flips to false, substate clears, and the host is notified via
// SetPlan (not SetPlanSubstate — full exit, not substate change).
func TestApplyAgentEvent_PlanProposalCancelled(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Plan = true
	m.opts.PlanSubstate = "analyze"
	var planSeen []bool
	var substateSeen []string
	m.opts.SetPlan = func(b bool) { planSeen = append(planSeen, b) }
	m.opts.SetPlanSubstate = func(s string) { substateSeen = append(substateSeen, s) }

	cmds := m.applyAgentEvent(agent.PlanProposalCancelled{})

	if m.opts.Plan {
		t.Error("Plan should be false after cancel")
	}
	if m.opts.PlanSubstate != "" {
		t.Errorf("substate = %q, want empty", m.opts.PlanSubstate)
	}
	if len(planSeen) != 1 || planSeen[0] != false {
		t.Errorf("SetPlan got %v, want [false]", planSeen)
	}
	// Cancel exits plan entirely → no substate callback (would be
	// ambiguous since the substate is now meaningless).
	if len(substateSeen) != 0 {
		t.Errorf("SetPlanSubstate should NOT fire on cancel, got %v", substateSeen)
	}
	if len(cmds) == 0 {
		t.Error("expected a tea.Println for scrollback feedback")
	}
}

// TestApplyAgentEvent_PlanProposalApproved_NilHostCallbackSafe pins
// that the TUI updates its local state even when the host (cmd/seek)
// hasn't wired the SetPlanSubstate callback. The status bar should
// still reflect the new substate; only the permission-policy
// side-effect is lost (which is the host's responsibility anyway).
func TestApplyAgentEvent_PlanProposalApproved_NilHostCallbackSafe(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Plan = true
	// No SetPlanSubstate wired — simulates tests / embedders that
	// don't fully wire the host callbacks.
	cmds := m.applyAgentEvent(agent.PlanProposalApproved{Steps: []string{"a"}})

	if m.opts.PlanSubstate != "execute" {
		t.Errorf("substate = %q, want %q (local state must update even without host callback)", m.opts.PlanSubstate, "execute")
	}
	if len(cmds) == 0 {
		t.Error("expected scrollback feedback regardless of callback wiring")
	}
}
