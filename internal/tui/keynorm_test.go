package tui

// Tests for normalizeControlRunes — the IME/terminal-bridge defense that
// rewrites '\n'/'\b' character events into Enter/Backspace key events.
// Driven through Update (the door keystrokes actually enter through), not
// the bare function, so the rewrite is proven to sit before routing —
// the same lesson as pitfalls "the test MUST enter through the same door
// the user's keystrokes do".

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNormalizeControlRunes_Table(t *testing.T) {
	cases := []struct {
		name string
		in   tea.KeyPressMsg
		want tea.KeyPressMsg
	}{
		{"\\n becomes Enter", tea.KeyPressMsg{Text: "\n"}, tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"\\r becomes Enter", tea.KeyPressMsg{Text: "\r"}, tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"\\b becomes Backspace", tea.KeyPressMsg{Text: "\b"}, tea.KeyPressMsg{Code: tea.KeyBackspace}},
		{"plain rune untouched", tea.KeyPressMsg{Text: "a"}, tea.KeyPressMsg{Text: "a"}},
		{"real Enter untouched", tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeControlRunes(tc.in); got != tc.want {
				t.Fatalf("normalizeControlRunes(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
	// The rewrite must produce keys whose String() matches what the
	// keymap and key-dispatch switches match on — a rewritten \n with a
	// stale Text field would String() as "\n" and never hit "enter".
	for _, tc := range []struct {
		in   tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Text: "\n"}, "enter"},
		{tea.KeyPressMsg{Text: "\r"}, "enter"},
		{tea.KeyPressMsg{Text: "\b"}, "backspace"},
	} {
		if got := normalizeControlRunes(tc.in).String(); got != tc.want {
			t.Errorf("normalizeControlRunes(%v).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Multi-rune payloads are real content, not keys: they pass through
	// untouched (in v2, bracketed paste is its own PasteMsg, but a
	// multi-rune key event must still never be rewritten).
	paste := tea.KeyPressMsg{Text: "a\nb"}
	if got := normalizeControlRunes(paste); got != paste {
		t.Fatalf("multi-rune payload must pass through untouched: %+v", got)
	}
}

// TestUpdate_EnterAsRuneSubmits: the reported Windows signature — typing
// works, Enter inserts a newline instead of sending — with Enter arriving
// as a '\n' character event (IME/ConPTY bridge). After normalization the
// message must SUBMIT.
func TestUpdate_EnterAsRuneSubmits(t *testing.T) {
	m := testModel().
		WithAgent(newFakeAgent()).
		WithCustomState(func(m *Model) {
			m.opts.Ctx = context.Background()
			m.legacyConhostInput = false // WT-like: paste guard off
		}).
		Build()
	m.input.SetValue("你好世界")
	// Fresh runes stamp — if the '\n' were left as runes, the textarea
	// would insert a newline; if it reached the conhost guard path (wrong
	// on this model), same. Only the rewrite submits.
	m.lastInputRunesAt = m.now

	out, _ := m.Update(tea.KeyPressMsg{Text: "\n"})
	mm := out.(Model)
	if !mm.streaming {
		t.Fatal("a '\\n' character event must be treated as Enter and submit the message")
	}
	if mm.input.Value() != "" {
		t.Fatalf("input should be cleared on submit, got %q", mm.input.Value())
	}
}

// TestUpdate_BackspaceAsRuneDeletes: '\b' as a character event must
// delete the character before the cursor like a real Backspace — on the
// broken path the textarea ignores it entirely ("backspace is dead").
func TestUpdate_BackspaceAsRuneDeletes(t *testing.T) {
	m := testModel().Build()
	m.input.SetValue("ab")
	m.input.CursorEnd()

	out, _ := m.Update(tea.KeyPressMsg{Text: "\b"})
	mm := out.(Model)
	if got := mm.input.Value(); got != "a" {
		t.Fatalf("'\\b' character event must delete like Backspace, input = %q", got)
	}
}

// TestUpdate_PastedNewlineIsNotEnter: a newline inside a paste payload is
// CONTENT — the rewrite must not turn a 4-line paste into 4 Enters. In v2
// paste arrives as a dedicated tea.PasteMsg, which must go straight into
// the input (folded), never into key routing.
func TestUpdate_PastedNewlineIsNotEnter(t *testing.T) {
	m := testModel().
		WithAgent(newFakeAgent()).
		WithCustomState(func(m *Model) {
			m.opts.Ctx = context.Background()
		}).
		Build()

	out, _ := m.Update(tea.PasteMsg{Content: "line1\nline2\nline3\nline4"})
	mm := out.(Model)
	if mm.streaming {
		t.Fatal("pasted newlines are content, not Enter — must not submit")
	}
	if !strings.Contains(mm.input.Value(), "line1") && !strings.Contains(mm.input.Value(), "pasted") {
		t.Fatalf("pasted text should be in the input (raw or folded), got %q", mm.input.Value())
	}
}
