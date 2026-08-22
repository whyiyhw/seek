package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/routines"
	"github.com/whyiyhw/seek/internal/subagent"
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
// The status bar is the LAST row of the live region — the terminal-
// bottom anchor. The input sits directly above it; at idle there are
// NO blank rows between content and input (the popup zone occupies
// rows only while a popup is open).
//
// CRITICAL: do NOT pad sb with trailing newlines to push the input to
// the absolute terminal floor. That was the M3-era drift class
// (`scrollbackLines` counter + `strings.Repeat("\n", pad)`). The
// renderer's cursor-up + EraseScreenBelow handles frame-to-frame
// height changes natively; let the live region sit where it sits.
func (m Model) View() tea.View {
	if !m.ready {
		// Pre-WindowSizeMsg: minimal hint so the user doesn't see a
		// blank screen if bubbletea takes a moment to size up.
		return tea.NewView(styleMuted.Render("starting…") + "\n")
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
			// Meta line: CWD (muted) · seek+version (User cyan) · creator (Accent magenta)
			sb.WriteString(styleMuted.Render("  " + m.opts.CWD + "  ·  "))
			sb.WriteString(lipgloss.NewStyle().Foreground(colourUser).Render("seek " + VersionString()))
			sb.WriteString(styleMuted.Render("  ·  "))
			sb.WriteString(lipgloss.NewStyle().Foreground(colourAccent).Render(Creator))
		} else {
			// Narrow fallback: same three segments on one line, no wordmark
			sb.WriteString(styleMuted.Render("  seek · " + m.opts.CWD + "  ·  "))
			sb.WriteString(lipgloss.NewStyle().Foreground(colourUser).Render(VersionString()))
			sb.WriteString(styleMuted.Render("  ·  "))
			sb.WriteString(lipgloss.NewStyle().Foreground(colourAccent).Render(Creator))
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

	// Active tool status — collapsed to a SINGLE live line listing every
	// still-running tool (spinner + label + live elapsed each, " · "
	// separated) plus "✓ N done" / "! N failed" counters, so the tool
	// zone stays one row no matter how many tools the turn dispatches.
	// Finished tools are NOT listed here — their authoritative record is
	// the `↳ name(args) → N bytes` line committed to scrollback at
	// ToolExecEnd; the live line only answers "what's running now +
	// how many finished". Constant height keeps the input from
	// twitching as tools complete within a turn.
	if len(m.activeTools) > 0 {
		var parts []string
		for _, t := range m.activeTools {
			if t.finished {
				continue
			}
			label, style := formatActiveToolLabel(t.name, t.args)
			if elapsed := formatToolElapsed(time.Since(t.started)); elapsed != "" {
				label += " · " + elapsed
			}
			parts = append(parts, m.spinner.View()+" "+style.Render(label))
		}
		var done, failed int
		for _, t := range m.activeTools {
			if t.finished {
				if t.failed {
					failed++
				} else {
					done++
				}
			}
		}
		if done > 0 {
			parts = append(parts, styleMuted.Render(fmt.Sprintf("✓ %d done", done)))
		}
		if failed > 0 {
			parts = append(parts, styleToolError.Render(fmt.Sprintf("! %d failed", failed)))
		}
		if len(parts) > 0 {
			fmt.Fprintf(&sb, "%s\n", strings.Join(parts, " · "))
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
			// Shown reasoning streams BEFORE content, so no chunk
			// commit can bound it yet (the guard runs on content
			// deltas). Render only the TAIL — a thinking stream taller
			// than the terminal is unreadable anyway and would trip the
			// inline-renderer freeze. The full reasoning still lands
			// once, in the MessageEnd scrollback commit.
			sb.WriteString(styleReasoning.Render(reasoningBlock(
				foldReasoningTail(m.curReasoning, m.width, m.reasoningTailRows()))))
			sb.WriteString("\n")
		} else {
			// Hidden-reasoning placeholder. Spinner + elapsed makes the
			// "still working" state visible — a static line reads as
			// frozen, and reasoning can be the longest silent gap in a
			// turn (10-90s on /distill, seconds on normal think tool
			// calls). Same pattern as the thinking… line above and the
			// active-tool slots.
			label := "reasoning…"
			if !m.streamStartTime.IsZero() {
				if el := formatToolElapsed(time.Since(m.streamStartTime)); el != "" {
					label = fmt.Sprintf("reasoning… %s", el)
				}
			}
			fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(), styleReasoning.Render("▸ "+label+" (Ctrl+R to toggle)"))
		}
	}

	// Bottom block — transient UI + separator + input + status bar.
	// Popup-style UI (queue hint / setup banner / approval / menu /
	// picker) renders INSIDE bottomBuf above the separator so it
	// visually anchors to the input region. The status bar is the
	// LAST row — the terminal-bottom anchor — with the input sitting
	// directly above it and no blank rows in between.
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

	// Decision UIs (approval / ask_user / distill review / help
	// overlay): variable height, user-blocking. They render in their
	// own space; the input shifts to accommodate them, which is
	// acceptable because the user has to attend to them before
	// continuing.
	//
	// Filter popups (slash menu, model picker, path picker): render
	// ONLY while open, directly above the separator. At idle nothing
	// is emitted — the input sits at the end of the live region, i.e.
	// the terminal bottom. Opening a popup grows the live region and
	// pushes the input down by menuMaxRows+1 rows; that shift is the
	// same "content grew" behaviour as streaming output and is fine —
	// the user is interacting with the popup. (The previous design
	// reserved the rows even when idle to keep the input byte-stable;
	// that is what produced the blank rows above the input that made
	// it look like it was floating mid-screen.)
	switch {
	case m.pendingApproval != nil:
		bottomBuf.WriteString(m.renderApprovalPrompt())
	case m.pendingBatch != nil:
		// v2 multi-question stack — takes precedence over v1
		// pendingQuestion since handleBatchKey installs a faux
		// pendingQuestion for the active question internally.
		bottomBuf.WriteString(m.renderBatchStack())
	case m.pendingQuestion != nil:
		bottomBuf.WriteString(m.renderUserQuestion())
	case m.distillReviewOpen:
		bottomBuf.WriteString(m.renderDistillReview())
	case m.helpOverlayOpen:
		bottomBuf.WriteString(m.renderHelpOverlay())
	case m.commandMenuOpen:
		bottomBuf.WriteString(m.renderCommandMenu())
	case m.modelPickerOpen:
		bottomBuf.WriteString(m.renderModelPicker())
	case m.pathPicker.open:
		bottomBuf.WriteString(m.renderPathPicker())
	}

	// Separator between the content / popup zone and the bottom block
	// (input + status bar). Always drawn — the input and status bar
	// are always present — so it is a stable demarcation.
	bottomBuf.WriteString(styleMuted.Render(strings.Repeat("─", m.width)))
	bottomBuf.WriteString("\n")

	if hint := m.renderSuggestedReplyHint(); hint != "" {
		bottomBuf.WriteString(hint)
		bottomBuf.WriteString("\n")
	}

	bottomBuf.WriteString(m.renderInput())
	bottomBuf.WriteString("\n")

	// Status bar as the LAST row — the terminal-bottom anchor. The
	// input sits directly above it (with any suggestion hint between),
	// so the bottom block reads as input + status with no dead space.
	// (An earlier attempt moved the status ABOVE the input so the
	// input would be the last row; that left the status floating
	// mid-screen — inline mode has no fixed top, so "above the input"
	// is not "at the top". The input does not need to be the last
	// row; it needs no blank rows around it.)
	bottomBuf.WriteString(m.renderStatusBar())

	sb.WriteString(bottomBuf.String())

	return tea.NewView(sb.String())
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
	// Long-reply chunk threshold: the assistant live block must stay
	// under the terminal height MINUS the rest of the live region —
	// tool status (1), reasoning placeholder (1), bottom block
	// (separator + input + status, ~6) and a 2-row safety margin. If
	// the TOTAL live region exceeds the terminal, the inline renderer's
	// cursor-up breaks AND the overflow rows (tool status, reasoning
	// placeholder, segment label) get pushed into the terminal
	// scrollback, where they interleave with committed segments and
	// read as format corruption. See shouldChunkCommit /
	// docs/pitfalls.md "Long streaming replies freeze the screen".
	//
	// The floor is 4, not a "sane minimum" like 12: a floor above
	// height-10 on short terminals (height ≤ 22 — vertical splits)
	// re-enables the overflow the threshold exists to prevent.
	// Chunking every few rows on a tiny terminal beats a frozen screen.
	base := m.height - 10
	if base < 4 {
		base = 4
	}
	m.chunkThreshold = base
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
		SubagentsActive:  subagentsActiveCount(m.opts.Subagents),
		CronsRegistered:  cronsRegisteredCount(),
		GoalActive:       m.goalActive,
		GoalTurns:        m.goalTurns,
		GoalMaxTurns:     m.goalCaps.MaxTurns,
	})
}

