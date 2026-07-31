package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// recordingHook implements every hook interface the agent loop fires.
// Each callback appends a tag to events; tests assert on the full slice
// to verify ordering. Behavioural knobs (prependMsg / denyTool /
// rewriteArgs) are opt-in so each test configures only what it needs.
type recordingHook struct {
	mu          sync.Mutex
	events      []string
	prependMsg  string
	denyTool    string
	rewriteArgs json.RawMessage

	prePromptHistoryLen int
}

func (r *recordingHook) record(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recordingHook) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recordingHook) OnPrePrompt(_ context.Context, in hooks.PrePromptIn) (hooks.PrePromptOut, error) {
	r.record("pre_prompt:" + in.UserText)
	r.mu.Lock()
	r.prePromptHistoryLen = len(in.History)
	r.mu.Unlock()
	out := hooks.PrePromptOut{}
	if r.prependMsg != "" {
		out.Prepend = []deepseek.Message{{
			Role:    deepseek.RoleUser,
			Content: r.prependMsg,
		}}
	}
	return out, nil
}

func (r *recordingHook) OnPreToolUse(_ context.Context, in hooks.PreToolUseIn) (hooks.PreToolUseOut, error) {
	r.record("pre_tool:" + in.Name)
	out := hooks.PreToolUseOut{}
	if r.denyTool != "" && in.Name == r.denyTool {
		out.Deny = "denied by test"
	}
	if r.rewriteArgs != nil {
		out.Args = r.rewriteArgs
	}
	return out, nil
}

func (r *recordingHook) OnPreTurn(_ context.Context, ev hooks.PreTurnEvent) {
	r.record(fmt.Sprintf("pre_turn:%d", ev.Index))
}

func (r *recordingHook) OnPostTurn(_ context.Context, ev hooks.PostTurnEvent) {
	r.record(fmt.Sprintf("post_turn:%d:tools=%d:finish=%s", ev.Index, ev.ToolCalls, ev.Finish))
}

func (r *recordingHook) OnPostToolUse(_ context.Context, ev hooks.PostToolUseEvent) {
	suffix := "ok"
	if ev.Result == "denied by test" {
		suffix = "denied"
	}
	r.record(fmt.Sprintf("post_tool:%s:%s", ev.Name, suffix))
}

// newHookedAgent wires a recordingHook into a fresh agent talking to the
// shared two-turn backend. Returns the agent, the backend, and the
// recorder for assertions.
func newHookedAgent(t *testing.T, rec *recordingHook, toolReply string) (*Agent, func()) {
	t.Helper()
	srv, _ := twoTurnBackend(t)

	stub := &stubTool{
		name:   "read",
		schema: `{"type":"object"}`,
		reply:  toolReply,
	}
	reg := tools.New().Add(stub)

	hreg := hooks.NewRegistry()
	hreg.Register(rec)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV4Flash,
		Tools:  reg,
		Hooks:  hreg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ag, func() { srv.Close() }
}

func drainPrompt(ag *Agent, text string) {
	for range ag.Prompt(context.Background(), text) {
		// drain; assertions happen on hook state
	}
}

func TestAgent_HooksFireInExpectedOrder(t *testing.T) {
	rec := &recordingHook{}
	ag, cleanup := newHookedAgent(t, rec, "file contents")
	defer cleanup()

	drainPrompt(ag, "what's in hello.txt?")

	// Turn 0: assistant emits a read tool call → PreToolUse + PostToolUse fire
	//          inside that turn; PostTurn reports tools=1.
	// Turn 1: assistant emits text only → PostTurn reports tools=0.
	want := []string{
		"pre_prompt:what's in hello.txt?",
		"pre_turn:0",
		"pre_tool:read",
		"post_tool:read:ok",
		"post_turn:0:tools=1:finish=tool_calls",
		"pre_turn:1",
		"post_turn:1:tools=0:finish=stop",
	}
	got := rec.Events()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("event order mismatch:\n got  %v\n want %v", got, want)
	}
}

func TestAgent_PrePrompt_PrependLandsBeforeUserMessage(t *testing.T) {
	rec := &recordingHook{prependMsg: "<context source=\"test\">memory injection</context>"}
	ag, cleanup := newHookedAgent(t, rec, "ok")
	defer cleanup()

	drainPrompt(ag, "real user input")

	hist := ag.Messages()

	// Find the prepended <context> message and the actual user message;
	// the context must appear first, both must be user-role.
	var ctxIdx, userIdx = -1, -1
	for i, m := range hist {
		if m.Role != deepseek.RoleUser {
			continue
		}
		switch {
		case strings.Contains(m.Content, "memory injection") && ctxIdx == -1:
			ctxIdx = i
		case strings.Contains(m.Content, "real user input") && userIdx == -1:
			userIdx = i
		}
	}
	if ctxIdx < 0 {
		t.Fatalf("prepended <context> message not found in history (got %d messages)", len(hist))
	}
	if userIdx < 0 {
		t.Fatalf("original user message not found in history")
	}
	if ctxIdx >= userIdx {
		t.Errorf("prepend must come before user message; got ctxIdx=%d, userIdx=%d", ctxIdx, userIdx)
	}
}

