package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/suggester"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// completionRe extracts "completion <number>" from think tool result text.
// formatResult in internal/tools/think produces:
//
//	usage: prompt 956, completion 2345, cache hit 0 / miss 956
//
// This is a TUI-level coupling to the think tool's output format — keep in
// sync if formatResult changes.
var completionRe = regexp.MustCompile(`completion (\d+)`)

// handleStreamEnd is the streamEndMsg case lifted out of Update(). It
// finalises a stream (clears live buffers, persists session, restores
// the user's last prompt on cancel) and then dispatches a queued or
// steered message if one is waiting. Returns batched cmds for Println
// output (interrupt notice, queue/steer marker) plus any submit-driven
// cmds when a queued message auto-fires.
// sessionNotifyCmd returns a best-effort command that pings the configured
// push webhook with a "session.completed" event when an interactive turn
// finished naturally after running at least Options.SessionNotifySeconds.
// Returns nil when interactive notify is off (no webhook / seconds<=0), the
// turn was Esc-cancelled, the start time is unset, or the turn was shorter
// than the gate. The POST runs off the UI thread inside a tea.Cmd and has
// its own timeout; the webhook dispatcher filters by each webhook's events
// list, so only webhooks subscribed to session.completed receive it.
func (m Model) sessionNotifyCmd(wasCanceled bool) tea.Cmd {
	if m.opts.Webhook == nil || m.opts.SessionNotifySeconds <= 0 || wasCanceled {
		return nil
	}
	if m.streamStartTime.IsZero() {
		return nil
	}
	elapsed := time.Since(m.streamStartTime)
	if elapsed < time.Duration(m.opts.SessionNotifySeconds)*time.Second {
		return nil
	}
	wh := m.opts.Webhook
	ctx := m.opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	title := fmt.Sprintf("seek: task finished in %s", elapsed.Round(time.Second))
	body := sessionNotifyBody(m.promptHistory)
	return func() tea.Msg {
		wh(ctx, "session.completed", title, body)
		return nil
	}
}

// sessionNotifyBody summarises which task finished, from the last submitted
// prompt (first line, trimmed, capped). Generic fallback when none recorded.
func sessionNotifyBody(history []string) string {
	if len(history) == 0 {
		return "Your seek task finished."
	}
	last := history[len(history)-1]
	if i := strings.IndexByte(last, '\n'); i >= 0 {
		last = last[:i]
	}
	last = strings.TrimSpace(last)
	// Truncate by RUNE, not byte — prompts are often Chinese, and a
	// byte-slice would split a multi-byte rune into mojibake.
	const max = 160
	if r := []rune(last); len(r) > max {
		last = string(r[:max-1]) + "…"
	}
	return "Task: " + last
}

