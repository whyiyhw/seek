package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/bubbles/textarea"
)

func TestHandlePasteFolding_BelowThreshold(t *testing.T) {
	// 5 lines or fewer → no folding.
	m := Model{input: textarea.New()}
	m.input.SetValue("line 1\nline 2\nline 3")
	m = m.handlePasteFolding()
	if m.pastedContent != "" {
		t.Errorf("3 lines should not fold, got pastedContent=%q", m.pastedContent)
	}

	m.input.SetValue("line 1\nline 2\nline 3\nline 4\nline 5")
	m = m.handlePasteFolding()
	if m.pastedContent != "" {
		t.Errorf("5 lines (exactly threshold) should not fold, got pastedContent=%q", m.pastedContent)
	}
}

func TestHandlePasteFolding_AboveThreshold(t *testing.T) {
	// 6 lines → fold.
	m := Model{input: textarea.New()}
	content := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6"
	m.input.SetValue(content)
	m = m.handlePasteFolding()
	if m.pastedContent != content {
		t.Errorf("6 lines should fold, got pastedContent=%q, want=%q", m.pastedContent, content)
	}
}

func TestHandlePasteFolding_UnfoldOnKeypress(t *testing.T) {
	// After folding, any keypress clears pastedContent and
	// restores the original content to the textarea.
	m := Model{input: textarea.New()}
	content := "a\nb\nc\nd\ne\nf"
	m.input.SetValue(content)
	m = m.handlePasteFolding()
	if m.pastedContent != content {
		t.Fatalf("setup: should be folded, got pastedContent=%q", m.pastedContent)
	}

	// Simulate a keypress: the textarea now has content + appended char.
	// In real Bubble Tea this would be a KeyRunes update, but we
	// simulate by appending "x" to the value and calling the method.
	m.input.SetValue(content + "x")
	m = m.handlePasteFolding()

	if m.pastedContent != "" {
		t.Errorf("after keypress, pastedContent should be cleared, got %q", m.pastedContent)
	}
	// Verify the trigger char was discarded and original content restored.
	if got := m.input.Value(); got != content {
		t.Errorf("textarea value: got=%q, want=%q (trigger char should be discarded)", got, content)
	}
}

func TestHandlePasteFolding_IdempotentWhenNotFolded(t *testing.T) {
	// Calling handlePasteFolding on a small input multiple times
	// should not change state.
	m := Model{input: textarea.New()}
	m.input.SetValue("hello")
	for i := 0; i < 3; i++ {
		m = m.handlePasteFolding()
		if m.pastedContent != "" {
			t.Fatalf("iteration %d: unexpected pastedContent=%q", i, m.pastedContent)
		}
	}
}

func TestRenderPastedPlaceholder_Formatting(t *testing.T) {
	tests := []struct {
		lines int
		want  string // substring that must appear
	}{
		{6, "pasted 6 lines"},
		{100, "pasted 100 lines"},
		{1, "pasted 1 line"},   // edge case: unlikely but should still format
	}

	for _, tc := range tests {
		// Build content with the required number of lines.
		parts := make([]string, tc.lines)
		for i := 0; i < tc.lines; i++ {
			parts[i] = "x"
		}
		content := strings.Join(parts, "\n")

		m := Model{
			input:         textarea.New(),
			pastedContent: content,
			width:         80,
		}
		result := m.renderPastedPlaceholder()
		if !strings.Contains(result, tc.want) {
			t.Errorf("for %d lines: got %q, want substring %q", tc.lines, result, tc.want)
		}
		// Must contain the emoji indicator.
		if !strings.Contains(result, "📋") {
			t.Errorf("placeholder should contain emoji indicator, got %q", result)
		}
	}
}

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

func TestHandleKey_StreamingEnter_EmptyInputIsNoOp(t *testing.T) {
	m := streamingModel(t, "   ") // whitespace-only

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.queuedText != "" {
		t.Errorf("empty input should not queue, got %q", m2.queuedText)
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
}

func TestHandleKey_StreamingEnter_ClearsPasteFoldFlag(t *testing.T) {
	// Regression: after folding a multi-line paste, pressing Enter (or
	// Alt+Enter) mid-stream must clear m.pastedContent — otherwise
	// View() keeps rendering "[pasted N lines, hidden]" while the
	// agent is busy with the new turn, even though the textarea has
	// already been Reset().
	//
	// We only cover the two streaming-branch paths here because they
	// don't invoke submit() (which would require a mocked Agent +
	// Context). The non-streaming path's clear lives on the same line
	// of update.go, visible at review time.
	cases := []struct {
		name string
		alt  bool
	}{
		{"streaming queue (Enter)", false},
		{"streaming steer (Alt+Enter)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{input: textarea.New(), streaming: true}
			m.input.SetValue("line 1\nline 2\nline 3\nline 4\nline 5\nline 6")
			m = m.handlePasteFolding()
			if m.pastedContent == "" {
				t.Fatalf("setup: paste should have folded")
			}
			m.cancelStream = func() {} // wired for Alt+Enter path

			out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: tc.alt})
			m2 := out.(Model)

			if m2.pastedContent != "" {
				t.Errorf("pastedContent should be cleared after Enter, got %q", m2.pastedContent)
			}
		})
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

func TestCmdModel_NoArgsOpensPicker(t *testing.T) {
	// /model with no args should open the picker for a curated provider
	// (DeepSeek), with the current model preselected.
	m := &Model{}
	m.opts.Model = "deepseek-reasoner"
	m.opts.ProviderName = "" // DeepSeek

	res := cmdModel(m, "")

	if !m.modelPickerOpen {
		t.Fatal("expected /model to open the picker")
	}
	if len(m.modelPickerFiltered) < 2 {
		t.Fatalf("expected at least 2 DeepSeek candidates, got %d", len(m.modelPickerFiltered))
	}
	// Preselect "deepseek-reasoner" since it matches the current model.
	got := m.modelPickerFiltered[m.modelPickerSelected].id
	if got != "deepseek-reasoner" {
		t.Errorf("expected current model preselected, got %q", got)
	}
	// No surface text when opening — the picker is the response.
	if res.text != "" {
		t.Errorf("expected empty text on picker open, got %q", res.text)
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
	m.modelPickerFiltered = []modelChoice{
		{"deepseek-chat", "current"},
		{"deepseek-reasoner", "Thinking mode"},
	}
	m.modelPickerSelected = 1 // user arrowed down to the second row

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := out.(Model)

	if m2.modelPickerOpen {
		t.Error("Enter on a picker row should close the picker")
	}
	if m2.opts.Model != "deepseek-reasoner" {
		t.Errorf("model should have switched, got %q", m2.opts.Model)
	}
}

func TestHandleKey_ModelPickerOpen_EscDismisses(t *testing.T) {
	// Esc closes the picker WITHOUT switching the model.
	m := Model{input: textarea.New()}
	m.opts.Model = "deepseek-chat"
	m.modelPickerOpen = true
	m.modelPickerFiltered = []modelChoice{
		{"deepseek-chat", "current"},
		{"deepseek-reasoner", "Thinking mode"},
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
