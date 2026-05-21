package tui

import (
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
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit

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
	return m, cmd
}

// submit kicks off an agent.Prompt for the given user text. Before
// streaming begins we commit the user's message to scrollback via
// tea.Println so it survives in native terminal history.
func (m Model) submit(text string) (tea.Model, tea.Cmd) {
	// Record in the prompt-history ring (used by ↑/↓ recall — landing
	// in a follow-up commit).
	m.promptHistory = append(m.promptHistory, text)

	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
	m.streaming = true
	m.input.Blur()

	ch := m.opts.Agent.Prompt(m.opts.Ctx, text)
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
			callID: e.CallID,
			name:   e.Name,
			args:   truncateOneLine(e.Args, 80),
		})

	case agent.ToolExecEnd:
		// ToolExecEnd carries Name/Result/Err but not Args — look the
		// args back up from the active list before we remove it.
		var args string
		for i, t := range m.activeTools {
			if t.callID == e.CallID {
				args = t.args
				m.activeTools = append(m.activeTools[:i], m.activeTools[i+1:]...)
				break
			}
		}
		var line string
		if e.Err != nil {
			line = renderCommittedToolErr(e.Name, args, e.Err.Error())
		} else {
			line = renderCommittedToolOk(e.Name, args, len(e.Result))
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
