package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/agent"
)

// View renders the LIVE region only. Under inline mode, the terminal
// owns scrollback — committed conversation lines (user prompts, tool
// output, completed assistant messages) live there via tea.Println.
// The live region holds only volatile state and floats naturally at
// the end of the output stream.
//
// Layout (idle):
//
//	> input
//	status: …
//
// Layout (active stream / popup):
//
//	[active tools, one line each with a spinner]
//	[streaming assistant text]
//	[streaming reasoning, when Ctrl+R]
//	[popup: approval / menu / picker, if any]
//	── separator ─────────────────
//	> input
//	status: …
//
// CRITICAL: do NOT pad sb with trailing newlines to push the input to
// the absolute terminal floor. That was the M3-era drift class
// (`scrollbackLines` counter + `strings.Repeat("\n", pad)`). The
// renderer's cursor-up + EraseScreenBelow handles frame-to-frame
// height changes natively; let the live region sit where it sits.
func (m Model) View() string {
	if !m.ready {
		// Pre-WindowSizeMsg: minimal hint so the user doesn't see a
		// blank screen if bubbletea takes a moment to size up.
		return styleMuted.Render("starting…") + "\n"
	}

	var sb strings.Builder

	// Welcome banner (wordmark + cwd) — shown only on a session that
	// hasn't received a user submission yet. Gated on promptHistory
	// rather than m.turns because m.turns increments at TurnEnd, NOT
	// at submit time — so a turns-only gate left the banner pinned in
	// the live region from submit → TurnEnd. During that window
	// tea.Println'd user/assistant lines landed in scrollback ABOVE
	// the stuck banner, splitting the conversation across a 11-row
	// banner divider. Worse, when TurnEnd finally fired the banner
	// vanished in a single 11-row layout shrink — bubbletea's
	// cursor-up + EraseScreenBelow over-erased and wiped real
	// scrollback content. Gating on promptHistory makes the banner
	// disappear on the FIRST Enter, before any streaming redraws.
	if m.turns == 0 && len(m.promptHistory) == 0 {
		// Narrow-terminal fallback: skip the pixel banner if the
		// terminal is too small to render it without wrapping.
		if m.width >= pixelBannerMinWidth {
			sb.WriteByte('\n')
			sb.WriteString(renderBanner(m.bannerFrame))
			sb.WriteByte('\n')
			sb.WriteString(styleMuted.Render("  " + m.opts.CWD))
		} else {
			sb.WriteString(styleMuted.Render("  seek · " + m.opts.CWD))
		}
		sb.WriteByte('\n')
		sb.WriteByte('\n')
	}

	// Second-tier provider banner: warn that DeepSeek-exclusive features
	// (cache stats, FIM, Reasoner) are disabled.
	if m.opts.ProviderName != "" {
		banner := lipgloss.NewStyle().
			Foreground(colourTool).
			Render("⚠ Provider: " + m.opts.ProviderName + " — FIM / cache stats / Reasoner disabled")
		sb.WriteString(banner)
		sb.WriteString("\n")
	}

	// Plan task list: rendered as a fixed block at the top of the live
	// region whenever a plan has been approved (PlanSteps non-empty).
	// Survives across turns, so the user always sees their plan and
	// which step is in progress. Cleared on PlanProposalCancelled or
	// /plan off (see applyAgentEvent).
	if len(m.opts.PlanSteps) > 0 {
		sb.WriteString(renderPlanTaskList(m.opts.PlanSteps, m.opts.PlanCurrentIdx))
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

	// Active tool slots — finished ones stay visible until streamEnd
	// clears the list, so the live region's tool zone grows but never
	// shrinks within a turn. Running tools render with a spinner +
	// live elapsed; finished tools render with ✓ + locked duration
	// (computed at ToolExecEnd, not re-measured each frame).
	for _, t := range m.activeTools {
		label, style := formatActiveToolLabel(t.name, t.args)
		if t.finished {
			duration := t.completed.Sub(t.started)
			if d := formatCommittedDuration(duration); d != "" {
				label += " · " + d
			}
			if tok := formatTokenTail(t.completionTokens); tok != "" {
				label += " · " + tok
			}
			fmt.Fprintf(&sb, "%s %s\n", styleMuted.Render("✓"), style.Render(label))
		} else {
			if elapsed := formatToolElapsed(time.Since(t.started)); elapsed != "" {
				label += " · " + elapsed
			}
			fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(), style.Render(label))
		}
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
			sb.WriteString(styleReasoning.Render("▸ reasoning… (Ctrl+R to toggle)"))
			sb.WriteString("\n")
		}
	}

	// Bottom block — transient UI + (conditional) separator + input.
	// Popup-style UI (queue hint / setup banner / skill-armed badge /
	// approval / menu / picker) is rendered INSIDE bottomBuf above the
	// separator so it visually anchors to the input region.
	var bottomBuf strings.Builder

	// Queue / steer hint — only meaningful mid-stream.
	if m.streaming {
		if hint := m.renderQueueHint(); hint != "" {
			bottomBuf.WriteString(hint)
			bottomBuf.WriteString("\n")
		}
	}

	// Setup key-entry banner.
	if m.setupKeyEntry {
		bottomBuf.WriteString(styleMuted.Render(fmt.Sprintf(
			"✎ paste API key for %s — Enter to save, Esc to cancel",
			m.setupProvider)))
		bottomBuf.WriteString("\n")
	}

	// Skill-armed badge.
	if m.pendingSkill != "" {
		bottomBuf.WriteString(styleToolSkill.Render(fmt.Sprintf("✦ skill armed: %s", m.pendingSkill)))
		bottomBuf.WriteString(styleMuted.Render(" — next message uses this skill (/skill use clear to cancel)"))
		bottomBuf.WriteString("\n")
	}

	// Decision UIs (approval / ask_user / distill review): variable
	// height, user-blocking. They render in their own space; the
	// input may shift to accommodate them, which is acceptable
	// because the user has to attend to them before continuing.
	//
	// Filter popups (slash menu, model picker, path picker): render
	// in a FIXED reserved zone of menuMaxRows + 1 rows above the
	// separator. When no filter popup is open, the zone is blank —
	// keeping the input position byte-stable across "user pressed /
	// vs not". Without the reserved zone, opening any filter popup
	// shifts the input by 9 rows, which is the residual jumping that
	// users complained about even after fixed-height popups.
	switch {
	case m.pendingApproval != nil:
		bottomBuf.WriteString(m.renderApprovalPrompt())
	case m.pendingQuestion != nil:
		bottomBuf.WriteString(m.renderUserQuestion())
	case m.distillReviewOpen:
		bottomBuf.WriteString(m.renderDistillReview())
	case m.helpOverlayOpen:
		bottomBuf.WriteString(m.renderHelpOverlay())
	default:
		// Reserved-zone branch. Exactly one of the filter popups OR
		// the blank-zone fallback fires; all paths emit menuMaxRows+1
		// rows so the input position is the same in every branch.
		switch {
		case m.commandMenuOpen:
			bottomBuf.WriteString(m.renderCommandMenu())
		case m.modelPickerOpen:
			bottomBuf.WriteString(m.renderModelPicker())
		case m.pathPicker.open:
			bottomBuf.WriteString(m.renderPathPicker())
		default:
			// Blank reserved zone. NOTE: this is fixed-height padding,
			// NOT terminal-floor-pin padding. The drift class that led
			// to the alt-screen detour was variable-height padding
			// computed against m.height; this is a constant N regardless
			// of terminal size.
			bottomBuf.WriteString(strings.Repeat("\n", menuMaxRows+1))
		}
	}

	// Separator above the input. Always drawn — there's now always
	// content above (reserved zone or decision UI), so the separator
	// is a stable demarcation rather than a conditional one.
	bottomBuf.WriteString(styleMuted.Render(strings.Repeat("─", m.width)))
	bottomBuf.WriteString("\n")

	bottomBuf.WriteString(m.renderInput())
	bottomBuf.WriteString("\n")

	sb.WriteString(bottomBuf.String())

	// Status line. No bottom rule — under inline mode the line below
	// the status is either the next streamed update (still ours) or
	// the post-exit shell prompt (cleanly separated by the program's
	// natural teardown). The rule was a leftover from alt-screen
	// where the live region needed a visual "seal".
	sb.WriteString(m.renderStatusBar())

	return sb.String()
}

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

	doneCount := 0
	for _, st := range m.opts.PlanSteps {
		if st.Status == "completed" || st.Status == "skipped" {
			doneCount++
		}
	}

	return RenderStatusBar(StatusSnapshot{
		Model:            m.opts.Model,
		Effort:           m.opts.Effort,
		Yolo:             m.opts.Yolo,
		Plan:             m.opts.Plan,
		PlanSubstate:     m.opts.PlanSubstate,
		PlanStepsTotal:   len(m.opts.PlanSteps),
		PlanStepsDone:    doneCount,
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

// renderPlanTaskList renders the fixed-position task list shown above
// the active-tools / streaming region whenever a plan is active. Each
// step gets a marker reflecting its status:
//
//	✓  completed (muted)
//	▸  in_progress (accent)
//	·  pending (muted)
//	—  skipped (muted with "(skipped)" tail)
//
// The list trails a single blank line so it visually separates from
// the streaming content underneath. Wrapping is left to the terminal;
// step text is capped at 200 chars upstream by the propose tool.
func renderPlanTaskList(steps []agent.PlanStep, currentIdx int) string {
	if len(steps) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleMuted.Render("▾ plan"))
	sb.WriteByte('\n')
	for i, st := range steps {
		switch st.Status {
		case "completed":
			sb.WriteString(styleMuted.Render(fmt.Sprintf("  ✓ %d. %s", i+1, st.Text)))
		case "in_progress":
			sb.WriteString(lipgloss.NewStyle().Foreground(colourTool).Bold(true).Render(fmt.Sprintf("  ▸ %d. %s", i+1, st.Text)))
		case "skipped":
			sb.WriteString(styleMuted.Render(fmt.Sprintf("  — %d. %s (skipped)", i+1, st.Text)))
		default: // pending
			sb.WriteString(styleMuted.Render(fmt.Sprintf("  · %d. %s", i+1, st.Text)))
		}
		sb.WriteByte('\n')
	}
	_ = currentIdx // status mark on the step itself is the source of truth
	sb.WriteByte('\n')
	return sb.String()
}

// formatTokensK renders a token count as a compact string: raw below
// 1000, one-decimal "Xk" above (e.g. 99600 → "99.6k").
func formatTokensK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// menuMaxRows is the fixed-height popup contract: every filter-driven
// menu/picker renders exactly this many item rows regardless of how
// many candidates match. Without this, filtering would shrink the
// popup as the user types and grow it as they backspace — which under
// inline mode visibly shifts the input up and down on every keystroke.
// Holding row count constant pins the input position while the user
// narrows their selection.
const menuMaxRows = 8

// menuWindow returns the visible [start, end) slice of items for a
// fixed-height popup with `total` candidates and the cursor at
// `selected`. When total <= menuMaxRows, the entire list is in view.
// Otherwise the window of size menuMaxRows follows selected so the
// cursor stays centred-ish (and never scrolls past either end).
func menuWindow(total, selected int) (start, end int) {
	if total <= menuMaxRows {
		return 0, total
	}
	start = selected - menuMaxRows/2
	if start < 0 {
		start = 0
	}
	end = start + menuMaxRows
	if end > total {
		end = total
		start = end - menuMaxRows
	}
	return start, end
}

// padMenuRows appends blank lines to sb so the menu's item section
// reaches exactly menuMaxRows rows. emitted is how many item rows the
// caller already wrote. No-op when the menu was already full.
func padMenuRows(sb *strings.Builder, emitted int) {
	for i := emitted; i < menuMaxRows; i++ {
		sb.WriteByte('\n')
	}
}

// renderCommittedUser renders the user's prompt for scrollback. Called
// before tea.Println.
// renderPathPicker draws the @-completion dropdown. Fixed-height
// rendering (menuMaxRows + footer) keeps the input from shifting as
// the user narrows the filter.
func (m Model) renderPathPicker() string {
	var sb strings.Builder
	if len(m.pathPicker.filtered) == 0 {
		sb.WriteString(styleMuted.Render("  (no files match — Esc to dismiss)"))
		sb.WriteByte('\n')
		// One row already emitted ("no files match"); pad the rest.
		padMenuRows(&sb, 1)
	} else {
		start, end := menuWindow(len(m.pathPicker.filtered), m.pathPicker.selected)
		for i := start; i < end; i++ {
			p := m.pathPicker.filtered[i]
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
		padMenuRows(&sb, end-start)
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

// renderHelpOverlay draws the /help content as a dismissable overlay
// in the live region. The overlay replaces the old scrollback-based
// help output so it doesn't mix with the conversation history.
func (m Model) renderHelpOverlay() string {
	if !m.helpOverlayOpen || m.helpContent == "" {
		return ""
	}
	// Wrap the help block in a NormalBorder + padding so it reads as
	// a panel rather than blending into scrollback. We deliberately do
	// NOT compute a centered floating panel against m.height (the M7
	// design did, and it required scrollbackLines accounting that
	// caused the M3-era drift bugs — see docs/pitfalls.md). The lighter
	// "just a border" treatment gives the visual cue without re-
	// introducing layout-by-magic-number.
	var sb strings.Builder
	sb.WriteString(m.helpContent)
	sb.WriteString("\n")
	sb.WriteString(styleMuted.Render("Press Esc or q to close help"))

	// Constrain the panel width so it doesn't span the whole terminal
	// on wide screens — readability tops out around 100 cols. Border
	// adds 2 cols, padding adds 2 more, so cap the inner content at
	// min(m.width-6, 100).
	maxW := m.width - 6
	if maxW > 100 {
		maxW = 100
	}
	if maxW < 20 {
		maxW = 20
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colourMuted).
		Padding(0, 1).
		Width(maxW).
		Render(sb.String()) + "\n"
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
//
// Fixed-height rendering (menuMaxRows item rows + 1 footer) keeps the
// input position pinned as the user types and the filtered list
// shrinks; without this, every keystroke would resize the popup and
// shift the input up or down.
func (m Model) renderCommandMenu() string {
	var sb strings.Builder
	if len(m.commandMenuFiltered) == 0 {
		sb.WriteString(styleMuted.Render("  (no commands match — Esc to dismiss)"))
		sb.WriteByte('\n')
		padMenuRows(&sb, 1)
	} else {
		start, end := menuWindow(len(m.commandMenuFiltered), m.commandMenuSelected)
		for i := start; i < end; i++ {
			c := m.commandMenuFiltered[i]
			row := fmt.Sprintf("%-22s  %s", c.usage, c.description)
			if i == m.commandMenuSelected {
				sb.WriteString(styleMenuSelected.Render("▸ " + row))
			} else {
				sb.WriteString(styleMenuItem.Render("  " + row))
			}
			sb.WriteString("\n")
		}
		padMenuRows(&sb, end-start)
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
	var sb strings.Builder
	if len(m.modelPickerFiltered) == 0 {
		sb.WriteString(styleMuted.Render("  (no models — Esc to dismiss)"))
		sb.WriteByte('\n')
		padMenuRows(&sb, 1)
	} else {
		start, end := menuWindow(len(m.modelPickerFiltered), m.modelPickerSelected)
		for i := start; i < end; i++ {
			mc := m.modelPickerFiltered[i]
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
		padMenuRows(&sb, end-start)
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

// renderUserBlock renders a completed user prompt for scrollback. Shared
// by the live submit() path (update_agent.go) and renderReplayHistory
// (replay.go) — the previous fork (renderCommittedUser + formatReplayUser)
// drifted in width handling and was the kind of divergence Option A of
// the v0.3.x review was meant to retire. width == 0 (replay pre-tea,
// terminal size unknown) skips lipgloss wrapping and lets the terminal
// wrap natively; live always passes m.width > 0.
func renderUserBlock(text string, width int) string {
	label := styleUserLabel.Render("▌ you")
	body := highlightRefs(text)
	if width > 0 {
		body = lipgloss.NewStyle().Width(width - 2).Render(body)
	}
	return "\n" + label + "\n" + body
}

// renderAssistantBlock renders a completed assistant message for
// scrollback. Returns "" when content is empty so callers can drop
// the no-op tea.Println — pure tool-call turns (reasoning + tool_calls,
// no narrative) MUST NOT emit a `▸ seek` block (the `↳ tool(...)` lines
// already convey what happened).
//
// Markdown rendering happens INSIDE the function (via md) so live and
// replay get byte-identical output for the same (content, reasoning,
// width, md, showReasoning) inputs. Previous design had callers pre-
// render Markdown, which is exactly where the replay path forgot to
// — the kind of divergence Option A was meant to retire. Pass md == nil
// to skip rendering and emit raw content (used by tests; production
// callers always have a renderer ready).
func renderAssistantBlock(content, reasoning string, showReasoning bool, width int, md *glamour.TermRenderer) string {
	if content == "" {
		return ""
	}
	rendered := renderMarkdown(md, content)
	if rendered == "" {
		rendered = content
	}
	out := styleAssistantLabel.Render("▸ seek") + "\n" + rendered
	if reasoning != "" {
		if showReasoning {
			out += "\n" + styleReasoning.Render("▸ reasoning:\n"+indent(reasoning, "    "))
		} else {
			out += "\n" + styleReasoning.Render("▸ reasoning hidden — Ctrl+R to toggle during streaming")
		}
	}
	_ = width // wrap is already applied via the Markdown renderer
	return out
}

// renderToolResultLine renders one committed tool invocation for
// scrollback. Replaces the old renderCommittedToolOk / Err pair so live
// and replay share a single code path. err != nil → error rendering
// (red header, error body inline, no diff section); err == nil → ok
// rendering (muted header, optional ```diff body coloured per-line).
//
// d == 0 suppresses the duration tail (replay has no recorded
// duration; formatCommittedDuration already drops sub-100ms operations
// anyway). completionTokens == 0 suppresses the token tail.
func renderToolResultLine(name, args, result string, err error, d time.Duration, completionTokens int) string {
	if err != nil {
		var head string
		if name == skillToolName {
			head = fmt.Sprintf("  ✦ skill: %s → ERROR: ", parseSkillName(args))
		} else {
			head = fmt.Sprintf("  ↳ %s(%s) → ERROR: ", name, args)
		}
		body := colorizeDiffBlocks(err.Error(), styleToolError)
		tail := durationTail(d)
		return styleToolError.Render(head) + body + styleToolError.Render(tail)
	}

	tokenTail := formatTokenTail(completionTokens)
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
