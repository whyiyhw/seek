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
		// Forward to viewport so it can reflow its own internal state.
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)

	case tea.KeyMsg:
		// Key dispatch is explicit: each key reaches exactly one of
		// {global, viewport, textarea}. Without this split, forwarding
		// every key to both textarea AND viewport meant typing a space
		// scrolled the conversation a page (viewport's default
		// PageDown binding is " ") while also inserting a space — and
		// PgUp/etc. only worked by accident.
		return m.handleKey(msg)

	case agentEventMsg:
		// Auto-scroll only when the user is already pinned to the
		// bottom. If they've scrolled up to read history, leave them
		// there — streamed deltas shouldn't yank them back.
		stickBottom := m.viewport.AtBottom()
		m.applyAgentEvent(msg.Event)
		m.viewport.SetContent(m.renderConversation())
		if stickBottom {
			m.viewport.GotoBottom()
		}
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

	default:
		// Non-Key, non-Window messages (mouse off; ticks; bubble
		// internals) — let the viewport peek so its blink/animation
		// states stay current.
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

// handleKey owns all KeyMsg routing so each key reaches exactly one of
// {global handler, viewport scroller, textarea editor}. Returning
// directly from here (rather than falling through to a viewport.Update
// at the bottom of Update) is what stops space/b/f from secretly
// scrolling while the user is just typing.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	// --- global keys ---
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
		// Slash command? Handle locally instead of sending to LLM.
		if handled, cmd := dispatchCommand(&m, text); handled {
			m.viewport.SetContent(m.renderConversation())
			m.viewport.GotoBottom()
			return m, cmd
		}
		return m.submit(text)
	case tea.KeyCtrlL:
		cmdClear(&m, "")
		return m, nil
	case tea.KeyCtrlR:
		m.showReasoning = !m.showReasoning
		m.viewport.SetContent(m.renderConversation())
		return m, nil

	// --- scroll keys: viewport-only ---
	//
	// PgUp/PgDn for full-page scroll; Ctrl+U/Ctrl+D for half-page
	// (handy on laptops without dedicated Page keys, and the vim
	// muscle memory is widespread among CLI users). Arrow keys stay
	// with the textarea — they're for cursor movement in the input,
	// not history scrolling.
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyCtrlU, tea.KeyCtrlD:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd
	}

	// --- everything else: textarea (when accepting input) ---
	if m.streaming {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
			// Persist the assembled assistant message and pre-render
			// its Markdown (cached on the historyItem to avoid running
			// glamour every redraw). ToolCalls embedded here are
			// handled by the subsequent ToolExec* events; we don't
			// render the raw call_ids.
			if m.curContent != "" || m.curReasoning != "" {
				m.history = append(m.history, historyItem{
					role:      "assistant",
					text:      m.curContent,
					reasoning: m.curReasoning,
					rendered:  renderMarkdown(m.md, m.curContent),
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
