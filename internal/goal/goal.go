// Package goal is the deterministic driver for the `/goal` feature: run a
// single agent across multiple turns toward a completion CONDITION, with a
// cheap model judging after each turn whether the condition is satisfied,
// auto-continuing until it is — or until a cap/stall/cancel stops it.
//
// Like the autopilot package (柱 N), the control flow lives here in Go,
// not in a free-form skill, because `/goal` runs unattended (especially
// the headless/cron path) — so the loop, caps, stall guard, and
// stop-reason accounting must be deterministic, bounded, and testable.
//
// This is the SINGLE-agent dual of autopilot's MULTI-agent fan-out: one
// worker grinding the SAME conversation, vs many workers in isolated
// worktrees. The two things this delegates — running one turn (Worker)
// and judging the condition (Judge) — are narrow interfaces so the driver
// is unit-testable with fakes (no real LLM / agent). The concrete adapters
// (agent.Agent turn + deepseek judge) wire at M-goal.2/.3.
//
// PRD: docs/prd/feature-goal.md.
package goal

import (
	"context"
	"time"
)

const (
	defaultMaxTurns   = 25
	defaultStallLimit = 3
)

// StopReason explains why a run ended. Wire-format-ish (surfaced in
// reports / status); append new values, don't renumber.
type StopReason string

const (
	StopMet      StopReason = "met"          // judge said the condition holds
	StopMaxTurns StopReason = "max_turns"    // hit the turn cap
	StopTimeout  StopReason = "timeout"      // wall-clock cap (Caps.Timeout)
	StopStalled  StopReason = "stalled"      // too many no-progress turns
	StopBudget   StopReason = "token_budget" // spent the token budget
	StopCanceled StopReason = "canceled"     // ctx canceled (Esc / SIGINT)
	StopError    StopReason = "error"        // the worker turn itself failed
)

// TurnResult is what one Worker turn produced — enough for the Judge to
// assess and for the driver's progress/budget accounting.
type TurnResult struct {
	Text      string // the assistant's final text this turn (fed to the Judge)
	ToolCalls int    // tools executed this turn; 0 = no progress (stall signal)
	Tokens    int    // tokens spent this turn (budget accounting); 0 if unknown
}

// Verdict is the Judge's call on whether the condition is satisfied.
type Verdict struct {
	Met    bool
	Reason string // why met / why not
	Hint   string // guidance for the next turn when not met (may be empty)
}

// Worker runs ONE agent turn given a directive (the condition on turn 1, a
// continuation thereafter) and reports what it produced. The real impl
// drains agent.Agent.Prompt's event stream; tests inject a fake. MUST
// honour ctx cancellation (return promptly on ctx.Done).
type Worker interface {
	RunTurn(ctx context.Context, directive string) (TurnResult, error)
}

// Judge decides whether the condition is satisfied given the latest turn.
// The real impl is one cheap deepseek.Client.Chat call; tests inject a
// fake. A Judge that ERRORS is treated by the driver as not-met (the loop
// continues) — a flaky judge call must never abort an unattended run.
type Judge interface {
	Judge(ctx context.Context, condition string, last TurnResult) (Verdict, error)
}

// Caps bound a run so it can't loop forever / burn the budget. Zero values
// fall back to defaults (MaxTurns/StallLimit) or "no limit" (Timeout,
// TokenBudget).
type Caps struct {
	MaxTurns    int           // hard cap on turns (default 25)
	StallLimit  int           // consecutive no-tool-call turns → stop (default 3)
	Timeout     time.Duration // total wall-clock; 0 = rely on caller ctx
	TokenBudget int           // stop once cumulative tokens reach this; 0 = no limit
}

// WithDefaults fills zero fields with safe defaults. Exported so the TUI's
// event-driven loop (M-goal.2) uses the same caps as the headless Driver.
func (c Caps) WithDefaults() Caps {
	if c.MaxTurns <= 0 {
		c.MaxTurns = defaultMaxTurns
	}
	if c.StallLimit <= 0 {
		c.StallLimit = defaultStallLimit
	}
	return c
}

// TurnLog is one row of the per-turn trace.
type TurnLog struct {
	Turn      int
	ToolCalls int
	Tokens    int
	Met       bool
	Reason    string
}

// Report is the outcome of a Run.
type Report struct {
	Condition string
	Met       bool
	Stop      StopReason
	Turns     int
	Tokens    int
	Reason    string    // the deciding judge reason / error detail
	Trace     []TurnLog // per-turn record
}

