package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/pricing"
)

// View renders the LIVE region only (no scrollback — that belongs to
// the terminal). Layout:
//
//   [active tools]    ← one line each, with a spinner
//   [streaming assistant text]
//   [streaming reasoning, when showReasoning]
//   ── separator ─────────────────
//   > input
//   status: …
func (m Model) View() string {
	if !m.ready {
		// Pre-WindowSizeMsg: minimal hint so the user doesn't see a
		// blank screen if bubbletea takes a moment to size up.
		return styleMuted.Render("starting…") + "\n"
	}

	var sb strings.Builder

	// Active tool lines.
	for _, t := range m.activeTools {
		fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(), styleToolLine.Render(fmt.Sprintf("%s(%s) …", t.name, t.args)))
	}

	// Streaming assistant text (volatile — committed at MessageEnd).
	if m.curContent != "" {
		body := styleAssistantText.Render(wrap(m.curContent, m.width-2))
		fmt.Fprintf(&sb, "%s\n%s\n", styleAssistantLabel.Render("▸ seek"), body)
	}

	// Streaming reasoning.
	if m.curReasoning != "" {
		if m.showReasoning {
			sb.WriteString(styleReasoning.Render("🧠 reasoning:\n" + indent(m.curReasoning, "    ")))
			sb.WriteString("\n")
		} else {
			sb.WriteString(styleReasoning.Render("🧠 reasoning… (Ctrl+R to expand)"))
			sb.WriteString("\n")
		}
	}

	// Separator only when there's live content above it — keeps idle
	// state clean.
	if sb.Len() > 0 {
		sb.WriteString(styleMuted.Render(strings.Repeat("─", m.width)))
		sb.WriteString("\n")
	}

	// Input area.
	sb.WriteString(m.input.View())
	sb.WriteString("\n")

	// Status line (single, not pinned — it scrolls with content).
	sb.WriteString(m.renderStatusBar())

	return sb.String()
}

// relayout adapts to a new terminal width. No viewport in inline mode,
// so this is just resizing the textarea and (re)building the Markdown
// renderer.
func (m Model) relayout() Model {
	if m.width == 0 || m.height == 0 {
		return m
	}
	m.input.SetWidth(m.width - 2)

	if m.md == nil || m.mdWidth != m.width {
		m.md = newMarkdownRenderer(m.width, m.opts.GlamourStyle)
		m.mdWidth = m.width
	}
	m.ready = true
	return m
}

func (m Model) renderStatusBar() string {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	tier := pricing.CurrentTier(now)
	nextTier, nextAt := pricing.NextTransition(now)
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
		Now:       now,
		Width:     m.width,
	})
}

// renderCommittedUser renders the user's prompt for scrollback. Called
// before tea.Println.
func renderCommittedUser(text string, width int) string {
	label := styleUserLabel.Render("▌ you")
	body := styleUserText.Render(wrap(text, width-2))
	return "\n" + label + "\n" + body
}

// renderCommittedAssistant renders a completed assistant message for
// scrollback. content is already Markdown-rendered when md was
// available.
func renderCommittedAssistant(content, reasoning string, showReasoning bool, width int) string {
	label := styleAssistantLabel.Render("▸ seek")
	body := content
	if body == "" {
		body = styleMuted.Render("(no content)")
	}
	out := label + "\n" + body
	if reasoning != "" {
		if showReasoning {
			out += "\n" + styleReasoning.Render("🧠 reasoning:\n"+indent(reasoning, "    "))
		} else {
			out += "\n" + styleReasoning.Render("🧠 reasoning hidden — Ctrl+R during streaming to expand")
		}
	}
	_ = width // wrap is already applied via the Markdown renderer
	return out
}

func renderCommittedToolOk(name, args string, resultBytes int) string {
	return styleToolLine.Render(fmt.Sprintf("  ↳ %s(%s) → %d bytes", name, args, resultBytes))
}

func renderCommittedToolErr(name, args, err string) string {
	return styleToolError.Render(fmt.Sprintf("  ↳ %s(%s) → ERROR: %s", name, args, err))
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func wrap(s string, width int) string {
	if width <= 4 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

// ---- Markdown rendering ----------------------------------------------

// newMarkdownRenderer builds a glamour renderer at the given width.
// style is "dark" / "light" / "" (auto fallback). The host pre-detects
// the style via termenv BEFORE the program starts (cmd/seek does this)
// to avoid the OSC 11 query leaking into the textarea — see PRD §4.9
// and docs/pitfalls.md.
func newMarkdownRenderer(width int, style string) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	opts := []glamour.TermRendererOption{
		glamour.WithWordWrap(width - 2),
		glamour.WithEmoji(),
	}
	if style != "" {
		opts = append(opts, glamour.WithStandardStyle(style))
	} else {
		opts = append(opts, glamour.WithAutoStyle())
	}
	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return nil
	}
	return r
}

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