func (m Model) handleStreamEnd(msg streamEndMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.streaming = false
	m.stream = nil
	m.input.Focus()
	// Stream ended — turn count / cache ratio changed; pick a new
	// placeholder so the user sees a fresh hint when the input
	// blinks back into focus.
	m.refreshPlaceholder()
	// Snapshot the current agent state into the session and
	// persist. Failures are logged via a system Println rather
	// than fatal — losing a save is annoying but shouldn't tear
	// down the TUI.
	m.persistSession()
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	// If the user pressed Esc, commit a visible interrupt notice
	// so it's clear something was aborted (any partial assistant
	// text already in m.curContent is still committable, but for
	// simplicity we drop it — the next prompt can re-ask).
	wasCanceled := m.userCanceled
	if m.userCanceled {
		cmds = append(cmds, m.appendHistory(styleMuted.Render("  ↰ interrupted")))
		m.userCanceled = false
	}
	// 柱 M interactive extension: ping the configured push webhook when a
	// long interactive turn finishes (you walked away → phone buzzes).
	// Best-effort, off the UI thread; skipped on Esc-cancel and short turns.
	if cmd := m.sessionNotifyCmd(wasCanceled); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// At this point all completed messages are already in
	// scrollback (committed during MessageEnd/ToolExecEnd). Clear
	// any residual live state.
	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
	// Restore the user's last submitted prompt into the textarea
	// after an Esc-cancelled stream, so they can edit and re-send.
	// Only when the textarea is still empty — the Esc handler may
	// have already restored queuedText into it.
	// Also skip when the user is in setup/review entry mode — restoring
	// a prior chat prompt into the API-key or branch-name field would
	// leak conversation text into config or git state.
	if wasCanceled && !m.setupKeyEntry && !m.reviewBranchEntry && m.input.Value() == "" && len(m.promptHistory) > 0 {
		m.input.SetValue(m.promptHistory[len(m.promptHistory)-1])
	}

	// Queue / steer dispatch — exactly one of these fires per
	// streamEndMsg, in priority order.
	//
	// pendingSteerText: set by Alt+Enter during the previous
	// stream; that path already called cancelStream(), so we
	// arrive here promptly. Repair() is implicit — the agent
	// loop's invariant check already dropped the half-baked
	// turn before we got here.
	//
	// queuedText: set by Enter during the previous stream. Only
	// auto-fires when the stream ended naturally (not via Esc),
	// otherwise the user's "Esc stops everything" expectation
	// would be violated.
	// Queue / steer dispatch — exactly one of these fires per
	// streamEndMsg, in priority order. We capture submit()'s
	// new Model so the streaming=true / streamStartTime reset
	// it performs isn't lost when this case returns.
	switch {
	case m.pendingSteerText != "":
		text := m.pendingSteerText
		m.pendingSteerText = ""
		m.queuedText = "" // a steer supersedes any queue
		cmds = append(cmds, m.appendHistory(styleMuted.Render("  ↪ steered")))
		newM, cmd := m.submit(text)
		cmds = append(cmds, cmd)
		return newM, tea.Batch(cmds...)
	case m.queuedText != "":
		text := m.queuedText
		m.queuedText = ""
		cmds = append(cmds, m.appendHistory(styleMuted.Render("  ↪ "+truncateOneLine(text, 60))))
		newM, cmd := m.submit(text)
		cmds = append(cmds, cmd)
		return newM, tea.Batch(cmds...)
	}

	// v4 柱 D suggested-reply: spawn side-channel prediction off the
	// bubbletea thread. Fires only at "at rest, ready for next user
	// input" (no queue / steer auto-fired above). PRD §3 triggers
	// gate further inside scheduleSuggestion.
	if cmd := m.scheduleSuggestion(wasCanceled); cmd != nil {
		cmds = append(cmds, cmd)
	}

	_ = msg
	return m, tea.Batch(cmds...)
}

// scheduleSuggestion returns a tea.Cmd that runs the suggester's
// side-channel prediction off the bubbletea thread and surfaces the
// result via suggestionReadyMsg. Returns nil when prediction
// shouldn't fire (PRD docs/prd/feature-suggested-reply.md §3):
//
//   - no suggester configured (—no-suggest)
//   - no agent / no history
//   - stream was user-canceled (Esc)
//   - input box already has text (user beat the prediction)
//   - last assistant turn was a tool dispatch (no text to predict from)
func (m Model) scheduleSuggestion(wasCanceled bool) tea.Cmd {
	if m.opts.Suggester == nil || m.opts.Agent == nil {
		return nil
	}
	if wasCanceled {
		return nil
	}
	if m.input.Value() != "" {
		return nil
	}
	history := m.opts.Agent.Messages()
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != deepseek.RoleAssistant || len(last.ToolCalls) > 0 {
		return nil
	}
	// Pre-flight heuristic gate: skip the prediction call entirely
	// when the assistant turn doesn't look like it invites a user
	// response (no question mark, no multi-choice markers, no
	// intent-eliciting phrases). Saves an API call AND saves the
	// user from a placeholder the model would have padded out
	// from nothing on an obviously-finished turn.
	// PRD docs/prd/feature-suggested-reply.md §3 + dogfood follow-up.
	if !suggester.ShouldPredict(last.Content) {
		return nil
	}
	turn := len(history)
	sug := m.opts.Suggester
	ctxRoot := m.opts.Ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctxRoot, suggester.PredictionTimeout)
		defer cancel()
		text := sug.Suggest(ctx, history)
		return suggestionReadyMsg{Text: text, Turn: turn}
	}
}

