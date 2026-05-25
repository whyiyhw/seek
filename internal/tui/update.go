package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.relayout()

	case tea.KeyMsg:
		// Inline mode has no in-process scrollable region — the
		// terminal does that natively. So we don't intercept PgUp/PgDn
		// for an internal viewport; they go to the terminal's
		// scrollback like in any normal shell.
		return m.handleKey(msg)

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

	case statusTickMsg:
		m.now = time.Now()
		// A minute passed — off-peak window may have just opened or
		// closed. Repick the placeholder so e.g. "🌙 off-peak" shows
		// up the moment the discount kicks in.
		m.refreshPlaceholder()
		cmds = append(cmds, tickStatusEvery(time.Minute))

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
		req := msg.req
		m.pendingQuestion = &req
		m.pendingQuestionSelected = map[int]bool{}
		m.pendingQuestionCursor = 0
		m.pendingQuestionFreeText = false
		// Single-select picker uses keyboard nav, not textarea; blur.
		// We re-focus when the user picks "Other" so they can type.
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

	case cleanupToolMsg:
		for i, t := range m.activeTools {
			if t.callID == msg.callID {
				m.activeTools = append(m.activeTools[:i], m.activeTools[i+1:]...)
				break
			}
		}
	}

	return m, tea.Batch(cmds...)
}
