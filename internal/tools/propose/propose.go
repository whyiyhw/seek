// Package propose implements the `propose` tool: the model presents a
// structured plan (problem statement + ordered steps) to the user and
// asks for explicit approval before transitioning from plan-analyze to
// plan-execute substate.
//
// Wire model: same as ask_user — holds an *auser.Policy (in
// internal/askuser) injected from cmd/seek. Optional Sink interface
// lets future code (agent event wiring in P2) observe the user's
// choice for permission/mode-reminder side effects; nil sink is fine
// and produces a no-op. P1 builds and tests the tool in isolation;
// the Sink stays nil until P2 wires it.
//
// See docs/prd/feature-plan-mode.md for the broader workflow.
package propose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	auser "github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/tools"
)

const toolName = "propose"

const (
	// Steps cap. The intent is "a focused, executable plan", not
	// "every TODO in the project". Tighter than the JSON schema's
	// maxItems so the tool can produce a guiding error message.
	maxSteps = 12
	// Per-step character cap. Steps are verifiable actions, not
	// paragraphs — the cap prevents "rephrase the entire design
	// doc into a single step" patterns.
	maxStepLength = 200
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "problem": {
      "type": "string",
      "description": "One-paragraph problem statement: what the user wants and what makes it non-trivial. Self-contained — assume the reader hasn't read prior turns."
    },
    "steps": {
      "type": "array",
      "minItems": 1,
      "maxItems": 12,
      "description": "Ordered concrete actions you will take. 3–8 items typical. Each step must be verifiable by the user (e.g. \"Add X handler in handlers.go\"), not internal phases (\"think about Y\"). Each step ≤ 200 chars. Don't include sub-bullets — if a step is too big, split it before proposing.",
      "items": {"type": "string"}
    },
    "why_now": {
      "type": "string",
      "description": "Optional. Briefly: why is now the right time to commit to this plan? Surfaces hidden assumptions to the user (e.g. \"this assumes the auth refactor in #234 is merged\")."
    }
  },
  "required": ["problem", "steps"],
  "additionalProperties": false
}`)

const description = "Propose a concrete plan (problem + ordered steps) to the user and wait for explicit approval. Use only in plan mode after gathering enough context to define what you'll do and how. Returns the user's choice (approve / adjust with feedback / cancel) as a structured string. Approval unlocks writes (plan-analyze → plan-execute substate). Do NOT use for: clarifying questions before you understand the problem (use ask_user); reporting status mid-execution (narrate in chat); asking which of N alternatives to pick (use ask_user)."

// Sink is the optional observer that's notified about the user's
// choice. The P2 wiring (pkg/agent event types + cmd/seek injection)
// will pass a non-nil sink that emits agent.Event values for the TUI
// to react to (permission switch, mode reminder change, status bar
// substate). P1 callers and unit tests pass nil; the tool degrades
// to "return result string, no side effects".
type Sink interface {
	// Approved is called when the user picked "approve". Steps is
	// the plan verbatim (same slice the model proposed), useful for
	// later panel rendering or for stuffing into the mode reminder.
	Approved(steps []string)
	// AdjustRequested is called when the user picked "adjust" OR
	// typed free-text feedback via the auto-appended Other row.
	// feedback may be empty when the user picked Adjust without
	// typing.
	AdjustRequested(feedback string)
	// Cancelled is called when the user picked "cancel" or pressed
	// Esc (Answer.Cancelled).
	Cancelled()
}

// Tool is the propose tool implementation.
type Tool struct {
	policy *auser.Policy
	sink   Sink
}

// New constructs the tool. policy must be non-nil. sink may be nil
// in P1 (no event wiring yet) and in unit tests.
func New(policy *auser.Policy, sink Sink) Tool {
	return Tool{policy: policy, sink: sink}
}

func (Tool) Name() string            { return toolName }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

type args struct {
	Problem string   `json:"problem"`
	Steps   []string `json:"steps"`
	WhyNow  string   `json:"why_now,omitempty"`
}

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict(toolName, raw, &a, "problem", "steps", "why_now"); err != nil {
		return "", err
	}
	if t.policy == nil {
		return "", errors.New(toolName + ": no askuser policy configured (programming error)")
	}

	if strings.TrimSpace(a.Problem) == "" {
		return "", tools.MissingField(toolName, "problem", raw, "problem", "steps", "why_now")
	}
	if len(a.Steps) == 0 {
		return "", tools.MissingField(toolName, "steps", raw, "problem", "steps", "why_now")
	}
	if len(a.Steps) > maxSteps {
		return "", fmt.Errorf("%s: too many steps (got %d, max %d). Split the plan into a smaller first batch and propose again after the first batch completes.",
			toolName, len(a.Steps), maxSteps)
	}
	for i, s := range a.Steps {
		if strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("%s: step %d is empty", toolName, i)
		}
		if len(s) > maxStepLength {
			return "", fmt.Errorf("%s: step %d is %d chars (max %d). Tighten the wording — steps describe one verifiable action, not a paragraph.",
				toolName, i, len(s), maxStepLength)
		}
	}

	q := buildQuestion(a)
	if err := auser.Validate(q); err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	ans, err := t.policy.Ask(q)
	if err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	return t.handleAnswer(a, ans), nil
}

// buildQuestion translates the propose args into an askuser.Question.
// The full proposal (problem + steps + why_now) goes into the picker
// header so the user can read the whole plan inline; v2 will move
// this rendering into a dedicated panel and shorten the header.
func buildQuestion(a args) auser.Question {
	var sb strings.Builder
	sb.WriteString(a.Problem)
	if a.WhyNow != "" {
		sb.WriteString("\n\nWhy now: ")
		sb.WriteString(a.WhyNow)
	}
	sb.WriteString("\n\nPlan:")
	for i, s := range a.Steps {
		fmt.Fprintf(&sb, "\n  %d. %s", i+1, s)
	}

	return auser.Question{
		Question: sb.String(),
		Options: []auser.Option{
			{ID: "approve", Label: "Go ahead", Description: "Approve the plan; unlock writes and start executing."},
			{ID: "adjust", Label: "Adjust", Description: "Reject this proposal; you can type feedback so the assistant re-plans."},
			{ID: "cancel", Label: "Cancel /plan", Description: "Stop the plan flow entirely. Exit plan mode."},
		},
	}
}

func (t Tool) handleAnswer(a args, ans auser.Answer) string {
	// Esc cancellation OR explicit "cancel" pick: terminate.
	if ans.Cancelled || pickedID(ans, "cancel") {
		if t.sink != nil {
			t.sink.Cancelled()
		}
		return "[plan: cancelled]\nUser cancelled the proposal and exited /plan mode. Do not execute. Wait for the user's next instruction."
	}

	// Free-text via "Other" row: the user wrote feedback. Route to
	// adjust with the feedback verbatim — this is the load-bearing
	// channel for refinement requests.
	if ans.FreeText != "" {
		if t.sink != nil {
			t.sink.AdjustRequested(ans.FreeText)
		}
		return adjustResult(ans.FreeText)
	}

	if pickedID(ans, "adjust") {
		if t.sink != nil {
			t.sink.AdjustRequested("")
		}
		return adjustResult("")
	}

	if pickedID(ans, "approve") {
		if t.sink != nil {
			t.sink.Approved(a.Steps)
		}
		return approveResult(a.Steps)
	}

	// Defensive: shouldn't be reachable given single-select picker
	// over 3 deterministic option IDs. If askuser ever evolves and
	// returns something unexpected, treat as cancellation rather
	// than silently proceeding.
	return "[plan: cancelled]\nUnexpected answer shape from picker — treating as cancellation. Please re-issue your last request."
}

func pickedID(ans auser.Answer, id string) bool {
	return slices.Contains(ans.ChosenIDs, id)
}

func approveResult(steps []string) string {
	var sb strings.Builder
	sb.WriteString("[plan: approved]\nProposal accepted by user. You are now in plan-execute substate — write/edit/bash will prompt for per-call confirmation. Execute the plan step by step and narrate progress in chat.\n\nApproved plan:")
	for i, s := range steps {
		fmt.Fprintf(&sb, "\n  %d. %s", i+1, s)
	}
	return sb.String()
}

func adjustResult(feedback string) string {
	var sb strings.Builder
	sb.WriteString("[plan: adjust requested]\nUser did NOT approve. Before re-proposing: (1) summarize in chat what's already done (if anything), so the next plan can build on it; (2) revise the plan based on the feedback below; (3) call propose() again with the revised plan.\n")
	if feedback != "" {
		sb.WriteString("\nUser feedback (treat as the primary constraint for the next plan):\n  ")
		sb.WriteString(feedback)
	} else {
		sb.WriteString("\nNo specific feedback provided — the user clicked Adjust without typing. Ask them what to change before re-proposing.")
	}
	return sb.String()
}
