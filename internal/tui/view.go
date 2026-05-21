package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/permission"
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

	// Active tool lines — each shows a spinner and an elapsed-time
	// tail. The spinner ticks ~80 ms which is also what drives the
	// elapsed-time refresh; without that the user can't tell whether
	// a long `think` call is alive or hung.
	for _, t := range m.activeTools {
		elapsed := formatToolElapsed(time.Since(t.started))
		label := fmt.Sprintf("%s(%s) …", t.name, t.args)
		if elapsed != "" {
			label += " · " + elapsed
		}
		fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(), styleToolLine.Render(label))
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

	// Approval prompt takes precedence over the slash menu — both can
	// never be active simultaneously (approval blurs the input).
	if m.pendingApproval != nil {
		sb.WriteString(m.renderApprovalPrompt())
	} else if m.commandMenuOpen {
		sb.WriteString(m.renderCommandMenu())
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
// renderApprovalPrompt draws the inline y/N/a chooser shown while a
// dangerous tool waits for a decision. Layout is intentionally compact
// — same vertical real estate as the slash menu so the input doesn't
// jump around.
func (m Model) renderApprovalPrompt() string {
	req := m.pendingApproval
	if req == nil {
		return ""
	}
	var subject string
	switch req.Action.Kind {
	case permission.KindBash:
		subject = fmt.Sprintf("bash %q", truncateOneLine(req.Action.Command, 100))
	default:
		subject = fmt.Sprintf("%s %q (outside CWD)", req.Action.Kind, req.Action.Path)
	}
	header := styleApprovalHeader.Render("⚠ approve " + subject + "?")
	hint := styleMuted.Render("  [y] allow once  [n] deny  [a] always (yolo for session)  [Esc] deny")
	return header + "\n" + hint + "\n"
}

// renderCommandMenu renders the slash-command dropdown. Selected row
// is highlighted with a ▸ marker and an accent colour; others get a
// neutral two-space indent so the visual rhythm matches.
func (m Model) renderCommandMenu() string {
	if len(m.commandMenuFiltered) == 0 {
		return styleMuted.Render("  (no commands match — Esc to dismiss)") + "\n"
	}
	var sb strings.Builder
	for i, c := range m.commandMenuFiltered {
		row := fmt.Sprintf("%-22s  %s", c.usage, c.description)
		if i == m.commandMenuSelected {
			sb.WriteString(styleMenuSelected.Render("▸ " + row))
		} else {
			sb.WriteString(styleMenuItem.Render("  " + row))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(styleMuted.Render("  Tab to complete · ↑/↓ to navigate · Esc to dismiss"))
	sb.WriteString("\n")
	return sb.String()
}

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

func renderCommittedToolOk(name, args string, resultBytes int, d time.Duration) string {
	return styleToolLine.Render(fmt.Sprintf("  ↳ %s(%s) → %d bytes%s",
		name, args, resultBytes, durationTail(d)))
}

func renderCommittedToolErr(name, args, err string, d time.Duration) string {
	return styleToolError.Render(fmt.Sprintf("  ↳ %s(%s) → ERROR: %s%s",
		name, args, err, durationTail(d)))
}

// durationTail is the trailing " · 0.8s" / " · 1m23s" we hang off
// completed tool lines. Empty for sub-100ms operations (read of a
// small file etc.) since the noise outweighs the info there.
func durationTail(d time.Duration) string {
	s := formatCommittedDuration(d)
	if s == "" {
		return ""
	}
	return " · " + s
}

func formatCommittedDuration(d time.Duration) string {
	switch {
	case d < 100*time.Millisecond:
		return ""
	case d < time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
}

// formatToolElapsed is the live counterpart for in-flight tools. Same
// shape as the committed version but empty for sub-1s — no point
// flickering "0s ←→ 1s" right at the start.
func formatToolElapsed(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	return formatCommittedDuration(d)
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

