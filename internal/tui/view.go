package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/pricing"
)

// newMarkdownRenderer builds a glamour renderer at the given width.
// Returns nil if construction fails — callers must handle that as a
// signal to fall back to raw text.
func newMarkdownRenderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-2),
		glamour.WithEmoji(),
	)
	if err != nil {
		return nil
	}
	return r
}

// renderMarkdown is glamour with safe defaults: empty input returns
// empty, a nil renderer is a no-op, render errors fall back to raw.
func renderMarkdown(r *glamour.TermRenderer, text string) string {
	if text == "" || r == nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

func (m Model) View() string {
	// Pre-WindowSizeMsg path: bubbletea sends sizing automatically once
	// alt-screen is up, but the first View() call typically fires
	// before that arrives. We render the welcome banner instead of a
	// placeholder so the very first frame already says something
	// meaningful — same content the viewport will hold once we know
	// the actual dimensions.
	if !m.ready {
		return welcomeText(m.opts)
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
		sb.WriteString(m.renderItem(h, false))
		sb.WriteString("\n")
	}

	// In-flight assistant content (still streaming). Rendered as a
	// "ghost" assistant message — raw text only (no markdown reflow
	// while the bytes are still arriving).
	if m.streaming && (m.curContent != "" || m.curReasoning != "") {
		ghost := historyItem{
			role:      "assistant",
			text:      m.curContent,
			reasoning: m.curReasoning,
		}
		sb.WriteString(m.renderItem(ghost, true))
	}

	if m.lastErr != nil {
		sb.WriteString("\n" + styleErr.Render("error: "+m.lastErr.Error()))
	}

	return sb.String()
}

// renderItem formats a single history entry. ghost=true means the entry
// is still streaming and should be rendered without markdown styling
// (which reflows badly across partial input).
func (m Model) renderItem(h historyItem, ghost bool) string {
	width := m.viewport.Width
	switch h.role {
	case "user":
		label := styleUserLabel.Render("▌ you")
		body := styleUserText.Render(wrap(h.text, width-2))
		return label + "\n" + body + "\n"

	case "assistant":
		label := styleAssistantLabel.Render("▸ seek")
		body := h.rendered
		if body == "" || ghost {
			body = styleAssistantText.Render(wrap(h.text, width-2))
		}
		var rea string
		if h.reasoning != "" {
			if m.showReasoning {
				rea = "\n" + styleReasoning.Render("🧠 reasoning:\n"+indent(h.reasoning, "    ")) + "\n"
			} else {
				rea = "\n" + styleReasoning.Render("🧠 reasoning hidden — Ctrl+R to expand") + "\n"
			}
		}
		return label + "\n" + body + rea + "\n"

	case "tool":
		base := fmt.Sprintf("  ↳ %s(%s) → %s", h.toolName, h.toolArgs, h.text)
		if h.toolErr {
			return styleToolError.Render(base)
		}
		return styleToolLine.Render(base)

	case "system":
		return styleMuted.Render("  ! " + h.text)

	default:
		return h.text
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
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

// relayout is called by Update on every WindowSizeMsg. It (re)sizes the
// viewport + input, (re)builds the markdown renderer at the current
// width, and re-renders previously committed assistant messages.
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

	if m.md == nil || m.mdWidth != m.viewport.Width {
		m.md = newMarkdownRenderer(m.viewport.Width)
		m.mdWidth = m.viewport.Width
		// Width changed: stale `rendered` caches need to be regenerated.
		for i := range m.history {
			if m.history[i].role == "assistant" && m.history[i].text != "" {
				m.history[i].rendered = renderMarkdown(m.md, m.history[i].text)
			}
		}
	}

	if !m.ready {
		m.viewport.SetContent(welcomeText(m.opts))
	} else {
		m.viewport.SetContent(m.renderConversation())
	}
	m.ready = true
	return m
}
