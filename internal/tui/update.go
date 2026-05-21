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

	case statusTickMsg:
		m.now = time.Now()
		cmds = append(cmds, tickStatusEvery(time.Minute))

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
			// Don't clear m.cancelStream here — streamEndMsg will do
			// it after the stream channel actually drains, otherwise
			// the next Esc within the same race window double-cancels.
		}
		return m, nil

	case tea.KeyEnter:
		if m.streaming {
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
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
	}

	// Everything else: feed the textarea (when not streaming).
	if m.streaming {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateCommandMenu()
	return m, cmd
}

// updateCommandMenu recomputes the slash-command dropdown state from
// the current input value. Called after every textarea-bound key.
//
// Open conditions: value starts with "/" AND contains no space yet
// (space means the user is past the command name and into arguments).
func (m *Model) updateCommandMenu() {
	v := strings.TrimRight(m.input.Value(), "\n")
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

// submit kicks off an agent.Prompt for the given user text. Before
// streaming begins we commit the user's message to scrollback via
// tea.Println so it survives in native terminal history.
//
// We derive a cancelable context from opts.Ctx so Esc can cancel the
// in-flight call without tearing down the outer SIGINT context.
func (m Model) submit(text string) (tea.Model, tea.Cmd) {
	m.promptHistory = append(m.promptHistory, text)

	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
	m.userCanceled = false
	m.streaming = true
	m.input.Blur()

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

	case agent.ToolExecEnd:
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

// truncateOneLine collapses newlines and clips the result to n chars.
func truncateOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (m Model) renderTurnFooter() string {
	c := m.opts.Tracker.Cumulative()
	cost := pricing.FormatCost(pricing.Cost(m.opts.Model, pricing.CurrentTier(m.now), c))
	return styleMuted.Render(fmt.Sprintf(
		"  · turn %d · %d tool · cache %s · %s",
		m.turns, m.toolCalls, deepseek.FormatHitRatio(c), cost))
}
