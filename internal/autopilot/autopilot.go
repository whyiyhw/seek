// Package autopilot is the deterministic orchestration driver for v7 柱 N:
// decompose a goal into scoped tasks, fan them out to parallel
// worktree-isolated subagents, and aggregate the outcomes into a report.
//
// The control flow lives here in Go (not a free-form model skill)
// because autopilot runs UNATTENDED — no human to course-correct — so
// fan-out, caps, and aggregation must be deterministic, bounded, and
// testable (PRD feature-autopilot.md §4 D1). The model's freedom is
// confined to one decompose call plus the per-task work inside each
// worktree.
//
// This package owns ONLY the orchestration logic. The two things it
// delegates — turning a goal into tasks (Decomposer) and running one
// task in an isolated worktree subagent (Fleet) — are narrow interfaces
// so the driver is unit-testable with fakes (no real LLM / subagent /
// worktree). The concrete adapters that wrap deepseek + subagent.Manager
// + the worktree manager are wired at M-A.3/M-A.4.
package autopilot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/verifier"
)

const (
	defaultMaxTasks      = 8
	defaultMaxConcurrent = 8
	// maxDoneWhenPerTask caps a decomposed task's done_when list —
	// same spirit as the propose tool's maxDoneWhen (12), tightened
	// because a fleet task is smaller than a user-facing plan.
	maxDoneWhenPerTask = 6
)

// Task is one scoped unit of work, produced by Decomposer and handed to
// Fleet to run in its own worktree.
type Task struct {
	ID     string
	Title  string
	Prompt string
	// DoneWhen are the task's checkable completion conditions — the
	// verifier's input (v8 柱 R R2). Produced by the decomposer in
	// the same shape as the propose tool's plan-level done_when.
	DoneWhen []string
}

// Outcome is the result of running one Task.
type Outcome struct {
	Task     Task
	Status   string // "done" | "failed"
	Summary  string // wire-format subagent summary, or the failure reason
	Worktree string // worktree path (kept for morning review); empty on early failure
	Commit   string // short SHA the Fleet committed in the worktree; empty if nothing changed

	// Evidence fields (v8 柱 R R1) — what the run cost and what it
	// changed. Filled by the production Fleet; zero is a legitimate
	// value ("nothing to report"), never a sentinel for error.

	// Tokens is the subagent's total token burn, prompt + completion
	// with cache hits included in prompt — the same convention as the
	// subagent wire-format footer. 0 on failure paths: Manager rolls
	// Tokens up only on the completed Result.
	Tokens int
	// CostUSD prices Tokens at the subagent's model via
	// internal/pricing. 0 when the model can't be priced (lane not
	// wired / empty model id) — the report never guesses a cost it
	// can't derive; unknown-but-nonempty model ids follow pricing's
	// documented Flash-card fallback.
	CostUSD float64
	// Diff footprint of the local commit (git show --numstat of
	// Commit). All zero when there is no commit (clean tree, failed
	// task) — Commit already distinguishes "no commit" from "commit".
	Files      int
	Insertions int
	Deletions  int

	// Verdict is the independent verifier's judgment of this task
	// against its DoneWhen (v8 柱 R R2): per-assertion rows with
	// grounded evidence, mechanical invariants, overall
	// pass/fail/doubt. Nil when no verifier was wired (nil fleet
	// verifier) — never on a judge failure, which lands as doubt.
	// The verdict OBSERVES; it does not flip Status — autopilot's
	// morning review remains the merge gate.
	Verdict *verifier.Verdict
}

// Report aggregates the whole run.
type Report struct {
	Goal     string
	Outcomes []Outcome
	Done     int
	Failed   int
	// TotalTokens / TotalCostUSD sum the outcome evidence fields so
	// the morning review reads the run's cost at a glance.
	TotalTokens  int
	TotalCostUSD float64
	// Verdict counts (v8 柱 R R2): how many verified tasks the
	// independent judge passed / flagged doubt / failed. The §9.1
	// 存疑率 remeasure reads exactly these.
	VerifiedPass  int
	VerifiedDoubt int
	VerifiedFail  int
}

