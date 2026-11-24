package agent

import "github.com/whyiyhw/seek/pkg/deepseek"

// Event is the interface implemented by all agent events. Use a type switch
// at the consumer to dispatch — concrete types live below.
type Event interface{ isEvent() }

// AgentStart is emitted once when Prompt begins.
type AgentStart struct{}

// AgentEnd is emitted once when Prompt returns. The whole-conversation
// Usage is the sum across all turns.
type AgentEnd struct {
	Usage     deepseek.Usage
	Turns     int
	ToolCalls int
	// Finish is the upstream finish_reason of the FINAL completed turn
	// ("stop" / "length" / "max_tokens" / "refusal" / "content_filter"
	// / …; lane-normalised vocabulary, never the "stream_error:" /
	// "decode_error:" prefixes). It is the typed signal consumers use
	// to classify HOW the conversation ended — the ACP backend maps it
	// onto the protocol's stopReason (max_tokens for the
	// finish_reason=length family) instead of string-matching notice
	// text. Empty when the prompt was cancelled mid-turn (the turn
	// never completed, so there is no honest finish to report).
	Finish string
}

// TurnStart begins a single LLM call iteration. Turns are zero-indexed.
type TurnStart struct{ Index int }

// TurnEnd terminates a turn after the LLM response (and any tool calls)
// have settled. Usage is just this turn's; sum yourself if you want total.
type TurnEnd struct {
	Index     int
	Usage     deepseek.Usage
	ToolCalls int
}

// MessageStart announces the start of a new assistant or tool-result message.
type MessageStart struct{ Message deepseek.Message }

// MessageDelta carries an incremental text chunk for the in-flight assistant
// message. Reasoning=true means the chunk is from a V4 thinking-mode
// `reasoning_content` stream (CoT), not the final answer.
type MessageDelta struct {
	Delta     string
	Reasoning bool
}

// MessageEnd carries the fully-assembled assistant message.
type MessageEnd struct{ Message deepseek.Message }

// ToolExecStart fires once per tool call before the tool runs.
type ToolExecStart struct {
	CallID string
	Name   string
	Args   string // raw JSON the LLM produced
}

// ToolDelta is an intermediate output chunk from a long-running
// streaming tool. The CallID and Name match
// the surrounding ToolExecStart/ToolExecEnd pair so the TUI can route
// deltas to the right live region. Reasoning=true means the delta is
// chain-of-thought (foldable); false means it's the tool's actual
// answer text.
type ToolDelta struct {
	CallID    string
	Name      string
	Delta     string
	Reasoning bool
}

// ToolExecEnd fires once per tool call when the tool has produced a result
// (or errored). Result is fed back to the LLM in the next turn.
type ToolExecEnd struct {
	CallID string
	Name   string
	Result string
	Err    error
	// Args mirrors ToolExecStart.Args (raw JSON the LLM produced).
	// Consumers that only see the end event — the ACP mapping, which
	// builds tool_call_update content (e.g. edit's diff block) from the
	// args — need it without re-plumbing the start event through.
	Args string
}

// ErrorEvent surfaces a non-recoverable error mid-stream. The channel will
// still be closed after the event is delivered.
type ErrorEvent struct{ Err error }

// InjectedMessage reports that a user message queued via
// Agent.QueueUserMessage was committed to history at a turn boundary —
// i.e. between two LLM calls of the SAME Prompt round, not at its end.
//
// Consumers use it to show the user their text actually landed mid-round
// (the TUI prints one `↪ inserted` line), and to distinguish an injected
// turn boundary from the ordinary queue/steer path, which fires only
// after the whole round returns.
//
// Index is the turn the message was inserted BEFORE: the next LLM call
// will be turn Index. Message is the committed message verbatim (its
// Content already carries the mode reminder suffix, like every other
// user turn).
//
// Source says who authored it (InjectedSourceUser or
// InjectedSourceGuidance) — wire format for the TUI's rendering and for
// a trace report splitting human break-ins from machine hints.
const (
	// InjectedSourceUser is text the human queued mid-round.
	InjectedSourceUser = "user"
	// InjectedSourceGuidance is a deterministic hint from
	// Agent.Config.Guidance (pkg/agent/guidance.go).
	InjectedSourceGuidance = "guidance"
)

type InjectedMessage struct {
	Index   int
	Message deepseek.Message
	// Source is InjectedSourceUser or InjectedSourceGuidance. Empty on
	// events from before the field existed — readers must treat it as
	// InjectedSourceUser.
	Source string
	// Rule names the guidance rule that spoke (GuidanceNamer), empty for
	// user injections and for nameless guidance implementations. Wire
	// format for per-rule firing counts.
	Rule string
}

// PlanProposalApproved fires when the propose tool returns and the user
// picked "approve". TUI consumers MUST switch permission policy to
// ModeAsk and update the mode reminder label to "plan-execute".
// Steps is the proposal verbatim (the same slice the model passed to
// propose.Tool); v2 panel rendering will subscribe to this event to
// render the approved scope.
//
// See docs/prd/feature-plan-mode.md §2.3 for the broader contract.
type PlanProposalApproved struct {
	Steps []string
}

// PlanProposalAdjustRequested fires when the user picked "adjust" or
// typed free-text feedback via the Other row. Permission policy and
// mode label stay in plan-analyze; the propose tool's result string
// already instructs the model to re-think and re-propose.
type PlanProposalAdjustRequested struct {
	Feedback string
}

// PlanProposalCancelled fires when the user picked "cancel" or pressed
// Esc on the propose picker. TUI consumers MUST toggle /plan off
// (permission → ModeAsk, mode label → "").
type PlanProposalCancelled struct{}

// PlanStep is one row of the live plan task list. Status values match
// the `plan` tool's Status constants verbatim ("pending" / "in_progress"
// / "completed" / "skipped"); the string form keeps the agent event
// layer free of an import on internal/tools/plan.
type PlanStep struct {
	Text   string
	Status string
}

// PlanStepUpdated fires whenever the `plan` tool mutates the live task
// list (on Seed after propose-approval, or on each start/complete/skip
// call). TUI consumers should mirror Steps + CurrentIdx into their
// model and re-render. CurrentIdx is the 0-based index of the step
// currently in_progress, or -1 when no step is active.
type PlanStepUpdated struct {
	Steps      []PlanStep
	CurrentIdx int
}

func (AgentStart) isEvent()                  {}
func (AgentEnd) isEvent()                    {}
func (TurnStart) isEvent()                   {}
func (TurnEnd) isEvent()                     {}
func (MessageStart) isEvent()                {}
func (MessageDelta) isEvent()                {}
func (MessageEnd) isEvent()                  {}
func (ToolExecStart) isEvent()               {}
func (ToolDelta) isEvent()                   {}
func (ToolExecEnd) isEvent()                 {}
func (ErrorEvent) isEvent()                  {}
func (InjectedMessage) isEvent()             {}
func (PlanProposalApproved) isEvent()        {}
func (PlanProposalAdjustRequested) isEvent() {}
func (PlanProposalCancelled) isEvent()       {}
func (PlanStepUpdated) isEvent()             {}