func TestAgent_PrePrompt_HistorySnapshotIsReadOnly(t *testing.T) {
	rec := &recordingHook{}
	ag, cleanup := newHookedAgent(t, rec, "ok")
	defer cleanup()

	// First prompt seeds history with whatever the backend returns.
	drainPrompt(ag, "first")
	firstLen := len(ag.Messages())

	// On the second prompt, the hook's History snapshot should reflect
	// the post-first-prompt state. The agent must pass a COPY, not the
	// live slice — mutation by the hook would orphan tool_call_ids and
	// poison subsequent turns.
	rec2 := &recordingHook{}
	ag.cfg.Hooks = hooks.NewRegistry()
	ag.cfg.Hooks.Register(rec2)

	drainPrompt(ag, "second")

	if rec2.prePromptHistoryLen != firstLen {
		t.Errorf("PrePrompt History should reflect post-first-prompt length %d, got %d",
			firstLen, rec2.prePromptHistoryLen)
	}
}

func TestAgent_PreToolUse_DenyShortCircuitsExecution(t *testing.T) {
	// twoTurnBackend's turn 1 emits a `read` tool call. By denying it,
	// the tool's Execute must NOT run, and the assistant's tool result
	// in history must be the deny text.
	rec := &recordingHook{denyTool: "read"}
	srv, _ := twoTurnBackend(t)
	defer srv.Close()

	stub := &stubTool{
		name:   "read",
		schema: `{"type":"object"}`,
		reply:  "executed (should not appear)",
	}
	reg := tools.New().Add(stub)

	hreg := hooks.NewRegistry()
	hreg.Register(rec)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV4Flash,
		Tools:  reg,
		Hooks:  hreg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	drainPrompt(ag, "what's in hello.txt?")

	if got := stub.gotArg.Load(); got != nil {
		t.Errorf("denied tool was executed (saw args=%q)", *got)
	}

	// The tool-result message in history should carry the deny text.
	hist := ag.Messages()
	var denyFound bool
	for _, m := range hist {
		if m.Role == deepseek.RoleTool && m.Content == "denied by test" {
			denyFound = true
			break
		}
	}
	if !denyFound {
		t.Errorf("expected a tool-role message with deny text in history")
	}

	// PostToolUse should still fire (audit observer needs to see it).
	got := rec.Events()
	var sawPostTool bool
	for _, e := range got {
		if e == "post_tool:read:denied" {
			sawPostTool = true
			break
		}
	}
	if !sawPostTool {
		t.Errorf("PostToolUse should fire on deny path, got events=%v", got)
	}
}

func TestAgent_PreToolUse_ArgsRewriteReachesTool(t *testing.T) {
	// Rewriting args is the supported "redact secrets" use case. Verify
	// the tool sees the rewritten args, not the LLM's original.
	rewritten := json.RawMessage(`{"path":"REDACTED"}`)
	rec := &recordingHook{rewriteArgs: rewritten}

	srv, _ := twoTurnBackend(t)
	defer srv.Close()

	stub := &stubTool{
		name:   "read",
		schema: `{"type":"object"}`,
		reply:  "ok",
	}
	reg := tools.New().Add(stub)

	hreg := hooks.NewRegistry()
	hreg.Register(rec)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV4Flash,
		Tools:  reg,
		Hooks:  hreg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	drainPrompt(ag, "what's in hello.txt?")

	got := stub.gotArg.Load()
	if got == nil {
		t.Fatalf("tool was not executed at all (expected to see rewritten args)")
	}
	if !strings.Contains(*got, "REDACTED") {
		t.Errorf("tool should have received rewritten args containing REDACTED, got %q", *got)
	}
	if strings.Contains(*got, "hello.txt") {
		t.Errorf("tool should NOT have seen the LLM's original args, got %q", *got)
	}
}

func TestAgent_NilHooksRunsCleanly(t *testing.T) {
	// Defensive check: an agent constructed without a Hooks registry
	// must behave identically to pre-hooks seek. This ensures every
	// Notify*/Apply* helper survives nil-receiver dispatch in the
	// real agent loop path (the hooks package unit tests cover the
	// registry-level guarantees; this proves the wiring honours them).
	srv, _ := twoTurnBackend(t)
	defer srv.Close()

	stub := &stubTool{name: "read", schema: `{"type":"object"}`, reply: "ok"}
	reg := tools.New().Add(stub)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV4Flash,
		Tools:  reg,
		// Hooks: nil — must not panic
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var sawEnd bool
	for ev := range ag.Prompt(context.Background(), "hi") {
		if _, ok := ev.(AgentEnd); ok {
			sawEnd = true
		}
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}
	if !sawEnd {
		t.Errorf("expected AgentEnd event with nil Hooks registry")
	}
}
