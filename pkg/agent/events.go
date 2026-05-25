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
// streaming tool (currently only `think`). The CallID and Name match
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
}

// ErrorEvent surfaces a non-recoverable error mid-stream. The channel will
// still be closed after the event is delivered.
type ErrorEvent struct{ Err error }

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
func (PlanProposalApproved) isEvent()        {}
func (PlanProposalAdjustRequested) isEvent() {}
func (PlanProposalCancelled) isEvent()       {}
