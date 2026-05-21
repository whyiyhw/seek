package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/pricing"
)

func (m Model) View() string {
	if !m.ready {
		return "starting seek …"
	}

	header := m.renderHeader()
	body := m.viewport.View()
	input := m.input.View()
	status := m.renderStatusBar()

	separator := styleMuted.Render(strings.Repeat("─", m.width))
	return strings.Join([]string{header, body, separator, input, status}, "\n")
}

func (m Model) renderHeader() string {
	left := styleHeader.Render("seek")
	right := styleMuted.Render(m.opts.CWD)
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if pad < 1 {
		pad = 1
	}
	return " " + left + strings.Repeat(" ", pad+2) + right + " "
}

func (m Model) renderStatusBar() string {
	tier := pricing.CurrentTier(m.now)
	nextTier, nextAt := pricing.NextTransition(m.now)
	return RenderStatusBar(StatusSnapshot{
		Model:     m.opts.Model,
		Yolo:      m.opts.Yolo,
		Tier:      tier,
		NextTier:  nextTier,
		NextAt:    nextAt,
		Turns:     m.turns,
		ToolCalls: m.toolCalls,
		Usage:     m.opts.Tracker.Cumulative(),
		Streaming: m.streaming,
		Now:       m.now,
		Width:     m.width,
	})
}

// renderConversation produces the viewport's content. It's a complete
// rebuild on every change — fine for the conversation lengths we
// realistically see; if it becomes a perf problem we can switch to an
// incremental buffer.
func (m Model) renderConversation() string {
	var sb strings.Builder

	// Stable history items.
	for _, h := range m.history {
		sb.WriteString(renderItem(h, m.viewport.Width))
		sb.WriteString("\n")
	}

	// In-flight assistant content (still streaming). We render this as
	// a "ghost" assistant message that hasn't been committed yet.
	if m.streaming && (m.curContent != "" || m.curReasoning != "") {
		ghost := historyItem{
			role:      "assistant",
			text:      m.curContent,
			reasoning: m.curReasoning,
		}
		sb.WriteString(renderItem(ghost, m.viewport.Width))
	}

	if m.lastErr != nil {
		sb.WriteString("\n" + styleErr.Render("error: "+m.lastErr.Error()))
	}

	return sb.String()
}

func renderItem(h historyItem, width int) string {
	switch h.role {
	case "user":
		label := styleUserLabel.Render("▌ you")
		body := styleUserText.Render(wrap(h.text, width-2))
		return label + "\n" + body + "\n"

	case "assistant":
		label := styleAssistantLabel.Render("▸ seek")
		body := styleAssistantText.Render(wrap(h.text, width-2))
		var rea string
		if h.reasoning != "" {
			rea = "\n" + styleReasoning.Render("🧠 "+truncateOneLine(h.reasoning, 200)) + "\n"
		}
		return label + "\n" + body + rea + "\n"

	case "tool":
		base := fmt.Sprintf("  ↳ %s(%s) → %s", h.toolName, h.toolArgs, h.text)
		if h.toolErr {
			return styleToolError.Render(base)
		}
		return styleToolLine.Render(base)

	case "system":
		return styleErr.Render("  ! " + h.text)

	default:
		return h.text
	}
}

// wrap is a very thin word-wrapper that breaks on whitespace at the
// soft column. It's purposely naïve — lipgloss has WordWrap but using
// it on streaming text introduces visible reflow churn; raw text with
// hard breaks every `width` characters is what we ship in M4.
func wrap(s string, width int) string {
	if width <= 4 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

// SetReady is called by Update on first WindowSizeMsg. Exposed so
// integration tests can flip it manually.
func (m Model) relayout() Model {
	if m.width == 0 || m.height == 0 {
		return m
	}
	// Layout budget: header(1) + separator(1) + input(3) + status(1) = 6 lines
	// for non-viewport content.
	const chrome = 6
	vpHeight := m.height - chrome
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpHeight
	m.input.SetWidth(m.width - 2)
	if !m.ready {
		m.viewport.SetContent(welcomeText(m.opts))
	} else {
		m.viewport.SetContent(m.renderConversation())
	}
	m.ready = true
	return m
}
