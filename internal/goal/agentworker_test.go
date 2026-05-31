package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// fakePromptAgent returns a pre-loaded, already-closed event channel per
// Prompt call — the synchronous shape AgentWorker drains.
type fakePromptAgent struct {
	events  []agent.Event
	prompts []string
}

func (a *fakePromptAgent) Prompt(_ context.Context, text string) <-chan agent.Event {
	a.prompts = append(a.prompts, text)
	ch := make(chan agent.Event, len(a.events)+1)
	for _, e := range a.events {
		ch <- e
	}
	close(ch)
	return ch
}

func TestAgentWorker_AccumulatesTurn(t *testing.T) {
	a := &fakePromptAgent{events: []agent.Event{
		agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "first"}},
		agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "final say"}},
		agent.TurnEnd{ToolCalls: 2, Usage: deepseek.Usage{TotalTokens: 40}},
		agent.TurnEnd{ToolCalls: 1, Usage: deepseek.Usage{TotalTokens: 10}},
	}}
	tr, err := NewAgentWorker(a).RunTurn(context.Background(), "do it")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "final say" {
		t.Fatalf("text should be the LAST assistant message: %q", tr.Text)
	}
	if tr.ToolCalls != 3 || tr.Tokens != 50 {
		t.Fatalf("tool calls / tokens should sum across TurnEnds: %+v", tr)
	}
	if len(a.prompts) != 1 || a.prompts[0] != "do it" {
		t.Fatalf("directive not forwarded: %v", a.prompts)
	}
}

func TestAgentWorker_IgnoresNonAssistantMessages(t *testing.T) {
	a := &fakePromptAgent{events: []agent.Event{
		agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleTool, Content: "tool output"}},
		agent.TurnEnd{ToolCalls: 1},
	}}
	tr, _ := NewAgentWorker(a).RunTurn(context.Background(), "x")
	if tr.Text != "" {
		t.Fatalf("tool/user messages must not become the judged text: %q", tr.Text)
	}
}

func TestAgentWorker_SurfacesErrorEvent(t *testing.T) {
	a := &fakePromptAgent{events: []agent.Event{
		agent.MessageEnd{Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "partial"}},
		agent.ErrorEvent{Err: errors.New("stream blew up")},
	}}
	tr, err := NewAgentWorker(a).RunTurn(context.Background(), "x")
	if err == nil || err.Error() != "stream blew up" {
		t.Fatalf("ErrorEvent must surface as the turn error, got %v", err)
	}
	// Still returns what it accumulated before the error.
	if tr.Text != "partial" {
		t.Fatalf("accumulated text lost on error: %q", tr.Text)
	}
}

func TestAgentWorker_CanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// No ErrorEvent in the stream — cancellation is inferred from ctx.
	a := &fakePromptAgent{}
	if _, err := NewAgentWorker(a).RunTurn(ctx, "x"); err == nil {
		t.Fatal("a canceled ctx with no ErrorEvent should still surface an error")
	}
}
