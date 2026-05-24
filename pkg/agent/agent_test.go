package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// slowReadTool is a read-only tool that sleeps for latency before
// returning so tests can distinguish parallel from sequential dispatch
// by measuring wall-clock time.
type slowReadTool struct {
	latency time.Duration
	called  atomic.Int32
}

func (t *slowReadTool) Name() string            { return "slow_read" }
func (t *slowReadTool) Description() string     { return "slow read" }
func (t *slowReadTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *slowReadTool) ReadOnly() bool          { return true }
func (t *slowReadTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.called.Add(1)
	select {
	case <-time.After(t.latency):
		return "contents", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// stubTool records the args it received and returns a canned response.
type stubTool struct {
	name   string
	schema string
	gotArg atomic.Pointer[string]
	reply  string
}

func (t *stubTool) Name() string            { return t.name }
func (t *stubTool) Description() string     { return "test tool " + t.name }
func (t *stubTool) Schema() json.RawMessage { return json.RawMessage(t.schema) }
func (t *stubTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	s := string(raw)
	t.gotArg.Store(&s)
	return t.reply, nil
}

// twoTurnBackend simulates a DeepSeek server that on the first request
// emits a tool_call delta, and on the second request emits a final text
// answer. Useful for end-to-end agent loop verification.
func twoTurnBackend(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		switch n {
		case 1:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\""}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"hello.txt\"}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		default:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"the file says hello"}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":12}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		}
	}))
	return srv, &calls
}

func TestAgent_ToolCallFlow(t *testing.T) {
	srv, calls := twoTurnBackend(t)
	defer srv.Close()

	stub := &stubTool{
		name:   "read",
		schema: `{"type":"object"}`,
		reply:  "file contents: hello",
	}
	reg := tools.New().Add(stub)

	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:        deepseek.ModelChat,
		SystemPrompt: "sys",
		Tools:        reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var (
		seen      []string
		finalText string
		end       AgentEnd
		toolStart ToolExecStart
		toolEnd   ToolExecEnd
	)
	for ev := range ag.Prompt(context.Background(), "what's in hello.txt?") {
		switch e := ev.(type) {
		case AgentStart:
			seen = append(seen, "agent_start")
		case TurnStart:
			seen = append(seen, "turn_start")
		case MessageStart:
			seen = append(seen, "message_start:"+e.Message.Role)
		case MessageDelta:
			if !e.Reasoning {
				finalText += e.Delta
			}
			seen = append(seen, "message_delta")
		case MessageEnd:
			seen = append(seen, "message_end:"+e.Message.Role)
		case ToolExecStart:
			toolStart = e
			seen = append(seen, "tool_start:"+e.Name)
		case ToolExecEnd:
			toolEnd = e
			seen = append(seen, "tool_end:"+e.Name)
		case TurnEnd:
			seen = append(seen, "turn_end")
		case AgentEnd:
			end = e
			seen = append(seen, "agent_end")
		case ErrorEvent:
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}

	// Backend hit exactly twice.
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Errorf("backend calls = %d, want 2", got)
	}

	// Stub tool was called with the assembled arguments.
	if got := stub.gotArg.Load(); got == nil || *got != `{"path":"hello.txt"}` {
		t.Errorf("tool args = %v, want {\"path\":\"hello.txt\"}", got)
	}

	// Tool start/end correlate by CallID.
	if toolStart.CallID != "call_1" || toolEnd.CallID != "call_1" {
		t.Errorf("tool ids = (%q, %q), want both call_1", toolStart.CallID, toolEnd.CallID)
	}
	if toolEnd.Err != nil {
		t.Errorf("tool end error: %v", toolEnd.Err)
	}

	// Final assistant text reached the consumer.
	if finalText != "the file says hello" {
		t.Errorf("finalText = %q", finalText)
	}

	// AgentEnd totals.
	if end.Turns != 2 {
		t.Errorf("Turns = %d, want 2", end.Turns)
	}
	if end.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", end.ToolCalls)
	}
	if end.Usage.PromptTokens != 30 {
		t.Errorf("PromptTokens = %d, want 30", end.Usage.PromptTokens)
	}
	if end.Usage.PromptCacheHitTokens != 8 {
		t.Errorf("PromptCacheHitTokens = %d, want 8", end.Usage.PromptCacheHitTokens)
	}

	// Event ordering: agent_start must precede turn_start; turn_end must
	// follow tool_end on the first turn; agent_end is terminal.
	if seen[0] != "agent_start" || seen[len(seen)-1] != "agent_end" {
		t.Errorf("bad envelope: first=%s last=%s (full=%v)", seen[0], seen[len(seen)-1], seen)
	}
	// Sanity: the assistant message that requested the tool, the tool
	// result message, and the final assistant message all reach MessageEnd.
	assistantEnds := 0
	toolEnds := 0
	for _, s := range seen {
		switch s {
		case "message_end:assistant":
			assistantEnds++
		case "message_end:tool":
			toolEnds++
		}
	}
	if assistantEnds != 2 || toolEnds != 1 {
		t.Errorf("message_end counts: assistant=%d tool=%d, want 2 and 1", assistantEnds, toolEnds)
	}
}

