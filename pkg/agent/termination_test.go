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
	"github.com/whyiyhw/seek/pkg/llm"
)

// termination_test.go pins the turn-termination contract (c_vfgeam):
// which event shapes end a prompt, in what order, and with what
// classification inputs — the invariants the ACP stopReason layer
// consumes (AgentEnd.Finish for the length/refusal mapping, the
// advisory-ErrorEvent-before-AgentEnd shape for drain-then-classify,
// panic containment for observability of the internal_error path).

// sseServer serves one canned SSE body for every request.
func sseServer(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, strings.Join(lines, "\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPrompt_LengthTruncationIsAdvisory: finish_reason=length emits the
// truncation NOTICE as an ErrorEvent but the turn still completes —
// TurnEnd and AgentEnd follow, and AgentEnd.Finish carries "length" as
// the typed classification input. This ordering (notice … AgentEnd,
// close after AgentEnd) is what the ACP backend's drain logic relies on
// to tell the advisory notice from a fatal error.
func TestPrompt_LengthTruncationIsAdvisory(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"partial answ"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data: [DONE]`,
		``,
	)
	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	var end AgentEnd
	for ev := range ag.Prompt(context.Background(), "hi") {
		switch e := ev.(type) {
		case ErrorEvent:
			if !strings.Contains(e.Err.Error(), "finish_reason=length") {
				t.Fatalf("unexpected error event: %v", e.Err)
			}
			order = append(order, "notice")
		case TurnEnd:
			order = append(order, "turn_end")
		case AgentEnd:
			end = e
			order = append(order, "agent_end")
		}
	}
	if strings.Join(order, ",") != "notice,turn_end,agent_end" {
		t.Fatalf("event order = %v, want notice,turn_end,agent_end (advisory: the turn completes)", order)
	}
	if end.Finish != "length" {
		t.Fatalf("AgentEnd.Finish = %q, want length", end.Finish)
	}
}

// TestPrompt_AgentEndFinishCarriesSecondTierVocabulary: the
// second-tier lanes pass provider stop reasons through verbatim, so
// Anthropic's truncation arrives as "max_tokens" — the length family's
// second spelling — and a normal stop stays "stop".
func TestPrompt_AgentEndFinishCarriesSecondTierVocabulary(t *testing.T) {
	for _, finish := range []string{"max_tokens", "stop"} {
		prov := &fakeUsageProvider{events: []llm.Event{
			llm.TextDelta{Delta: "hi"},
			llm.TurnDone{FinishReason: finish},
		}}
		ag, err := New(Config{Provider: prov, Model: "glm-5.3"})
		if err != nil {
			t.Fatal(err)
		}
		var end AgentEnd
		for ev := range ag.Prompt(context.Background(), "hello") {
			if e, ok := ev.(AgentEnd); ok {
				end = e
			}
		}
		if end.Finish != finish {
			t.Errorf("finish %q: AgentEnd.Finish = %q (must pass through verbatim)", finish, end.Finish)
		}
	}
}

// panickingTool explodes mid-Execute — the contained path under test.
type panickingTool struct{ name string }

func (p *panickingTool) Name() string            { return p.name }
func (p *panickingTool) Description() string     { return "test tool " + p.name }
func (p *panickingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (p *panickingTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	panic("tool exploded")
}

// TestPrompt_ToolPanicContained: a panicking tool must degrade to a
// failed tool call, not kill the process (tools run on dispatch
// goroutines of their own, so the prompt goroutine's recover never
// sees it). The model receives the error as the tool result, the loop
// continues, and the prompt still terminates with an AgentEnd — the
// precondition for the ACP layer to classify anything at all.
func TestPrompt_ToolPanicContained(t *testing.T) {
	// Turn 1 requests the panicking tool; turn 2 answers after the
	// failed tool result comes back.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"boom","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
			return
		}
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"recovered"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()
	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
		Tools:        tools.New().Add(&panickingTool{name: "boom"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var toolErr error
	var end AgentEnd
	sawEnd := false
	for ev := range ag.Prompt(context.Background(), "hi") {
		switch e := ev.(type) {
		case ToolExecEnd:
			if e.Name == "boom" {
				toolErr = e.Err
			}
		case AgentEnd:
			end = e
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Fatal("no AgentEnd — the prompt never terminated observably")
	}
	if toolErr == nil || !strings.Contains(toolErr.Error(), "tool boom panicked") {
		t.Fatalf("tool error = %v, want the contained panic", toolErr)
	}
	if end.Turns != 2 {
		t.Fatalf("turns = %d, want the loop to continue past the failed tool", end.Turns)
	}
}

// panickingProvider explodes inside ChatStream — the main-loop path.
type panickingProvider struct{}

func (panickingProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.Event, error) {
	panic("provider exploded")
}
func (panickingProvider) Name() string { return "panic" }

// TestPrompt_MainLoopPanicRecovered: a panic above the tool layer (the
// streaming/assembly path runs on the prompt goroutine itself) used to
// take the whole process down — an unattended ACP client's silent
// death. Recovered, it is a first-class ErrorEvent the ACP layer
// classifies as internal_error with the stack in the detail.
func TestPrompt_MainLoopPanicRecovered(t *testing.T) {
	ag, err := New(Config{Provider: panickingProvider{}, Model: "glm-5.3"})
	if err != nil {
		t.Fatal(err)
	}
	var gotErr error
	sawEnd := false
	for ev := range ag.Prompt(context.Background(), "hi") {
		switch e := ev.(type) {
		case ErrorEvent:
			gotErr = e.Err
		case AgentEnd:
			sawEnd = true
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "recovered from panic: provider exploded") {
		t.Fatalf("error = %v, want the recovered panic as an ErrorEvent", gotErr)
	}
	if sawEnd {
		t.Fatal("a recovered panic is fatal — AgentEnd must not follow")
	}
}
