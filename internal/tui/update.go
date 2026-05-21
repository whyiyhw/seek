package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.streaming {
				// Refuse to queue follow-ups in M4. The model isn't
				// designed for it yet; we'd silently lose them.
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			return m.submit(text)
		case tea.KeyCtrlL:
			m.viewport.SetContent(welcomeText(m.opts))
			m.viewport.GotoTop()
			return m, nil
		default:
			if !m.streaming {
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

	case agentEventMsg:
		m.applyAgentEvent(msg.Event)
		m.viewport.SetContent(m.renderConversation())
		m.viewport.GotoBottom()
		// Pull the next event off the stream channel.
		cmds = append(cmds, waitForAgentEvent(m.stream))

	case streamEndMsg:
		m.streaming = false
		m.stream = nil
		m.input.Focus()
		m.curContent = ""
		m.curReasoning = ""
		m.curToolActive = map[string]string{}

	case statusTickMsg:
		m.now = time.Now()
		cmds = append(cmds, tickStatusEvery(time.Minute))
	}

	// Always refresh the viewport scroll position via its own update,
	// so PgUp/PgDn etc. work even mid-stream.
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) submit(text string) (tea.Model, tea.Cmd) {
	m.history = append(m.history, historyItem{role: "user", text: text})
	m.curContent = ""
	m.curReasoning = ""
	m.curToolActive = map[string]string{}
	m.streaming = true
	m.input.Blur()

	ch := m.opts.Agent.Prompt(m.opts.Ctx, text)
	m.stream = ch

	m.viewport.SetContent(m.renderConversation())
	m.viewport.GotoBottom()

	return m, waitForAgentEvent(ch)
}

func (m *Model) applyAgentEvent(ev agent.Event) {
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
		switch e.Message.Role {
		case deepseek.RoleAssistant:
			// Persist the assembled assistant message. ToolCalls
			// embedded here are handled by the subsequent ToolExec*
			// events; we don't render the raw call_ids.
			if m.curContent != "" || m.curReasoning != "" {
				m.history = append(m.history, historyItem{
					role:      "assistant",
					text:      m.curContent,
					reasoning: m.curReasoning,
				})
			}
			m.curContent = ""
			m.curReasoning = ""
		case deepseek.RoleTool:
			// Tool result messages are already represented by the
			// ToolExecEnd line we appended; skip duplicating.
		}

	case agent.ToolExecStart:
		argsTrim := truncateOneLine(e.Args, 80)
		m.curToolActive[e.CallID] = fmt.Sprintf("%s(%s)", e.Name, argsTrim)
		m.history = append(m.history, historyItem{
			role:     "tool",
			toolName: e.Name,
			toolArgs: argsTrim,
			text:     "…",
		})

	case agent.ToolExecEnd:
		// Locate the most recent matching tool entry (by name) and fill
		// in the result/error.
		for i := len(m.history) - 1; i >= 0; i-- {
			if m.history[i].role == "tool" && m.history[i].toolName == e.Name && m.history[i].text == "…" {
				if e.Err != nil {
					m.history[i].toolErr = true
					m.history[i].text = "ERROR: " + e.Err.Error()
				} else {
					m.history[i].text = fmt.Sprintf("%d bytes", len(e.Result))
				}
				break
			}
		}
		delete(m.curToolActive, e.CallID)

	case agent.TurnEnd:
		m.turns++
		m.opts.Tracker.Record(e.Usage)
		m.toolCalls += e.ToolCalls

	case agent.AgentEnd:
		// Counters already accumulated via TurnEnd. Nothing further.

	case agent.ErrorEvent:
		m.lastErr = e.Err
		m.history = append(m.history, historyItem{
			role: "system",
			text: "ERROR: " + e.Err.Error(),
		})
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