// submit kicks off an agent.Prompt for the given user text. Before
// streaming begins we commit the user's message to scrollback via
// tea.Println so it survives in native terminal history.
//
// We derive a cancelable context from opts.Ctx so Esc can cancel the
// in-flight call without tearing down the outer SIGINT context.
func (m Model) submit(text string) (tea.Model, tea.Cmd) {
	m.promptHistory = append(m.promptHistory, text)
	m.historyIdx = -1
	m.savedDraft = ""

	// v4 柱 D: clear the suggested-reply state on submit. The
	// PredictedNext field on the prior assistant message is already
	// persisted (via attachPredictedNext fired on suggestionReadyMsg
	// arrival); the TUI placeholder is per-turn UX and resets to
	// empty for the next turn.
	m.suggestedReply = ""
	m.suggestedReplyValid = false

	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
	m.userCanceled = false
	m.streaming = true
	m.streamStartTime = time.Now()
	m.streamDeltaBytes = 0
	// Leave the textarea focused — the user may want to type a
	// queue/steer message during the stream (see handleKey's
	// streaming branch on KeyEnter).

	ctx, cancel := context.WithCancel(m.opts.Ctx)
	m.cancelStream = cancel

	// v7 柱 Q: expand image refs → OCR'd text before the agent sees it.
	// No-op unless the input references an existing image file.
	if m.opts.ExpandInput != nil {
		text = m.opts.ExpandInput(text)
	}

	ch := m.opts.Agent.Prompt(ctx, text)
	m.stream = ch

	printUser := renderUserBlock(text, m.width)
	pcmd := (&m).appendHistory(printUser)
	return m, tea.Batch(pcmd, waitForAgentEvent(ch))
}

