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

	tea "github.com/charmbracelet/bubbletea"
)

func TestNormalizeControlRunes_Table(t *testing.T) {
	cases := []struct {
		name string
		in   tea.KeyMsg
		want tea.KeyType
	}{
		{"\\n becomes Enter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\n'}}, tea.KeyEnter},
		{"\\r becomes Enter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\r'}}, tea.KeyEnter},
		{"\\b becomes Backspace", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\b'}}, tea.KeyBackspace},
		{"plain rune untouched", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, tea.KeyRunes},
		{"real Enter untouched", tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyEnter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeControlRunes(tc.in); got.Type != tc.want {
				t.Fatalf("normalizeControlRunes(%v).Type = %v, want %v", tc.in, got.Type, tc.want)
			}
		})
	}
	// Paste content and multi-rune payloads are real content, not keys.
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb"), Paste: true}
	if got := normalizeControlRunes(paste); got.Type != tea.KeyRunes || len(got.Runes) != 3 {
		t.Fatalf("paste payload must pass through untouched: %+v", got)
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

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\n'}})
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

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\b'}})
	mm := out.(Model)
	if got := mm.input.Value(); got != "a" {
		t.Fatalf("'\\b' character event must delete like Backspace, input = %q", got)
	}
}

// TestUpdate_PastedNewlineIsNotEnter: a newline inside a paste payload is
// CONTENT — the rewrite must not turn a 4-line paste into 4 Enters.
func TestUpdate_PastedNewlineIsNotEnter(t *testing.T) {
	m := testModel().
		WithAgent(newFakeAgent()).
		WithCustomState(func(m *Model) {
			m.opts.Ctx = context.Background()
		}).
		Build()

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1\nline2\nline3\nline4"), Paste: true})
	mm := out.(Model)
	if mm.streaming {
		t.Fatal("pasted newlines are content, not Enter — must not submit")
	}
	if !strings.Contains(mm.input.Value(), "line1") && !strings.Contains(mm.input.Value(), "pasted") {
		t.Fatalf("pasted text should be in the input (raw or folded), got %q", mm.input.Value())
	}
}
