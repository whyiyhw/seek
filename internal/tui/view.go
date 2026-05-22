package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
)

// View renders the LIVE region only (no scrollback — that belongs to
// the terminal). Layout (idle welcome):
//
//	[padding lines — fill terminal height]
//	> input
//	status: …
//
// Layout (active):
//
//	[active tools]    ← one line each, with a spinner
//	[streaming assistant text]
//	[streaming reasoning, when showReasoning]
//	── separator ─────────────────
//	> input
//	status: …
func (m Model) View() string {
	if !m.ready {
		// Pre-WindowSizeMsg: minimal hint so the user doesn't see a
		// blank screen if bubbletea takes a moment to size up.
		return styleMuted.Render("starting…") + "\n"
	}

	if m.helpOverlayOpen {
		return m.renderHelpOverlay()
	}

	var sb strings.Builder

	// Second-tier provider banner: warn that DeepSeek-exclusive features
	// (cache stats, FIM, Reasoner) are disabled.
	if m.opts.ProviderName != "" {
		banner := lipgloss.NewStyle().
			Foreground(colourTool).
			Render("⚠ Provider: " + m.opts.ProviderName + " — FIM / cache stats / Reasoner disabled")
		sb.WriteString(banner)
		sb.WriteString("\n")
	}

	// "thinking…" placeholder for the gap between submit and the
	// first user-visible byte. There are two such gaps in a typical
	// turn:
	//   1. Submit → first content delta (network + TTFT on the
	//      server). On a large cached prompt this can still be 2-5s.
	//   2. Last content delta → ToolExecStart, when the model is
	//      streaming tool_call argument deltas (these don't surface
	//      as TUI events — the assistant is still working but the
	//      live region looks frozen).
	// Without this line the user has no signal that the agent is
	// alive during those windows.
	if m.streaming &&
		m.curContent == "" && m.curReasoning == "" && len(m.activeTools) == 0 {
		fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(), styleMuted.Render(m.streamingLabel()))
	}

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

	// When the live region is idle (no streaming, no tools, no menus,
	// no approval prompt), push the input toward the bottom of the
	// terminal so the welcome screen fills the height. m.height is
	// the FULL terminal height (from tea.WindowSizeMsg), but bubbletea
	// only owns the bottom slice — the welcome banner above us takes
	// welcomeFixedLines rows we can't reclaim. welcomePadding handles
	// the bookkeeping (and caps at welcomePadMax so a 60-row window
	// doesn't end up with 40 lines of empty space).
	//
	// Gated on m.turns == 0 so that once the conversation starts —
	// tea.Println pushes content above us — we stop adding pad and
	// the input glides up to sit right under the last response.
	if m.isWelcomeScreen() && m.turns == 0 {
		if pad := welcomePadding(m.height); pad > 0 {
			sb.WriteString(strings.Repeat("\n", pad))
		}
	}

	// Queue / steer hint — only meaningful mid-stream. Sits ABOVE the
	// textarea so the user can see "what's already queued" and "what
	// I'm currently typing" without the two visually merging.
	if m.streaming {
		if hint := m.renderQueueHint(); hint != "" {
			sb.WriteString(hint)
			sb.WriteString("\n")
		}
	}

	// Setup key-entry banner — same slot as the queue hint (mutually
	// exclusive: /setup can't be opened mid-stream so we never need
	// both at once).
	if m.setupKeyEntry {
		sb.WriteString(styleMuted.Render(fmt.Sprintf(
			"✎ paste API key for %s — Enter to save, Esc to cancel",
			m.setupProvider)))
		sb.WriteString("\n")
	}

	// Input area FIRST (above any dropdown) — autocomplete popups
	// attach BELOW the input, matching IDE / shell completer
	// convention. With dropdowns above the input we were visually
	// pushing the in-flight conversation upward when the user typed
	// "/" or "@", obscuring whatever they were reading. Below-the-
	// input keeps the upper conversation steady; the menu grows
	// downward into the space just above the status bar.
	if m.pastedContent != "" {
		// Multi-line paste folded — show a compact placeholder instead
		// of the full content in the textarea. The full content is still
		// stored and will be sent to the LLM on submit.
		sb.WriteString(m.renderPastedPlaceholder())
	} else {
		sb.WriteString(m.input.View())
	}
	sb.WriteString("\n")

	// Approval prompt takes precedence — blurs the input, blocks
	// everything else. After that, command menu and path picker are
	// mutually exclusive in practice (different trigger chars).
	switch {
	case m.pendingApproval != nil:
		sb.WriteString(m.renderApprovalPrompt())
	case m.distillReviewOpen:
		sb.WriteString(m.renderDistillReview())
	case m.commandMenuOpen:
		sb.WriteString(m.renderCommandMenu())
	case m.modelPickerOpen:
		sb.WriteString(m.renderModelPicker())
	case m.pathPicker.open:
		sb.WriteString(m.renderPathPicker())
	}

	// Status line (single, not pinned — it scrolls with content).
	sb.WriteString(m.renderStatusBar())

	// Bottom rule — closes seek's live region visually so the next line
	// in the terminal (shell prompt, neighbouring tmux pane bleed-through,
	// etc.) doesn't appear to belong to seek. Muted so it recedes; full
	// width so it acts as a clean horizontal seal.
	if m.width > 0 {
		sb.WriteString("\n")
		sb.WriteString(styleMuted.Render(strings.Repeat("─", m.width)))
	}

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

	var streamElapsed time.Duration
	if m.streaming && !m.streamStartTime.IsZero() {
		streamElapsed = time.Since(m.streamStartTime)
	}

	return RenderStatusBar(StatusSnapshot{
		Model:            m.opts.Model,
		Yolo:             m.opts.Yolo,
		Tier:             tier,
		NextTier:         nextTier,
		NextAt:           nextAt,
		Turns:            m.turns,
		ToolCalls:        m.toolCalls,
		Usage:            m.opts.Tracker.Cumulative(),
		LastUsage:        m.opts.Tracker.Last(),
		Streaming:        m.streaming,
		Now:              now,
		Width:            m.width,
		StreamElapsed:    streamElapsed,
		StreamDeltaBytes: m.streamDeltaBytes,
		UpgradeAvailable: m.upgradeAvailable,
	})
}