// applyAgentEvent updates Model state for an agent event and returns
// any tea.Cmds needed to commit content to scrollback. Pointer receiver
// so we can mutate m without returning a copy.
func (m *Model) applyAgentEvent(ev agent.Event) []tea.Cmd {
	var cmds []tea.Cmd

	switch e := ev.(type) {
	case agent.AgentStart, agent.TurnStart:
		// no UI change
	case agent.MessageStart:
		// A new message is starting — discard any residual live state
		// from a prior failed attempt (e.g. stream_error that triggered
		// an agent-level retry of the same turn).
		m.curContent = ""
		m.curReasoning = ""

	case agent.MessageDelta:
		if e.Reasoning {
			m.curReasoning += e.Delta
		} else {
			m.curContent += e.Delta
			m.streamDeltaBytes += len(e.Delta)
		}

	case agent.MessageEnd:
		if e.Message.Role == deepseek.RoleAssistant {
			// renderAssistantBlock also returns "" for empty content
			// (defense in depth), but we skip the append at the caller
			// so cmds doesn't grow with a nil entry — the
			// "pure tool-call turn → no scrollback commit" contract
			// asserted by TestApplyAgentEvent_PureToolCallTurnSkipsCommit.
			// The `↳ tool(...)` lines committed via ToolExecEnd already
			// convey what happened on a silent reasoning round.
			if m.curContent != "" {
				line := renderAssistantBlock(m.curContent, m.curReasoning, m.showReasoning, m.width, m.md)
				cmds = append(cmds, m.appendHistory(line))
			}
			// Always reset the live-region buffers — leaving them
			// populated would leak this turn's reasoning/content into
			// the next turn's commit and produce a confusing splice.
			m.curContent = ""
			m.curReasoning = ""
		}
		// MessageEnd for RoleTool: skip, ToolExecEnd already committed.

	case agent.ToolExecStart:
		m.activeTools = append(m.activeTools, activeTool{
			callID:  e.CallID,
			name:    e.Name,
			args:    truncateOneLine(e.Args, 80),
			started: time.Now(),
		})

	case agent.ToolDelta:
		// Streaming output from a long-running tool (currently only
		// `think`). Routed into the same live-region buffers the
		// chat model uses — sequential agent dispatch makes the
		// aliasing safe:
		//   1. chat model streams content → MessageEnd commits +
		//      clears the buffers
		//   2. then tool dispatch loop runs → ToolDelta refills the
		//      buffers with tool output
		//   3. ToolExecEnd clears the buffers again before the next
		//      chat turn streams in
		// NOTE: this aliasing breaks the moment parallel tool dispatch
		// lands (see PRD §3.1 — currently flagged as a post-v1.0 item:
		// "M1: sequential tool dispatch. Parallel via errgroup lands
		// with the parallel-execution work in a later milestone"). At
		// that point ToolDelta routing must move to a per-CallID live
		// region keyed off m.activeTools.
		if e.Reasoning {
			m.curReasoning += e.Delta
		} else {
			m.curContent += e.Delta
		}

	case agent.ToolExecEnd:
		// If this was a streaming tool (only `think` today), the live
		// buffers hold what we showed during execution; the tool's
		// full result is about to land in scrollback as a single
		// committed line, so the live preview is now stale and would
		// bleed into the next chat turn's display.
		//
		// Unconditional clear is intentional: a non-streaming tool
		// leaves both buffers empty (nothing ever pushed via
		// ToolDelta), so the assignment is a no-op rather than a
		// bug. Cheaper than adding a "did we stream?" flag.
		m.curContent = ""
		m.curReasoning = ""
		// ToolExecEnd carries Name/Result/Err but not Args/started —
		// look both back up from the active list. We do NOT remove the
		// slot here; it stays visible (rendered with ✓ + locked
		// duration) until handleStreamEnd clears m.activeTools at the
		// end of the turn. See activeTool's doc for why.
		var (
			args             string
			duration         time.Duration
			completionTokens int
		)
		now := time.Now()
		for i, t := range m.activeTools {
			if t.callID == e.CallID {
				args = t.args
				duration = now.Sub(t.started)
				m.activeTools[i].completed = now
				m.activeTools[i].finished = true
				if e.Result != "" {
					matches := completionRe.FindStringSubmatch(e.Result)
					if len(matches) >= 2 {
						fmt.Sscanf(matches[1], "%d", &completionTokens)
						if completionTokens > 0 {
							m.activeTools[i].completionTokens = completionTokens
						}
					}
				}
				break
			}
		}
		line := renderToolResultLine(e.Name, args, e.Result, e.Err, duration, completionTokens)
		cmds = append(cmds, m.appendHistory(line))

	case agent.TurnEnd:
		m.turns++
		// Lock in cost at the (model, tier) active when the turn settled
		// — see internal/cache doc. Using m.opts.Model rather than a
		// model captured at TurnStart is a deliberate small race: a
		// /model switch while the previous turn is still streaming
		// would mis-attribute that turn's cost. /model is gated to
		// non-streaming state in handleKey so the race is mostly
		// theoretical; the alternative (carry model on TurnEnd events)
		// would propagate plumbing into pkg/agent for negligible win.
		m.opts.Tracker.Record(e.Usage, m.opts.Model, pricing.CurrentTier(time.Now()))
		m.toolCalls += e.ToolCalls

	case agent.AgentEnd:
		// Stats footer at turn boundary — gives the user visible
		// "checkpoints" in history for long sessions.
		cmds = append(cmds, m.appendHistory(m.renderTurnFooter()))

	case agent.ErrorEvent:
		m.lastErr = e.Err
		cmds = append(cmds, m.appendHistory(styleErr.Render("  ! error: "+e.Err.Error())))

	case agent.PlanProposalApproved:
		// User approved the proposed plan via the propose tool's
		// picker. Flip into plan-execute substate: permission policy
		// becomes ModeAsk (writes prompt per call) and the mode
		// reminder switches to "plan-execute". Status bar shows
		// PLAN:EXEC in warning colour so the user can see the gate
		// is open. See PRD §2.5.
		_ = e // Steps field reserved for v2 panel rendering
		m.opts.PlanSubstate = "execute"
		if m.opts.SetPlanSubstate != nil {
			m.opts.SetPlanSubstate("execute")
		}
		cmds = append(cmds, m.appendHistory(styleMuted.Render("  ▸ plan approved — write/edit/bash now ask per call")))

	case agent.PlanProposalAdjustRequested:
		// User declined the proposed plan with optional free-text
		// feedback. Stay in plan-analyze (permission policy unchanged
		// at ModePlan). The propose tool's result string already
		// instructs the model to re-think; nothing else to wire here.
		m.opts.PlanSubstate = "analyze"
		if m.opts.SetPlanSubstate != nil {
			m.opts.SetPlanSubstate("analyze")
		}
		msg := "  ▸ plan rejected — re-thinking"
		if e.Feedback != "" {
			msg += " (feedback: " + truncateOneLine(e.Feedback, 60) + ")"
		}
		cmds = append(cmds, m.appendHistory(styleMuted.Render(msg)))

	case agent.PlanProposalCancelled:
		// User aborted /plan entirely from the propose picker. Same
		// effect as toggling /plan off manually: permission to
		// ModeAsk, mode label cleared, status bar drops the PLAN
		// badge.
		m.opts.Plan = false
		m.opts.PlanSubstate = ""
		m.opts.PlanSteps = nil
		m.opts.PlanCurrentIdx = -1
		if m.opts.SetPlan != nil {
			m.opts.SetPlan(false)
		}
		m.refreshPlaceholder()
		cmds = append(cmds, m.appendHistory(styleMuted.Render("  ▸ plan cancelled — exited /plan mode")))

	case agent.PlanStepUpdated:
		// Live task-list mutation from the `plan` tool. Mirror into
		// Options so View() can re-render the fixed task-list block
		// and the status bar can show the done/total counter. No
		// scrollback line — the rendered block IS the update; spamming
		// scrollback on every step change would drown the conversation.
		m.opts.PlanSteps = e.Steps
		m.opts.PlanCurrentIdx = e.CurrentIdx
	}

	return cmds
}

