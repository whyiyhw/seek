package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/askuser"
)

// handleApprovalKey is the inline-prompt key handler. While
// pendingApproval is set, every key reaches here. Keys:
//
//	y / Y / Enter  → reply true (allow once)
//	n / N / Esc    → reply false (deny once)
//	a / A          → reply true AND upgrade session to ModeYolo
//	Ctrl+C         → reply false then quit (so the agent unblocks)
//
// Replies on req.Reply are non-blocking because the channel is
// buffered to 1; we still wrap in a select to be defensive.
func (m Model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingApproval == nil {
		return m, nil
	}
	allow := false
	always := false
	answered := true

	switch msg.Type {
	case tea.KeyEnter:
		allow = true
	case tea.KeyEsc:
		allow = false
	case tea.KeyCtrlC:
		// Reply deny, then quit. Without this the agent goroutine
		// would block forever on the reply channel.
		m.replyApproval(false)
		return m, tea.Quit
	default:
		// Character keys.
		s := strings.ToLower(msg.String())
		switch s {
		case "y", "yes":
			allow = true
		case "n", "no":
			allow = false
		case "a", "always":
			allow = true
			always = true
		default:
			answered = false
		}
	}

	if !answered {
		return m, nil
	}

	m.replyApproval(allow)
	if always && m.opts.SetYolo != nil {
		m.opts.SetYolo(true)
		m.opts.Yolo = true
		// Yolo just turned on via "always" — refresh so the next
		// idle moment shows the YOLO warning placeholder.
		m.refreshPlaceholder()
	}
	m.pendingApproval = nil
	m.input.Focus()
	// Re-arm the approval listener so the next dangerous tool call
	// triggers a fresh prompt.
	return m, waitForApproval(m.opts.ApprovalCh)
}

// replyApproval is a small wrapper around the buffered send. We use
// non-blocking semantics so a missing reader (shouldn't happen, but
// defensive) doesn't deadlock the UI.
func (m *Model) replyApproval(allow bool) {
	if m.pendingApproval == nil {
		return
	}
	select {
	case m.pendingApproval.Reply <- allow:
	default:
		// Buffered channel was already written or no reader — either
		// way nothing more we can do here.
	}
}

