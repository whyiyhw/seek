// acp_usage.go — the ACP lane's context-window frames (card c_uzlczr,
// P0-4). Design and wire-shape rationale live in internal/acp/usage.go;
// this file is the production glue: one completed turn's token accounting
// in, one session/update out (plus an alert frame on the crossing turn).
//
// Same-source discipline (the card's ②): the occupancy proxy is the LAST
// turn's prompt+completion — exactly what internal/tui's formatBudget and
// the Web ContextMeter (W5 c_oyp2qf) read. A cumulative sum inflates
// quadratically, because every turn re-sends the whole history. The window
// is budget.Limit(model), the same authority those two surfaces use, and
// the thresholds are budget.Classify's (0.60 warn / 0.75 critical). No
// table is declared here.
package main

import (
	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// acpCtxMeter turns successive turns' usage into the ACP context-window
// frames for one session.
//
// Two frames are possible, and the split is deliberate:
//
//   - the PLAIN usage_update — every completed turn. used/size at the top
//     level per the ACP RFD (clients derive remaining + percentage), with
//     seek's own percent/severity under _meta.seek.ctx.
//   - the ALERT usage_update — only on the turn where severity first
//     reaches warn/critical. Same used/size, plus `alert` and a
//     human-readable `notice` under the same _meta object. Emitting it as
//     an EXTRA frame on the crossing (rather than every turn above the
//     line, or as a marker on the plain frame) is what keeps "a warning
//     fired" distinguishable from "the meter moved" without a duplicate
//     row per turn: clients render one meter per arriving usage_update.
//
// last is the level-trigger memory for that crossing; it lives for the
// life of the process (one seek process = one ACP session) and is reset
// when a different conversation is loaded into it.
type acpCtxMeter struct {
	alerts bool   // SEEK_ACP_CTX_ALERT resolved once at lane start
	last   string // severity of the previous turn ("" before the first)
}

// update returns this turn's frames. A zero window is the only silent
// case (the ACP RFD defines no null size, and says an agent that cannot
// give a meaningful window should simply not send the update); an
// unknown model is NOT silent — budget.Limit falls back to
// budget.Default, so a client still gets a usable meter.
func (m *acpCtxMeter) update(sessionID, model string, u deepseek.Usage) []acp.SessionUpdate {
	size := budget.Limit(model)
	if size <= 0 {
		return nil
	}
	used := u.PromptTokens + u.CompletionTokens
	percent := int(budget.Fraction(model, used) * 100)
	sev := acp.CtxSeverityOK
	switch budget.Classify(model, used) {
	case budget.SeverityCritical:
		sev = acp.CtxSeverityCritical
	case budget.SeverityWarn:
		sev = acp.CtxSeverityWarn
	}
	out := []acp.SessionUpdate{acp.UsageUpdate(sessionID, used, size, percent, sev, false)}
	if m.alerts && sev != acp.CtxSeverityOK && sev != m.last {
		out = append(out, acp.UsageUpdate(sessionID, used, size, percent, sev, true))
	}
	m.last = sev
	return out
}

// reset forgets the crossing memory — called when session/load swaps a
// different conversation into the process, so the resumed session's first
// over-threshold turn fires its own alert instead of being compared
// against a stranger's severity.
func (m *acpCtxMeter) reset() { m.last = "" }
