package goal

import (
	"context"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// PromptAgent is the slice of *agent.Agent the headless goal Worker needs.
// *agent.Agent satisfies it structurally; tests inject a fake.
type PromptAgent interface {
	Prompt(ctx context.Context, text string) <-chan agent.Event
}

// AgentWorker adapts a PromptAgent to goal.Worker for the headless path:
// one RunTurn drives a full agent.Prompt to completion — one "turn" of the
// goal loop = one agent run (possibly several internal tool rounds) — and
// accumulates the turn's final assistant text, total tool calls, and token
// spend for the Judge + caps. This is the synchronous analog of the TUI's
// event handling (handleStreamEnd), which is exactly what the Driver's
// turn-by-turn loop wants.
type AgentWorker struct{ ag PromptAgent }

// NewAgentWorker wraps ag as a goal.Worker.
func NewAgentWorker(ag PromptAgent) AgentWorker { return AgentWorker{ag: ag} }

// compile-time: AgentWorker is a Worker.
var _ Worker = AgentWorker{}

func (w AgentWorker) RunTurn(ctx context.Context, directive string) (TurnResult, error) {
	var (
		tr       TurnResult
		firstErr error
	)
	// Drain the whole event stream (don't break early — that would leave
	// the agent goroutine blocked writing to the channel).
	for ev := range w.ag.Prompt(ctx, directive) {
		switch e := ev.(type) {
		case agent.MessageEnd:
			// The latest assistant message's full text is what the Judge
			// sees as "the latest work". Later messages overwrite earlier
			// ones so we keep the turn's final say.
			if e.Message.Role == deepseek.RoleAssistant && e.Message.Content != "" {
				tr.Text = e.Message.Content
			}
		case agent.TurnEnd:
			tr.ToolCalls += e.ToolCalls
			tr.Tokens += e.Usage.TotalTokens
		case agent.ErrorEvent:
			if firstErr == nil {
				firstErr = e.Err
			}
		}
	}
	if firstErr != nil {
		return tr, firstErr
	}
	// A canceled/expired ctx can close the stream without an ErrorEvent;
	// surface it so the Driver maps it to StopCanceled / StopTimeout.
	if ctx.Err() != nil {
		return tr, ctx.Err()
	}
	return tr, nil
}