func TestAgent_NoTools_StraightAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"42"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
	})

	var text string
	var end AgentEnd
	for ev := range ag.Prompt(context.Background(), "what is 6*7?") {
		switch e := ev.(type) {
		case MessageDelta:
			text += e.Delta
		case AgentEnd:
			end = e
		}
	}
	if text != "42" {
		t.Errorf("text = %q", text)
	}
	if end.Turns != 1 || end.ToolCalls != 0 {
		t.Errorf("Turns=%d ToolCalls=%d", end.Turns, end.ToolCalls)
	}
}

// TestAgent_PreservesReasoningContentOnToolCallTurns is the wire-level
// pin for the V4 thinking-mode contract: when a prior assistant turn
// performed tool_calls, its reasoning_content MUST be replayed in
// subsequent requests (api-docs.deepseek.com/guides/thinking_mode).
// Stripping it would yield a 400 from DeepSeek and brick the session.
//
// The test runs a two-request agent loop. Request 1 returns a
// tool_call + reasoning_content; the agent dispatches the (stub) tool
// and immediately fires Request 2. Request 2's body is captured and
// the prior assistant message inside its `messages` array is asserted
// to STILL carry reasoning_content. Companion check: an earlier
// assistant turn WITHOUT tool_calls in the same history (we inject
// one via InitialMessages) has its reasoning_content STRIPPED, since
// the contract only protects tool-call turns.
func TestAgent_PreservesReasoningContentOnToolCallTurns(t *testing.T) {
	var (
		callCount int32
		req2Body  struct {
			Messages []deepseek.Message `json:"messages"`
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 2 {
			_ = json.NewDecoder(r.Body).Decode(&req2Body)
		}
		switch n {
		case 1:
			// Stream a tool_call delta WITH reasoning_content interleaved.
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"I should read the file."}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"stub","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		default:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		}
	}))
	defer srv.Close()

	reg := tools.New().Add(&stubTool{name: "stub", schema: `{"type":"object"}`, reply: "ok"})
	ag, _ := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV4Pro,
		Tools:  reg,
		InitialMessages: []deepseek.Message{
			// Plain assistant turn (no tool_calls) with reasoning — should
			// be STRIPPED on the wire. Pins the negative case.
			{Role: deepseek.RoleUser, Content: "earlier prompt"},
			{Role: deepseek.RoleAssistant, Content: "earlier answer",
				ReasoningContent: "earlier CoT (drop me)"},
		},
	})

	for range ag.Prompt(context.Background(), "do it") {
	}

	if atomic.LoadInt32(&callCount) != 2 {
		t.Fatalf("expected 2 backend calls (turn 1 → tool, turn 2 → final), got %d", callCount)
	}

	// Locate the two assistant turns in req2Body.Messages and check both contracts.
	var (
		toolCallAssistant *deepseek.Message
		plainAssistant    *deepseek.Message
	)
	for i := range req2Body.Messages {
		m := &req2Body.Messages[i]
		if m.Role != deepseek.RoleAssistant {
			continue
		}
		if len(m.ToolCalls) > 0 {
			toolCallAssistant = m
		} else {
			plainAssistant = m
		}
	}

	if toolCallAssistant == nil {
		t.Fatalf("turn-2 request body missing the tool-call assistant turn; got messages=%+v", req2Body.Messages)
	}
	if toolCallAssistant.ReasoningContent != "I should read the file." {
		t.Errorf("tool-call assistant turn LOST reasoning_content on resend — would 400 from DeepSeek; got %q",
			toolCallAssistant.ReasoningContent)
	}

	if plainAssistant == nil {
		t.Fatalf("turn-2 request body missing the plain assistant turn from InitialMessages; got messages=%+v", req2Body.Messages)
	}
	if plainAssistant.ReasoningContent != "" {
		t.Errorf("plain assistant turn (no tool_calls) should have reasoning_content stripped to save tokens; got %q",
			plainAssistant.ReasoningContent)
	}
}