// subagentsActiveCount nil-safe wraps Manager.ActiveCount so the
// view-side caller (a hot render path) doesn't have to guard each
// time. nil Manager → 0, suppressing the badge. Lives next to the
// status-bar producer rather than as a Manager method because
// nil-safety is a TUI concern, not a Manager API concern.
func subagentsActiveCount(m *subagent.Manager) int {
	if m == nil {
		return 0
	}
	return m.ActiveCount()
}

// cronsRegisteredCount opens the routines Store and counts
// registered jobs. Per-render filesystem I/O — but the
// render path fires on Bubble Tea events (input, ticks,
// agent stream), NOT at 60fps, so the cost is bounded. Any
// I/O error (read-only ~/.seek, jobs.jsonl corrupt) silently
// returns 0 so the badge disappears rather than the TUI
// rendering a confused error in the status bar.
//
// Lives at the TUI layer (not as a routines.Manager method)
// for the same reason as subagentsActiveCount: error tolerance
// is a TUI concern, not a Store API concern.
func cronsRegisteredCount() int {
	store, err := routines.OpenStore()
	if err != nil {
		return 0
	}
	jobs, err := store.List()
	if err != nil {
		return 0
	}
	return len(jobs)
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
// narrows their selection. The rows exist only while the popup is
// open — idle View() emits nothing for them (see the bottom block in
// View).
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

	// v2 preview rendering: when the cursor is on an option that
	// has a Preview string, append the preview block. Layout
	// depends on terminal width:
	//   - >= 100 cols: render side-by-side via lipgloss
	//     JoinHorizontal (picker on the left, preview on the right)
	//   - < 100 cols: append inline as an indented block under
	//     the picker.
	// Cursor over the auto-appended "Other" row has no preview
	// (free-text mode), so we only render when the cursor is on a
	// real option.
	if m.pendingQuestionCursor < len(q.Options) {
		opt := q.Options[m.pendingQuestionCursor]
		if opt.Preview != "" {
			preview := truncatePreview(opt.Preview, previewMaxLines, previewMaxCols)
			if m.width >= previewSidePanelMinCols {
				// Wide terminal — assemble picker + preview side
				// by side. Replace the picker output we just built
				// with the joined version.
				panel := stylePreviewBox.Render(preview)
				joined := lipgloss.JoinHorizontal(lipgloss.Top, sb.String(), "  ", panel)
				return joined + "\n"
			}
			// Narrow terminal — inline below the picker.
			sb.WriteString("\n")
			sb.WriteString(stylePreviewBox.Render(preview))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// Preview rendering knobs. See feature-askuser-v2.md §5.3.
const (
	previewMaxLines         = 12  // truncate vertically
	previewMaxCols          = 80  // truncate horizontally
	previewSidePanelMinCols = 100 // wide-terminal threshold
)

// stylePreviewBox draws the preview content with a thin border so
// it reads as a distinct panel from the picker. Colours come from
// the muted palette already used elsewhere in the TUI so it
// doesn't compete visually with the active cursor.
var stylePreviewBox = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder()).
	BorderForeground(colourMuted).
	Padding(0, 1)

// truncatePreview caps a preview string to at most maxLines lines
// and maxCols runes per line. If either bound trips, appends an
// explicit "[truncated]" marker so the user knows there's more
// content the option author wrote. Trailing whitespace on
// truncated lines is preserved (might be part of ASCII art).
func truncatePreview(s string, maxLines, maxCols int) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	for i, line := range lines {
		// Rune-aware truncation — multi-byte chars (CJK / box-
		// drawing glyphs) shouldn't get sliced mid-byte.
		runes := []rune(line)
		if len(runes) > maxCols {
			lines[i] = string(runes[:maxCols])
			truncated = true
		}
	}
	out := strings.Join(lines, "\n")
	if truncated {
		out += "\n" + styleMuted.Render("[truncated]")
	}
	return out
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

// renderBatchStack (v2) draws the multi-question stack: answered
// questions show dim with their chosen label / free-text, the
// currently-active question shows the full picker (delegating to
// renderUserQuestion via the borrowed pendingQuestion pointer),
// and pending questions show as dim placeholders.
//
// Layout in narrow / wide terminals:
//   - Narrow (< 100 cols): everything stacks vertically; preview
//     for the active option renders inline as an indented block
//     under the picker.
//   - Wide  (>= 100 cols): planned for phase 2b — same vertical
//     stack, but preview moves to a right-hand side panel. The
//     side-panel branch is not yet wired (PRD §3.3 follow-up).
//
// Single-question batches degenerate to the v1 single-picker UX
// since the "answered" and "pending" lists are both empty and the
// active branch renders renderUserQuestion. Useful: callers
// always get the same code path whether they sent 1 or 4
// questions.
func (m Model) renderBatchStack() string {
	if m.pendingBatch == nil {
		return ""
	}
	var sb strings.Builder
	questions := m.pendingBatch.Batch.Questions
	total := len(questions)

	// Top header — shows progress when there's more than one
	// question. Single-question batches skip this to match the v1
	// look exactly.
	if total > 1 {
		hdr := fmt.Sprintf("? %d question%s · %d of %d",
			total, plural(total), m.pendingBatchIdx+1, total)
		sb.WriteString(styleApprovalHeader.Render(hdr))
		sb.WriteString("\n")
	}

	// Already-answered questions: dim, with chosen labels.
	for i := 0; i < m.pendingBatchIdx; i++ {
		q := questions[i]
		ans := m.pendingBatchAnswers[i]
		topic := batchTopicLabel(q, i+1)
		sb.WriteString(styleMuted.Render("  ✓ " + topic + ": " + summariseAnswer(q, ans)))
		sb.WriteString("\n")
	}

	// Currently-active question: borrow the v1 single-picker
	// renderer. handleBatchKey already installed pendingQuestion
	// to point at this question's data; renderUserQuestion reads
	// pendingQuestion + the shared per-question state and draws
	// the picker exactly like the v1 path.
	if m.pendingBatchIdx < total {
		q := questions[m.pendingBatchIdx]
		if total > 1 {
			topic := batchTopicLabel(q, m.pendingBatchIdx+1)
			sb.WriteString(styleAssistantLabel.Render("▸ " + topic))
			sb.WriteString("\n")
		}
		// renderUserQuestion needs pendingQuestion to be set; the
		// handleBatchKey path sets it but the view runs out of
		// band, so set a transient one here for rendering only.
		// (Direct field write — Model is a value type so this
		// doesn't mutate the actual Model state used by Update.)
		mview := m
		mview.pendingQuestion = &askuser.Request{Question: q}
		sb.WriteString(mview.renderUserQuestion())
	}

	// Pending (not-yet-asked) questions: just the topic label.
	for i := m.pendingBatchIdx + 1; i < total; i++ {
		q := questions[i]
		topic := batchTopicLabel(q, i+1)
		sb.WriteString(styleMuted.Render("  · " + topic))
		sb.WriteString("\n")
	}

	return sb.String()
}

// batchTopicLabel returns the chip-style label for a question in
// a batch stack — prefers q.Header (the v2 field) and falls back
// to a numbered "Question N" when no header was set.
func batchTopicLabel(q askuser.Question, n int) string {
	if q.Header != "" {
		return q.Header
	}
	return fmt.Sprintf("Question %d", n)
}

// summariseAnswer renders a one-line summary of an Answer for the
// "already answered" section of the batch stack. Multi-select
// answers concatenate option labels with commas; free-text shows
// truncated with quotes; cancelled answers show as "(cancelled)".
func summariseAnswer(q askuser.Question, ans askuser.Answer) string {
	if ans.Cancelled {
		return "(cancelled)"
	}
	if ans.FreeText != "" {
		txt := ans.FreeText
		if len(txt) > 40 {
			txt = txt[:37] + "..."
		}
		return `"` + txt + `"`
	}
	if len(ans.ChosenIDs) == 0 {
		return "(empty)"
	}
	// Map ChosenIDs back to labels for display.
	labels := make([]string, 0, len(ans.ChosenIDs))
	for _, id := range ans.ChosenIDs {
		for _, opt := range q.Options {
			if opt.ID == id {
				labels = append(labels, opt.Label)
				break
			}
		}
	}
	if len(labels) == 0 {
		return strings.Join(ans.ChosenIDs, ", ")
	}
	return strings.Join(labels, ", ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
		if req.Action.Display.MemoryTagline != "" {
			subject = fmt.Sprintf("save memory %q — %s",
				req.Action.Display.MemoryName,
				truncateOneLine(req.Action.Display.MemoryTagline, 100))
		} else {
			subject = fmt.Sprintf("save memory %q", req.Action.Display.MemoryName)
		}
	case permission.KindSkillInstall:
		// Three load-bearing pieces of info: which skill, from where,
		// to where. The source is what the model deduced from the
		// user's request — surfacing it gives the user the chance to
		// catch hallucinated URLs ("I asked for X, why is it pulling
		// from Y?") before files land on disk.
		subject = fmt.Sprintf("install skill %q from %s to %s",
			req.Action.Display.SkillName,
			truncateOneLine(req.Action.Display.SkillSource, 80),
			req.Action.Display.SkillTarget)
	default:
		subject = fmt.Sprintf("%s %q (outside CWD)", req.Action.Kind, req.Action.Path)
	}

	var sb strings.Builder
	sb.WriteString(styleApprovalHeader.Render("⚠ approve " + subject + "?"))
	sb.WriteString("\n")

	if req.Action.Display.Diff != "" {
		sb.WriteString(renderDiff(req.Action.Display.Diff, m.width))
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
// row with "(current)" — works for model / effort pickers.
func (m Model) isCurrentPickerItem(id string) bool {
	switch m.pickerPurpose {
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
			// The (current) suffix rides in the free-form description
			// column, NOT on the id: %-32s pads but never truncates, so
			// a 28-char id + " (current)" would break the id column's
			// alignment (and, on a long id, push the row past terminal
			// width — the wrap-ghost failure class again).
			desc := mc.description
			if m.isCurrentPickerItem(mc.id) {
				desc = desc + " (current)"
			}
			row := fmt.Sprintf("%-32s  %s", mc.id, desc)
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

// renderSuggestedReplyHint returns the muted "↳ tab: ..." line shown
// below the input box when a v4 柱 D side-channel prediction is
// available AND the input box is empty. Returns "" otherwise — the
// caller suppresses the surrounding newline. PRD docs/prd/
// feature-suggested-reply.md §4.4.
//
// Gate (in order):
//  1. suggestedReply non-empty
//  2. suggestedReplyValid true (not invalidated by Esc / typing)
//  3. input box empty (don't render OVER a user-typed prompt)
//  4. not streaming + not in modal entry mode — the placeholder is a
//     suggestion for "what to send next", which is meaningless during
//     review-branch / setup-key wizards.
func (m Model) renderSuggestedReplyHint() string {
	if m.suggestedReply == "" || !m.suggestedReplyValid {
		return ""
	}
	if m.input.Value() != "" {
		return ""
	}
	if m.streaming || m.setupKeyEntry || m.reviewBranchEntry {
		return ""
	}
	return styleMuted.Render("  ↳ tab: " + truncateOneLine(m.suggestedReply, max(20, m.width-12)))
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
	body := dimImageMarkers(highlightRefs(text))
	if width > 0 {
		body = lipgloss.NewStyle().Width(width - 2).Render(body)
	}
	return "\n" + label + "\n" + body
}

// dimImageMarkers mutes every "[image: " marker line — the collapsed
// display for natively-attached and OCR-era images (feature-vision
// §7.3: folded marker, not thumbnail). One rule covers live submit,
// scrollback and replay since they all go through renderUserBlock; the
// prefix is the wire-format family shared with the 柱 Q blocks, so
// both generations dim identically.
func dimImageMarkers(s string) string {
	if !strings.Contains(s, "[image: ") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "[image: ") {
			lines[i] = styleMuted.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// reasoningBlock formats the model's reasoning as a visually distinct
// aside: a "▸ reasoning" header above a left-gutter (│) block. The
// gutter — not colour — is what sets the model's private reasoning
// apart from the assistant prose around it, matching styles.go's
// "differentiation comes from layout, not chroma". Callers wrap the
// whole string in styleReasoning (italic + dim), so this stays a pure,
// deterministic formatter — safe for renderAssistantBlock's byte-
// identical live/replay contract. Shared by the streaming live region
// and the committed scrollback block so both render identically.
func reasoningBlock(reasoning string) string {
	return "▸ reasoning\n" + indent(reasoning, "│ ")
}

// reasoningTailRows is the rendered-row budget for shown streaming
// reasoning: chunkThreshold minus the rows the rest of the live block
// needs (label, a couple of content rows, the fold marker). Keeps the
// live region inside one terminal height during a long thinking phase,
// where no chunk commit can fire yet (the guard runs on content
// deltas; reasoning streams first).
func (m Model) reasoningTailRows() int {
	if m.chunkThreshold <= 0 {
		return 1 << 30 // no layout yet — never fold
	}
	n := m.chunkThreshold - 4
	if n < 3 {
		return 3
	}
	return n
}

// foldReasoningTail bounds shown streaming reasoning to its last
// maxRows wrapped rows, prefixing a fold marker when it truncates.
// Wrapping matches shouldChunkCommit's row estimate (wrap at width-2)
// so the estimate follows the actual render shape.
func foldReasoningTail(reasoning string, width, maxRows int) string {
	wrapped := wrap(reasoning, width-2)
	lines := strings.Split(wrapped, "\n")
	if len(lines) <= maxRows {
		return wrapped
	}
	keep := maxRows - 1 // one row goes to the fold marker
	if keep < 1 {
		keep = 1
	}
	return "… (earlier reasoning folded)\n" + strings.Join(lines[len(lines)-keep:], "\n")
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
	return renderAssistantBlockLabel(content, reasoning, showReasoning, width, md, "▸ seek")
}

// renderAssistantBlockLabel is the label-parameterised form of
// renderAssistantBlock. Long replies are chunk-committed with a
// "▸ seek (续)" continuation label (see commitChunk); the plain
// renderer keeps "▸ seek" for single-block messages and replay.
func renderAssistantBlockLabel(content, reasoning string, showReasoning bool, width int, md *glamour.TermRenderer, label string) string {
	if content == "" {
		return ""
	}
	rendered := renderMarkdown(md, content)
	if rendered == "" {
		rendered = content
	}
	out := styleAssistantLabel.Render(label) + "\n" + rendered
	if reasoning != "" {
		if showReasoning {
			out += "\n" + styleReasoning.Render(reasoningBlock(reasoning))
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
//
// glamour v2 (charm.land module path) since 2026-08 — the last v1-era
// dependency retired; the module graph no longer carries termenv /
// reflow / lipgloss v1. v2 removed WithAutoStyle: the empty-style
// fallback below pins "dark" explicitly, which is both v2's default
// and detectGlamourStyle's non-tty fallback, so the two stay aligned.

// newMarkdownRenderer builds a glamour renderer at the given width.
// style is "dark" / "light" / "" (dark fallback). The host pre-detects
// the style BEFORE the program starts (cmd/seek does this) to avoid the
// OSC 11 query leaking into the textarea — see PRD §4.9 and
// docs/pitfalls.md.
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
		// v2 removed WithAutoStyle (its runtime OSC query was the
		// leak-into-textarea pitfall anyway). Explicit dark matches v2's
		// own default AND detectGlamourStyle's non-tty fallback.
		opts = append(opts, glamour.WithStylePath("dark"))
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
