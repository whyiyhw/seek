// Package guidance implements the deterministic turn-boundary hints that
// pkg/agent's Guidance seam injects (pkg/agent/guidance.go).
//
// # Why deterministic, not a model
//
// Every rule here is a predicate over turn shapes — a counter, a streak,
// an equality key. Nothing calls an LLM. Two reasons, both measured:
//
//   - Cost discipline: a per-turn judge call would double the request
//     count of every session to buy advice that a counter already knows.
//     internal/goal's judge is the precedent for WHEN a model call is
//     worth it (a whole autonomous loop's control flow); a nudge is not.
//   - Testability: "does this hint fire and does it stop firing" has to
//     be a Go assertion, not a sampling study (eval/README.md's rule: if
//     a check can be asserted without the model, it does not belong in
//     eval).
//
// # Why these rules and not more
//
// The bash success-path advisory fired 3,871 times across 40 sessions
// and changed behaviour approximately never (docs/pitfalls/eval/
// success-path-hints-do-not-teach-measured-3871-firings-zero-effect).
// The lesson was not "hints don't work" but "a hint needs an audience":
// a nudge after a SUCCESSFUL action reviews something already done. So
// every rule below fires only on a FAILURE shape — the model is stuck,
// visibly, and the transcript alone cannot tell it so:
//
//	retry-loop    the same call (name + canonical args) failed and was
//	              re-issued — the model has no memory of "I tried this"
//	              because the previous failure is thousands of tokens back
//	failure-run   several consecutive tool calls failed, possibly
//	              different ones — no single result shows the pattern
//	silence-run   several consecutive turns ran tools and said nothing
//	              — the operator cannot see progress, and the model
//	              cannot see that it has been silent
//
// # Rate limiting is part of the contract, not a refinement
//
// Each rule fires at most maxFiringsPerRule per session and has a
// cooldown in turns. The advisory's own fix (internal/tools/bash/
// advisory.go) is the precedent: a nudge nobody acts on, repeated, is the
// most expensive text in the transcript — it lands in history and is
// re-sent on every later request. A rule that cannot point at a
// behavioural change stays quiet after its budget.
package guidance

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/agent"
)

// Rule names. Wire format once a trace report counts firings by rule —
// additions go after, never renamed.
const (
	RuleRetryLoop  = "retry-loop"
	RuleFailureRun = "failure-run"
	RuleSilenceRun = "silence-run"
)

// Thresholds. Each is the point where the pattern stops being plausible
// healthy work, chosen by the same reasoning RepeatGuard's [3,5,8]
// escalation uses: 2 failures is a retry, 3 means the approach is wrong.
const (
	// retryLoopAt: the same call has now failed this many times.
	retryLoopAt = 3
	// failureRunAt: this many consecutive failed calls (any tool).
	failureRunAt = 3
	// silenceRunAt: this many consecutive turns with tools and no prose.
	silenceRunAt = 4
	// cooldownTurns between firings of the SAME rule.
	cooldownTurns = 4
	// maxFiringsPerRule caps each rule's lifetime output. Two is enough
	// to say it once and once more after the model had a chance to act;
	// a third is nagging.
	maxFiringsPerRule = 2
)

// Guide is the session-scoped implementation of agent.Guidance. Create
// one per session (its counters are per-conversation).
//
// Not safe for concurrent use — the agent consults it on its own
// goroutine (see the Guidance interface doc).
type Guide struct {
	// callFails counts failures per (name, canonical args) equality key
	// — the same key RepeatGuard and internal/friction use, so "the same
	// call" means one thing across the codebase.
	callFails map[string]int
	// failRun is the current run of consecutive failed calls.
	failRun int
	// silentRun is the current run of consecutive tool-only turns.
	silentRun int
	// lastTurn is the last turn index examined (guard against the same
	// turn being advised twice — the loop can consult at two boundaries
	// in one iteration when a settle-point drain fires).
	lastTurn int
	seenTurn bool
	// fired[rule] = turns on which it fired, for the cooldown and cap.
	fired map[string][]int
	// LastRule/LastText describe the most recent advice, for the trace
	// row and for tests that want to know what fired without parsing.
	LastRule string
	LastText string
}

// New returns an empty Guide.
func New() *Guide {
	return &Guide{
		callFails: map[string]int{},
		fired:     map[string][]int{},
	}
}

// LastAdviceRule implements agent.GuidanceNamer: the rule name of the
// most recent non-empty advice, "" otherwise. Read by pkg/agent right
// after Advise and carried on the event for per-rule counting.
func (g *Guide) LastAdviceRule() string { return g.LastRule }

