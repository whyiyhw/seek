package tui

// User-interaction tracing: the TUI's half of internal/uitrace.
//
// Why the TUI is the only place that can write these rows: they describe
// what the USER did, and the user's actions are not all visible in the
// transcript. A message injected at a turn boundary and a message that
// cancelled the round and resubmitted both land on disk as identical
// `user` rows; a withdrawn queue leaves nothing at all. See the package
// doc in internal/uitrace for the full argument.
//
// Every call site is on a keystroke path, so traceEvent is best-effort
// by construction: a broken trace must never break typing.

import "github.com/whyiyhw/seek/internal/uitrace"

// traceEvent appends one row. nil-safe (m.opts.UITrace == nil is the
// normal case for tests, --no-save, and embedders), and it swallows the
// write error on purpose — see the package comment.
//
// turn is read from m.turns (the same counter the status bar and the
// session header use), so a report can say "interventions cluster after
// turn 20" without a second source of truth.
func (m *Model) traceEvent(event, detail string, chars int) {
	if m.opts.UITrace == nil {
		return
	}
	sid := ""
	if m.opts.Session != nil {
		sid = m.opts.Session.ID
	}
	_ = m.opts.UITrace.Append(uitrace.Event{
		SID:    sid,
		Event:  event,
		Turn:   m.turns,
		Chars:  chars,
		Detail: detail,
	})
}

// traceGuidance records a machine-authored hint. Separate entry point so
// the rule name is impossible to forget — a guidance row without its rule
// is useless for the only question it answers ("which rule earns its
// tokens?").
func (m *Model) traceGuidance(rule string, chars int) {
	m.traceEvent(uitrace.EventGuidance, rule, chars)
}

// traceEventDur is traceEvent plus a duration and a token magnitude, for
// the operations where elapsed time and context size are the interesting
// numbers (compact, goal).
func (m *Model) traceEventDur(event, detail string, chars int, tokens int, durMS int64) {
	if m.opts.UITrace == nil {
		return
	}
	sid := ""
	if m.opts.Session != nil {
		sid = m.opts.Session.ID
	}
	_ = m.opts.UITrace.Append(uitrace.Event{
		SID:    sid,
		Event:  event,
		Turn:   m.turns,
		Chars:  chars,
		Tokens: tokens,
		DurMS:  durMS,
		Detail: detail,
	})
}