// TestAgent_ThinkingParamForReasoningModels verifies the agent sends
// {"thinking":{"type":"enabled"}} for reasoning models and omits it
// for fast-chat models. This is the wire-level pin for the V4 routing
// fix — without it, deepseek-reasoner silently falls back to V4-Flash
// (the bug that motivated pkg/deepseek.ShouldEnableThinking).
func TestAgent_ThinkingParamForReasoningModels(t *testing.T) {
	cases := []struct {
		model       string
		wantEnabled bool
	}{
		{deepseek.ModelReasoner, true},
		{deepseek.ModelV4Pro, true},
		{deepseek.ModelV4Flash, false},
		{deepseek.ModelChat, false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			var captured struct {
				Thinking *deepseek.ThinkingMode `json:"thinking"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&captured)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, strings.Join([]string{
					`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
					``,
					`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
					``,
					`data: [DONE]`,
					``,
				}, "\n"))
			}))
			defer srv.Close()

			ag, _ := New(Config{
				Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
				Model:  c.model,
			})
			for range ag.Prompt(context.Background(), "hi") {
			}

			gotEnabled := captured.Thinking != nil && captured.Thinking.Type == "enabled"
			if gotEnabled != c.wantEnabled {
				t.Errorf("model=%s: thinking enabled = %v (raw=%+v), want %v",
					c.model, gotEnabled, captured.Thinking, c.wantEnabled)
			}
		})
	}
}

// TestBuildToolResultMsg covers the empty-content contract directly.
// The helper exists for exactly this case, so a unit test against it
// (no HTTP, no stream) is the right level of detail to pin the rule:
//
//   - non-empty result → passes through unchanged
//   - empty result + nil error → "(no output)" placeholder
//   - error → "tool error: ..." takes precedence over result
//   - error wins even when the partial result is non-empty
func TestBuildToolResultMsg(t *testing.T) {
	cases := []struct {
		name string
		id   string
		res  string
		err  error
		want string
	}{
		{"normal", "c1", "real content", nil, "real content"},
		{"empty success", "c2", "", nil, emptyToolContentPlaceholder},
		{"error wins over empty result", "c3", "", errors.New("boom"), "tool error: boom"},
		{"error wins over partial result", "c4", "partial", errors.New("boom"), "tool error: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := buildToolResultMsg(c.id, c.res, c.err)
			if msg.Role != deepseek.RoleTool {
				t.Errorf("Role = %q, want %q", msg.Role, deepseek.RoleTool)
			}
			if msg.ToolCallID != c.id {
				t.Errorf("ToolCallID = %q, want %q", msg.ToolCallID, c.id)
			}
			if msg.Content != c.want {
				t.Errorf("Content = %q, want %q", msg.Content, c.want)
			}
		})
	}
}

// TestAgent_EmptyToolResult_WirePresent is the end-to-end regression
// for the "messages[N]: missing field `content`" failure mode we hit
// in the wild: memory_observe returned ("", nil), the agent persisted
// a tool message with Content="", and on the next turn DeepSeek
// rejected the request because omitempty stripped the `content` key
// from the wire body. This test captures the second-turn request and
// asserts the tool message has a `content` field with the placeholder.
func TestAgent_EmptyToolResult_WirePresent(t *testing.T) {
	var (
		callCount     int32
		secondReqBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 2 {
			secondReqBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"memory_observe","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		default:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		}
	}))
	defer srv.Close()

	silent := &stubTool{name: "memory_observe", schema: `{"type":"object"}`, reply: ""}
	reg := tools.New().Add(silent)

	ag, _ := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelChat,
		Tools:  reg,
	})
	for ev := range ag.Prompt(context.Background(), "go") {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}

	// In-memory history must already have the placeholder so a session
	// save/resume round-trip doesn't re-introduce the bug.
	var toolMsgs int
	for _, m := range ag.Messages() {
		if m.Role == deepseek.RoleTool {
			toolMsgs++
			if m.Content == "" {
				t.Errorf("in-memory tool message has empty Content")
			}
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("expected exactly 1 tool message in history, got %d", toolMsgs)
	}

	// The wire body for turn 2 must contain a `content` field on the
	// tool message. We do a structural decode rather than a substring
	// match so a future Message field reorder can't false-pass.
	var body struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(secondReqBody, &body); err != nil {
		t.Fatalf("decode 2nd req body: %v\n%s", err, secondReqBody)
	}
	var sawToolWithContent bool
	for _, m := range body.Messages {
		role, _ := m["role"]
		if string(role) != `"tool"` {
			continue
		}
		content, present := m["content"]
		if !present {
			t.Errorf("tool message on the wire is missing `content` field: %+v", m)
			continue
		}
		if len(content) == 0 || string(content) == `""` {
			t.Errorf("tool message `content` is empty on the wire: %s", content)
			continue
		}
		sawToolWithContent = true
	}
	if !sawToolWithContent {
		t.Errorf("no tool-role message with non-empty content in 2nd request body")
	}
}

// TestAgent_EffortOverridesThinking covers the /effort wire contract:
//   - Effort="" sends no reasoning_effort and only enables Thinking when
//     ShouldEnableThinking(Model) returns true (the old behaviour).
//   - Effort="high"/"max" forces Thinking on AND sets reasoning_effort —
//     even on V4-Flash, which is normally non-reasoning. The explicit
//     user intent ("I want this turn to think harder") trumps the
//     model's stock default.
//   - SetEffort changes the value visible on the very next prompt,
//     without rebuilding the agent.
func TestAgent_EffortOverridesThinking(t *testing.T) {
	cases := []struct {
		name              string
		model             string
		effort            string
		wantThinking      bool
		wantReasoningSent string // "" means field absent
	}{
		{"empty effort on flash → no thinking", deepseek.ModelV4Flash, "", false, ""},
		{"empty effort on pro → thinking on, no effort", deepseek.ModelV4Pro, "", true, ""},
		{"high on flash → thinking on, effort=high", deepseek.ModelV4Flash, "high", true, "high"},
		{"max on flash → thinking on, effort=max", deepseek.ModelV4Flash, "max", true, "max"},
		{"max on pro → thinking on, effort=max", deepseek.ModelV4Pro, "max", true, "max"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var captured struct {
				Thinking        *deepseek.ThinkingMode `json:"thinking"`
				ReasoningEffort string                 `json:"reasoning_effort"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&captured)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, strings.Join([]string{
					`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
					``,
					`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
					``,
					`data: [DONE]`,
					``,
				}, "\n"))
			}))
			defer srv.Close()

			ag, _ := New(Config{
				Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
				Model:  c.model,
				Effort: c.effort,
			})
			for range ag.Prompt(context.Background(), "hi") {
			}

			gotThinking := captured.Thinking != nil && captured.Thinking.Type == "enabled"
			if gotThinking != c.wantThinking {
				t.Errorf("thinking enabled = %v, want %v (Thinking=%+v)",
					gotThinking, c.wantThinking, captured.Thinking)
			}
			if captured.ReasoningEffort != c.wantReasoningSent {
				t.Errorf("reasoning_effort = %q, want %q",
					captured.ReasoningEffort, c.wantReasoningSent)
			}
		})
	}
}

