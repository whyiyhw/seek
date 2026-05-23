package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/bubbles/textarea"
)

// streamingModel returns a Model in the streaming state, with a textarea
// pre-populated. Used by the queue/steer tests below.
func streamingModel(t *testing.T, input string) Model {
	t.Helper()
	m := Model{input: textarea.New(), streaming: true}
	m.input.SetValue(input)
	return m
}

func TestHandleKey_StreamingEnter_QueuesText(t *testing.T) {
	m := streamingModel(t, "  follow-up: also check main.go  ")

	// Enter (no modifier) during a stream → queue, do NOT submit.
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
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
	m := streamingModel(t, "wait, undo that change")

	// Wire a cancel func so we can observe it being called.
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
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

func TestHandleKey_StreamingEnter_EmptyInputNothingToWithdrawIsNoOp(t *testing.T) {
	// Empty textarea + empty queue + empty pending steer + Enter → no-op.
	// (When something IS pending, the withdraw path runs — see the
	// next two tests.)
	m := streamingModel(t, "   ") // whitespace-only

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText != "" {
		t.Errorf("empty input should not queue, got %q", m2.queuedText)
	}
}

func TestHandleKey_StreamingEnter_EmptyInputWithdrawsQueue(t *testing.T) {
	// User typed "do A", queued it (Enter), then changed their mind —
	// pressing Enter on an empty textarea should withdraw the queued
	// message without cancelling the in-flight stream.
	m := streamingModel(t, "") // empty textarea
	m.queuedText = "look at server.go"
	// Wire a no-op cancel so we can verify it does NOT get called —
	// withdrawing a queue must not touch the stream.
	cancelCalled := false
	m.cancelStream = func() { cancelCalled = true }

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
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
	// Same shape, steer instead of queue. A pending steer means
	// cancelStream has already been called (it's how steer works) —
	// withdrawal here just clears the about-to-fire submission, the
	// stream is already on its way out.
	m := streamingModel(t, "")
	m.pendingSteerText = "wait, undo that"

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.pendingSteerText != "" {
		t.Errorf("pending steer should clear, got %q", m2.pendingSteerText)
	}
	if m2.queuedText != "" {
		t.Errorf("queue should remain empty, got %q", m2.queuedText)
	}
}

func TestHandleKey_StreamingEnter_SecondPressReplacesQueue(t *testing.T) {
	// First Enter queues "first"; second Enter (with new textarea
	// content) replaces — "last thing you said is what you meant".
	m := streamingModel(t, "first message")
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)
	if m.queuedText != "first message" {
		t.Fatalf("setup: queuedText=%q, want %q", m.queuedText, "first message")
	}

	m.input.SetValue("second message — disregard the first")
	out, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(Model)

	if m.queuedText != "second message — disregard the first" {
		t.Errorf("second Enter should replace queue; got %q", m.queuedText)
	}
}