// handleQuestionKey drives the ask_user picker. Two states:
//
//   - choice mode (free-text bool false): ↑/↓ moves the cursor,
//     Space toggles in multi-select, Enter accepts (single-select)
//     or confirms toggled set (multi). Picking the "Other" row
//     transitions to free-text mode.
//   - free-text mode: the textarea is focused; Enter submits the
//     typed content as FreeText; Esc reverts to choice mode.
//
// Esc at top level cancels: Answer{Cancelled: true} goes back, the
// tool returns {cancelled: true} to the model. The model is
// expected to gracefully step down to plain-text questioning when
// it sees that.
func (m Model) handleQuestionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingQuestion == nil {
		return m, nil
	}
	q := m.pendingQuestion.Question
	otherIdx := len(q.Options)

	if m.pendingQuestionFreeText {
		switch msg.Type {
		case tea.KeyEsc:
			// Back to choices.
			m.pendingQuestionFreeText = false
			m.input.Blur()
			m.input.Reset()
			return m, nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil // Empty Enter is a no-op — don't submit blank.
			}
			m.input.Reset()
			return m.completeQuestion(askuser.Answer{FreeText: text})
		case tea.KeyCtrlC:
			// Reply cancelled before quitting so the agent unblocks.
			return m.completeQuestion(askuser.Answer{Cancelled: true})
		}
		// Otherwise pass through to the textarea (typing).
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// Choice mode.
	switch msg.Type {
	case tea.KeyUp:
		if m.pendingQuestionCursor > 0 {
			m.pendingQuestionCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.pendingQuestionCursor < otherIdx {
			m.pendingQuestionCursor++
		}
		return m, nil
	case tea.KeySpace:
		if !q.MultiSelect {
			return m, nil
		}
		// Toggle current row. The "Other" row is mutually exclusive
		// with the option rows: picking Other clears everything else,
		// picking anything else clears Other. This matches the
		// "free-text replaces structured choices" semantic from the
		// design — you wouldn't say "ids: A, B + free_text: ..." at
		// the same time, the reply schema is one-or-the-other.
		if m.pendingQuestionCursor == otherIdx {
			if m.pendingQuestionSelected[otherIdx] {
				delete(m.pendingQuestionSelected, otherIdx)
			} else {
				m.pendingQuestionSelected = map[int]bool{otherIdx: true}
			}
		} else {
			if m.pendingQuestionSelected[m.pendingQuestionCursor] {
				delete(m.pendingQuestionSelected, m.pendingQuestionCursor)
			} else {
				delete(m.pendingQuestionSelected, otherIdx)
				m.pendingQuestionSelected[m.pendingQuestionCursor] = true
			}
		}
		return m, nil
	case tea.KeyEnter:
		// Single-select: accept the cursor row.
		// Multi-select:  if nothing toggled, treat current row as
		//                the single pick (a one-line shortcut for
		//                "I just want this one"); otherwise confirm
		//                the toggled set.
		if !q.MultiSelect {
			if m.pendingQuestionCursor == otherIdx {
				return m.enterFreeText()
			}
			id := q.Options[m.pendingQuestionCursor].ID
			return m.completeQuestion(askuser.Answer{ChosenIDs: []string{id}})
		}
		// Multi-select.
		selected := m.toggledIDs(q)
		if len(selected) == 0 {
			// Empty Enter on cursor row: same as toggling that
			// single row and confirming. The shortcut keeps the
			// common "I just want this one" case to one keypress.
			if m.pendingQuestionCursor == otherIdx {
				return m.enterFreeText()
			}
			return m.completeQuestion(askuser.Answer{ChosenIDs: []string{q.Options[m.pendingQuestionCursor].ID}})
		}
		// If "Other" was toggled (alone, by the exclusivity rule),
		// flip into free-text mode instead of returning an empty
		// ChosenIDs answer.
		if m.pendingQuestionSelected[otherIdx] {
			return m.enterFreeText()
		}
		return m.completeQuestion(askuser.Answer{ChosenIDs: selected})
	case tea.KeyEsc:
		return m.completeQuestion(askuser.Answer{Cancelled: true})
	case tea.KeyCtrlC:
		// Reply cancelled before quitting so the agent unblocks.
		return m.completeQuestion(askuser.Answer{Cancelled: true})
	}
	return m, nil
}

// enterFreeText flips the picker into "Other / type your own"
// mode: textarea takes focus, the picker collapses to a single
// status line. The Answer flows back via the next Enter handled
// in the free-text branch above.
func (m Model) enterFreeText() (tea.Model, tea.Cmd) {
	m.pendingQuestionFreeText = true
	m.input.Reset()
	m.input.Focus()
	return m, nil
}

// toggledIDs returns the picker's selected option ids (excluding the
// auto-Other row, which is handled separately). Order matches the
// option order, not the order the user toggled.
func (m *Model) toggledIDs(q askuser.Question) []string {
	var out []string
	for i, opt := range q.Options {
		if m.pendingQuestionSelected[i] {
			out = append(out, opt.ID)
		}
	}
	return out
}

// completeQuestion sends Answer back through the reply channel,
// clears all pending state, and re-arms the listener for the next
// ask_user call.
func (m Model) completeQuestion(ans askuser.Answer) (tea.Model, tea.Cmd) {
	if m.pendingQuestion == nil {
		return m, nil
	}
	select {
	case m.pendingQuestion.Reply <- ans:
	default:
		// Buffered to 1 — should always succeed on first send.
	}
	m.pendingQuestion = nil
	m.pendingQuestionSelected = nil
	m.pendingQuestionCursor = 0
	m.pendingQuestionFreeText = false
	m.input.Focus()
	return m, waitForAskUser(m.opts.AskUserCh)
}