// TestAgent_SetEffortVisibleNextPrompt pins that flipping effort
// between turns takes effect immediately (no rebuild needed). Without
// this, /effort would feel "sticky" — the user toggles max, sends a
// prompt, and the agent quietly keeps running at the prior level.
func TestAgent_SetEffortVisibleNextPrompt(t *testing.T) {
	var lastEffort string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastEffort = body.ReasoningEffort
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV4Flash,
	})
	for range ag.Prompt(context.Background(), "hi") {
	}
	if lastEffort != "" {
		t.Errorf("turn 1: expected no effort, got %q", lastEffort)
	}

	ag.SetEffort("max")
	if got := ag.Effort(); got != "max" {
		t.Errorf("Effort() after SetEffort: got %q, want %q", got, "max")
	}
	for range ag.Prompt(context.Background(), "again") {
	}
	if lastEffort != "max" {
		t.Errorf("turn 2: expected effort=max, got %q", lastEffort)
	}
}

func TestAgent_Reset_PreservesSystemDropsSystemInHistory(t *testing.T) {
	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		SystemPrompt: "you are seek",
	})
	// Seed the agent with messages as if a few turns ran.
	ag.messages = append(ag.messages,
		deepseek.Message{Role: deepseek.RoleUser, Content: "first"},
		deepseek.Message{Role: deepseek.RoleAssistant, Content: "reply"},
	)

	ag.Reset([]deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "should-be-dropped"},
		{Role: deepseek.RoleUser, Content: "summary"},
		{Role: deepseek.RoleAssistant, Content: "ack"},
	})

	got := ag.Messages()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (system + 2)", len(got))
	}
	if got[0].Role != deepseek.RoleSystem || got[0].Content != "you are seek" {
		t.Errorf("system prompt not preserved: %+v", got[0])
	}
	if got[1].Content != "summary" || got[2].Content != "ack" {
		t.Errorf("history mismatch: %+v", got)
	}
}

func TestAgent_Reset_EmptyHistoryLeavesJustSystem(t *testing.T) {
	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		SystemPrompt: "sys",
	})
	ag.messages = append(ag.messages, deepseek.Message{Role: deepseek.RoleUser, Content: "x"})
	ag.Reset(nil)
	got := ag.Messages()
	if len(got) != 1 || got[0].Role != deepseek.RoleSystem {
		t.Errorf("expected [system], got %+v", got)
	}
}