// renderQueueHint returns a one-line indicator for queued / steering
// state during a stream. Returns "" when there's nothing to show. The
// hint sits above the textarea so the user can see "what's already
// queued for after this turn" vs "what I'm currently typing".
//
// Priority: pendingSteerText (transitive — only set while cancelStream
// is propagating) > queuedText. Both states are mutually exclusive
// during steady-state but the steer path briefly holds both before
// streamEndMsg clears queuedText, so the precedence matters.
func (m Model) renderQueueHint() string {
	switch {
	case m.pendingSteerText != "":
		preview := truncateOneLine(m.pendingSteerText, 60)
		return styleMuted.Render("↪ steering: ") + styleMuted.Render(preview)
	case m.queuedText != "":
		preview := truncateOneLine(m.queuedText, 60)
		return styleMuted.Render("↰ queued: ") + styleMuted.Render(preview)
	}
	return ""
}

// streamingLabel returns the "thinking…" placeholder text for the live
// region. After the first second it appends the elapsed time so the user
// knows the agent is alive during slow first-token or tool-gap windows.
func (m Model) streamingLabel() string {
	if m.streamStartTime.IsZero() {
		return "thinking…"
	}
	elapsed := time.Since(m.streamStartTime)
	if elapsed < time.Second {
		return "thinking…"
	}
	s := int(elapsed.Seconds())
	if s < 60 {
		return fmt.Sprintf("thinking… %ds", s)
	}
	return fmt.Sprintf("thinking… %dm%ds", s/60, s%60)
}

