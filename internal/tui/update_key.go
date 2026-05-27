package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Help overlay: dismiss on any key. The overlay is purely
	// informational and blocks the view until dismissed. Esc and q
	// are the advertised dismiss keys; any other key also dismisses
	// so the user doesn't feel stuck.
	if m.helpOverlayOpen {
		m.helpOverlayOpen = false
		m.helpContent = ""
		// Ctrl+C must quit even with the overlay open — the help
		// text itself advertises "Ctrl+C → Quit seek", and swallowing
		// it would make the user press Ctrl+C twice.
		if msg.Type == tea.KeyCtrlC {
			(&m).persistSession()
			return m, tea.Quit
		}
		return m, nil
	}

	// An open approval prompt grabs ALL keys — the agent goroutine is
	// blocked waiting on this answer, so nothing else can usefully
	// happen until the user picks y / n / a.
	if m.pendingApproval != nil {
		return m.handleApprovalKey(msg)
	}

	// An open ask_user picker also grabs everything for the same
	// reason: the tool's goroutine is parked on the Reply channel.
	if m.pendingQuestion != nil {
		return m.handleQuestionKey(msg)
	}

	// Distill review modal grabs all keys until the user finishes the
	// pass. Edit-mode (one of the y/n/e/q states) reuses the main
	// input area for textarea-style content editing.
	if m.distillReviewOpen {
		return m.handleDistillKey(msg)
	}

	// @-path picker has the same priority as the slash menu; the
	// trigger character ("@" vs "/") makes them mutually exclusive
	// in practice.
	if m.pathPicker.open {
		switch msg.Type {
		case tea.KeyTab:
			m.applyPathCompletion()
			return m, nil
		case tea.KeyEnter:
			// Picker open with results: Enter accepts the highlighted
			// path, same as Tab. Two Enters total reach a "submit with
			// path inserted" outcome (accept → submit), matching IDE
			// convention. With zero results we fall through so the user
			// can still submit / queue plain text starting with @.
			if len(m.pathPicker.filtered) > 0 {
				m.applyPathCompletion()
				return m, nil
			}
		case tea.KeyUp:
			if m.pathPicker.selected > 0 {
				m.pathPicker.selected--
			}
			return m, nil
		case tea.KeyDown:
			if m.pathPicker.selected < len(m.pathPicker.filtered)-1 {
				m.pathPicker.selected++
			}
			return m, nil
		case tea.KeyEsc:
			m.pathPicker.open = false
			m.pathPicker.filtered = nil
			m.pathPicker.selected = 0
			return m, nil
		}
		// Other keys fall through (typing more chars refines the
		// filter via updatePathCompleter at end of handleKey).
	}

	// Slash-command menu takes priority when open. It can only open
	// when input is focused (i.e. not streaming), so menu navigation
	// and stream cancellation never compete for the same key.
	if m.commandMenuOpen {
		switch msg.Type {
		case tea.KeyTab:
			if len(m.commandMenuFiltered) > 0 {
				name := m.commandMenuFiltered[m.commandMenuSelected].names[0]
				m.input.SetValue(name + " ")
				m.commandMenuOpen = false
				m.commandMenuFiltered = nil
				m.commandMenuSelected = 0
				// Accepting a command that takes args (e.g. /model)
				// should immediately hand off to its argument picker.
				// updateCommandMenu's state machine reads the new
				// textarea value and opens the model picker when it
				// sees "/model<space>" — without this call the user
				// gets a stuck "/model " with no candidates until
				// the next keystroke.
				m.updateCommandMenu()
			}
			return m, nil
		case tea.KeyEnter:
			// Menu open with candidates: Enter dispatches the highlighted
			// command immediately — typing "/h" + Enter fires /help. This
			// matches user expectation: pressing Enter while a candidate
			// is visibly selected is "submit this thing", not "stage it
			// for editing". Tab still does accept-and-add-space when the
			// user actually wants to edit args (e.g. "/model gpt-4").
			// Zero candidates (user typed "/xxxxx") → fall through so the
			// literal text can be queued/submitted.
			if len(m.commandMenuFiltered) > 0 {
				name := m.commandMenuFiltered[m.commandMenuSelected].names[0]
				m.commandMenuOpen = false
				m.commandMenuFiltered = nil
				m.commandMenuSelected = 0
				m.input.Reset()
				if handled, cmd := dispatchCommand(&m, name); handled {
					return m, cmd
				}
				return m, nil
			}
		case tea.KeyUp:
			if m.commandMenuSelected > 0 {
				m.commandMenuSelected--
			}
			return m, nil
		case tea.KeyDown:
			if m.commandMenuSelected < len(m.commandMenuFiltered)-1 {
				m.commandMenuSelected++
			}
			return m, nil
		case tea.KeyEsc:
			// Menu open & not streaming: Esc = "cancel this slash
			// command entirely" — clears the partial `/foo` text along
			// with the menu. Previous behaviour kept the input contents
			// (the assumption being "user might want to keep typing a
			// literal /path/..."), but in practice users hit Esc to
			// abort the command intent, not to repurpose the leading `/`
			// as plain text; the leftover `/` then looked like residue
			// after submission.
			m.commandMenuOpen = false
			m.commandMenuFiltered = nil
			m.commandMenuSelected = 0
			m.input.Reset()
			return m, nil
		}
		// Other keys fall through to the normal switch (Enter still
		// submits, character keys still type into the textarea — which
		// then refreshes the filter via updateCommandMenu).
	}

	// Model / setup picker. Same key vocabulary as the slash menu /
	// path picker: Tab + Enter accept, Up/Down navigate, Esc dismisses.
	//
	// Two opening paths affect what other keys do:
	//
	//   - /setup picker: fully modal. The textarea is empty (cmdSetup
	//     opens a fresh picker on a clean input), there's nothing for
	//     stray characters to mean.
	//   - /model picker auto-opened from "/model " in updateCommandMenu:
	//     the textarea still holds "/model ..." and the user might want
	//     to back out (Backspace deletes the space → picker closes via
	//     updateCommandMenu's "stale picker" branch → slash menu reopens)
	//     OR type a freeform model id (characters flow through; once
	//     the input no longer starts with "/model ", the picker also
	//     auto-closes).
	//
	// So Backspace and printable characters fall through to the
	// textarea when purpose=="model"; for purpose=="setup-provider"
	// (and any future modal picker) we swallow them to stay modal.
	if m.modelPickerOpen {
		switch msg.Type {
		case tea.KeyTab, tea.KeyEnter:
			if m.pickerPurpose == "review" {
				return m.handleReviewPick()
			}
			m.applyModelChoice(m.modelPickerSelected)
			return m, nil
		case tea.KeyUp:
			if m.modelPickerSelected > 0 {
				m.modelPickerSelected--
			}
			return m, nil
		case tea.KeyDown:
			if m.modelPickerSelected < len(m.modelPickerFiltered)-1 {
				m.modelPickerSelected++
			}
			return m, nil
		case tea.KeyEsc:
			m.modelPickerOpen = false
			m.modelPickerFiltered = nil
			m.modelPickerSelected = 0
			m.pickerPurpose = ""
			return m, nil
		}
		// Auto-opened pickers (/model, /effort, /lang, /review, /skill, /skill use, /help with trailing space):
		// Backspace + printable chars fall through so the user can keep
		// editing the textarea (backspace the space to dismiss, or type
		// a full id to bypass the picker). Modal pickers (e.g. /setup,
		// or Enter-opened with empty input) swallow all other keys.
		// Backspace on an already-empty input is a harmless no-op, so
		// it's safe to allow unconditionally.
		switch m.pickerPurpose {
		case "model", "effort", "lang", "review", "skill-verb", "skill-name", "help-topic":
			switch msg.Type {
			case tea.KeyBackspace, tea.KeyRunes, tea.KeySpace:
				// fall through to textarea Update at the end of handleKey
			default:
				return m, nil
			}
		default:
			// Modal picker (e.g. /setup): swallow all other keys.
			return m, nil
		}
	}

	// Inline mode: PgUp/PgDn/Home/End and the mouse wheel all go to
	// the terminal's native scrollback. We no longer intercept them.

	switch msg.Type {
	case tea.KeyCtrlC:
		(&m).persistSession()
		return m, tea.Quit

	case tea.KeyEsc:
		// Esc in review branch-entry mode cancels without action.
		// Checked BEFORE the streaming branch so a streaming user
		// in branch-entry mode can cancel the entry, not the stream.
		if m.reviewBranchEntry {
			m.reviewBranchEntry = false
			m.input.Reset()
			return m, (&m).appendHistory(styleMuted.Render("  review: cancelled"))
		}
		// Esc only does something when there's an active stream — at
		// rest we leave it alone so the textarea / future overlays can
		// claim it for their own purposes.
		if m.streaming && m.cancelStream != nil {
			m.userCanceled = true
			m.cancelStream()
			// Revoke any plan-execute batch pre-approval. The user
			// pressing Esc means "stop, give me back per-call
			// control" — leaving the gate open across the next
			// prompt would surprise them. The plan task list stays
			// (no auto-revert) so the model can re-arm via
			// plan(start=N) when it resumes work.
			if m.opts.RevokePlanPreApproval != nil {
				m.opts.RevokePlanPreApproval()
			}
			// "Esc stops everything" — clear steer, but restore
			// queued text into the textarea so the user can edit
			// and re-submit (the textarea was already cleared on
			// Enter, so this doesn't overwrite anything).
			if m.queuedText != "" {
				m.input.SetValue(m.queuedText)
				m.queuedText = ""
			}
			m.pendingSteerText = ""
			// Don't clear m.cancelStream here — streamEndMsg will do
			// it after the stream channel actually drains, otherwise
			// the next Esc within the same race window double-cancels.
			return m, nil
		}
		// Esc in setup key-entry mode cancels the wizard without saving.
		// Checked AFTER the streaming branch above so an in-flight
		// stream isn't accidentally bypassed.
		if m.setupKeyEntry {
			cmd := m.cancelSetup()
			return m, cmd
		}
		return m, nil

	case tea.KeyEnter:
		// Review branch-entry mode: Enter submits /review <typed>.
		// Comes BEFORE setupKeyEntry — both set a flag and use the
		// textarea, and review is stateless so there's no ambiguity.
		if m.reviewBranchEntry {
			// Resolve folded paste before reading the textarea value.
			if m.pastedContent != "" {
				marker := fmt.Sprintf("📋 pasted %d lines — press Enter to send", m.pastedLineCount)
				val := m.input.Value()
				if strings.Contains(val, marker) {
					m.input.SetValue(strings.Replace(val, marker, m.pastedContent, 1))
				}
				m.pastedContent = ""
				m.pastedLineCount = 0
			}
			branch := strings.TrimSpace(m.input.Value())
			m.reviewBranchEntry = false
			m.input.Reset()
			if branch == "" {
				return m, (&m).appendHistory(styleErr.Render("review: no branch name entered"))
			}
			// Re-dispatch through dispatchCommand so the existing
			// arg-handling path (/review <branch>) is reused.
			handled, cmd := dispatchCommand(&m, "/review "+branch)
			if !handled {
				return m, (&m).appendHistory(styleErr.Render("review: invalid branch name"))
			}
			return m, cmd
		}
		// Setup key-entry mode: Enter saves the typed key to config
		// and exits setup. Comes BEFORE the streaming branch because
		// /setup can't be opened while streaming (slash menu is
		// closed during streams), so there's no ambiguity.
		if m.setupKeyEntry {
			// Resolve folded paste before reading the textarea value.
			if m.pastedContent != "" {
				marker := fmt.Sprintf("📋 pasted %d lines — press Enter to send", m.pastedLineCount)
				val := m.input.Value()
				if strings.Contains(val, marker) {
					m.input.SetValue(strings.Replace(val, marker, m.pastedContent, 1))
				}
				m.pastedContent = ""
				m.pastedLineCount = 0
			}
			key := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			cmd := m.finishSetup(key)
			return m, cmd
		}
		// Streaming branch: Enter = queue, Alt+Enter = steer.
		// Ctrl+J / Ctrl+Enter inserts a newline — the textarea has
		// InsertNewline bound to "ctrl+j" (model.go:311), so the key
		// falls through to m.input.Update(msg) at the end of handleKey
		// and the textarea handles it natively.
		if m.streaming {
			// Resolve folded paste: replace marker with actual content before sending.
			if m.pastedContent != "" {
				marker := fmt.Sprintf("📋 pasted %d lines — press Enter to send", m.pastedLineCount)
				val := m.input.Value()
				if strings.Contains(val, marker) {
					m.input.SetValue(strings.Replace(val, marker, m.pastedContent, 1))
				}
				m.pastedContent = ""
				m.pastedLineCount = 0
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				// Empty Enter on an empty textarea has no submission
				// meaning, so we reuse it as the "withdraw the pending
				// queue / steer" gesture. Without this, the only way
				// to clear a queued message was Esc, which ALSO
				// cancels the in-flight stream — too coarse when the
				// agent is already 30 seconds into a useful turn.
				if m.queuedText != "" || m.pendingSteerText != "" {
					wasSteer := m.pendingSteerText != ""
					m.queuedText = ""
					m.pendingSteerText = ""
					label := "↰ withdrew queued message"
					if wasSteer {
						label = "↰ withdrew pending steer"
					}
					return m, (&m).appendHistory(styleMuted.Render("  " + label))
				}
				return m, nil
			}
			m.input.Reset()
			// Slash commands are TUI-side, not bound for the agent.
			// Execute immediately rather than queueing/steering —
			// otherwise "/help" while streaming would arrive as the
			// next user message to the model instead of opening the
			// overlay. Commands that genuinely shouldn't run mid-
			// stream (/branch, /compact, /new) already self-reject
			// with a "wait for the current turn to finish" notice.
			if handled, cmd := dispatchCommand(&m, text); handled {
				return m, cmd
			}
			// Consume any armed skill: wraps text with a "Please use the X
			// skill" preamble and clears m.pendingSkill. Done AFTER the
			// slash-dispatch check so slash commands (including the arm
			// command itself) do not eat the arm.
			text = m.consumeArm(text)
			if msg.Alt {
				// Steer: cancel current stream and stash text for
				// streamEndMsg to submit once the channel drains.
				steerStream(&m, text)
			} else {
				// Queue: stash text; streamEndMsg auto-submits when the
				// agent loop reaches its natural end (not userCanceled).
				// Second Enter during the same stream replaces — last
				// thing you said is what you meant.
				m.queuedText = text
			}
			return m, nil
		}
		// Resolve folded paste: replace marker with actual content before sending.
		if m.pastedContent != "" {
			marker := fmt.Sprintf("📋 pasted %d lines — press Enter to send", m.pastedLineCount)
			val := m.input.Value()
			if strings.Contains(val, marker) {
				m.input.SetValue(strings.Replace(val, marker, m.pastedContent, 1))
			}
			m.pastedContent = ""
			m.pastedLineCount = 0
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		if handled, cmd := dispatchCommand(&m, text); handled {
			return m, cmd
		}
		// Consume any armed skill before submit. Mirrors the streaming
		// branch above; slash commands have already short-circuited so
		// they never reach this line and never consume the arm.
		text = m.consumeArm(text)
		return m.submit(text)

	case tea.KeyCtrlL:
		// Ctrl+L mirrors /clear: blank the visible terminal; scrollback
		// above is the terminal's, untouched. No banner re-print, no
		// state reset — same semantics as the shell's `clear`.
		return m, tea.ClearScreen

	case tea.KeyCtrlR:
		m.showReasoning = !m.showReasoning
		return m, nil

	case tea.KeyShiftTab:
		m.cycleMode()
		return m, (&m).appendHistory(styleMuted.Render("  " + m.modeLabel()))

	case tea.KeyUp:
		// History recall — only when the textarea is empty (so it
		// doesn't fight cursor-up in a multi-line draft) OR when
		// we're already navigating history.
		if m.tryHistoryUp() {
			return m, nil
		}

	case tea.KeyDown:
		if m.tryHistoryDown() {
			return m, nil
		}

	}

	// ? hotkey: open the help overlay when the textarea is empty.
	// Matches the documented "/help, /? or ?" key binding in the
	// help overlay. Intercepted before the textarea sees it so a
	// bare '?' opens help rather than typing a literal '?' into the
	// input. When the textarea already has content (e.g. typing a
	// prompt or the slash menu is open) the key passes through to
	// let the textarea handle it normally.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == '?' {
		if m.input.Value() == "" {
			cmdHelp(&m, "")
			return m, nil
		}
	}

	// Everything else: feed the textarea. The completion affordances (/
	// menu and @ path picker) are pure display — keep them live during
	// streaming too, since a queued/steered message routinely references
	// files ("also look at @internal/server.go") or invokes a TUI-side
	// slash command ("/help" to peek at bindings without stopping the
	// agent). Commands that genuinely don't apply mid-stream (/branch,
	// /compact, /new, …) self-reject at dispatch time with their own
	// "wait for the current turn to finish" notice.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateCommandMenu()
	m.updatePathCompleter()

	// Paste folding: when pasted content exceeds textarea height, fold
	// it into a compact placeholder to keep the small textarea manageable.
	// Only fires on paste events (msg.Paste) so normal typing never
	// triggers it.
	if msg.Paste {
		m = m.handlePasteFolding()
	}

	return m, cmd
}

