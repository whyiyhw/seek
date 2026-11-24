package agent

import "github.com/whyiyhw/seek/pkg/deepseek"

// Deterministic turn-boundary guidance: the agent's half of
// internal/guidance.
//
// The seam exists because SOME loop shapes are invisible from inside any
// single tool result. RepeatGuard (internal/tools) can see one call
// repeated; it cannot see "four consecutive turns ran tools and the
// model said nothing", or "the last three batches failed". Those are
// aggregate facts over a turn, so the only place they can be computed is
// where turns end: right here, at the same two boundary points the
// user-injection queue drains at (inject.go).
//
// Design constraints this deliberately inherits:
//
//   - Turn 0 is exempt, exactly like QueueUserMessage: the user's own
//     prompt must not be preempted by machine-authored text.
//   - Guidance gets the IN-LOOP boundary only, never the settle point
//     (see the settle site in agent.go). A hint must never extend a
//     round: the model already decided it was done, and a rule that
//     answers "not yet" every time produces an unbounded loop — measured
//     the hard way, 397 consecutive injections in the first run.
//   - Guidance is a USER-role message appended at the tail, so the
//     prefix cache keeps hitting (same argument as every other
//     injection; see docs/prd/feature-hooks-injection.md §4.1).
//   - The implementation decides WHEN something is worth saying. This
//     file only guarantees the facts are accurate and the insertion
//     point is legal.
//
// Nothing here decides policy. The measured lesson from the bash
// success-path advisory (3,871 firings, zero effect — docs/pitfalls/eval/
// success-path-hints-do-not-teach) is that a nudge's value depends on
// whether its audience exists; that judgement belongs in a package that
// can be tested and retuned on its own, which is internal/guidance.

// ToolBrief is one settled tool call, as guidance sees it.
type ToolBrief struct {
	// Name is the tool's registered name.
	Name string
	// Args is the raw JSON the model produced (not canonicalised here;
	// a rule that needs canonical form calls tools.CanonicalArgs).
	Args string
	// Result is the final result text, reminder suffixes included.
	Result string
	// Err is non-nil when the call failed (execution error, permission
	// denial, or a tool-level failure). pkg/agent's tool-result builder
	// folds most of these into Result with a prefix, so a rule that
	// cares about failure should check both fields.
	Err error
}

// TurnBrief is what one finished turn looked like, handed to Guidance.
//
// TextChars is the assistant's own prose length for that turn — 0 means
// the turn was pure tool churn, which is the signal the no-narration
// rule keys on. It counts content only, not reasoning: a model that
// thinks at length and says nothing to the user is still opaque.
type TurnBrief struct {
	// Index is the turn that just finished (0-based, matching
	// TurnStart/TurnEnd).
	Index int
	// Tools are the calls that settled in it, in dispatch order. Empty
	// when the turn produced no tool calls.
	Tools []ToolBrief
	// TextChars is the length of the assistant's content for the turn.
	TextChars int
}

// Guidance is consulted once per turn boundary. Returning "" means "say
// nothing" — the common case, and the only acceptable default: a nudge
// that fires every turn is just noise with a token bill.
//
// Implementations may keep state across calls (streaks, cooldowns). They
// run on the agent's own goroutine, so no locking is required, but they
// must not block: this is inside the loop's critical path.
type Guidance interface {
	Advise(b TurnBrief) string
}

// GuidanceNamer is an OPTIONAL sibling of Guidance (AGENTS.md "Sink
// interfaces: don't break the main contract — add OPTIONAL sibling
// interfaces"). A Guidance implementation that wants its hints labelled
// in the UI and the trace implements LastAdviceRule; one that does not
// keeps compiling unchanged.
//
// It answers "which rule just spoke?", read immediately after a
// non-empty Advise call. The agent never interprets the value — it is
// pass-through for surfaces that count firings per rule, which is the
// only way to answer "is this hint earning its tokens?".
type GuidanceNamer interface {
	LastAdviceRule() string
}

// guidanceText appends the mode reminder like every other user turn, so
// a guidance message is indistinguishable in form from a real one — the
// model should treat it as the operator speaking, not as a system
// annotation it can discount.
func guidanceText(text, modeLabel string) string {
	return text + modeReminder(modeLabel)
}

// commitGuidance runs the configured Guidance for this turn and appends
// its advice, if any. Reports whether something landed.
//
// Called from ONE boundary point — the top of a continuing loop
// iteration — and only when no user injection landed first: a human
// break-in supersedes a machine hint, always (the user's text is the more
// specific instruction, and saying both in one breath dilutes it).
//
// The settle point is deliberately excluded. There the model has just
// decided the round is over; a hint arriving there turns "about to
// return" into another turn, whose new brief qualifies again, forever.
// Only a human injection earns an extra turn.
func (a *Agent) commitGuidance(turn int, brief TurnBrief, out chan<- Event) bool {
	if a.cfg.Guidance == nil {
		return false
	}
	text := a.cfg.Guidance.Advise(brief)
	if text == "" {
		return false
	}
	rule := ""
	if n, ok := a.cfg.Guidance.(GuidanceNamer); ok {
		rule = n.LastAdviceRule()
	}
	msg := deepseek.Message{Role: deepseek.RoleUser, Content: guidanceText(text, a.cfg.ModeLabel)}
	a.appendMessage(msg)
	out <- InjectedMessage{Index: turn, Message: msg, Source: InjectedSourceGuidance, Rule: rule}
	return true
}
