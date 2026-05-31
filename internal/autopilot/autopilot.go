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
)

const (
	defaultMaxTasks      = 8
	defaultMaxConcurrent = 8
)

// Task is one scoped unit of work, produced by Decomposer and handed to
// Fleet to run in its own worktree.
type Task struct {
	ID     string
	Title  string
	Prompt string
}

// Outcome is the result of running one Task.
type Outcome struct {
	Task     Task
	Status   string // "done" | "failed"
	Summary  string // wire-format subagent summary, or the failure reason
	Worktree string // worktree path (kept for morning review); empty on early failure
	Commit   string // short SHA the Fleet committed in the worktree; empty if nothing changed
}

// Report aggregates the whole run.
type Report struct {
	Goal     string
	Outcomes []Outcome
	Done     int
	Failed   int
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
	}
	return rep, nil
}