// handleCompactDone reacts to the summariser's response: on success,
// forks the session to preserve the full history, then resets the
// agent to a two-message summary so the conversation can continue
// with a fresh context window.
//
// Session chain after compact:
//
//	<old-id>  (full history, preserved on disk) ← ParentID
//	<new-id>  (summary pair, active session)    → continues here
//
// The chain is traversable via --resume and visible in --list, so no
// history is ever permanently lost.
func (m *Model) handleCompactDone(msg compactDoneMsg) []tea.Cmd {
	if msg.err != nil {
		return []tea.Cmd{m.appendHistory(styleErr.Render("  ! compact failed: " + msg.err.Error()))}
	}
	if m.opts.Agent == nil {
		return nil
	}
	// Fold the summariser's own usage into the cumulative tracker so
	// the cost line in the status bar stays honest — the compact call
	// is a real billed request.
	if m.opts.Tracker != nil {
		m.opts.Tracker.Record(msg.usage, m.opts.Model, pricing.CurrentTier(time.Now()))
	}

	var snapshotID string

	// When session persistence is active: flush the full history to disk
	// under the current session ID (the "snapshot"), then fork a child
	// session that will hold only the compact summary. This preserves every
	// message ever exchanged and makes the chain inspectable via --list.
	if m.opts.Session != nil && m.opts.Store != nil {
		// 1. Write full history to the current (snapshot) session.
		m.persistSession()
		snapshotID = m.opts.Session.ID

		// 2. Fork: new session, ParentID → snapshot. Counters reset to 0
		//    so the child's stats reflect only post-compact activity.
		child := m.opts.Session.Fork()
		m.opts.Session = child
		m.resetSessionCounters()
	}

	// The user→assistant bootstrap pair is what upstream pi / Claude
	// Code does: the next real user message slots in cleanly after an
	// assistant turn, no role-ordering weirdness for the API.
	m.opts.Agent.Reset([]deepseek.Message{
		{
			Role:    deepseek.RoleUser,
			Content: "Here is a summary of our earlier conversation. Continue from this context:\n\n" + msg.summary,
		},
		{
			Role:    deepseek.RoleAssistant,
			Content: "Understood — I have the context. Ready to continue.",
		},
	})

	// 3. Write summary messages to the child session.
	m.persistSession()

	var notice string
	if snapshotID != "" && m.opts.Session != nil {
		notice = fmt.Sprintf(
			"compacted: snapshot %s → continuing on %s (%d prompt + %d completion tokens)",
			snapshotID, m.opts.Session.ID,
			msg.usage.PromptTokens, msg.usage.CompletionTokens)
	} else {
		// --no-save: no fork, just report token cost.
		notice = fmt.Sprintf(
			"compacted: history replaced with summary (%d prompt + %d completion tokens)",
			msg.usage.PromptTokens, msg.usage.CompletionTokens)
	}
	return []tea.Cmd{m.appendHistory(styleMuted.Render(notice))}
}

