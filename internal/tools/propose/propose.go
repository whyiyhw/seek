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
	// "every TODO in the project". 20 is loose enough to fit a real
	// migration (e.g. an auth refactor with 12-15 verifiable steps)
	// while still tight enough that the user can scan the whole plan
	// in the picker. Tighter than the JSON schema's maxItems so the
	// tool can produce a guiding error message.
	maxSteps = 20
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
      "maxItems": 20,
      "description": "Ordered concrete actions you will take. 3–8 items typical, up to 20 for larger migrations. Each step must be verifiable by the user (e.g. \"Add X handler in handlers.go\"), not internal phases (\"think about Y\"). Each step ≤ 200 chars. Don't include sub-bullets — if a step is too big, split it before proposing.",
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
	// Approved is called when the user picked an approve option.
	// Steps is the plan verbatim (same slice the model proposed).
	// batch is true when the user picked "auto-approve writes per
	// step" — the host should arm the plan tool's pre-approval gate
	// so that write/edit/bash inside a plan(start)…plan(complete)
	// window bypass the per-call y/N prompt. false = legacy
	// per-call gating (Phase C of plan-mode optimisation).
	Approved(steps []string, batch bool)
	// AdjustRequested is called when the user picked "adjust" OR
	// typed free-text feedback via the auto-appended Other row.
	// feedback may be empty when the user picked Adjust without
	// typing.
	AdjustRequested(feedback string)
	// Cancelled is called when the user picked "cancel" or pressed
	// Esc (Answer.Cancelled).
	Cancelled()
}

// DuplicateChecker is an OPTIONAL interface a Sink may implement.
// When present, propose calls IsDuplicateOfLastApproved with the
// incoming steps before showing the picker. A true return short-
// circuits the call: no picker, no user interruption, result text
// asks the model to re-think instead of re-proposing the same plan.
//
// Implementations should compare order-sensitively against the most
// recently approved step list (whitespace-trimmed). Returning false
// when no plan has been approved yet is correct — there's nothing
// to dedupe against.
type DuplicateChecker interface {
	IsDuplicateOfLastApproved(steps []string) bool
}

// ProgressReporter is an OPTIONAL interface a Sink may implement.
// When present, propose calls ProgressSummary on adjust paths and
// folds the returned summary into the result text, so the model's
// next proposal preserves work already completed. Empty string =
// no progress to report (return as such; propose elides the section
// entirely).
//
// The summary should be terse and self-contained — propose drops it
// verbatim into the result without further formatting. Recommended
// shape: "Completed: 1, 2. In progress: 3. Pending: 4, 5."
type ProgressReporter interface {
	ProgressSummary() string
}

// ContextReceiver is an OPTIONAL interface a Sink may implement.
// When present, propose calls OnProposeStart with the full propose
// args (problem + steps + why_now) BEFORE the picker pops. The host
// uses this to capture context that Approved alone doesn't carry —
// e.g. the plan artifact writer needs the problem statement, but
// Approved's signature is (steps, batch). The call fires for every
// Execute invocation regardless of the user's eventual choice; the
// host should hold the data only as long as it might be needed
// (typically: until the next OnProposeStart or until the workflow
// resets).
type ContextReceiver interface {
	OnProposeStart(problem string, steps []string, whyNow string)
}

// ArtifactReporter is an OPTIONAL interface a Sink may implement.
// After Approved returns, propose calls LastArtifactStatus and folds
// the result into the approve-path tool text:
//
//   - path != "" && err == nil → success: "Plan artifact: <path>"
//   - err != nil               → failure: "(note: plan artifact …)"
//   - path == "" && err == nil → host doesn't write artifacts; quiet
//
// See PRD §八 (plan-mode-v2.x artifact).
type ArtifactReporter interface {
	LastArtifactStatus() (path string, err error)
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

	// Duplicate-of-last-approved short-circuit. The model sometimes
	// re-proposes verbatim after an adjust loop went in circles, or
	// after a tangent interrupted the flow. Without this guard, the
	// user gets the same picker again and learns to ignore it. The
	// short-circuit returns a result that steers the model to either
	// (a) re-think with the user's pending feedback, or (b) execute
	// the existing plan rather than re-propose. No sink callback —
	// nothing happened from the user's perspective.
	if dc, ok := t.sink.(DuplicateChecker); ok && dc.IsDuplicateOfLastApproved(a.Steps) {
		return duplicateResult(a.Steps), nil
	}

	// Hand the full propose args to the host BEFORE the picker pops.
	// Approved's signature only carries steps + batch, but downstream
	// consumers (e.g. the plan artifact writer) need the problem
	// statement and why_now. The host stores these and consumes them
	// later if/when the user approves.
	if cr, ok := t.sink.(ContextReceiver); ok {
		cr.OnProposeStart(a.Problem, a.Steps, a.WhyNow)
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
			{ID: "approve", Label: "Go ahead — ask per call", Description: "Approve the plan; each write/edit/bash still prompts for y/N (default, safest)."},
			{ID: "approve_batch", Label: "Go ahead — auto-approve per step", Description: "Approve the plan; while plan(start=N) is active, writes/edits/bash auto-pass without prompting. Esc revokes for the rest of the step."},
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
		return adjustResult(ans.FreeText, t.progressSummary())
	}

	if pickedID(ans, "adjust") {
		if t.sink != nil {
			t.sink.AdjustRequested("")
		}
		return adjustResult("", t.progressSummary())
	}

	if pickedID(ans, "approve") {
		if t.sink != nil {
			t.sink.Approved(a.Steps, false)
		}
		path, werr := t.artifactStatus()
		return approveResult(a.Steps, false, path, werr)
	}

	if pickedID(ans, "approve_batch") {
		if t.sink != nil {
			t.sink.Approved(a.Steps, true)
		}
		path, werr := t.artifactStatus()
		return approveResult(a.Steps, true, path, werr)
	}

	// Defensive: shouldn't be reachable given single-select picker
	// over 4 deterministic option IDs. If askuser ever evolves and
	// returns something unexpected, treat as cancellation rather
	// than silently proceeding.
	return "[plan: cancelled]\nUnexpected answer shape from picker — treating as cancellation. Please re-issue your last request."
}

