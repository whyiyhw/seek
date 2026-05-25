package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
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
	// When completionTokens > 0 (set at ToolExecEnd, before deferred
	// cleanup), the final token count is shown for one frame.
	for _, t := range m.activeTools {
		elapsed := formatToolElapsed(time.Since(t.started))
		label, style := formatActiveToolLabel(t.name, t.args)
		if elapsed != "" {
			label += " · " + elapsed
		}
		tok := formatTokenTail(t.completionTokens)
		if tok != "" {
			label += " · " + tok
		}
		fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(), style.Render(label))
	}

	// Distill spinner — shown while /distill's reasoner call is
	// in-flight (10-90s). Same pattern as active tool lines above.
	if m.distilling {
		elapsed := formatToolElapsed(time.Since(m.distillSince))
		label := fmt.Sprintf("distilling %d messages …", m.distillMsgCount)
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
			sb.WriteString(styleReasoning.Render("▸ reasoning:\n" + indent(m.curReasoning, "    ")))
			sb.WriteString("\n")
		} else {
			sb.WriteString(styleReasoning.Render("▸ reasoning… (Ctrl+R to expand)"))
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
	// terminal. m.height is the FULL terminal height (from
	// tea.WindowSizeMsg); welcomePadding fills whatever's left after
	// the banner rows + the live region's own height — uncapped, so
	// the input always pins to the bottom.
	//
	// Gated on scrollbackLines == 0 — i.e. "nothing has been Println'd
	// above us". This covers BOTH first-launch (no turns yet) AND
	// post-/clear (turns > 0 but the visible viewport is empty because
	// tea.ClearScreen wiped it and cmdClear reset the counter). Using
	// m.turns as the gate was a proxy that broke after /clear: input
	// rendered at the TOP of the terminal until the next streamed turn
	// scrolled it back down.
	if m.isWelcomeScreen() && m.scrollbackLines == 0 {
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

	// Skill-armed badge — sits directly above the input so the user
	// sees, at the moment they hit Enter, that their message will be
	// wrapped with a skill instruction. Co-exists with the queue hint
	// above (mid-stream + armed is legitimate — the armed wrapping
	// applies to whatever message gets queued).
	if m.pendingSkill != "" {
		sb.WriteString(styleToolSkill.Render(fmt.Sprintf("✦ skill armed: %s", m.pendingSkill)))
		sb.WriteString(styleMuted.Render(" — next message uses this skill (/skill use clear to cancel)"))
		sb.WriteString("\n")
	}

	// Input area FIRST (above any dropdown) — autocomplete popups
	// attach BELOW the input, matching IDE / shell completer
	// convention. With dropdowns above the input we were visually
	// pushing the in-flight conversation upward when the user typed
	// "/" or "@", obscuring whatever they were reading. Below-the-
	// input keeps the upper conversation steady; the menu grows
	// downward into the space just above the status bar.
	sb.WriteString(m.renderInput())
	sb.WriteString("\n")

	// Approval prompt takes precedence — blurs the input, blocks
	// everything else. After that, command menu and path picker are
	// mutually exclusive in practice (different trigger chars).
	// pendingQuestion sits one rank below pendingApproval: approvals
	// gate filesystem mutations, so they outrank a model's "which
	// option?" — though in practice only one of the two is ever
	// active because they both block the agent goroutine.
	switch {
	case m.pendingApproval != nil:
		sb.WriteString(m.renderApprovalPrompt())
	case m.pendingQuestion != nil:
		sb.WriteString(m.renderUserQuestion())
	case m.distillReviewOpen:
		sb.WriteString(m.renderDistillReview())
	case m.commandMenuOpen:
		sb.WriteString(m.renderCommandMenu())
	case m.modelPickerOpen:
		sb.WriteString(m.renderModelPicker())
	case m.pathPicker.open:
		sb.WriteString(m.renderPathPicker())
	}

	// Pin the status bar to the bottom of the terminal window.
	// cursorRow = welcomeFixedLines + total tea.Println scrollback lines.
	// Add padding to fill remaining vertical space so the status bar
	// always sits on the terminal's last visible line.
	if m.scrollbackLines > 0 && m.height > 0 {
		contentHeight := strings.Count(sb.String(), "\n") + 1
		cursorRow := welcomeFixedLines + m.scrollbackLines
		remaining := m.height - cursorRow
		pad := remaining - contentHeight - 2 // -2 for status bar + bottom rule
		if pad > 0 {
			sb.WriteString(strings.Repeat("\n", pad))
		}
	}

	// Status line.
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
		Effort:           m.opts.Effort,
		Yolo:             m.opts.Yolo,
		Plan:             m.opts.Plan,
		PlanSubstate:     m.opts.PlanSubstate,
		Tier:             tier,
		NextTier:         nextTier,
		NextAt:           nextAt,
		Turns:            m.turns,
		ToolCalls:        m.toolCalls,
		Usage:            m.opts.Tracker.Cumulative(),
		LastUsage:        m.opts.Tracker.Last(),
		CumulativeCost:   m.opts.Tracker.CumulativeCost(),
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
		if m.pathPicker.token != "" {
			sb.WriteString(m.renderHighlightedPath(p, i == m.pathPicker.selected))
		} else {
			// No token — plain list (empty @ prompt).
			if i == m.pathPicker.selected {
				sb.WriteString(styleMenuSelected.Render("▸ " + p))
			} else {
				sb.WriteString(styleMenuItem.Render("  " + p))
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString(styleMuted.Render("  Tab to insert · ↑/↓ to navigate · Esc to dismiss"))
	sb.WriteString("\n")
	return sb.String()
}

// renderHighlightedPath renders a single @-completion list item with the
// matching portion of the path highlighted. token is the text after "@".
// Matches follow filterPaths: basename-prefix first (tier 1), then full-path
// substring (tier 2). The matching characters get an accent colour.
func (m Model) renderHighlightedPath(path string, selected bool) string {
	token := m.pathPicker.token
	if token == "" {
		if selected {
			return styleMenuSelected.Render("▸ " + path)
		}
		return styleMenuItem.Render("  " + path)
	}

	q := strings.ToLower(token)
	base := filepath.Base(path)
	matchIdx := -1

	// Tier 1: basename prefix (case-insensitive).
	if strings.HasPrefix(strings.ToLower(base), q) {
		matchIdx = len(path) - len(base)
	}
	// Tier 2: full-path substring.
	if matchIdx < 0 {
		matchIdx = strings.Index(strings.ToLower(path), q)
	}
	if matchIdx < 0 {
		// Should not happen since filtered paths all match, but be safe.
		if selected {
			return styleMenuSelected.Render("▸ " + path)
		}
		return styleMenuItem.Render("  " + path)
	}

	matchEnd := matchIdx + len(token)
	before := path[:matchIdx]
	matched := path[matchIdx:matchEnd]
	after := path[matchEnd:]

	itemStyle := styleMenuItem
	prefix := "  "
	if selected {
		itemStyle = styleMenuSelected
		prefix = "▸ "
	}
	// The matched portion gets the accent highlight style regardless of
	// selection state, so the matching characters always stand out.
	return itemStyle.Render(prefix+before) + styleMatchHighlight.Render(matched) + itemStyle.Render(after)
}

// renderUserQuestion draws the inline picker shown while ask_user is
// waiting for a choice. Two modes:
//
//	single-select: ▸ marks the cursor. Enter on cursor accepts.
//	multi-select:  [x] / [ ] left of each row. Space toggles, Enter
//	               confirms the toggled set.
//
// The auto-appended "Other / type your own" row sits at index
// len(options); when the user picks it, pendingQuestionFreeText
// flips true and the textarea collects their reply until the next
// Enter.
func (m Model) renderUserQuestion() string {
	if m.pendingQuestion == nil {
		return ""
	}
	q := m.pendingQuestion.Question
	var sb strings.Builder

	sb.WriteString(styleApprovalHeader.Render("? " + q.Question))
	sb.WriteString("\n")

	// Free-text capture mode: picker collapses to a single line
	// echoing the user's typing. The textarea above the picker
	// renders the live input; this line just reminds the user
	// what mode they're in.
	if m.pendingQuestionFreeText {
		sb.WriteString(styleMuted.Render("  ✎ typing your own answer — Enter to submit · Esc to go back to choices"))
		sb.WriteString("\n")
		return sb.String()
	}

	otherIdx := len(q.Options)
	for i, opt := range q.Options {
		sb.WriteString(formatQuestionRow(i, opt.Label, opt.Description,
			i == m.pendingQuestionCursor,
			q.MultiSelect,
			m.pendingQuestionSelected[i],
		))
		sb.WriteString("\n")
	}
	// Auto-appended Other row. Slightly different label so the
	// user reads it as "I want to write my own answer" rather
	// than picking a fifth option.
	sb.WriteString(formatQuestionRow(otherIdx, "Other — type your own answer", "",
		otherIdx == m.pendingQuestionCursor,
		q.MultiSelect,
		m.pendingQuestionSelected[otherIdx],
	))
	sb.WriteString("\n")

	hint := "  ↑/↓ navigate · Enter accept · Esc cancel"
	if q.MultiSelect {
		hint = "  ↑/↓ navigate · Space toggle · Enter confirm · Esc cancel"
	}
	sb.WriteString(styleMuted.Render(hint))
	sb.WriteString("\n")
	return sb.String()
}

// formatQuestionRow renders one picker row. selected applies only in
// multi-select mode (Space toggle); cursor applies in both.
func formatQuestionRow(_ int, label, description string, cursor, multi, selected bool) string {
	// Marker column: cursor arrow + (multi-select only) checkbox.
	marker := "  "
	if cursor {
		marker = "▸ "
	}
	box := ""
	if multi {
		if selected {
			box = "[x] "
		} else {
			box = "[ ] "
		}
	}
	row := marker + box + label
	if description != "" {
		row += "  " + description
	}
	if cursor {
		return styleMenuSelected.Render(row)
	}
	if multi && selected {
		// Toggled-on rows in multi-select stand out even when the
		// cursor isn't on them, so the user can scan the page and
		// see "what have I picked so far?".
		return styleAssistantLabel.Render(row)
	}
	return styleMenuItem.Render(row)
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
	case permission.KindSkillInstall:
		// Three load-bearing pieces of info: which skill, from where,
		// to where. The source is what the model deduced from the
		// user's request — surfacing it gives the user the chance to
		// catch hallucinated URLs ("I asked for X, why is it pulling
		// from Y?") before files land on disk.
		subject = fmt.Sprintf("install skill %q from %s to %s",
			req.Action.SkillName,
			truncateOneLine(req.Action.SkillSource, 80),
			req.Action.SkillTarget)
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
// isCurrentPickerItem returns true when id matches the currently-active
// value for the open picker. This lets the picker annotate the preselected
// row with "(current)" — works for model / lang / effort pickers.
func (m Model) isCurrentPickerItem(id string) bool {
	switch m.pickerPurpose {
	case "lang":
		current := m.opts.Lang
		if current == "" {
			current = "auto"
		}
		return id == current
	case "effort":
		current := m.opts.Effort
		if current == "" {
			current = "off"
		}
		return id == current
	default: // "model", "", "setup-provider"
		return id == m.opts.Model
	}
}

func (m Model) renderModelPicker() string {
	if len(m.modelPickerFiltered) == 0 {
		return styleMuted.Render("  (no models — Esc to dismiss)") + "\n"
	}
	var sb strings.Builder
	for i, mc := range m.modelPickerFiltered {
		marker := "  "
		idLabel := mc.id
		if m.isCurrentPickerItem(mc.id) {
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

// highlightRefs finds @-prefixed file references in text and wraps each
// with the accent highlight style so they visually pop against the base
// user-message colour. Uses a regex matching @ followed by word, dot, slash,
// hyphen, or underscore characters (common file path patterns).
func highlightRefs(text string) string {
	re := regexp.MustCompile(`(@[\w.\-/]+)`)
	parts := re.Split(text, -1)
	matches := re.FindAllString(text, -1)

	var styled strings.Builder
	for i, part := range parts {
		if part != "" {
			styled.WriteString(styleUserText.Render(part))
		}
		if i < len(matches) {
			styled.WriteString(styleRefHighlight.Render(matches[i]))
		}
	}
	return styled.String()
}

// renderInput returns the textarea's view with @-prefixed file references
// highlighted in the accent colour. It post-processes the textarea.View()
// output so the user sees purple @-refs as they type.
func (m Model) renderInput() string {
	re := regexp.MustCompile(`@[\w.\-/]+`)
	return re.ReplaceAllStringFunc(m.input.View(), func(match string) string {
		return styleRefHighlight.Render(match)
	})
}

func renderCommittedUser(text string, width int) string {
	label := styleUserLabel.Render("▌ you")
	body := lipgloss.NewStyle().Width(width - 2).Render(highlightRefs(text))
	return "\n" + label + "\n" + body
}

// renderCommittedAssistant renders a completed assistant message for
// scrollback. content is already Markdown-rendered when md was
// available. The caller (applyAgentEvent on MessageEnd) only invokes
// this when content is non-empty — pure tool-call turns no longer get
// a `▸ seek` block — so we do not bother with an empty-content
// placeholder here.
func renderCommittedAssistant(content, reasoning string, showReasoning bool, width int) string {
	label := styleAssistantLabel.Render("▸ seek")
	out := label + "\n" + content
	if reasoning != "" {
		if showReasoning {
			out += "\n" + styleReasoning.Render("▸ reasoning:\n"+indent(reasoning, "    "))
		} else {
			out += "\n" + styleReasoning.Render("▸ reasoning hidden — Ctrl+R during streaming to expand")
		}
	}
	_ = width // wrap is already applied via the Markdown renderer
	return out
}

func renderCommittedToolOk(name, args, result string, d time.Duration, tokenTail string) string {
	var head string
	if name == skillToolName {
		head = styleToolSkill.Render(fmt.Sprintf("  ✦ skill: %s → %d bytes%s%s",
			parseSkillName(args), len(result), durationTail(d), tokenTail))
	} else {
		head = styleToolLine.Render(fmt.Sprintf("  ↳ %s(%s) → %d bytes%s%s",
			name, args, len(result), durationTail(d), tokenTail))
	}
	// If the result carries a ```diff fenced section (today: only emitted
	// by the edit tool), surface the diff coloured under the summary line
	// so the human can verify the change without leaving the TUI. Other
	// tools' results have no fence and stay one-line.
	body := extractDiffSection(result)
	if body == "" {
		return head
	}
	return head + "\n" + colorizeDiffBody(body)
}

// skillToolName matches internal/tools/skilltool.toolName; duplicated as a
// constant here to avoid pulling the whole tool package into the TUI's
// import graph for one string comparison.
const skillToolName = "Skill"

// formatActiveToolLabel builds the in-flight label + colour for an active
// tool call. Skill calls get the dedicated "✦ skill: <name>" form so the
// user sees the *which-skill* signal directly; everything else stays on
// the canonical "name(args) …" amber line.
func formatActiveToolLabel(name, args string) (string, lipgloss.Style) {
	if name == skillToolName {
		return fmt.Sprintf("✦ skill: %s …", parseSkillName(args)), styleToolSkill
	}
	return fmt.Sprintf("%s(%s) …", name, args), styleToolLine
}

// parseSkillName extracts the "name" field from the Skill tool's args
// JSON. The schema only has one required field (PRD v0 §4.6.3), so the
// parse is intentionally minimal — and tolerant: if args was truncated
// past the closing brace by truncateOneLine, falls back to "?" rather
// than failing the render.
func parseSkillName(args string) string {
	var v struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &v); err == nil && v.Name != "" {
		return v.Name
	}
	return "?"
}

// extractDiffSection returns the body of the first ```diff ... ``` fence
// found in s, with the fence delimiters themselves stripped. Returns ""
// when no fence is present.
//
// This is intentionally narrow: it pulls only the diff portion, dropping
// anything that came before (e.g. the edit tool's "edited /path: N
// replacements" header — which the TUI already shows in its own summary
// line). What's left is raw unified-diff text ready for line-by-line
// colouring.
func extractDiffSection(s string) string {
	const open = "```diff\n"
	start := strings.Index(s, open)
	if start == -1 {
		return ""
	}
	body := s[start+len(open):]
	if end := strings.Index(body, "\n```"); end != -1 {
		body = body[:end]
	}
	return body
}

// colorizeDiffBody renders a block of de-fenced unified-diff text. Caller
// must have already stripped the ```diff / ``` delimiters (see
// extractDiffSection). Per-line colours match colorizeDiffBlocks so the
// success and failure paths look the same.
func colorizeDiffBody(s string) string {
	var out strings.Builder
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		var rendered string
		switch {
		case strings.HasPrefix(ln, "---"), strings.HasPrefix(ln, "+++"):
			rendered = styleMuted.Render(ln)
		case strings.HasPrefix(ln, "+"):
			rendered = styleDiffAdd.Render(ln)
		case strings.HasPrefix(ln, "-"):
			rendered = styleToolError.Render(ln)
		default:
			rendered = styleMuted.Render(ln)
		}
		out.WriteString(rendered)
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func renderCommittedToolErr(name, args, err string, d time.Duration) string {
	// Split the message into [chrome] + [body] + [tail] so the body can
	// receive per-line styling while the framing keeps the uniform error
	// colour. The body recogniser is in colorizeDiffBlocks — outside any
	// ```diff fence it falls back to styleToolError, so a plain-text
	// error renders byte-for-byte the way it always did.
	//
	// Skill errors reuse the "✦ skill: <name>" header so the failed call
	// is still visually attributable to the skill mechanism — the colour
	// itself (red) is what marks it as failed.
	var head string
	if name == skillToolName {
		head = fmt.Sprintf("  ✦ skill: %s → ERROR: ", parseSkillName(args))
	} else {
		head = fmt.Sprintf("  ↳ %s(%s) → ERROR: ", name, args)
	}
	body := colorizeDiffBlocks(err, styleToolError)
	tail := durationTail(d)
	return styleToolError.Render(head) + body + styleToolError.Render(tail)
}

// colorizeDiffBlocks scans s for ```diff ... ``` fenced code blocks and
// renders the lines inside with per-line colour: `+` green (styleDiffAdd),
// `-` red (styleToolError), and structural lines (fence, `---`/`+++` file
// headers, `@@` hunks, context) muted. Everything OUTSIDE any fence is
// wrapped in defaultStyle.
//
// This is a TUI-display-only transformation. The tool's own result string
// — what the LLM sees — stays plain text; colours are added at scrollback-
// commit time and never travel back through the API. That separation is
// the load-bearing property: the model gets clean bytes, the human gets
// visual differentiation.
//
// Inputs without any ```diff fence take a fast path (single Render call).
func colorizeDiffBlocks(s string, defaultStyle lipgloss.Style) string {
	if !strings.Contains(s, "```diff") {
		return defaultStyle.Render(s)
	}
	lines := strings.Split(s, "\n")
	var out strings.Builder
	inDiff := false
	for i, ln := range lines {
		var rendered string
		switch {
		case !inDiff && strings.HasPrefix(ln, "```diff"):
			inDiff = true
			rendered = styleMuted.Render(ln)
		case inDiff && strings.HasPrefix(ln, "```"):
			inDiff = false
			rendered = styleMuted.Render(ln)
		case inDiff:
			switch {
			// `---` / `+++` file headers come BEFORE `-` / `+` add/del
			// detection — otherwise HasPrefix("---", "-") would mis-classify
			// them as deletions.
			case strings.HasPrefix(ln, "---"), strings.HasPrefix(ln, "+++"):
				rendered = styleMuted.Render(ln)
			case strings.HasPrefix(ln, "+"):
				rendered = styleDiffAdd.Render(ln)
			case strings.HasPrefix(ln, "-"):
				rendered = styleToolError.Render(ln)
			default:
				// Hunk headers (`@@ ...`) and unchanged context lines go
				// here. Muted keeps them readable but pushes the eye to
				// the `+`/`-` differences, which is where the signal is.
				rendered = styleMuted.Render(ln)
			}
		default:
			rendered = defaultStyle.Render(ln)
		}
		out.WriteString(rendered)
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
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

// formatTokenTail formats a completion token count for display after the
// elapsed-time tail on both the live spinner line and the committed line.
// Returns e.g. " · ↓2.3ktok" or " · ↓489tok". Empty string when n == 0.
func formatTokenTail(n int) string {
	if n == 0 {
		return ""
	}
	return " · ↓" + formatTokensK(n) + "tok"
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
		{"Shift+Tab", "Cycle mode: ask → plan → yolo → ask"},
		{"Ctrl+J", "Insert newline in input"},
		{"Ctrl+L", "Clear visible screen (same as /clear)"},
		{"Ctrl+R", "Toggle reasoning visibility"},
		{"Ctrl+C", "Quit seek"},
		{"/steer or Alt+Enter", "Interrupt current response with new instructions"},
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
	sb.WriteString(m.renderInput())
	sb.WriteString("\n")
	sb.WriteString(m.renderStatusBar())

	return sb.String()
}