// Caps bound an unattended run so it can't run away / burn the budget.
// Zero values fall back to defaults (MaxTasks/MaxConcurrent) or "no
// limit" (Timeout).
type Caps struct {
	MaxTasks      int           // hard cap on decomposed tasks (default 8)
	MaxConcurrent int           // simultaneous subagents (default 8)
	Timeout       time.Duration // total wall-clock; 0 = rely on caller ctx
}

func (c Caps) withDefaults() Caps {
	if c.MaxTasks <= 0 {
		c.MaxTasks = defaultMaxTasks
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = defaultMaxConcurrent
	}
	return c
}

// Decomposer turns a goal into at most max scoped tasks (one model call).
type Decomposer interface {
	Decompose(ctx context.Context, goal string, max int) ([]Task, error)
}

// Fleet runs one task to completion in an isolated worktree subagent and
// returns its Outcome. The real implementation wraps subagent.Manager +
// the worktree manager; tests inject a fake. A Fleet implementation must
// honour ctx cancellation (return a failed/canceled Outcome promptly).
type Fleet interface {
	Run(ctx context.Context, t Task) Outcome
}

// Driver is the orchestration engine.
type Driver struct {
	decomposer Decomposer
	fleet      Fleet
	caps       Caps
}

// New builds a Driver. caps zero-values fall back to safe defaults.
func New(d Decomposer, f Fleet, caps Caps) *Driver {
	return &Driver{decomposer: d, fleet: f, caps: caps.withDefaults()}
}

// Run executes the full loop: decompose → parallel fan-out (bounded by
// caps) → aggregate. It does NOT abort the whole run on a single task
// failure — failed tasks are recorded and the rest proceed (PRD §8
// "部分失败"). Cancelling ctx (kill-switch) propagates to each in-flight
// Fleet.Run, which should return a failed/canceled Outcome promptly.
func (d *Driver) Run(ctx context.Context, goal string) (Report, error) {
	if d.caps.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.caps.Timeout)
		defer cancel()
	}

	tasks, err := d.decomposer.Decompose(ctx, goal, d.caps.MaxTasks)
	if err != nil {
		return Report{Goal: goal}, fmt.Errorf("autopilot: decompose: %w", err)
	}
	// Enforce the cap even if the decomposer over-produces — never trust
	// the model to respect the limit.
	if len(tasks) > d.caps.MaxTasks {
		tasks = tasks[:d.caps.MaxTasks]
	}
	if len(tasks) == 0 {
		return Report{Goal: goal}, nil
	}

	outcomes := make([]Outcome, len(tasks))
	sem := make(chan struct{}, d.caps.MaxConcurrent)
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t Task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A panicking task must not take down the whole fleet.
			defer func() {
				if r := recover(); r != nil {
					outcomes[i] = Outcome{Task: t, Status: "failed", Summary: fmt.Sprintf("panic: %v", r)}
				}
			}()
			outcomes[i] = d.fleet.Run(ctx, t)
		}(i, t)
	}
	wg.Wait()

	rep := Report{Goal: goal, Outcomes: outcomes}
	for _, o := range outcomes {
		if o.Status == "done" {
			rep.Done++
		} else {
			rep.Failed++
		}
		rep.TotalTokens += o.Tokens
		rep.TotalCostUSD += o.CostUSD
		switch {
		case o.Verdict == nil:
			// no verifier wired — nothing to count
		case o.Verdict.Overall == verifier.Fail:
			rep.VerifiedFail++
		case o.Verdict.Overall == verifier.Doubt:
			rep.VerifiedDoubt++
		default:
			rep.VerifiedPass++
		}
	}
	return rep, nil
}
