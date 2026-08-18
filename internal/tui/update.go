package tui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/whyiyhw/seek/internal/askuser"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.relayout()

	case tea.KeyPressMsg:
		// Debug aid: record the raw event before routing decides
		// anything (SEEK_KEYLOG=<file> to enable; no-op otherwise).
		logKeyMsg(msg)
		// IME/terminal bridges can deliver Enter/Backspace as character
		// events; rewrite them to their key equivalents before routing.
		msg = normalizeControlRunes(msg)
		// Inline mode: PgUp/PgDn/Home/End and the mouse wheel all go
		// to the terminal's native scrollback — we don't capture mouse
		// events, and the viewport widget is gone. handleKey only
		// owns key bindings the textarea / overlays care about.
		return m.handleKey(msg)

	case tea.PasteMsg:
		// Bracketed-paste content is real payload — inject wholesale so
		// embedded \r bytes never arrive as separate Enter keys between
		// lines. v2 delivers paste as a dedicated message type instead
		// of a key with a Paste flag.
		m = m.insertPasteText(msg.Content)
		m.updateCommandMenu()
		m.updatePathCompleter()
		return m, nil

	case agentEventMsg:
		// applyAgentEvent may emit Println commands for committed
		// content. We collect them and let Bubble Tea write them above
		// the live region.
		printCmds := m.applyAgentEvent(msg.Event)
		cmds = append(cmds, printCmds...)
		cmds = append(cmds, waitForAgentEvent(m.stream))

	case streamEndMsg:
		// Finalise the stream and dispatch any queued/steered follow-up.
		// All cmds for this case are returned from handleStreamEnd.
		return m.handleStreamEnd(msg)

	case goalStartMsg:
		// /goal set the loop state; fire the first turn here so submit()'s
		// streaming Model is the one we return (M-goal.2).
		m.goalToolsBase = m.toolCalls
		return m.submit(m.goalCond)

	case goalVerdictMsg:
		// The judge assessed the just-finished goal turn — continue or stop.
		return m.handleGoalVerdict(msg)

	case suggestionReadyMsg:
		// v4 柱 D side-channel prediction landed. Drop stale results
		// (user submitted another turn while the goroutine was in
		// flight) and "no prediction" sentinels. Otherwise stash for
		// rendering + persist to agent so calibration sees it next
		// turn.
		if m.opts.Agent == nil {
			return m, nil
		}
		if len(m.opts.Agent.Messages()) != msg.Turn {
			return m, nil
		}
		if msg.Text == "" {
			return m, nil
		}
		// User started typing while we were waiting — don't shove
		// a placeholder under their cursor.
		if m.input.Value() != "" {
			return m, nil
		}
		m.suggestedReply = msg.Text
		m.suggestedReplyValid = true
		m.suggestedReplyTurn = msg.Turn
		if pa, ok := m.opts.Agent.(predictionAttacher); ok {
			pa.AttachPredictedNext(msg.Text)
			(&m).persistSession()
		}
		return m, nil

	case statusTickMsg:
		m.now = time.Now()
		// A minute passed — off-peak window may have just opened or
		// closed. Repick the placeholder so e.g. "🌙 off-peak" shows
		// up the moment the discount kicks in.
		m.refreshPlaceholder()
		cmds = append(cmds, tickStatusEvery(time.Minute))

	case bannerTickMsg:
		// Advance the wordmark reveal animation by one frame.
		if m.bannerFrame < len(letterEndCols) {
			m.bannerFrame++
			// Schedule next tick. 150ms gives a crisp letter-by-letter
			// reveal without feeling sluggish.
			if m.bannerFrame < len(letterEndCols) {
				cmds = append(cmds, tickBannerEvery(150*time.Millisecond))
			}
		}

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)

	case approvalRequestMsg:
		// New approval prompt — grab focus.
		req := msg.req
		m.pendingApproval = &req
		m.input.Blur()

	case askUserRequestMsg:
		// New ask_user request — grab focus and reset picker state.
		// (Re-arming happens at completion in completeQuestion, not
		// here, to match the approval-channel pattern.)
		req := msg.req
		m.pendingQuestion = &req
		m.pendingQuestionSelected = map[int]bool{}
		m.pendingQuestionCursor = 0
		m.pendingQuestionFreeText = false
		// Single-select picker uses keyboard nav, not textarea; blur.
		// We re-focus when the user picks "Other" so they can type.
		m.input.Blur()

	case askUserBatchRequestMsg:
		// New v2 batch request — initialise stack state at Q1.
		// Per-question picker state (cursor / selected / freeText)
		// is shared with the single-question path; we reset it
		// here for Q1 and again each time we advance to Q_(i+1).
		// Re-arming happens at completion via completeBatch, same
		// pattern as askUserRequestMsg.
		req := msg.req
		m.pendingBatch = &req
		m.pendingBatchIdx = 0
		m.pendingBatchAnswers = make([]askuser.Answer, 0, len(req.Batch.Questions))
		m.pendingQuestionSelected = map[int]bool{}
		m.pendingQuestionCursor = 0
		m.pendingQuestionFreeText = false
		m.input.Blur()

	case compactDoneMsg:
		cmds = append(cmds, m.handleCompactDone(msg)...)

	case distillDoneMsg:
		cmds = append(cmds, m.handleDistillDone(msg)...)

	case observeDoneMsg:
		cmds = append(cmds, m.handleObserveDone(msg)...)
		if m.opts.ObserveResultChan != nil {
			cmds = append(cmds, waitForObserveResult(m.opts.ObserveResultChan))
		}

	case versionCheckDoneMsg:
		// Store the newer tag for the status-bar segment. Idempotent —
		// the cmd never re-fires within a session, but a second run
		// after /upgrade would simply overwrite this field.
		m.upgradeAvailable = msg.NewerTag

	case upgradeDoneMsg:
		cmds = append(cmds, m.handleUpgradeDone(msg)...)
	}

	return m, tea.Batch(cmds...)
}