func TestAgent_Summarise_ReturnsContentDoesNotMutateHistory(t *testing.T) {
	// Non-streaming Chat endpoint returns one assistant message.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request body has both the real history AND the appended
		// summariser user turn — i.e. Summarise didn't accidentally
		// truncate context before asking.
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "earlier work") {
			t.Errorf("request body missing seeded history: %s", string(body))
		}
		if !strings.Contains(string(body), "Summarise the conversation") {
			t.Errorf("request body missing summariser prompt: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id":"x","model":"deepseek-chat",
			"choices":[{"index":0,"message":{"role":"assistant","content":"## briefing\n- goal: X"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":50,"completion_tokens":12,"total_tokens":62}
		}`)
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
	})
	ag.messages = append(ag.messages,
		deepseek.Message{Role: deepseek.RoleUser, Content: "do earlier work"},
		deepseek.Message{Role: deepseek.RoleAssistant, Content: "ok"},
	)
	before := len(ag.messages)

	summary, usage, err := ag.Summarise(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "briefing") {
		t.Errorf("summary = %q", summary)
	}
	if usage.PromptTokens != 50 || usage.CompletionTokens != 12 {
		t.Errorf("usage = %+v", usage)
	}
	if len(ag.messages) != before {
		t.Errorf("Summarise mutated history: len %d → %d", before, len(ag.messages))
	}
}

// TestAgent_CancelDuringToolCallStreamDoesNotPoisonHistory guards the
// "An assistant message with 'tool_calls' must be followed by tool
// messages" regression. The server streams an assistant message that
// is mid-way through emitting a tool_call delta; the test cancels
// ctx before [DONE] arrives. We then verify that:
//
//   - the agent's history does NOT end with a tool_calls-bearing
//     assistant message (which would orphan tool_call_ids); and
//   - a follow-up Prompt against a normal server succeeds, proving
//     the session is still usable.
func TestAgent_CancelDuringToolCallStreamDoesNotPoisonHistory(t *testing.T) {
	// First server: streams a tool_call delta then blocks forever.
	// The test cancels ctx to trigger the cleanup path.
	firstBlock := make(chan struct{})
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"think","arguments":"{\"task\":\""}}]}}]}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Hold the connection open until the test cancels.
		select {
		case <-firstBlock:
		case <-r.Context().Done():
		}
	}))
	defer first.Close()
	defer close(firstBlock)

	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(first.URL)),
		SystemPrompt: "sys",
	})

	ctx, cancel := context.WithCancel(context.Background())
	stream := ag.Prompt(ctx, "do something that triggers a tool call")

	// Wait until we see the tool-call delta, then cancel — that's
	// the exact race the bug fired in.
	gotToolCallDelta := make(chan struct{})
	go func() {
		for ev := range stream {
			if _, ok := ev.(MessageStart); ok {
				close(gotToolCallDelta)
				return
			}
		}
	}()
	select {
	case <-gotToolCallDelta:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("never saw MessageStart from blocking server")
	}
	cancel()

	// Drain the channel.
	for range stream {
	}

	hist := ag.Messages()
	if n := len(hist); n == 0 {
		t.Fatalf("history empty after cancel")
	}
	last := hist[len(hist)-1]
	if last.Role == deepseek.RoleAssistant && len(last.ToolCalls) > 0 {
		t.Errorf("history left with orphan tool_calls assistant message: %+v", last)
	}
	// The user message MAY or MAY NOT be in history depending on
	// where exactly the cancel landed; the load-bearing assertion is
	// just "no orphan tool_calls". If the user message is present
	// without an assistant reply, that's fine — the next turn will
	// send it together with a new prompt.

	// Second server: a normal happy-path turn. If the history above
	// is well-formed, this Prompt should succeed without DeepSeek
	// rejecting it.
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := io.ReadAll(r.Body)
		// Sanity: the request body MUST NOT contain a dangling
		// assistant tool_calls message. Test the literal field name
		// — that's what DeepSeek would reject on.
		if strings.Contains(string(body), `"role":"assistant"`) &&
			strings.Contains(string(body), `"tool_calls"`) {
			t.Errorf("follow-up request includes orphan assistant tool_calls: %s", string(body))
		}
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
	defer second.Close()

	// Point the client at the second server. We do this by
	// rebuilding the agent with the same history — same shape as
	// what would happen if the user saved + resumed.
	ag2, _ := New(Config{
		Client:          deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(second.URL)),
		SystemPrompt:    "sys",
		InitialMessages: hist,
	})

	var sawError bool
	for ev := range ag2.Prompt(context.Background(), "still there?") {
		if _, ok := ev.(ErrorEvent); ok {
			sawError = true
		}
	}
	if sawError {
		t.Errorf("follow-up Prompt errored after cancel cleanup")
	}
}

// streamingStub is a Tool that also implements tools.StreamingTool —
// used to verify the agent routes deltas through as ToolDelta events.
type streamingStub struct {
	stubTool
	chunks []tools.StreamDelta
}

func (s *streamingStub) ExecuteStream(_ context.Context, _ json.RawMessage, push func(tools.StreamDelta) error) (string, error) {
	for _, d := range s.chunks {
		if err := push(d); err != nil {
			return "", err
		}
	}
	return "final result", nil
}

// TestAgent_StreamingTool_RoutesToolDeltaEvents pins the contract
// dispatchTool relies on: when a tool implements tools.StreamingTool,
// the agent must surface its intermediate output as ToolDelta events
// in-order between the matching ToolExecStart and ToolExecEnd.
func TestAgent_StreamingTool_RoutesToolDeltaEvents(t *testing.T) {
	// Server: emits one tool_call delta for "stream_me" then closes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_z","type":"function","function":{"name":"stream_me","arguments":"{}"}}]}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	streamer := &streamingStub{
		stubTool: stubTool{name: "stream_me", schema: `{}`},
		chunks: []tools.StreamDelta{
			{Delta: "thinking...", Reasoning: true},
			{Delta: " more thoughts", Reasoning: true},
			{Delta: "the answer", Reasoning: false},
		},
	}
	reg := tools.New().Add(streamer)

	ag, _ := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Tools:    reg,
		MaxTurns: 1,
	})

	var (
		seen     []string // event-type names in order
		deltas   []ToolDelta
		startCID string
		endCID   string
	)
	for ev := range ag.Prompt(context.Background(), "go") {
		switch e := ev.(type) {
		case ToolExecStart:
			seen = append(seen, "start")
			startCID = e.CallID
		case ToolDelta:
			seen = append(seen, "delta")
			deltas = append(deltas, e)
		case ToolExecEnd:
			seen = append(seen, "end")
			endCID = e.CallID
		}
	}

	// Event order must be start → delta* → end (no reordering across
	// goroutine boundaries — the pump goroutine in dispatchTool MUST
	// fully drain before ToolExecEnd lands).
	if got := strings.Join(seen, ","); got != "start,delta,delta,delta,end" {
		t.Errorf("event order = %q, want start,delta×3,end", got)
	}
	if startCID != "call_z" || endCID != "call_z" {
		t.Errorf("CallIDs mismatched: start=%q end=%q", startCID, endCID)
	}
	if len(deltas) != 3 ||
		deltas[0].Delta != "thinking..." || !deltas[0].Reasoning ||
		deltas[1].Delta != " more thoughts" || !deltas[1].Reasoning ||
		deltas[2].Delta != "the answer" || deltas[2].Reasoning {
		t.Errorf("delta sequence wrong: %+v", deltas)
	}
	for _, d := range deltas {
		if d.CallID != "call_z" {
			t.Errorf("delta CallID = %q, want call_z", d.CallID)
		}
		if d.Name != "stream_me" {
			t.Errorf("delta Name = %q, want stream_me", d.Name)
		}
	}
}

// --- Boundary stress: paths to corrupt state -----------------------
//
// The orphan-tool_calls regression (commit 986a485) lived under green
// CI for weeks because the suite only covered the happy-path stream
// shape. The tests below exercise every way runTurn can return an
// assistant message whose finish_reason disagrees with its tool_calls
// — ctx-cancel was just one of them. Each test asserts the same
// invariant: history MUST NOT end with an assistant message that
// carries unverified tool_calls, and a follow-up Prompt against the
// same agent must succeed.

// agentTurnResult drains every event off an agent.Prompt channel and
// returns a categorized summary. Pulled out so the assertions in each
// test stay readable.
type agentTurnResult struct {
	events    []string // event-type names in order
	errors    []error
	toolEnds  []ToolExecEnd
	assistant deepseek.Message // last MessageEnd carrying a non-empty assistant
}

func drainAgent(stream <-chan Event) agentTurnResult {
	var r agentTurnResult
	for ev := range stream {
		switch e := ev.(type) {
		case AgentStart:
			r.events = append(r.events, "AgentStart")
		case TurnStart:
			r.events = append(r.events, "TurnStart")
		case MessageStart:
			r.events = append(r.events, "MessageStart")
		case MessageDelta:
			r.events = append(r.events, "MessageDelta")
		case MessageEnd:
			r.events = append(r.events, "MessageEnd")
			if e.Message.Role == deepseek.RoleAssistant {
				r.assistant = e.Message
			}
		case ToolExecStart:
			r.events = append(r.events, "ToolExecStart")
		case ToolExecEnd:
			r.events = append(r.events, "ToolExecEnd")
			r.toolEnds = append(r.toolEnds, e)
		case ToolDelta:
			r.events = append(r.events, "ToolDelta")
		case TurnEnd:
			r.events = append(r.events, "TurnEnd")
		case AgentEnd:
			r.events = append(r.events, "AgentEnd")
		case ErrorEvent:
			r.events = append(r.events, "ErrorEvent")
			r.errors = append(r.errors, e.Err)
		}
	}
	return r
}

// assertNoOrphanToolCalls is the load-bearing assertion shared by
// every boundary-stress test: the agent's history must NEVER end with
// an assistant message that has tool_calls but no matching tool
// result messages after it. That's exactly the shape DeepSeek rejects.
func assertNoOrphanToolCalls(t *testing.T, hist []deepseek.Message) {
	t.Helper()
	if len(hist) == 0 {
		return
	}
	for i := len(hist) - 1; i >= 0; i-- {
		m := hist[i]
		if m.Role != deepseek.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		// Found an assistant message with tool_calls. Every ID must
		// be matched by a tool message later in the slice.
		needed := map[string]bool{}
		for _, tc := range m.ToolCalls {
			needed[tc.ID] = true
		}
		for j := i + 1; j < len(hist); j++ {
			if hist[j].Role == deepseek.RoleTool {
				delete(needed, hist[j].ToolCallID)
			}
		}
		if len(needed) > 0 {
			t.Errorf("orphan tool_calls in history at index %d: unmatched IDs %v\nhistory: %+v",
				i, needed, hist)
		}
		return
	}
}

// happyChatServer emits a single content delta + finish=stop. Used to
// confirm that an agent whose history survived a corrupt turn can
// still run a follow-up turn without being rejected by the API. Also
// asserts on the request body that no orphan tool_calls leaked.
func happyChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"role":"assistant"`) &&
			strings.Contains(string(body), `"tool_calls"`) {
			t.Errorf("follow-up request leaked orphan assistant tool_calls: %s", string(body))
		}
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
}

// TestAgent_StreamTruncatedMidToolCall is the non-ctx-cancel sibling
// of 986a485's regression test. The server flushes a tool_call delta
// and then closes the connection — no [DONE], no finish_reason, no
// user cancellation. Pre-fix, the agent appended the orphan assistant
// and the session was bricked.
func TestAgent_StreamTruncatedMidToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_t","type":"function","function":{"name":"think","arguments":"{\"task\":\"x\"}"}}]}}]}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Close the response without sending finish_reason or [DONE].
		// httptest closes when the handler returns.
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
	})
	res := drainAgent(ag.Prompt(context.Background(), "trigger a tool"))
	if len(res.errors) == 0 {
		t.Errorf("expected ErrorEvent for truncated stream; events=%v", res.events)
	}
	assertNoOrphanToolCalls(t, ag.Messages())

	// Follow-up: the agent must still be usable. Build a clean server
	// and prove the request body contains no orphan tool_calls.
	second := happyChatServer(t)
	defer second.Close()
	ag2, _ := New(Config{
		Client:          deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(second.URL)),
		SystemPrompt:    "sys",
		InitialMessages: ag.Messages(),
	})
	res2 := drainAgent(ag2.Prompt(context.Background(), "retry"))
	if len(res2.errors) > 0 {
		t.Errorf("follow-up Prompt errored: %v", res2.errors)
	}
}

