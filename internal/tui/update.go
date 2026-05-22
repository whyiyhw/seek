package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

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
		if m.userCanceled {
			cmds = append(cmds, tea.Println(styleMuted.Render("  ↰ interrupted")))
			m.userCanceled = false
		}
		// At this point all completed messages are already in
		// scrollback (committed during MessageEnd/ToolExecEnd). Clear
		// any residual live state.
		m.curContent = ""
		m.curReasoning = ""
		m.activeTools = nil

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
			cmds = append(cmds, cmd)
			return newM, tea.Batch(cmds...)
		case m.queuedText != "":
			text := m.queuedText
			m.queuedText = ""
			cmds = append(cmds, tea.Println(styleMuted.Render("  ↪ "+truncateOneLine(text, 60))))
			newM, cmd := m.submit(text)
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

	case compactDoneMsg:
		cmds = append(cmds, m.handleCompactDone(msg)...)

	case versionCheckDoneMsg:
		// Store the newer tag for the status-bar segment. Idempotent —
		// the cmd never re-fires within a session, but a second run
		// after /upgrade would simply overwrite this field.
		m.upgradeAvailable = msg.NewerTag

	case upgradeDoneMsg:
		cmds = append(cmds, m.handleUpgradeDone(msg)...)
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
		// auto-opened /model picker: Backspace + printable chars fall
		// through so the user can keep editing the textarea.
		if m.pickerPurpose == "model" {
			switch msg.Type {
			case tea.KeyBackspace, tea.KeyRunes, tea.KeySpace:
				// fall through to textarea Update at the end of handleKey
			default:
				return m, nil
			}
		} else {
			// Modal picker (e.g. /setup): swallow all other keys.
			return m, nil
		}
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit

	case tea.KeyEsc:
		// Esc only does something when there's an active stream — at
		// rest we leave it alone so the textarea / future overlays can
		// claim it for their own purposes.
		if m.streaming && m.cancelStream != nil {
			m.userCanceled = true
			m.cancelStream()
			// "Esc stops everything" — also clear queued/pending steer
			// so the user isn't surprised by a follow-up prompt firing
			// from a queue they thought they cancelled.
			m.queuedText = ""
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
		// (Ctrl+Enter and Ctrl+J retain their textarea-newline behaviour
		// because the textarea sees those events directly via its own
		// Update — Bubble Tea's KeyMsg.Type for them is NOT KeyEnter.)
		if m.streaming {
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			// Clear the paste-fold flag too: if the user just folded a
			// long paste and hit Enter, the textarea is reset but the
			// View() still renders the "[pasted N lines, hidden]"
			// placeholder because it keys off m.pastedContent.
			m.pastedContent = ""
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
			if msg.Alt {
				// Steer: cancel current stream and stash text for
				// streamEndMsg to submit once the channel drains.
				m.pendingSteerText = text
				if m.cancelStream != nil {
					m.userCanceled = false
					m.cancelStream()
				}
			} else {
				// Queue: stash text; streamEndMsg auto-submits when the
				// agent loop reaches its natural end (not userCanceled).
				// Second Enter during the same stream replaces — last
				// thing you said is what you meant.
				m.queuedText = text
			}
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		// See the streaming-branch note above: the paste-fold flag
		// would otherwise outlive the submit and re-render the
		// placeholder on the next frame.
		m.pastedContent = ""
		if handled, cmd := dispatchCommand(&m, text); handled {
			return m, cmd
		}
		return m.submit(text)

	case tea.KeyCtrlL:
		// "clear" no longer maps to a viewport reset — in inline mode
		// the terminal owns the scrollback. We print a clear-screen
		// escape via tea.ClearScreen, then redraw.
		return m, tea.ClearScreen

	case tea.KeyCtrlR:
		m.showReasoning = !m.showReasoning
		return m, nil

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

	// Multi-line paste folding: if the input has >5 lines, collapse the
	// display to a placeholder so the terminal scrollback isn't flooded,
	// but keep the full content for submission (see submit() below).
	m = m.handlePasteFolding()
	return m, cmd
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

	// Branch 1: "/model<space>..." — auto-open the model picker. Close
	// any open command menu first; they're mutually exclusive.
	if strings.HasPrefix(v, "/model ") || v == "/model " {
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		// Only (re)populate if not already open for this purpose —
		// avoids resetting the user's arrow-key position on every keypress.
		if !m.modelPickerOpen || m.pickerPurpose != "model" {
			m.modelPickerFiltered = knownModelsForProvider(m.opts.ProviderName)
			if len(m.modelPickerFiltered) == 0 {
				// Uncurated provider — no candidates to show; leave the
				// picker closed and let the user type a freeform id.
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
	}
	// Branch 2: not in "/model " state but a stale auto-opened model
	// picker is still showing (e.g. user backspaced the space). Close it.
	if m.modelPickerOpen && m.pickerPurpose == "model" {
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

// handlePasteFolding detects multi-line pastes (>5 lines) and stores
// the full content in m.pastedContent. The display is collapsed to a
// placeholder in View(); the full content is sent on submit (see
// submit() below). Once folded, any subsequent keypress clears the
// folding flag so the user can edit normally.
//
// When unfolding, the trigger character is discarded and the original
// pasted content is restored to the textarea — the user's intent is
// "show me the content", not "append this character to the hidden text".
func (m Model) handlePasteFolding() Model {
	const thresholdLines = 5
	val := m.input.Value()

	if m.pastedContent != "" {
		// Already folded — any keypress by the user unfolds.
		// Restore the stored content to the textarea, discarding
		// the trigger character (user wanted to unfold, not to type).
		m.input.Reset()
		m.input.SetValue(m.pastedContent)
		m.pastedContent = ""
		return m
	}

	// Not folded — count lines and fold if content is large enough.
	// This catches both pastes and typed multi-line blocks.
	lines := strings.Count(val, "\n") + 1
	if lines > thresholdLines {
		m.pastedContent = val
	}
	return m
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

// submit kicks off an agent.Prompt for the given user text. Before
// streaming begins we commit the user's message to scrollback via
// tea.Println so it survives in native terminal history.
//
// We derive a cancelable context from opts.Ctx so Esc can cancel the
// in-flight call without tearing down the outer SIGINT context.
//
// NOTE: text is already the full user input — view-time paste folding
// (handlePasteFolding + renderPastedPlaceholder) keeps the complete
// content in the textarea at all times; only the View() layer shows a
// placeholder. If the folding strategy is changed to replace the
// textarea value with a placeholder, re-add a pastedContent override
// here as the single source of truth.
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

	printUser := tea.Println(renderCommittedUser(text, m.width))
	return m, tea.Batch(printUser, waitForAgentEvent(ch))
}

// applyAgentEvent updates Model state for an agent event and returns
// any tea.Cmds needed to commit content to scrollback. Pointer receiver
// so we can mutate m without returning a copy.
func (m *Model) applyAgentEvent(ev agent.Event) []tea.Cmd {
	var cmds []tea.Cmd

	switch e := ev.(type) {
	case agent.AgentStart, agent.TurnStart, agent.MessageStart:
		// no UI change

	case agent.MessageDelta:
		if e.Reasoning {
			m.curReasoning += e.Delta
		} else {
			m.curContent += e.Delta
			m.streamDeltaBytes += len(e.Delta)
		}

	case agent.MessageEnd:
		if e.Message.Role == deepseek.RoleAssistant && (m.curContent != "" || m.curReasoning != "") {
			// Commit the assistant message to scrollback. The live
			// region's curContent buffer is cleared so the next
			// streamed chunk starts a fresh ghost.
			rendered := renderMarkdown(m.md, m.curContent)
			if rendered == "" {
				rendered = m.curContent
			}
			cmds = append(cmds, tea.Println(renderCommittedAssistant(rendered, m.curReasoning, m.showReasoning, m.width)))
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
		// look both back up from the active list before we remove it.
		var (
			args     string
			duration time.Duration
		)
		for i, t := range m.activeTools {
			if t.callID == e.CallID {
				args = t.args
				duration = time.Since(t.started)
				m.activeTools = append(m.activeTools[:i], m.activeTools[i+1:]...)
				break
			}
		}
		var line string
		if e.Err != nil {
			line = renderCommittedToolErr(e.Name, args, e.Err.Error(), duration)
		} else {
			line = renderCommittedToolOk(e.Name, args, len(e.Result), duration)
		}
		cmds = append(cmds, tea.Println(line))

	case agent.TurnEnd:
		m.turns++
		m.opts.Tracker.Record(e.Usage)
		m.toolCalls += e.ToolCalls

	case agent.AgentEnd:
		// Stats footer at turn boundary — print a thin separator with
		// running totals so a long session has visible "checkpoints"
		// in the scrollback.
		cmds = append(cmds, tea.Println(m.renderTurnFooter()))

	case agent.ErrorEvent:
		m.lastErr = e.Err
		cmds = append(cmds, tea.Println(styleErr.Render("  ! error: "+e.Err.Error())))
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
		return []tea.Cmd{tea.Println(styleErr.Render("  ! compact failed: " + msg.err.Error()))}
	}
	if m.opts.Agent == nil {
		return nil
	}
	// Fold the summariser's own usage into the cumulative tracker so
	// the cost line in the status bar stays honest — the compact call
	// is a real billed request.
	if m.opts.Tracker != nil {
		m.opts.Tracker.Record(msg.usage)
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
func truncateOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (m Model) renderTurnFooter() string {
	c := m.opts.Tracker.Last()
	cost := pricing.FormatCost(pricing.Cost(m.opts.Model, pricing.CurrentTier(m.now), c))

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