// persistSession snapshots the agent's current message history,
// counters, and usage into Options.Session and saves via Options.Store.
// No-op when either is nil (ephemeral run / --no-save).
func (m *Model) persistSession() {
	if m.opts.Session == nil || m.opts.Store == nil || m.opts.Agent == nil {
		return
	}
	m.opts.Session.Messages = m.opts.Agent.Messages()
	m.opts.Session.Turns = m.turns
	m.opts.Session.ToolCalls = m.toolCalls
	if m.opts.Tracker != nil {
		m.opts.Session.Usage = m.opts.Tracker.Cumulative()
	}
	m.opts.Session.Model = m.opts.Model
	m.opts.Session.Yolo = m.opts.Yolo
	m.opts.Session.Effort = m.opts.Effort
	if err := m.opts.Store.Save(m.opts.Session); err != nil {
		// Surface to scrollback so the user knows persistence broke;
		// they can keep working but should investigate before losing
		// the session.
		// We can't tea.Println from a pointer-receiver helper without
		// returning a Cmd — so just stash on lastErr and let View
		// pick it up on next render.
		m.lastErr = err
	}
}

// truncateOneLine collapses newlines and clips the result to n chars.
// Uses rune-level slicing so multi-byte UTF-8 characters (Chinese, emoji,
// etc.) are never split mid-sequence — see docs/pitfalls.md "s[:n] on a
// multi-byte UTF-8 string produces broken runes".
func truncateOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func (m Model) renderTurnFooter() string {
	c := m.opts.Tracker.Last()
	// Cost is the locked-in amount from when this turn was recorded —
	// not a re-derivation against the current model/tier. Avoids the
	// "switched model mid-turn → footer shows wrong dollars" race.
	cost := pricing.FormatCost(m.opts.Tracker.LastCost())

	var cacheNote string
	if c.PromptTokens > 0 && c.PromptCacheHitTokens > 0 {
		pct := int(float64(c.PromptCacheHitTokens) / float64(c.PromptTokens) * 100)
		cacheNote = fmt.Sprintf(" (%d%% cache)", pct)
	}

	return styleMuted.Render(fmt.Sprintf(
		"  · turn %d · %d tools · ↑%s prompt%s · ↓%s tok · %s",
		m.turns, m.toolCalls,
		formatTokensK(c.PromptTokens), cacheNote,
		formatTokensK(c.CompletionTokens), cost))
}