func pickedID(ans auser.Answer, id string) bool {
	return slices.Contains(ans.ChosenIDs, id)
}

// progressSummary returns the host's progress summary if the sink
// implements ProgressReporter; otherwise the empty string. Captured
// at the moment the adjust path fires so the snapshot reflects
// "what was done by the time the user pressed Adjust", not "what's
// done now".
func (t Tool) progressSummary() string {
	if r, ok := t.sink.(ProgressReporter); ok {
		return r.ProgressSummary()
	}
	return ""
}

// artifactStatus returns the host's most recent artifact write
// outcome (path, err) if the sink implements ArtifactReporter;
// otherwise (zero, nil). Called AFTER sink.Approved so the host has
// had a chance to do the write.
func (t Tool) artifactStatus() (string, error) {
	if r, ok := t.sink.(ArtifactReporter); ok {
		return r.LastArtifactStatus()
	}
	return "", nil
}

func approveResult(steps []string, batch bool, artifactPath string, artifactErr error) string {
	// The "[plan: approved]" prefix is load-bearing — the resume
	// path (internal/tools/plan/reconstruct.go) scans for it
	// verbatim to find the seeding point. Variants go AFTER the
	// closing bracket.
	var sb strings.Builder
	if batch {
		sb.WriteString("[plan: approved] (auto-approve-per-step)\nProposal accepted by user with auto-approve-per-step. You are now in plan-execute substate. Call plan(action=\"start\", index=N) before each step's writes/edits/bash — that arms the per-step pre-approval gate so those calls auto-pass without a y/N prompt. Call plan(action=\"complete\", index=N) when the step is done to disarm the gate. If the user Esc's mid-step, the gate is revoked and subsequent writes go back to per-call y/N until you re-arm via plan(start). Narrate progress in chat.\n\nApproved plan:")
	} else {
		sb.WriteString("[plan: approved]\nProposal accepted by user. You are now in plan-execute substate — write/edit/bash will prompt for per-call confirmation. Execute the plan step by step and narrate progress in chat. Use plan(action=\"start\", index=N) and plan(action=\"complete\", index=N) to track progress.\n\nApproved plan:")
	}
	for i, s := range steps {
		fmt.Fprintf(&sb, "\n  %d. %s", i+1, s)
	}
	if artifactErr != nil {
		// Failure surfaces in the model-visible tool result AND on
		// stderr (host responsibility). PRD §8.7: failure is
		// observational — the workflow continues regardless.
		fmt.Fprintf(&sb, "\n\n(note: plan artifact write failed: %v — workflow continues)", artifactErr)
	} else if artifactPath != "" {
		fmt.Fprintf(&sb, "\n\nPlan artifact: %s", artifactPath)
	}
	return sb.String()
}

func adjustResult(feedback, progress string) string {
	var sb strings.Builder
	sb.WriteString("[plan: adjust requested]\nUser did NOT approve. Before re-proposing: (1) summarize in chat what's already done (if anything), so the next plan can build on it; (2) revise the plan based on the feedback below; (3) call propose() again with the revised plan.\n")
	if feedback != "" {
		sb.WriteString("\nUser feedback (treat as the primary constraint for the next plan):\n  ")
		sb.WriteString(feedback)
	} else {
		sb.WriteString("\nNo specific feedback provided — the user clicked Adjust without typing. Ask them what to change before re-proposing.")
	}
	if progress != "" {
		// Inject the structured progress summary so the next
		// propose() can build ON the work the user already
		// approved. Without this, the model has to re-derive
		// "what's done" from chat narrative — which routinely
		// causes the new plan to redo steps 1-2 that already
		// landed. The summary lives in the tool result, NOT
		// in the system prompt, so each adjust loop's snapshot
		// is captured at that loop's actual progress state.
		sb.WriteString("\n\nProgress on the previous (now-superseded) plan — preserve this when proposing the next one:\n  ")
		sb.WriteString(progress)
	}
	return sb.String()
}

// duplicateResult is the canned response when the model re-proposes
// the exact same step list. We don't fire the Sink (no Approved /
// AdjustRequested / Cancelled — nothing happened from the user's
// view) and we don't pop the picker. The text steers the model:
// either execute the already-approved plan, or call ask_user to
// resolve whatever ambiguity tripped the duplicate.
func duplicateResult(steps []string) string {
	var sb strings.Builder
	sb.WriteString("[plan: duplicate]\nThis proposal is byte-identical to the last plan the user already approved. Do not show the user the same picker again. Either: (a) execute the existing plan step by step (use plan(action=\"start\", index=N) to track progress); or (b) if you're unsure what to do next, call ask_user with a specific question — duplicating propose is not a question.\n\nApproved (and active) plan:")
	for i, s := range steps {
		fmt.Fprintf(&sb, "\n  %d. %s", i+1, s)
	}
	return sb.String()
}
