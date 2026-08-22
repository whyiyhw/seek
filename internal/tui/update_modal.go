package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/askuser"
)

// handleApprovalKey is the inline-prompt key handler. While
// pendingApproval is set, every key reaches here. Keys:
//
//	y / Y / Enter  → reply true (allow once)
//	n / N / Esc    → reply false (deny once)
//	a / A          → reply true AND grant this Action's Kind for the
//	                 session (per-Kind allowlist — NOT session yolo;
//	                 the full escalation lives only behind /yolo)
//	j / k          → scroll the diff window (when the diff overflows)
//	Ctrl+C         → reply false then quit (so the agent unblocks)
//
// Replies on req.Reply are non-blocking because the channel is
// buffered to 1; we still wrap in a select to be defensive.
func (m Model) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pendingApproval == nil {
		return m, nil
	}
	allow := false
	always := false
	answered := true

	switch msg.String() {
	case "enter":
		allow = true
	case "esc":
		allow = false
	case "ctrl+c":
		// Reply deny, then quit. Without this the agent goroutine
		// would block forever on the reply channel.
		m.replyApproval(false)
		(&m).persistSession()
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
		case "j", "k":
			// Scroll the diff window without answering. Clamping
			// against the diff's real length happens here so the
			// renderer stays a pure formatter.
			if delta := map[string]int{"j": 1, "k": -1}[s]; delta != 0 {
				m.approvalDiffOffset = clampDiffOffset(
					m.approvalDiffOffset+delta,
					m.pendingApproval.Action.Display.Diff)
			}
			return m, nil
		default:
			answered = false
		}
	}

	if !answered {
		return m, nil
	}

	m.replyApproval(allow)
	if always {
		// Per-Kind session grant — the narrow replacement for the old
		// "[a] = yolo for session" escalation. The permission layer
		// stops asking about THIS Kind; everything else keeps its
		// prompt. Session-wide yolo remains an explicit /yolo act.
		if m.opts.AlwaysAllowKind != nil {
			m.opts.AlwaysAllowKind(m.pendingApproval.Action.Kind)
		}
	}
	m.pendingApproval = nil
	m.approvalDiffOffset = 0
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
func (m Model) handleQuestionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pendingQuestion == nil {
		return m, nil
	}
	q := m.pendingQuestion.Question
	otherIdx := len(q.Options)

	if m.pendingQuestionFreeText {
		switch msg.String() {
		case "esc":
			// Back to choices.
			m.pendingQuestionFreeText = false
			m.input.Blur()
			m.input.Reset()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil // Empty Enter is a no-op — don't submit blank.
			}
			m.input.Reset()
			return m.completeQuestion(askuser.Answer{FreeText: text})
		case "ctrl+c":
			// Reply cancelled before quitting so the agent unblocks.
			return m.completeQuestion(askuser.Answer{Cancelled: true})
		}
		// Otherwise pass through to the textarea (typing).
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// Choice mode.
	switch msg.String() {
	case "up":
		if m.pendingQuestionCursor > 0 {
			m.pendingQuestionCursor--
		}
		return m, nil
	case "down":
		if m.pendingQuestionCursor < otherIdx {
			m.pendingQuestionCursor++
		}
		return m, nil
	case "space":
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
	case "enter":
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
	case "esc":
		return m.completeQuestion(askuser.Answer{Cancelled: true})
	case "ctrl+c":
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

// handleBatchKey is the v2 multi-question dispatcher. It works by
// temporarily setting m.pendingQuestion to the current question
// in the batch, delegating to handleQuestionKey (which uses the
// shared cursor / selected / freeText state), intercepting the
// resulting Answer, and either advancing pendingBatchIdx or
// completing the batch.
//
// The delegation trick keeps single-question and multi-question
// pickers byte-identical at the input layer — the only difference
// is what completion does: single completes the request; batch
// either advances or completes the BATCH request.
func (m Model) handleBatchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pendingBatch == nil {
		return m, nil
	}
	if m.pendingBatchIdx >= len(m.pendingBatch.Batch.Questions) {
		// Defensive: shouldn't happen — completeBatch fires the
		// reply and clears state when Idx hits len. If we get here,
		// something raced. Reply with whatever we have + clean up.
		return m.completeBatch(askuser.Answer{})
	}

	// Sentinel: borrow a faux *Request that points at the active
	// question so handleQuestionKey can read it via m.pendingQuestion.
	// We DON'T install a real channel — the caller (this function)
	// intercepts the Answer via the per-question pipeline below,
	// bypassing the v1 reply path entirely.
	q := m.pendingBatch.Batch.Questions[m.pendingBatchIdx]
	answerCh := make(chan askuser.Answer, 1)
	m.pendingQuestion = &askuser.Request{Question: q, Reply: answerCh}

	// Delegate. handleQuestionKey may call completeQuestion which
	// sends to answerCh + clears m.pendingQuestion + re-arms the
	// v1 channel (the last is harmless — re-arming a never-firing
	// channel just queues a Cmd that blocks forever in Go's
	// scheduler, GC'd when the program exits).
	updated, _ := m.handleQuestionKey(msg)
	m = updated.(Model)

	// Did the per-question handler complete an answer?
	select {
	case ans := <-answerCh:
		return m.batchAdvance(ans)
	default:
		// In-progress (navigating, toggling, typing free-text). Clear
		// the transient faux pendingQuestion we installed above: it must
		// NOT survive into the next key, or the router's single-question
		// branch would capture that key and send its answer to this now-
		// orphaned channel (the "only the first option works" bug).
		// handleBatchKey rebuilds the faux request fresh on every key, so
		// nothing is lost; the shared cursor / selected / freeText state
		// lives on the Model and persists independently. The router also
		// prefers pendingBatch over pendingQuestion as a second line of
		// defense — see handleKey.
		m.pendingQuestion = nil
		return m, nil
	}
}