// formatTokensK renders a token count as a compact string: raw below
// 1000, one-decimal "Xk" above (e.g. 99600 → "99.6k").
func formatTokensK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// renderCommittedUser renders the user's prompt for scrollback. Called
// before tea.Println.
// renderPathPicker draws the @-completion dropdown. Same vertical
// shape as the slash menu so the input doesn't jump when the user
// switches between "@" and "/".
func (m Model) renderPathPicker() string {
	if len(m.pathPicker.filtered) == 0 {
		return styleMuted.Render("  (no files match — Esc to dismiss)") + "\n"
	}
	var sb strings.Builder
	for i, p := range m.pathPicker.filtered {
		if i == m.pathPicker.selected {
			sb.WriteString(styleMenuSelected.Render("▸ " + p))
		} else {
			sb.WriteString(styleMenuItem.Render("  " + p))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(styleMuted.Render("  Tab to insert · ↑/↓ to navigate · Esc to dismiss"))
	sb.WriteString("\n")
	return sb.String()
}

// renderApprovalPrompt draws the inline y/N/a chooser shown while a
// dangerous tool waits for a decision. For edit actions it also renders
// the unified diff so the user can see exactly what will change.
func (m Model) renderApprovalPrompt() string {
	req := m.pendingApproval
	if req == nil {
		return ""
	}
	var subject string
	switch req.Action.Kind {
	case permission.KindBash:
		subject = fmt.Sprintf("bash %q", truncateOneLine(req.Action.Command, 100))
	case permission.KindMemoryRemember:
		// Tagline is the one-line summary — show it next to the name so
		// the user knows what they're committing to project memory
		// without having to read the (possibly long) content body.
		if req.Action.MemoryTagline != "" {
			subject = fmt.Sprintf("save memory %q — %s",
				req.Action.MemoryName,
				truncateOneLine(req.Action.MemoryTagline, 100))
		} else {
			subject = fmt.Sprintf("save memory %q", req.Action.MemoryName)
		}
	default:
		subject = fmt.Sprintf("%s %q (outside CWD)", req.Action.Kind, req.Action.Path)
	}

	var sb strings.Builder
	sb.WriteString(styleApprovalHeader.Render("⚠ approve " + subject + "?"))
	sb.WriteString("\n")

	if req.Action.Diff != "" {
		sb.WriteString(renderDiff(req.Action.Diff, m.width))
	}

	sb.WriteString(styleMuted.Render("  [y] allow once  [n] deny  [a] always (yolo for session)  [Esc] deny"))
	sb.WriteString("\n")
	return sb.String()
}

// renderDiff colourises a unified diff for inline display in the TUI.
// Lines starting with '+' are green, '-' are red, '@' are cyan, others muted.
// Capped at maxDiffLines to avoid shoving the input box off screen.
func renderDiff(udiff string, _ int) string {
	const maxDiffLines = 24
	lines := strings.Split(strings.TrimRight(udiff, "\n"), "\n")
	if len(lines) > maxDiffLines {
		lines = lines[:maxDiffLines]
		lines = append(lines, styleMuted.Render(fmt.Sprintf("  … (%d more lines)", len(strings.Split(udiff, "\n"))-maxDiffLines)))
	}
	styleAdd := lipgloss.NewStyle().Foreground(colourOk)
	styleDel := lipgloss.NewStyle().Foreground(colourToolErr)
	styleAt := lipgloss.NewStyle().Foreground(colourUser)

	var sb strings.Builder
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			sb.WriteString(styleAdd.Render(l))
		case strings.HasPrefix(l, "-"):
			sb.WriteString(styleDel.Render(l))
		case strings.HasPrefix(l, "@"):
			sb.WriteString(styleAt.Render(l))
		default:
			sb.WriteString(styleMuted.Render(l))
		}
		sb.WriteString("\n")
	}
	return sb.String()
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

// renderModelPicker renders the /model dropdown. Same visual shape as
// the slash-command menu — same indent, same ▸ marker, same footer
// hint — so users don't need to learn a second affordance.
func (m Model) renderModelPicker() string {
	if len(m.modelPickerFiltered) == 0 {
		return styleMuted.Render("  (no models — Esc to dismiss)") + "\n"
	}
	var sb strings.Builder
	for i, mc := range m.modelPickerFiltered {
		marker := "  "
		idLabel := mc.id
		if mc.id == m.opts.Model {
			// Annotate the active model so Enter on a fresh picker is
			// visibly a no-op rather than a silent commit to the same id.
			idLabel = idLabel + " (current)"
		}
		row := fmt.Sprintf("%-32s  %s", idLabel, mc.description)
		if i == m.modelPickerSelected {
			sb.WriteString(styleMenuSelected.Render("▸ " + row))
		} else {
			sb.WriteString(styleMenuItem.Render(marker + row))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(styleMuted.Render("  Tab/Enter to switch · ↑/↓ to navigate · Esc to dismiss"))
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

// ---- Help overlay ---------------------------------------------------------

// renderHelpOverlay builds a floating centered panel showing all slash
// commands and keybindings. Called from View when helpOverlayOpen is true.
func (m Model) renderHelpOverlay() string {
	// Collect content: commands + keys.
	var content strings.Builder
	content.WriteString(styleHeader.Render("Help — seek"))
	content.WriteString("\n\n")

	// Slash commands.
	content.WriteString(styleStatusOffPeak.Render(" Commands "))
	content.WriteString("\n")
	sorted := allCommands()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].usage < sorted[j].usage })
	for _, c := range sorted {
		names := strings.Join(c.names, ", ")
		content.WriteString(fmt.Sprintf("  %-22s  %s\n", names, c.description))
	}
	content.WriteString("\n")

	// Key bindings.
	content.WriteString(styleStatusOffPeak.Render(" Keys "))
	content.WriteString("\n")
	type binding struct{ key, desc string }
	bindings := []binding{
		{"/help, /? or ?", "Show this help overlay"},
		{"Enter", "Send prompt"},
		{"↑ / ↓", "Recall prompt history (when input is empty)"},
		{"Esc", "Cancel ongoing assistant response"},
		{"Ctrl+J", "Insert newline in input"},
		{"Ctrl+L", "Clear visible screen (same as /clear)"},
		{"Ctrl+R", "Toggle reasoning visibility"},
		{"Ctrl+C", "Quit seek"},
	}
	for _, b := range bindings {
		content.WriteString(fmt.Sprintf("  %-22s  %s\n", b.key, b.desc))
	}
	content.WriteString("\n")
	content.WriteString(styleMuted.Render("Scrollback: use your terminal's native scrollback (not captured by seek)."))
	content.WriteString("\n\n")
	content.WriteString(styleMuted.Render("Esc / Enter / q  to close"))

	// Panel width: 60% of terminal width, with sensible bounds.
	panelW := int(float64(m.width) * 0.6)
	if panelW < 50 {
		if m.width > 54 {
			panelW = 50
		} else {
			panelW = m.width - 4 // leave 2-char margin on each side
		}
	}
	if panelW > 80 {
		panelW = 80
	}
	if panelW < 30 {
		panelW = 30 // absolute minimum — still readable
	}

	// Panel height: content-driven; let lipgloss handle wrapping and
	// we measure the actual rendered line count for centering.
	contentStr := content.String()
	panel := lipgloss.NewStyle().
		Width(panelW).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colourAccent).
		Padding(1, 2).
		Render(contentStr)

	// Center the panel vertically. Use the actual rendered line count.
	panelLines := strings.Count(strings.TrimRight(panel, "\n"), "\n") + 1
	available := m.height - 3 // reserve for input line + status bar + margin
	padTop := (available - panelLines) / 2
	if padTop < 0 {
		padTop = 0
	}
	padBottom := available - panelLines - padTop
	if padBottom < 0 {
		padBottom = 0
	}

	// Pad the panel horizontally — lipgloss already centers via Width,
	// but we add left padding to shift it toward center.
	leftPad := (m.width - panelW) / 2
	if leftPad < 0 {
		leftPad = 0
	}

	var sb strings.Builder
	sb.WriteString(strings.Repeat("\n", padTop))
	for _, line := range strings.Split(strings.TrimRight(panel, "\n"), "\n") {
		sb.WriteString(strings.Repeat(" ", leftPad))
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString(strings.Repeat("\n", padBottom))

	// Input area and status bar still visible at the bottom.
	sb.WriteString(m.input.View())
	sb.WriteString("\n")
	sb.WriteString(m.renderStatusBar())

	return sb.String()
}

// renderPastedPlaceholder returns a compact one-line representation of
// a folded multi-line paste, styled to look like a textarea prompt line.
func (m Model) renderPastedPlaceholder() string {
	lines := strings.Count(m.pastedContent, "\n") + 1
	label := fmt.Sprintf("📋 [pasted %d lines, hidden] — type any key to edit", lines)
	// Style it like an input line (same width, muted foreground).
	w := m.width - 2
	if w < 0 {
		w = 0
	}
	if w > len(label) {
		label += strings.Repeat(" ", w-len(label))
	}
	return styleMuted.Render("> " + label)
}