// Driver runs the goal loop.
type Driver struct {
	worker Worker
	judge  Judge
	caps   Caps
	// OnTurn, if set, is called after each turn's verdict with that turn's
	// log row — for live progress (the headless `seek goal run` path prints
	// it). The TUI uses its own event-driven loop and leaves this nil.
	OnTurn func(TurnLog)
}

// New builds a Driver. caps zero-values fall back to safe defaults.
func New(w Worker, j Judge, caps Caps) *Driver {
	return &Driver{worker: w, judge: j, caps: caps.WithDefaults()}
}

// Run executes the loop: each turn the Worker works toward the condition,
// then the Judge assesses; met → stop; not-met → continue with the
// judge's reason/hint as the next directive. Bounded by MaxTurns, Timeout,
// StallLimit, and TokenBudget. Cancellation (ctx) stops cleanly.
//
// Run does NOT return a Go error for normal stop reasons (met / caps /
// stall / cancel) — those are reported via Report.Stop, mirroring
// autopilot. It returns a non-nil error only if it can't run at all
// (currently never; reserved).
func (d *Driver) Run(ctx context.Context, condition string) (Report, error) {
	if d.caps.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.caps.Timeout)
		defer cancel()
	}

	rep := Report{Condition: condition}
	directive := condition // turn 1: the condition IS the directive
	stalls := 0

	for turn := 1; turn <= d.caps.MaxTurns; turn++ {
		if stop, done := stopForCtx(ctx); done {
			rep.Stop = stop
			return rep, nil
		}

		tr, err := d.worker.RunTurn(ctx, directive)
		if err != nil {
			// Distinguish a cancellation surfaced as a worker error from a
			// genuine worker failure.
			if stop, done := stopForCtx(ctx); done {
				rep.Stop = stop
				return rep, nil
			}
			rep.Turns = turn
			rep.Stop = StopError
			rep.Reason = err.Error()
			return rep, nil
		}
		rep.Turns = turn
		rep.Tokens += tr.Tokens

		// Judge errors are non-fatal: treat as not-met and keep going so a
		// flaky judge call can't abort an unattended run.
		v, jerr := d.judge.Judge(ctx, condition, tr)
		if jerr != nil {
			v = Verdict{Met: false, Reason: "judge error: " + jerr.Error()}
		}
		tl := TurnLog{
			Turn: turn, ToolCalls: tr.ToolCalls, Tokens: tr.Tokens,
			Met: v.Met, Reason: v.Reason,
		}
		rep.Trace = append(rep.Trace, tl)
		if d.OnTurn != nil {
			d.OnTurn(tl)
		}

		if v.Met {
			rep.Met = true
			rep.Stop = StopMet
			rep.Reason = v.Reason
			return rep, nil
		}

		if d.caps.TokenBudget > 0 && rep.Tokens >= d.caps.TokenBudget {
			rep.Stop = StopBudget
			rep.Reason = v.Reason
			return rep, nil
		}

		// Stall: a turn that ran no tools made no progress toward the goal.
		if tr.ToolCalls == 0 {
			stalls++
			if stalls >= d.caps.StallLimit {
				rep.Stop = StopStalled
				rep.Reason = v.Reason
				return rep, nil
			}
		} else {
			stalls = 0
		}

		directive = Continuation(condition, v)
	}

	rep.Stop = StopMaxTurns
	return rep, nil
}

// stopForCtx maps a canceled/expired ctx to a StopReason. done=false when
// the ctx is still live.
func stopForCtx(ctx context.Context) (StopReason, bool) {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return StopTimeout, true
	case context.Canceled:
		return StopCanceled, true
	default:
		return "", false
	}
}

// Continuation builds the next turn's directive from the judge's verdict.
// Append-only: this becomes a new user message in the SAME conversation,
// never a rewrite of history — preserving prefix-cache byte stability
// (CLAUDE.md token/cache constraint). Exported so the TUI's event-driven
// loop (M-goal.2) produces byte-identical continuations to the headless
// Driver.
func Continuation(condition string, v Verdict) string {
	s := "The goal is NOT yet met: " + condition
	if v.Reason != "" {
		s += "\nWhy not: " + v.Reason
	}
	if v.Hint != "" {
		s += "\nNext step: " + v.Hint
	}
	s += "\nKeep working toward the goal."
	return s
}