// TestAgent_FinishReasonMismatch_DropsOrphanToolCalls covers the
// defensive case where the server emits tool_calls AND a non-matching
// finish_reason in the same stream — should never happen per spec
// but we don't trust the spec to hold across proxies / SDKs.
func TestAgent_FinishReasonMismatch_DropsOrphanToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			// Tool-call delta…
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_m","type":"function","function":{"name":"think","arguments":"{}"}}]}}]}`,
			``,
			// …but finish_reason says "stop", not "tool_calls".
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
	})
	res := drainAgent(ag.Prompt(context.Background(), "go"))
	if len(res.errors) == 0 {
		t.Errorf("expected ErrorEvent for finish=stop with tool_calls present")
	}
	// The error message should name what went wrong so an operator
	// can tell at a glance from the TUI scrollback.
	if len(res.errors) > 0 {
		msg := res.errors[0].Error()
		if !strings.Contains(msg, "tool_call") || !strings.Contains(msg, "stop") {
			t.Errorf("error message lacks diagnostic detail: %q", msg)
		}
	}
	assertNoOrphanToolCalls(t, ag.Messages())
}

// TestAgent_DecodeErrorMidStream_DropsTurn pins behaviour for the
// SSE-decode-failure path. ChatStream surfaces finish_reason like
// "decode_error:..." in that case, which the new invariant treats as
// "anything that isn't 'tool_calls' means drop the tool_calls".
func TestAgent_DecodeErrorMidStream_DropsTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Valid tool_call delta first…
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_d","type":"function","function":{"name":"think","arguments":"{}"}}]}}]}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// …then a chunk whose payload is malformed JSON. ChatStream
		// reports finish_reason="decode_error:..." for this.
		io.WriteString(w, "data: {not valid json here\n\n")
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
	})
	res := drainAgent(ag.Prompt(context.Background(), "go"))
	if len(res.errors) == 0 {
		t.Errorf("expected ErrorEvent for decode error mid-stream")
	}
	assertNoOrphanToolCalls(t, ag.Messages())
}

