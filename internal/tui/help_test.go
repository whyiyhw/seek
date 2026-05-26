package tui

import (
	"reflect"
	"runtime"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdName reflects out the symbolic name of a tea.Cmd's function. We
// use it to distinguish "tea.Quit" from "any other cmd" without
// invoking the cmd (invoking tea.Quit returns a quitMsg, which is
// internal and unexported — we can't compare to it directly).
func cmdName(c tea.Cmd) string {
	if c == nil {
		return "<nil>"
	}
	fn := runtime.FuncForPC(reflect.ValueOf(c).Pointer())
	if fn == nil {
		return "<anonymous>"
	}
	return fn.Name()
}

// isQuit reports whether c is the tea.Quit sentinel (or its batched
// equivalent). Used to assert that handleKey returns the quit cmd.
func isQuit(c tea.Cmd) bool {
	if c == nil {
		return false
	}
	// tea.Quit is a package-level variable; comparing function
	// pointers via reflect is the only way to identify it without
	// invoking the cmd (which would emit a quitMsg back into the
	// program loop).
	return reflect.ValueOf(c).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

// TestHelpOverlay_CtrlCQuits pins review finding A2/B4: when the
// help overlay is open, Ctrl+C must STILL quit the program — the
// overlay itself advertises Ctrl+C as "Quit seek" in its content.
// Before the fix, the overlay-dismiss block swallowed every key
// including Ctrl+C, so a Ctrl+C while help was open only dismissed
// the overlay and the user had to press Ctrl+C a second time.
func TestHelpOverlay_CtrlCQuits(t *testing.T) {
	t.Parallel()

	m := testModel().WithCustomState(func(m *Model) {
		m.helpOverlayOpen = true
		m.helpContent = "(rendered help)"
	}).Build()

	out, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	final := out.(Model)

	if !isQuit(cmd) {
		t.Errorf("Ctrl+C while help overlay open must return tea.Quit, got %s",
			cmdName(cmd))
	}
	// The overlay state can be either way — the contract is about
	// quit-vs-not-quit. But the live region won't redraw anyway since
	// we're about to exit, so it doesn't matter visually. Leave it
	// alone in the assertion.
	_ = final
}

// TestHelpOverlay_OtherKeysDismiss is the positive control: every
// non-Ctrl+C key still dismisses the overlay cleanly. The "any key
// to dismiss" affordance is the UX feature the original change added
// (user doesn't have to remember Esc/q); only Ctrl+C is the
// exception.
func TestHelpOverlay_OtherKeysDismiss(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"Esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"Enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"q", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}},
		{"random rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := testModel().WithCustomState(func(m *Model) {
				m.helpOverlayOpen = true
				m.helpContent = "(rendered help)"
			}).Build()

			out, cmd := m.handleKey(tc.key)
			final := out.(Model)

			if isQuit(cmd) {
				t.Errorf("%s should NOT quit while help is open, only Ctrl+C does", tc.name)
			}
			if final.helpOverlayOpen {
				t.Errorf("%s should dismiss the overlay, got helpOverlayOpen=true", tc.name)
			}
			if final.helpContent != "" {
				t.Errorf("%s should clear helpContent, got %q", tc.name, final.helpContent)
			}
		})
	}
}