// cycleMode advances through the three permission modes in order:
// Ask → Plan → Yolo → Ask → ...
// Triggered by Shift+Tab.
func (m *Model) cycleMode() {
	switch {
	case m.opts.Yolo:
		// Yolo → Ask: turn off yolo, stay in Ask
		m.opts.Yolo = false
		m.opts.Plan = false
		m.opts.PlanSubstate = ""
		if m.opts.SetYolo != nil {
			m.opts.SetYolo(false)
		}
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(false)
		}
		if m.opts.SetPlanSubstate != nil {
			m.opts.SetPlanSubstate("")
		}
	case m.opts.Plan:
		// Plan → Yolo: turn off plan, turn on yolo. Clear the substate
		// so it doesn't leak into Yolo or the next Ask→Plan entry.
		m.opts.Plan = false
		m.opts.Yolo = true
		m.opts.PlanSubstate = ""
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(false)
		}
		if m.opts.SetYolo != nil {
			m.opts.SetYolo(true)
		}
		if m.opts.SetPlanSubstate != nil {
			m.opts.SetPlanSubstate("")
		}
	default:
		// Ask → Plan: turn on plan and start in the analyze substate,
		// matching cmdPlan's contract (PRD §2.1).
		m.opts.Plan = true
		m.opts.Yolo = false
		m.opts.PlanSubstate = "analyze"
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(true)
		}
		if m.opts.SetPlanSubstate != nil {
			m.opts.SetPlanSubstate("analyze")
		}
	}
	m.refreshPlaceholder()
}