// Advise implements agent.Guidance.
func (g *Guide) Advise(b agent.TurnBrief) string {
	// One consultation per turn: the loop can hit a boundary twice in
	// one iteration (settle-point drain → next TurnStart), and counting
	// the same turn twice would halve effective thresholds.
	if g.seenTurn && b.Index == g.lastTurn {
		return ""
	}
	g.seenTurn = true
	g.lastTurn = b.Index

	// A zero-value Guide (embedded, not built via New) must not panic on
	// its first map write.
	if g.callFails == nil {
		g.callFails = map[string]int{}
	}
	if g.fired == nil {
		g.fired = map[string][]int{}
	}

	g.observe(b)

	if text, rule := g.pick(b); text != "" {
		g.fired[rule] = append(g.fired[rule], b.Index)
		g.LastRule, g.LastText = rule, text
		return text
	}
	g.LastRule, g.LastText = "", ""
	return ""
}

// observe folds this turn into the running counters. Kept separate from
// pick so a test can assert the counters without triggering a rule.
func (g *Guide) observe(b agent.TurnBrief) {
	for _, t := range b.Tools {
		if isFailure(t) {
			g.failRun++
			g.callFails[t.Name+"\x00"+canonical(t.Args)]++
			continue
		}
		// A success means the approach worked: reset both the run and
		// that call's failure memory (a legitimate retry after an edit
		// must not accumulate toward the retry-loop rule forever).
		g.failRun = 0
		delete(g.callFails, t.Name+"\x00"+canonical(t.Args))
	}
	if len(b.Tools) > 0 && b.TextChars == 0 {
		g.silentRun++
	} else if b.TextChars > 0 {
		g.silentRun = 0
	}
}

// pick returns the first rule willing to speak this turn, in a fixed
// order so the choice is deterministic and testable.
func (g *Guide) pick(b agent.TurnBrief) (string, string) {
	// retry-loop: the most specific failure — the model is re-issuing an
	// identical call that has failed repeatedly.
	if g.failRun > 0 {
		for _, t := range b.Tools {
			if !isFailure(t) {
				continue
			}
			n := g.callFails[t.Name+"\x00"+canonical(t.Args)]
			if n >= retryLoopAt && g.allow(RuleRetryLoop, b.Index) {
				return fmt.Sprintf(
					"You have issued this exact call %d times and it failed each time: `%s` with arguments %s.\n"+
						"Re-running it unchanged cannot produce a different result. Read the error, change the arguments, "+
						"try a different approach, or tell the user what is blocking you.",
					n, t.Name, preview(t.Args)), RuleRetryLoop
			}
		}
	}

	// failure-run: different calls, all failing — no single result shows
	// the pattern.
	if g.failRun >= failureRunAt && g.allow(RuleFailureRun, b.Index) {
		n := g.failRun
		// Reset on fire: the run that triggered it has been reported, so
		// the next count starts fresh. Without this the same run would
		// re-qualify at every subsequent boundary until a success.
		g.failRun = 0
		return fmt.Sprintf(
			"The last %d tool calls all failed. That is usually a sign the current approach is wrong, "+
				"not that the next attempt will differ. Summarise what you have learned and change strategy — "+
				"or ask the user with ask_user if the blocker is outside the repository.", n), RuleFailureRun
	}

	// silence-run: tools happening, nothing said.
	if g.silentRun >= silenceRunAt && g.allow(RuleSilenceRun, b.Index) {
		n := g.silentRun
		g.silentRun = 0
		return fmt.Sprintf(
			"You have run tools for %d turns in a row without telling the user anything. "+
				"Write one short sentence now about what you are doing and what you have found so far, "+
				"then continue.", n), RuleSilenceRun
	}

	return "", ""
}

// allow enforces the per-rule cooldown and lifetime cap.
func (g *Guide) allow(rule string, turn int) bool {
	hist := g.fired[rule]
	if len(hist) >= maxFiringsPerRule {
		return false
	}
	if len(hist) > 0 && turn-hist[len(hist)-1] < cooldownTurns {
		return false
	}
	return true
}

// isFailure mirrors the two ways a failed call can reach here: a non-nil
// execution error, or a result the tool itself marked as an error (the
// agent's tool-result builder prefixes those with agent.ToolErrorPrefix —
// permission denials included).
func isFailure(t agent.ToolBrief) bool {
	if t.Err != nil {
		return true
	}
	return strings.HasPrefix(t.Result, agent.ToolErrorPrefix)
}

// canonical is tools.CanonicalArgs on a string. One equality key for
// "the same call" across RepeatGuard, friction and this package.
func canonical(args string) string {
	return tools.CanonicalArgs([]byte(args))
}

// preview bounds how much of the argument blob enters the hint. Same
// reasoning as RepeatGuard's maxArgPreview: echoing a large payload back
// to say "you are repeating yourself" would be the bigger mistake.
func preview(args string) string {
	const max = 160
	c := canonical(args)
	if len(c) > max {
		return c[:max] + "…"
	}
	return c
}
