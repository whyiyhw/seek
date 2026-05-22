package tui

import (
	"strings"
	"testing"

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
