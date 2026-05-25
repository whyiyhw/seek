package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// completionRe extracts "completion <number>" from think tool result text.
// formatResult in internal/tools/think produces:
//
//	usage: prompt 956, completion 2345, cache hit 0 / miss 956
//
// This is a TUI-level coupling to the think tool's output format — keep in
// sync if formatResult changes.
var completionRe = regexp.MustCompile(`completion (\d+)`)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.relayout()

	case tea.KeyMsg:
		// Inline mode has no in-process scrollable region — the
		// terminal does that natively. So we don't intercept PgUp/PgDn
		// for an internal viewport; they go to the terminal's
		// scrollback like in any normal shell.
		return m.handleKey(msg)

	case agentEventMsg:
		// applyAgentEvent may emit Println commands for committed
		// content. We collect them and let Bubble Tea write them above
		// the live region.
		printCmds := m.applyAgentEvent(msg.Event)
		cmds = append(cmds, printCmds...)
		cmds = append(cmds, waitForAgentEvent(m.stream))

	case streamEndMsg:
		m.streaming = false
		m.stream = nil
		m.input.Focus()
		// Stream ended — turn count / cache ratio changed; pick a new
		// placeholder so the user sees a fresh hint when the input
		// blinks back into focus.
		m.refreshPlaceholder()
		// Snapshot the current agent state into the session and
		// persist. Failures are logged via a system Println rather
		// than fatal — losing a save is annoying but shouldn't tear
		// down the TUI.
		m.persistSession()
		if m.cancelStream != nil {
			m.cancelStream()
			m.cancelStream = nil
		}
		// If the user pressed Esc, commit a visible interrupt notice
		// so it's clear something was aborted (any partial assistant
		// text already in m.curContent is still committable, but for
		// simplicity we drop it — the next prompt can re-ask).
		wasCanceled := m.userCanceled
		if m.userCanceled {
			cmds = append(cmds, tea.Println(styleMuted.Render("  ↰ interrupted")))
			m.scrollbackLines++
			m.userCanceled = false
		}
		// At this point all completed messages are already in
		// scrollback (committed during MessageEnd/ToolExecEnd). Clear
		// any residual live state.
		m.curContent = ""
		m.curReasoning = ""
		m.activeTools = nil
		// Restore the user's last submitted prompt into the textarea
		// after an Esc-cancelled stream, so they can edit and re-send.
		// Only when the textarea is still empty — the Esc handler may
		// have already restored queuedText into it.
		if wasCanceled && m.input.Value() == "" && len(m.promptHistory) > 0 {
			m.input.SetValue(m.promptHistory[len(m.promptHistory)-1])
		}

		// Queue / steer dispatch — exactly one of these fires per
		// streamEndMsg, in priority order.
		//
		// pendingSteerText: set by Alt+Enter during the previous
		// stream; that path already called cancelStream(), so we
		// arrive here promptly. Repair() is implicit — the agent
		// loop's invariant check already dropped the half-baked
		// turn before we got here.
		//
		// queuedText: set by Enter during the previous stream. Only
		// auto-fires when the stream ended naturally (not via Esc),
		// otherwise the user's "Esc stops everything" expectation
		// would be violated.
		// Queue / steer dispatch — exactly one of these fires per
		// streamEndMsg, in priority order. We capture submit()'s
		// new Model so the streaming=true / streamStartTime reset
		// it performs isn't lost when this case returns.
		switch {
		case m.pendingSteerText != "":
			text := m.pendingSteerText
			m.pendingSteerText = ""
			m.queuedText = "" // a steer supersedes any queue
			cmds = append(cmds, tea.Println(styleMuted.Render("  ↪ steered")))
			newM, cmd := m.submit(text)
			m2 := newM.(Model)
			m2.scrollbackLines++
			newM = m2
			cmds = append(cmds, cmd)
			return newM, tea.Batch(cmds...)
		case m.queuedText != "":
			text := m.queuedText
			m.queuedText = ""
			cmds = append(cmds, tea.Println(styleMuted.Render("  ↪ "+truncateOneLine(text, 60))))
			newM, cmd := m.submit(text)
			m2 := newM.(Model)
			m2.scrollbackLines++
			newM = m2
			cmds = append(cmds, cmd)
			return newM, tea.Batch(cmds...)
		}

	case statusTickMsg:
		m.now = time.Now()
		// A minute passed — off-peak window may have just opened or
		// closed. Repick the placeholder so e.g. "🌙 off-peak" shows
		// up the moment the discount kicks in.
		m.refreshPlaceholder()
		cmds = append(cmds, tickStatusEvery(time.Minute))

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)

	case approvalRequestMsg:
		// New approval prompt — grab focus.
		req := msg.req
		m.pendingApproval = &req
		m.input.Blur()

	case askUserRequestMsg:
		// New ask_user request — grab focus and reset picker state.
		req := msg.req
		m.pendingQuestion = &req
		m.pendingQuestionSelected = map[int]bool{}
		m.pendingQuestionCursor = 0
		m.pendingQuestionFreeText = false
		// Single-select picker uses keyboard nav, not textarea; blur.
		// We re-focus when the user picks "Other" so they can type.
		m.input.Blur()

	case compactDoneMsg:
		cmds = append(cmds, m.handleCompactDone(msg)...)

	case distillDoneMsg:
		cmds = append(cmds, m.handleDistillDone(msg)...)

	case observeDoneMsg:
		cmds = append(cmds, m.handleObserveDone(msg)...)
		if m.opts.ObserveResultChan != nil {
			cmds = append(cmds, waitForObserveResult(m.opts.ObserveResultChan))
		}

	case versionCheckDoneMsg:
		// Store the newer tag for the status-bar segment. Idempotent —
		// the cmd never re-fires within a session, but a second run
		// after /upgrade would simply overwrite this field.
		m.upgradeAvailable = msg.NewerTag

	case upgradeDoneMsg:
		cmds = append(cmds, m.handleUpgradeDone(msg)...)

	case cleanupToolMsg:
		for i, t := range m.activeTools {
			if t.callID == msg.callID {
				m.activeTools = append(m.activeTools[:i], m.activeTools[i+1:]...)
				break
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Help overlay grabs keys when open — user can dismiss it.
	if m.helpOverlayOpen {
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyEnter:
			m.helpOverlayOpen = false
			return m, nil
		case tea.KeyRunes:
			// Use case-insensitive check for "q" close.
			if len(msg.Runes) == 1 && (msg.Runes[0] == 'q' || msg.Runes[0] == 'Q') {
				m.helpOverlayOpen = false
				return m, nil
			}
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
			// Menu open with candidates: Enter accepts the highlighted
			// command, same as Tab — avoids the surprise where typing
			// "/h" + Enter submits the literal string "/h" instead of
			// the /help command the menu was clearly suggesting. With
			// zero candidates (user typed "/xxxxx"), fall through so
			// they can still queue / submit the literal text.
			if len(m.commandMenuFiltered) > 0 {
				name := m.commandMenuFiltered[m.commandMenuSelected].names[0]
				m.input.SetValue(name + " ")
				m.commandMenuOpen = false
				m.commandMenuFiltered = nil
				m.commandMenuSelected = 0
				// Same handoff as the Tab branch above — see comment there.
				m.updateCommandMenu()
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
			// Menu open & not streaming: Esc closes the menu without
			// affecting input contents.
			m.commandMenuOpen = false
			m.commandMenuFiltered = nil
			m.commandMenuSelected = 0
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
		// Auto-opened pickers (/model, /effort, /lang with trailing space):
		// Backspace + printable chars fall through so the user can keep
		// editing the textarea (backspace the space to dismiss, or type
		// a full id to bypass the picker). Modal pickers (e.g. /setup,
		// or Enter-opened with empty input) swallow all other keys.
		// Backspace on an already-empty input is a harmless no-op, so
		// it's safe to allow unconditionally.
		switch m.pickerPurpose {
		case "model", "effort", "lang":
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

	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit

	case tea.KeyEsc:
		// Esc in review branch-entry mode cancels without action.
		// Checked BEFORE the streaming branch so a streaming user
		// in branch-entry mode can cancel the entry, not the stream.
		if m.reviewBranchEntry {
			m.reviewBranchEntry = false
			m.input.Reset()
			return m, tea.Println(styleMuted.Render("  review: cancelled"))
		}
		// Esc only does something when there's an active stream — at
		// rest we leave it alone so the textarea / future overlays can
		// claim it for their own purposes.
		if m.streaming && m.cancelStream != nil {
			m.userCanceled = true
			m.cancelStream()
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
			branch := strings.TrimSpace(m.input.Value())
			m.reviewBranchEntry = false
			m.input.Reset()
			if branch == "" {
				return m, tea.Println(styleErr.Render("review: no branch name entered"))
			}
			// Re-dispatch through dispatchCommand so the existing
			// arg-handling path (/review <branch>) is reused.
			handled, cmd := dispatchCommand(&m, "/review "+branch)
			if !handled {
				return m, tea.Println(styleErr.Render("review: invalid branch name"))
			}
			return m, cmd
		}
		// Setup key-entry mode: Enter saves the typed key to config
		// and exits setup. Comes BEFORE the streaming branch because
		// /setup can't be opened while streaming (slash menu is
		// closed during streams), so there's no ambiguity.
		if m.setupKeyEntry {
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
					m.scrollbackLines++
					return m, tea.Println(styleMuted.Render("  " + label))
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
		// "clear" no longer maps to a viewport reset — in inline mode
		// the terminal owns the scrollback. We print a clear-screen
		// escape via tea.ClearScreen, then redraw.
		return m, tea.ClearScreen

	case tea.KeyCtrlR:
		m.showReasoning = !m.showReasoning
		return m, nil

	case tea.KeyShiftTab:
		m.cycleMode()
		m.scrollbackLines++
		return m, tea.Println(styleMuted.Render("  " + m.modeLabel()))

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
		if m.opts.SetYolo != nil {
			m.opts.SetYolo(false)
		}
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(false)
		}
	case m.opts.Plan:
		// Plan → Yolo: turn off plan, turn on yolo
		m.opts.Plan = false
		m.opts.Yolo = true
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(false)
		}
		if m.opts.SetYolo != nil {
			m.opts.SetYolo(true)
		}
	default:
		// Ask → Plan: turn on plan
		m.opts.Plan = true
		m.opts.Yolo = false
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(true)
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

// updateCommandMenu recomputes the slash-command dropdown state from
// the current input value. Called after every textarea-bound key.
//
// State machine:
//
//	"/"             → command menu (filters as you type)
//	"/model "       → model picker (typing "/model<space>" hands off
//	                  to the model dropdown; the user no longer needs
//	                  to commit with Enter just to see what's available)
//	anything else   → both closed
//
// The handoff at "/model " keeps the screen from going visually
// empty in the half-second between "/" menu (closes on first space)
// and an Enter that opens the model picker explicitly.
func (m *Model) updateCommandMenu() {
	v := strings.TrimRight(m.input.Value(), "\n")

	// Branch 1: "<cmd><space>..." — auto-open pickers for model / effort / lang.
	// Close any open command menu first; they're mutually exclusive.
	// Stale cleanup (Branch 2 below) handles closing when the user backspaces.
	switch {
	case strings.HasPrefix(v, "/model ") || v == "/model ":
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "model" {
			m.modelPickerFiltered = knownModelsForProvider(m.opts.ProviderName)
			if len(m.modelPickerFiltered) == 0 {
				return
			}
			m.modelPickerSelected = 0
			for i, mc := range m.modelPickerFiltered {
				if mc.id == m.opts.Model {
					m.modelPickerSelected = i
					break
				}
			}
			m.modelPickerOpen = true
			m.pickerPurpose = "model"
		}
		return

	case strings.HasPrefix(v, "/effort ") || v == "/effort ":
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "effort" {
			choices := effortChoices()
			m.modelPickerFiltered = choices
			m.modelPickerSelected = 0
			current := m.opts.Effort
			if current == "" {
				current = "off"
			}
			for i, c := range choices {
				if c.id == current {
					m.modelPickerSelected = i
					break
				}
			}
			m.modelPickerOpen = true
			m.pickerPurpose = "effort"
		}
		return

	case strings.HasPrefix(v, "/lang ") || v == "/lang ":
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "lang" {
			choices := langChoices()
			m.modelPickerFiltered = choices
			m.modelPickerSelected = 0
			current := m.opts.Lang
			if current == "" {
				current = "auto"
			}
			for i, c := range choices {
				if c.id == current {
					m.modelPickerSelected = i
					break
				}
			}
			m.modelPickerOpen = true
			m.pickerPurpose = "lang"
		}
		return

	case strings.HasPrefix(v, "/review "):
		// Auto-open the review picker when the user types "/review "
		// (trailing space). Mirrors the /model /effort /lang pattern.
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "review" {
			choices := reviewChoices(m.opts.CWD)
			if len(choices) == 0 {
				return
			}
			m.modelPickerFiltered = choices
			m.modelPickerSelected = 0
			m.modelPickerOpen = true
			m.pickerPurpose = "review"
		}
		return

	// `/skill use <partial>` — second-level picker for loaded skill
	// names. Check this BEFORE the `/skill ` branch below: prefix
	// "/skill use " also matches "/skill " naively, and we want the
	// name picker to win once the user has committed to the `use`
	// verb. We close the picker once the user types a space after the
	// name (i.e. they're moving on to the inline task), so candidates
	// don't keep showing up under unrelated text.
	case strings.HasPrefix(v, "/skill use ") || v == "/skill use ":
		tail := strings.TrimPrefix(v, "/skill use ")
		if strings.Contains(tail, " ") {
			// User has moved past the name into the inline-task
			// position. Close any stale name picker and fall through
			// so the live region returns to plain composition.
			if m.modelPickerOpen && m.pickerPurpose == "skill-name" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			m.commandMenuOpen = false
			m.commandMenuFiltered = nil
			m.commandMenuSelected = 0
			return
		}
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		all := skillNameChoices(m.opts.Skills)
		filtered := filterChoicesByPrefix(all, tail)
		if len(filtered) == 0 {
			// Nothing to pick. Close any prior open picker; the user
			// keeps typing into a plain textarea (cmdSkillUse will
			// give a clear error if they Enter with an unknown name).
			if m.modelPickerOpen && m.pickerPurpose == "skill-name" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			return
		}
		m.modelPickerFiltered = filtered
		// Reset selection to top — keystrokes that change the filter
		// shouldn't strand the highlight on a row that's no longer
		// in the candidate set.
		m.modelPickerSelected = 0
		m.modelPickerOpen = true
		m.pickerPurpose = "skill-name"
		return

	// `/skill <verb-partial>` — first-level picker for sub-verbs.
	// Triggered by the space after `/skill`; closes once the user has
	// typed past the verb (any second space). The handoff into the
	// name picker for `use` happens on the next updateCommandMenu
	// cycle: applyModelChoice replaces the textarea contents with
	// "/skill use " which then matches the branch above.
	case strings.HasPrefix(v, "/skill ") || v == "/skill ":
		tail := strings.TrimPrefix(v, "/skill ")
		if strings.Contains(tail, " ") {
			// Past the verb — close.
			if m.modelPickerOpen && m.pickerPurpose == "skill-verb" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			m.commandMenuOpen = false
			m.commandMenuFiltered = nil
			m.commandMenuSelected = 0
			return
		}
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		filtered := filterChoicesByPrefix(skillVerbChoices(), tail)
		if len(filtered) == 0 {
			if m.modelPickerOpen && m.pickerPurpose == "skill-verb" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			return
		}
		m.modelPickerFiltered = filtered
		m.modelPickerSelected = 0
		m.modelPickerOpen = true
		m.pickerPurpose = "skill-verb"
		return
	}

	// Branch 2: not in a known auto-open state but a stale auto-opened picker
	// is still showing (e.g. user backspaced the space). Close it.
	if m.modelPickerOpen && (m.pickerPurpose == "model" || m.pickerPurpose == "effort" || m.pickerPurpose == "lang" || m.pickerPurpose == "review" || m.pickerPurpose == "skill-verb" || m.pickerPurpose == "skill-name") {
		m.modelPickerOpen = false
		m.modelPickerFiltered = nil
		m.modelPickerSelected = 0
		m.pickerPurpose = ""
	}

	// Branch 3: standard slash-command menu (no space yet).
	if !strings.HasPrefix(v, "/") || strings.Contains(v, " ") {
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		return
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = filterCommands(allCommands(), v)
	if m.commandMenuSelected >= len(m.commandMenuFiltered) {
		m.commandMenuSelected = 0
	}
}

// filterCommands returns the subset of cmds whose canonical name OR
// any alias starts with prefix. Empty prefix → return everything.
// Order is preserved (allCommands() order is intentional).
func filterCommands(cmds []command, prefix string) []command {
	if prefix == "" || prefix == "/" {
		return cmds
	}
	var out []command
	for _, c := range cmds {
		for _, name := range c.names {
			if strings.HasPrefix(name, prefix) {
				out = append(out, c)
				break
			}
		}
	}
	return out
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

// submit kicks off an agent.Prompt for the given user text. Before
// streaming begins we commit the user's message to scrollback via
// tea.Println so it survives in native terminal history.
//
// We derive a cancelable context from opts.Ctx so Esc can cancel the
// in-flight call without tearing down the outer SIGINT context.
func (m Model) submit(text string) (tea.Model, tea.Cmd) {
	m.promptHistory = append(m.promptHistory, text)
	m.historyIdx = -1
	m.savedDraft = ""

	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
	m.userCanceled = false
	m.streaming = true
	m.streamStartTime = time.Now()
	m.streamDeltaBytes = 0
	// Leave the textarea focused — the user may want to type a
	// queue/steer message during the stream (see handleKey's
	// streaming branch on KeyEnter).

	ctx, cancel := context.WithCancel(m.opts.Ctx)
	m.cancelStream = cancel

	ch := m.opts.Agent.Prompt(ctx, text)
	m.stream = ch

	printUser := renderCommittedUser(text, m.width)
	m.scrollbackLines += scrollbackLineCount(printUser)
	return m, tea.Batch(tea.Println(printUser), waitForAgentEvent(ch))
}

// applyAgentEvent updates Model state for an agent event and returns
// any tea.Cmds needed to commit content to scrollback. Pointer receiver
// so we can mutate m without returning a copy.
func (m *Model) applyAgentEvent(ev agent.Event) []tea.Cmd {
	var cmds []tea.Cmd

	switch e := ev.(type) {
	case agent.AgentStart, agent.TurnStart:
		// no UI change
	case agent.MessageStart:
		// A new message is starting — discard any residual live state
		// from a prior failed attempt (e.g. stream_error that triggered
		// an agent-level retry of the same turn).
		m.curContent = ""
		m.curReasoning = ""

	case agent.MessageDelta:
		if e.Reasoning {
			m.curReasoning += e.Delta
		} else {
			m.curContent += e.Delta
			m.streamDeltaBytes += len(e.Delta)
		}

	case agent.MessageEnd:
		if e.Message.Role == deepseek.RoleAssistant {
			// Only commit a `▸ seek` scrollback block when the model
			// actually produced narrative text. Pure tool-call turns
			// (reasoning + tool_calls, no content) used to render as
			// "(no content)" — that's noise: the `↳ tool(...)` lines
			// directly below already convey what happened on that
			// turn, and the reasoning was visible mid-stream via
			// Ctrl+R. Skipping the commit collapses N consecutive
			// silent reasoning rounds into just their tool lines.
			if m.curContent != "" {
				rendered := renderMarkdown(m.md, m.curContent)
				if rendered == "" {
					rendered = m.curContent
				}
				line := renderCommittedAssistant(rendered, m.curReasoning, m.showReasoning, m.width)
				cmds = append(cmds, tea.Println(line))
				m.scrollbackLines += scrollbackLineCount(line)
			}
			// Always reset the live-region buffers — leaving them
			// populated would leak this turn's reasoning/content into
			// the next turn's commit and produce a confusing splice.
			m.curContent = ""
			m.curReasoning = ""
		}
		// MessageEnd for RoleTool: skip, ToolExecEnd already committed.

	case agent.ToolExecStart:
		m.activeTools = append(m.activeTools, activeTool{
			callID:  e.CallID,
			name:    e.Name,
			args:    truncateOneLine(e.Args, 80),
			started: time.Now(),
		})

	case agent.ToolDelta:
		// Streaming output from a long-running tool (currently only
		// `think`). Routed into the same live-region buffers the
		// chat model uses — sequential agent dispatch makes the
		// aliasing safe:
		//   1. chat model streams content → MessageEnd commits +
		//      clears the buffers
		//   2. then tool dispatch loop runs → ToolDelta refills the
		//      buffers with tool output
		//   3. ToolExecEnd clears the buffers again before the next
		//      chat turn streams in
		// NOTE: this aliasing breaks the moment parallel tool dispatch
		// lands (see PRD §3.1 — currently flagged as a post-v1.0 item:
		// "M1: sequential tool dispatch. Parallel via errgroup lands
		// with the parallel-execution work in a later milestone"). At
		// that point ToolDelta routing must move to a per-CallID live
		// region keyed off m.activeTools.
		if e.Reasoning {
			m.curReasoning += e.Delta
		} else {
			m.curContent += e.Delta
		}

	case agent.ToolExecEnd:
		// If this was a streaming tool (only `think` today), the live
		// buffers hold what we showed during execution; the tool's
		// full result is about to land in scrollback as a single
		// committed line, so the live preview is now stale and would
		// bleed into the next chat turn's display.
		//
		// Unconditional clear is intentional: a non-streaming tool
		// leaves both buffers empty (nothing ever pushed via
		// ToolDelta), so the assignment is a no-op rather than a
		// bug. Cheaper than adding a "did we stream?" flag.
		m.curContent = ""
		m.curReasoning = ""
		// ToolExecEnd carries Name/Result/Err but not Args/started —
		// look both back up from the active list.
		var (
			args             string
			duration         time.Duration
			completionTokens int
		)
		for i, t := range m.activeTools {
			if t.callID == e.CallID {
				args = t.args
				duration = time.Since(t.started)
				// Parse completion tokens from result and set on the
				// active tool so View() can show them for one frame
				// before cleanupToolMsg removes it.
				if e.Result != "" {
					matches := completionRe.FindStringSubmatch(e.Result)
					if len(matches) >= 2 {
						fmt.Sscanf(matches[1], "%d", &completionTokens)
						if completionTokens > 0 {
							m.activeTools[i].completionTokens = completionTokens
						}
					}
				}
				// Defer removal — queue cleanup for next frame so
				// View() renders the final token count once.
				cmds = append(cmds, func() tea.Msg {
					return cleanupToolMsg{callID: e.CallID}
				})
				break
			}
		}
		var line string
		tokenTail := formatTokenTail(completionTokens)
		if e.Err != nil {
			line = renderCommittedToolErr(e.Name, args, e.Err.Error(), duration)
		} else {
			line = renderCommittedToolOk(e.Name, args, e.Result, duration, tokenTail)
		}
		cmds = append(cmds, tea.Println(line))
		m.scrollbackLines += scrollbackLineCount(line)

	case agent.TurnEnd:
		m.turns++
		// Lock in cost at the (model, tier) active when the turn settled
		// — see internal/cache doc. Using m.opts.Model rather than a
		// model captured at TurnStart is a deliberate small race: a
		// /model switch while the previous turn is still streaming
		// would mis-attribute that turn's cost. /model is gated to
		// non-streaming state in handleKey so the race is mostly
		// theoretical; the alternative (carry model on TurnEnd events)
		// would propagate plumbing into pkg/agent for negligible win.
		m.opts.Tracker.Record(e.Usage, m.opts.Model, pricing.CurrentTier(time.Now()))
		m.toolCalls += e.ToolCalls

	case agent.AgentEnd:
		// Stats footer at turn boundary — print a thin separator with
		// running totals so a long session has visible "checkpoints"
		// in the scrollback.
		cmds = append(cmds, tea.Println(m.renderTurnFooter()))
		m.scrollbackLines++

	case agent.ErrorEvent:
		m.lastErr = e.Err
		errLine := styleErr.Render("  ! error: " + e.Err.Error())
		cmds = append(cmds, tea.Println(errLine))
		m.scrollbackLines += scrollbackLineCount(errLine)
	}

	return cmds
}

// handleCompactDone reacts to the summariser's response: on success,
// forks the session to preserve the full history, then resets the
// agent to a two-message summary so the conversation can continue
// with a fresh context window.
//
// Session chain after compact:
//
//	<old-id>  (full history, preserved on disk) ← ParentID
//	<new-id>  (summary pair, active session)    → continues here
//
// The chain is traversable via --resume and visible in --list, so no
// history is ever permanently lost.
func (m *Model) handleCompactDone(msg compactDoneMsg) []tea.Cmd {
	if msg.err != nil {
		line := styleErr.Render("  ! compact failed: " + msg.err.Error())
		m.scrollbackLines += scrollbackLineCount(line)
		return []tea.Cmd{tea.Println(line)}
	}
	if m.opts.Agent == nil {
		return nil
	}
	// Fold the summariser's own usage into the cumulative tracker so
	// the cost line in the status bar stays honest — the compact call
	// is a real billed request.
	if m.opts.Tracker != nil {
		m.opts.Tracker.Record(msg.usage, m.opts.Model, pricing.CurrentTier(time.Now()))
	}

	var snapshotID string

	// When session persistence is active: flush the full history to disk
	// under the current session ID (the "snapshot"), then fork a child
	// session that will hold only the compact summary. This preserves every
	// message ever exchanged and makes the chain inspectable via --list.
	if m.opts.Session != nil && m.opts.Store != nil {
		// 1. Write full history to the current (snapshot) session.
		m.persistSession()
		snapshotID = m.opts.Session.ID

		// 2. Fork: new session, ParentID → snapshot. Counters reset to 0
		//    so the child's stats reflect only post-compact activity.
		child := m.opts.Session.Fork()
		m.opts.Session = child
		m.turns = 0
		m.toolCalls = 0
	}

	// The user→assistant bootstrap pair is what upstream pi / Claude
	// Code does: the next real user message slots in cleanly after an
	// assistant turn, no role-ordering weirdness for the API.
	m.opts.Agent.Reset([]deepseek.Message{
		{
			Role:    deepseek.RoleUser,
			Content: "Here is a summary of our earlier conversation. Continue from this context:\n\n" + msg.summary,
		},
		{
			Role:    deepseek.RoleAssistant,
			Content: "Understood — I have the context. Ready to continue.",
		},
	})

	// 3. Write summary messages to the child session.
	m.persistSession()

	var notice string
	if snapshotID != "" && m.opts.Session != nil {
		notice = fmt.Sprintf(
			"compacted: snapshot %s → continuing on %s (%d prompt + %d completion tokens)",
			snapshotID, m.opts.Session.ID,
			msg.usage.PromptTokens, msg.usage.CompletionTokens)
	} else {
		// --no-save: no fork, just report token cost.
		notice = fmt.Sprintf(
			"compacted: history replaced with summary (%d prompt + %d completion tokens)",
			msg.usage.PromptTokens, msg.usage.CompletionTokens)
	}
	m.scrollbackLines += scrollbackLineCount(styleMuted.Render(notice))
	return []tea.Cmd{tea.Println(styleMuted.Render(notice))}
}

// persistSession snapshots the agent's current message history,
// counters, and usage into Options.Session and saves via Options.Store.
// No-op when either is nil (ephemeral run / --no-save).
func (m *Model) persistSession() {
	if m.opts.Session == nil || m.opts.Store == nil || m.opts.Agent == nil {
		return
	}
	m.opts.Session.Messages = m.opts.Agent.Messages()
	m.opts.Session.Turns = m.turns
	m.opts.Session.ToolCalls = m.toolCalls
	m.opts.Session.Usage = m.opts.Tracker.Cumulative()
	m.opts.Session.Model = m.opts.Model
	m.opts.Session.Yolo = m.opts.Yolo
	m.opts.Session.Effort = m.opts.Effort
	if err := m.opts.Store.Save(m.opts.Session); err != nil {
		// Surface to scrollback so the user knows persistence broke;
		// they can keep working but should investigate before losing
		// the session.
		// We can't tea.Println from a pointer-receiver helper without
		// returning a Cmd — so just stash on lastErr and let View
		// pick it up on next render.
		m.lastErr = err
	}
}

// truncateOneLine collapses newlines and clips the result to n chars.
// Uses rune-level slicing so multi-byte UTF-8 characters (Chinese, emoji,
// etc.) are never split mid-sequence — see docs/pitfalls.md "s[:n] on a
// multi-byte UTF-8 string produces broken runes".
func truncateOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func (m Model) renderTurnFooter() string {
	c := m.opts.Tracker.Last()
	// Cost is the locked-in amount from when this turn was recorded —
	// not a re-derivation against the current model/tier. Avoids the
	// "switched model mid-turn → footer shows wrong dollars" race.
	cost := pricing.FormatCost(m.opts.Tracker.LastCost())

	var cacheNote string
	if c.PromptTokens > 0 && c.PromptCacheHitTokens > 0 {
		pct := int(float64(c.PromptCacheHitTokens) / float64(c.PromptTokens) * 100)
		cacheNote = fmt.Sprintf(" (%d%% cache)", pct)
	}

	return styleMuted.Render(fmt.Sprintf(
		"  · turn %d · %d tools · ↑%s prompt%s · ↓%s tok · %s",
		m.turns, m.toolCalls,
		formatTokensK(c.PromptTokens), cacheNote,
		formatTokensK(c.CompletionTokens), cost))
}