func TestHandleKey_StreamingEsc_ClearsQueueAndSteer(t *testing.T) {
	// Esc during a stream cancels AND clears both queue and pending
	// steer — "Esc stops everything" must include latent state.
	m := streamingModel(t, "")
	m.queuedText = "stale queue"
	m.pendingSteerText = "stale steer"
	canceled := false
	m.cancelStream = func() { canceled = true }

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
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

func TestHandleKey_CommandMenuOpen_EnterAcceptsCandidate(t *testing.T) {
	// When the slash-command menu is open and has candidates, Enter
	// should fill in the highlighted command (same as Tab) — NOT
	// submit the partial literal text and queue/dispatch on that.
	m := Model{input: textarea.New()}
	m.input.SetValue("/h")
	// Force the menu state directly rather than driving it via key events.
	cmds := filterCommands(allCommands(), "/h")
	if len(cmds) == 0 {
		t.Fatalf("setup: filterCommands returned 0 candidates for '/h'")
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = cmds
	m.commandMenuSelected = 0

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.commandMenuOpen {
		t.Error("Enter on candidate should close the menu")
	}
	want := cmds[0].names[0] + " "
	if got := m2.input.Value(); got != want {
		t.Errorf("textarea should be filled with the candidate, got %q want %q", got, want)
	}
}

func TestHandleKey_SlashMenuEnter_HandsOffToModelPicker(t *testing.T) {
	// User flow: type "/model" → slash menu opens with /model highlighted →
	// press Enter. The expected result is that the model picker opens
	// immediately (not on the NEXT keystroke). Before the handoff fix,
	// accepting a slash-menu candidate set the textarea to "/model "
	// but didn't trigger updateCommandMenu, leaving the screen empty
	// until something else moved the input.
	m := emptyModel()
	m.opts.Model = "deepseek-chat"
	m.input.SetValue("/model")
	m.updateCommandMenu() // simulate the state after typing "/model"
	if !m.commandMenuOpen {
		t.Fatalf("setup: slash menu should be open for '/model'")
	}

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.commandMenuOpen {
		t.Error("slash menu should be closed after accept")
	}
	if !m2.modelPickerOpen {
		t.Error("model picker should auto-open right after /model is accepted")
	}
	if m2.pickerPurpose != "model" {
		t.Errorf("pickerPurpose = %q, want 'model'", m2.pickerPurpose)
	}
	if got := m2.input.Value(); got != "/model " {
		t.Errorf("textarea after accept = %q, want '/model '", got)
	}
}

func TestUpdateCommandMenu_AutoOpensModelPickerOnSpace(t *testing.T) {
	// Regression: when the user types "/model<space>" the model picker
	// should auto-open. Before this fix, the slash menu closed (because
	// it forbids spaces in args mode) and nothing replaced it — the
	// user saw nothing until they hit Enter.
	m := emptyModel()
	m.opts.Model = "deepseek-chat"
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
	if m.modelPickerFiltered[m.modelPickerSelected].id != "deepseek-chat" {
		t.Errorf("expected current model preselected, got %q",
			m.modelPickerFiltered[m.modelPickerSelected].id)
	}
}

func TestUpdateCommandMenu_ClosesAutoPickerOnBackspace(t *testing.T) {
	// Once the input is no longer "/model<space>..." (e.g. user
	// backspaced the space back to "/model"), the auto-opened picker
	// must close so the regular slash menu can re-appear.
	m := emptyModel()
	m.opts.Model = "deepseek-chat"
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

func TestCmdModel_NoArgsOpensPicker(t *testing.T) {
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
	// legacy "deepseek-reasoner" alias is intentionally not surfaced).
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
// picker no longer lists "deepseek-reasoner" — the explicit V4 name
// replaced it (see knownModelsForProvider). If someone re-adds reasoner
// to the curated list this test fails on purpose. Direct-id use via
// /model deepseek-reasoner is still valid (covered elsewhere) — the
// alias is hidden, not removed.
func TestCmdModel_LegacyReasonerNotInPicker(t *testing.T) {
	m := &Model{}
	m.opts.Model = "deepseek-chat"
	m.opts.ProviderName = ""

	cmdModel(m, "")

	for _, mc := range m.modelPickerFiltered {
		if mc.id == "deepseek-reasoner" {
			t.Errorf("deepseek-reasoner should not appear in the curated picker, found in: %+v",
				m.modelPickerFiltered)
		}
	}
}

func TestCmdModel_ArgsPathStillWorks(t *testing.T) {
	// /model <id> should bypass the picker — used by power users and
	// for compatible-provider freeform ids.
	m := &Model{}
	m.opts.Model = "deepseek-chat"

	res := cmdModel(m, "deepseek-reasoner")

	if m.modelPickerOpen {
		t.Error("/model <id> must not open the picker")
	}
	if m.opts.Model != "deepseek-reasoner" {
		t.Errorf("model not switched: got %q", m.opts.Model)
	}
	if !strings.Contains(res.text, "deepseek-reasoner") {
		t.Errorf("transition message should mention new model: %q", res.text)
	}
}

func TestCmdModel_UnknownProviderFallsBackToHint(t *testing.T) {
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
	// Picker open + Enter on a different row → model switches, picker closes.
	m := Model{input: textarea.New()}
	m.opts.Model = "deepseek-chat"
	m.modelPickerOpen = true
	m.pickerPurpose = "model"
	m.modelPickerFiltered = []modelChoice{
		{"deepseek-chat", "current"},
		{"deepseek-v4-pro", "Thinking mode"},
	}
	m.modelPickerSelected = 1 // user arrowed down to the second row
	// The user arrived here by typing "/model " — textarea still
	// holds that. Verify the accept cleans it up.
	m.input.SetValue("/model ")

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
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
			m.opts.Model = "deepseek-chat"
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
			modelChanged := m.opts.Model != "deepseek-chat"
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
	// Esc during key entry should clear the wizard state without
	// hitting Store.Save (no config file written).
	m := emptyModel()
	m.setupKeyEntry = true
	m.setupProvider = "anthropic"
	m.input.SetValue("sk-ant-partial")

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
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
	// Pressing Enter with an empty key shouldn't save garbage to
	// config — finishSetup returns a "cancelled" Println and clears
	// state. We verify state-clear here; config side-effect-absence
	// is covered structurally (empty key short-circuits before Save).
	m := emptyModel()
	m.setupKeyEntry = true
	m.setupProvider = "deepseek"
	m.input.SetValue("   ") // whitespace-only — trimmed to empty

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.setupKeyEntry {
		t.Error("Enter should exit key-entry mode even when key is empty")
	}
}

func TestHandleKey_ModelPickerOpen_EscDismisses(t *testing.T) {
	// Esc closes the picker WITHOUT switching the model.
	m := Model{input: textarea.New()}
	m.opts.Model = "deepseek-chat"
	m.modelPickerOpen = true
	m.modelPickerFiltered = []modelChoice{
		{"deepseek-chat", "current"},
		{"deepseek-v4-pro", "Thinking mode"},
	}
	m.modelPickerSelected = 1

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := out.(Model)

	if m2.modelPickerOpen {
		t.Error("Esc should close the picker")
	}
	if m2.opts.Model != "deepseek-chat" {
		t.Errorf("Esc must not switch the model; got %q", m2.opts.Model)
	}
}

func TestHandleKey_PathPickerOpen_EnterAcceptsHighlighted(t *testing.T) {
	// Picker open with candidates: Enter accepts (same as Tab). The
	// user then presses Enter again to submit.
	m := Model{input: textarea.New()}
	m.input.SetValue("@RE")
	m.pathPicker.open = true
	m.pathPicker.filtered = []string{"README.md"}
	m.pathPicker.selected = 0
	m.pathPicker.tokenStart = 0
	m.pathPicker.token = "RE"

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.pathPicker.open {
		t.Error("Enter on candidate should close the picker")
	}
	if !strings.Contains(m2.input.Value(), "README.md") {
		t.Errorf("textarea should contain the chosen path, got %q", m2.input.Value())
	}
}

func TestHandleKey_StreamingEnter_SlashCommandRunsImmediately(t *testing.T) {
	// Regression: while streaming, typing "/help" and pressing Enter
	// must open the help overlay immediately — NOT stash "/help" into
	// queuedText and dispatch it as a user message to the model when
	// the turn ends. Slash commands are TUI-side, not LLM-bound.
	m := streamingModel(t, "/help")

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText != "" {
		t.Errorf("/help during stream must not queue, got queuedText=%q", m2.queuedText)
	}
	if !m2.helpOverlayOpen {
		t.Error("/help during stream should open the help overlay immediately")
	}
}

func TestRenderQueueHint_States(t *testing.T) {
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
