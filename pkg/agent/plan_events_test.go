package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// emittingStub is a stub tool whose Execute calls a configurable
// emit closure (typically wrapping the agent's EmitEvent). Used to
// exercise the "tool pushes side-effect event onto the active
// Prompt's channel" path that propose relies on.
type emittingStub struct {
	name  string
	reply string
	emit  func()
}

func (t *emittingStub) Name() string            { return t.name }
func (t *emittingStub) Description() string     { return "test emitting tool" }
func (t *emittingStub) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *emittingStub) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	if t.emit != nil {
		t.emit()
	}
	return t.reply, nil
}

func TestEmitEvent_ForwardsDuringPrompt(t *testing.T) {
	t.Parallel()
	srv, _ := twoTurnBackend(t)
	defer srv.Close()

	stub := &emittingStub{name: "read", reply: "file contents: hello"}
	reg := tools.New().Add(stub)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelChat,
		Tools:  reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Inside Execute, push each plan event. The harness around Execute
	// is the active Prompt's event channel — EmitEvent should forward
	// each one synchronously.
	stub.emit = func() {
		ag.EmitEvent(PlanProposalApproved{Steps: []string{"step A", "step B"}})
		ag.EmitEvent(PlanProposalAdjustRequested{Feedback: "needs tweaks"})
		ag.EmitEvent(PlanProposalCancelled{})
	}

	var (
		approved []PlanProposalApproved
		adjust   []PlanProposalAdjustRequested
		cancel   []PlanProposalCancelled
	)
	for ev := range ag.Prompt(context.Background(), "go") {
		switch e := ev.(type) {
		case PlanProposalApproved:
			approved = append(approved, e)
		case PlanProposalAdjustRequested:
			adjust = append(adjust, e)
		case PlanProposalCancelled:
			cancel = append(cancel, e)
		case ErrorEvent:
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}

	if len(approved) != 1 {
		t.Errorf("expected 1 PlanProposalApproved, got %d", len(approved))
	} else if got := approved[0].Steps; len(got) != 2 || got[0] != "step A" || got[1] != "step B" {
		t.Errorf("approved steps = %v, want [step A, step B]", got)
	}
	if len(adjust) != 1 {
		t.Errorf("expected 1 PlanProposalAdjustRequested, got %d", len(adjust))
	} else if adjust[0].Feedback != "needs tweaks" {
		t.Errorf("adjust feedback = %q, want %q", adjust[0].Feedback, "needs tweaks")
	}
	if len(cancel) != 1 {
		t.Errorf("expected 1 PlanProposalCancelled, got %d", len(cancel))
	}
}

func TestEmitEvent_NoActivePromptIsNoOp(t *testing.T) {
	t.Parallel()
	// No Prompt has ever been called → currentEvents is nil. Must not
	// panic; must not block. The agent's contract says this is a no-op.
	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL("http://127.0.0.1:1")),
		Model:  deepseek.ModelChat,
		Tools:  tools.New(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() {
		ag.EmitEvent(PlanProposalApproved{Steps: []string{"x"}})
		ag.EmitEvent(PlanProposalAdjustRequested{Feedback: "y"})
		ag.EmitEvent(PlanProposalCancelled{})
		close(done)
	}()
	<-done // would hang or panic if EmitEvent didn't no-op cleanly
}

func TestEmitEvent_AfterPromptEndedIsNoOp(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelChat,
		Tools:  tools.New(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Drain Prompt to completion so currentEvents is cleared.
	for range ag.Prompt(context.Background(), "hi") {
	}

	// Now EmitEvent runs outside an active Prompt — must be a no-op.
	done := make(chan struct{})
	go func() {
		ag.EmitEvent(PlanProposalCancelled{})
		close(done)
	}()
	<-done
}

func TestPlanEvents_ImplementEventInterface(t *testing.T) {
	t.Parallel()
	// Compile-time check via the interface assertion; runtime check
	// ensures the type switch consumers will use (TUI) can dispatch
	// cleanly without falling through to a default no-op.
	events := []Event{
		PlanProposalApproved{Steps: []string{"a"}},
		PlanProposalAdjustRequested{Feedback: "b"},
		PlanProposalCancelled{},
	}
	for _, ev := range events {
		switch ev.(type) {
		case PlanProposalApproved, PlanProposalAdjustRequested, PlanProposalCancelled:
			// ok
		default:
			t.Errorf("%T did not match any expected Plan event type", ev)
		}
	}
}

// guard against accidental concurrent EmitEvent from a streaming tool
// in a future refactor: today emit is single-goroutine by Prompt's
// contract, but tests are cheap insurance.
func TestEmitEvent_RaceFreeUnderConcurrentNoOps(t *testing.T) {
	t.Parallel()
	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL("http://127.0.0.1:1")),
		Model:  deepseek.ModelChat,
		Tools:  tools.New(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg atomic.Int32
	wg.Add(50)
	for range 50 {
		go func() {
			defer wg.Add(-1)
			ag.EmitEvent(PlanProposalCancelled{})
		}()
	}
	for wg.Load() > 0 {
	}
}