// TestAgent_EmptyChoicesUsageOnly verifies that a stream yielding no
// content and no tool_calls produces an error. The empty assistant message
// would otherwise poison the next request: on the following turn,
// StripReasoningContent removes the (absent) reasoning_content, leaving
// role=assistant with no content and no tool_calls, which DeepSeek
// rejects with "content or tool_calls must be set".
func TestAgent_EmptyChoicesUsageOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
	})
	res := drainAgent(ag.Prompt(context.Background(), "hello"))
	if len(res.errors) == 0 {
		t.Fatal("expected error for empty response (no content, no tool_calls), got none")
	}
	if !strings.Contains(res.errors[0].Error(), "empty response") {
		t.Errorf("error should mention empty response, got: %v", res.errors[0])
	}
	// No orphan tool_calls — the empty message is NOT committed.
	assertNoOrphanToolCalls(t, ag.Messages())
}

// TestAgent_MalformedToolArgs_RecordedAsToolError covers the
// not-the-orphan but adjacent case: the stream completes cleanly with
// finish=tool_calls, but the arguments string isn't valid JSON. The
// tool's Execute returns an unmarshal error; the agent must record it
// as a tool result message so the next turn carries it back to the
// model, which can then correct itself.
func TestAgent_MalformedToolArgs_RecordedAsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_bad","type":"function","function":{"name":"strict","arguments":"{not-json"}}]}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	// A strict tool that fails json.Unmarshal on malformed input —
	// the realistic shape of any production tool.
	strict := &strictArgsTool{name: "strict"}
	reg := tools.New().Add(strict)

	ag, _ := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Tools:    reg,
		MaxTurns: 1,
	})
	res := drainAgent(ag.Prompt(context.Background(), "use the tool"))

	// No ErrorEvent at the agent level — tool errors aren't agent
	// errors. They flow back as tool result messages.
	if len(res.errors) > 0 {
		t.Errorf("agent-level error fired for tool error (should be a tool result instead): %v", res.errors)
	}
	if len(res.toolEnds) != 1 || res.toolEnds[0].Err == nil {
		t.Fatalf("expected one ToolExecEnd with a non-nil error, got %+v", res.toolEnds)
	}
	hist := ag.Messages()
	last := hist[len(hist)-1]
	if last.Role != deepseek.RoleTool {
		t.Fatalf("last message should be tool result, got role=%s", last.Role)
	}
	if !strings.Contains(last.Content, "tool error") {
		t.Errorf("tool result missing error marker: %q", last.Content)
	}
	// History shape MUST be API-valid: every tool_call_id matched.
	assertNoOrphanToolCalls(t, hist)
}

// strictArgsTool is a Tool that returns a JSON-unmarshal error on
// malformed input. Modelled on a typical production tool —
// internal/tools/listdir, read, etc. all do this.
type strictArgsTool struct {
	name string
}

func (t *strictArgsTool) Name() string            { return t.name }
func (t *strictArgsTool) Description() string     { return "strict" }
func (t *strictArgsTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *strictArgsTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return "ok", nil
}