// modeLabel returns a human-readable label for the current permission
// mode, used by Shift+Tab feedback.
func (m *Model) modeLabel() string {
	switch {
	case m.opts.Yolo:
		return "mode: yolo"
	case m.opts.Plan:
		return "mode: plan"
	default:
		return "mode: ask"
	}
}

// tryHistoryUp moves backwards through the prompt history. Returns
// true if the input was updated (so the caller skips forwarding the
// key to the textarea), false to fall through to normal cursor-up.
//
// Eligibility: textarea empty OR we're already in history-nav mode.
// Without this guard, ↑ in a multi-line draft would clobber the
// draft.
func (m *Model) tryHistoryUp() bool {
	if len(m.promptHistory) == 0 {
		return false
	}
	if m.historyIdx == -1 {
		if strings.TrimSpace(m.input.Value()) != "" {
			return false
		}
		m.savedDraft = m.input.Value()
		m.historyIdx = len(m.promptHistory) - 1
		m.input.SetValue(m.promptHistory[m.historyIdx])
		return true
	}
	if m.historyIdx > 0 {
		m.historyIdx--
		m.input.SetValue(m.promptHistory[m.historyIdx])
	}
	return true
}

// tryHistoryDown is the mirror — moves forwards, restoring the saved
// draft once we pass the end of history.
func (m *Model) tryHistoryDown() bool {
	if m.historyIdx == -1 {
		return false
	}
	if m.historyIdx < len(m.promptHistory)-1 {
		m.historyIdx++
		m.input.SetValue(m.promptHistory[m.historyIdx])
	} else {
		m.historyIdx = -1
		m.input.SetValue(m.savedDraft)
		m.savedDraft = ""
	}
	return true
}

// handlePasteFolding checks if the textarea content exceeds its visible
// height and, if so, replaces the display with a compact marker placeholder.
// The full content is preserved in m.pastedContent and is substituted at
// submit time (Enter) when the marker is replaced with the actual pasted
// content.
//
// Only called on paste events (msg.Paste == true) so normal typing or
// Ctrl+J newlines never trigger folding.
func (m Model) handlePasteFolding() Model {
	val := m.input.Value()
	lines := strings.Count(val, "\n") + 1
	if lines > m.input.Height() {
		m.pastedContent = val
		m.pastedLineCount = lines
		m.input.Reset()
		m.input.SetValue(fmt.Sprintf(
			"📋 pasted %d lines — press Enter to send", lines,
		))
	}
	return m
}