// batchAdvance records an answer for the current question and
// either moves to the next question or completes the batch.
// Cancel semantics: a Cancelled answer at index i appends
// Cancelled for the rest (mirrors AskBatch's v1-fallback loop)
// and fires the reply immediately — user gave up, stop asking.
func (m Model) batchAdvance(ans askuser.Answer) (tea.Model, tea.Cmd) {
	if m.pendingBatch == nil {
		return m, nil
	}
	m.pendingBatchAnswers = append(m.pendingBatchAnswers, ans)
	if ans.Cancelled {
		// Pad the rest as cancelled and fire the reply.
		for i := len(m.pendingBatchAnswers); i < len(m.pendingBatch.Batch.Questions); i++ {
			m.pendingBatchAnswers = append(m.pendingBatchAnswers, askuser.Answer{Cancelled: true})
		}
		return m.completeBatch(ans) // ans content unused — completeBatch reads pendingBatchAnswers
	}
	m.pendingBatchIdx++
	if m.pendingBatchIdx >= len(m.pendingBatch.Batch.Questions) {
		return m.completeBatch(ans)
	}
	// Reset per-question state for the next question.
	m.pendingQuestion = nil
	m.pendingQuestionSelected = map[int]bool{}
	m.pendingQuestionCursor = 0
	m.pendingQuestionFreeText = false
	return m, nil
}

// completeBatch fires the batch Reply channel with the accumulated
// answers, clears state, and re-arms the batch listener.
func (m Model) completeBatch(_ askuser.Answer) (tea.Model, tea.Cmd) {
	if m.pendingBatch == nil {
		return m, nil
	}
	select {
	case m.pendingBatch.Reply <- m.pendingBatchAnswers:
	default:
		// Buffered to 1 — should always succeed.
	}
	m.pendingBatch = nil
	m.pendingBatchIdx = 0
	m.pendingBatchAnswers = nil
	m.pendingQuestion = nil
	m.pendingQuestionSelected = nil
	m.pendingQuestionCursor = 0
	m.pendingQuestionFreeText = false
	m.input.Focus()
	return m, waitForAskBatch(m.opts.AskUserBatchCh)
}