// TestAgent_MultiTurn_RoundTripsCleanHistory exercises the resume
// path the user actually relies on: run a turn that includes a tool
// call, take the resulting history, seed a fresh agent with it
// (mimicking --resume), and run another turn. The follow-up request
// must be API-valid (no orphan tool_calls).
//
// This is the load-bearing integration scenario: if any of the
// earlier tests in this file regress, this one catches the end-to-end
// effect for the user.
func TestAgent_MultiTurn_RoundTripsCleanHistory(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"role":"assistant"`) &&
			strings.Contains(string(body), `"tool_calls"`) {
			// Body contains an assistant tool_calls message. Make
			// sure each tool_call_id is matched by a tool message
			// somewhere later in the JSON. The simplest sufficient
			// check is "if you see call_X in tool_calls, also see it
			// in tool_call_id". Crude but effective.
			for _, id := range []string{"call_1"} {
				if strings.Count(string(body), id) < 2 {
					t.Errorf("turn %d body has unbalanced tool_call_id %s: %s",
						calls, id, string(body))
				}
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			// First turn: emit a tool_call.
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"strict","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		default:
			// Second turn (after tool result fed back): final answer.
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"all done"}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		}
	}))
	defer srv.Close()

	reg := tools.New().Add(&strictArgsTool{name: "strict"})

	// First agent runs the full tool-using turn.
	ag1, _ := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
		Tools:        reg,
	})
	res1 := drainAgent(ag1.Prompt(context.Background(), "use the tool"))
	if len(res1.errors) > 0 {
		t.Fatalf("first turn errored: %v", res1.errors)
	}
	hist := ag1.Messages()
	assertNoOrphanToolCalls(t, hist)

	// Second agent is constructed from the first's history — same
	// thing that --resume / --continue does in cmd/seek.
	ag2, _ := New(Config{
		Client:          deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt:    "sys",
		Tools:           reg,
		InitialMessages: hist,
	})
	res2 := drainAgent(ag2.Prompt(context.Background(), "anything else?"))
	if len(res2.errors) > 0 {
		t.Fatalf("resumed turn errored: %v", res2.errors)
	}
	assertNoOrphanToolCalls(t, ag2.Messages())
}

func TestAgent_UnknownToolErrorsCleanly(t *testing.T) {
	// LLM asks to call a tool we didn't register.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"nope","arguments":"{}"}}]}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	// Register a different tool so Tools != nil.
	reg := tools.New().Add(&stubTool{name: "other", schema: `{}`, reply: "x"})

	ag, _ := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Tools:  reg,
		// Bound the loop so the assistant doesn't keep getting hit.
		MaxTurns: 1,
	})

	var toolEnd ToolExecEnd
	for ev := range ag.Prompt(context.Background(), "x") {
		if e, ok := ev.(ToolExecEnd); ok {
			toolEnd = e
		}
	}
	if toolEnd.Err == nil {
		t.Fatal("expected unknown-tool error")
	}
	if !strings.Contains(toolEnd.Err.Error(), "unknown tool") {
		t.Errorf("err = %v, want unknown tool", toolEnd.Err)
	}
}

// TestAgent_ParallelReadOnlyDispatch proves that when the LLM returns
// multiple read-only tool calls in one turn the agent dispatches them
// concurrently. With toolCount tools each sleeping toolLatency the
// total elapsed time must be less than toolCount×toolLatency — a bound
// that would be violated if dispatch were sequential.
func TestAgent_ParallelReadOnlyDispatch(t *testing.T) {
	const (
		toolCount   = 3
		toolLatency = 50 * time.Millisecond
	)

	// Backend turn 1: emit three slow_read tool calls.
	// Turn 2: final text answer.
	var backendCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		n := backendCalls.Add(1)
		switch n {
		case 1:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_p1","type":"function","function":{"name":"slow_read","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_p2","type":"function","function":{"name":"slow_read","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_p3","type":"function","function":{"name":"slow_read","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		default:
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
		}
	}))
	defer srv.Close()

	tool := &slowReadTool{latency: toolLatency}
	reg := tools.New().Add(tool)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Tools:  reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	res := drainAgent(ag.Prompt(context.Background(), "read three files"))
	elapsed := time.Since(start)

	if len(res.errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.errors)
	}

	// All three tools must have been called.
	if got := tool.called.Load(); got != toolCount {
		t.Errorf("tool called %d times, want %d", got, toolCount)
	}

	// Parallel: elapsed must be less than sequential lower bound.
	sequential := time.Duration(toolCount) * toolLatency
	if elapsed >= sequential {
		t.Errorf("dispatch appears sequential: elapsed=%v >= sequential_bound=%v", elapsed, sequential)
	}

	// Tool results must appear in the original call order (call_p1, p2, p3).
	hist := ag.Messages()
	assertNoOrphanToolCalls(t, hist)
	var toolIDs []string
	for _, m := range hist {
		if m.Role == deepseek.RoleTool {
			toolIDs = append(toolIDs, m.ToolCallID)
		}
	}
	want := []string{"call_p1", "call_p2", "call_p3"}
	if len(toolIDs) != len(want) {
		t.Fatalf("tool result count = %d, want %d", len(toolIDs), len(want))
	}
	for i, id := range toolIDs {
		if id != want[i] {
			t.Errorf("tool result[%d] = %q, want %q", i, id, want[i])
		}
	}
}
