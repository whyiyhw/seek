package agent

// Mid-turn injection tests (inject.go). The invariants under test are
// the ones whose violation would be expensive:
//
//  1. A queued message lands BETWEEN two model calls of the same round,
//     after the tool batch's results are already in history — never
//     before them (that would orphan assistant tool_calls).
//  2. Nothing is injected before the user's own turn (turn 0).
//  3. An injection racing the model's decision to stop turns "about to
//     return" into one more turn instead of being dropped.
//  4. QueueUserMessage outside a round reports false (never leaks into
//     the next round), and Esc's cancel clears what was queued.
//  5. Caps are enforced at queue time, not by silent truncation.
//
// Timing note: the queue is filled from an OnPostTurn observer rather
// than from the test's event loop. Observed events are consumed
// asynchronously (the channel is buffered), so queueing "when
// ToolExecEnd arrives" races the producer and would make these tests
// flaky in a way that says nothing about the feature. OnPostTurn runs ON
// the agent goroutine, synchronously at exactly the boundary the
// feature targets.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// queueOnPostTurn is a test observer that calls QueueUserMessage once,
// on the given turn index. Deterministic: it runs on the agent's own
// goroutine, synchronously at the turn boundary.
type queueOnPostTurn struct {
	at   int
	text string
	ag   *Agent
	ok   bool // reported result of the one call
	done bool
}

func (q *queueOnPostTurn) OnPostTurn(_ context.Context, ev hooks.PostTurnEvent) {
	if q.done || ev.Index != q.at {
		return
	}
	q.done = true
	q.ok = q.ag.QueueUserMessage(q.text)
}

// TestInjectionLandsAtToolBoundary is the load-bearing shape test: the
// injected text must appear in the SECOND request's messages, and must
// appear AFTER the tool result message (otherwise the request would
// carry assistant(tool_calls) not immediately followed by its tool
// result, which DeepSeek rejects).
func TestInjectionLandsAtToolBoundary(t *testing.T) {
	t.Parallel()

	var (
		calls     atomic.Int32
		secondReq struct {
			Messages []deepseek.Message `json:"messages"`
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 2 {
			_ = json.NewDecoder(r.Body).Decode(&secondReq)
		}
		if n == 1 {
			io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
			return
		}
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	hreg := hooks.NewRegistry()
	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
		Tools:  tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "file body"}),
		Hooks:  hreg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q := &queueOnPostTurn{at: 0, text: "no wait, use the other file", ag: ag}
	hreg.Register(q)

	var injected []InjectedMessage
	for ev := range ag.Prompt(context.Background(), "start") {
		if e, ok := ev.(InjectedMessage); ok {
			injected = append(injected, e)
		}
	}

	if !q.ok {
		t.Fatal("QueueUserMessage reported false from OnPostTurn(0)")
	}
	if len(injected) != 1 {
		t.Fatalf("InjectedMessage events = %d, want 1", len(injected))
	}
	if injected[0].Index != 1 {
		t.Errorf("injected before turn %d, want 1", injected[0].Index)
	}
	if !strings.Contains(injected[0].Message.Content, "no wait, use the other file") {
		t.Errorf("committed content = %q, want the injected text", injected[0].Message.Content)
	}

	// Wire-level: the tool result must precede the injected user message.
	msgs := secondReq.Messages
	toolIdx, injIdx := -1, -1
	for i, m := range msgs {
		if m.Role == deepseek.RoleTool {
			toolIdx = i
		}
		if m.Role == deepseek.RoleUser && strings.Contains(m.Content, "no wait, use the other file") {
			injIdx = i
		}
	}
	if toolIdx < 0 || injIdx < 0 {
		t.Fatalf("second request missing tool result (%d) or injected message (%d): %+v", toolIdx, injIdx, msgs)
	}
	if injIdx < toolIdx {
		t.Errorf("injected message at %d precedes tool result at %d — orphan tool_calls would 400", injIdx, toolIdx)
	}
	if injIdx != len(msgs)-1 {
		t.Errorf("injected message at %d, want last (%d)", injIdx, len(msgs)-1)
	}
}

// TestInjectionNotBeforeTurnZero: the user's own prompt owns turn 0.
// An injection queued before any tool batch must not preempt it.
func TestInjectionNotBeforeTurnZero(t *testing.T) {
	t.Parallel()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Not in flight yet: must reject rather than queue for later.
	if ag.QueueUserMessage("early") {
		t.Error("QueueUserMessage accepted text outside a round")
	}
	// Pre-seeded queue (simulating a race that slipped in before the
	// flag was set) must be wiped by beginTurnSession, not injected.
	ag.injectQueue = []string{"stale"}
	ag.beginTurnSession()
	ag.injectMu.Lock()
	left := len(ag.injectQueue)
	ag.injectMu.Unlock()
	if left != 0 {
		t.Errorf("beginTurnSession left %d entries queued, want 0", left)
	}
	ag.endTurnSession()
}

// TestInjectionSurvivesModelStop: the model wanted to settle (finish=
// stop, no tool calls); an injection queued while that turn streamed
// must turn the settle into one more turn, not be dropped.
func TestInjectionSurvivesModelStop(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		text := "first answer"
		if n > 1 {
			text = "second answer"
		}
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"` + text + `"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	hreg := hooks.NewRegistry()
	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
		Hooks:  hreg,
	})
	if err != nil {
		t.Fatal(err)
	}
	q := &queueOnPostTurn{at: 0, text: "actually do X", ag: ag}
	hreg.Register(q)

	var sawInjected bool
	for ev := range ag.Prompt(context.Background(), "hi") {
		if _, ok := ev.(InjectedMessage); ok {
			sawInjected = true
		}
	}

	if !q.ok {
		t.Fatal("QueueUserMessage reported false during a settling turn")
	}
	if !sawInjected {
		t.Fatal("injection queued during a settling turn was dropped")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("backend calls = %d, want 2 (the injection must earn a turn)", got)
	}
	// The injected message must be in history, followed by the model's
	// answer — i.e. it really was appended, not just reported.
	msgs := ag.Messages()
	var hasInj bool
	for _, m := range msgs {
		if m.Role == deepseek.RoleUser && strings.Contains(m.Content, "actually do X") {
			hasInj = true
		}
	}
	if !hasInj {
		t.Errorf("injected message missing from history: %+v", msgs)
	}
}

// TestQueueUserMessageRejectsOutsideRound pins the false path that the
// TUI depends on for its queuedText fallback.
func TestQueueUserMessageRejectsOutsideRound(t *testing.T) {
	t.Parallel()
	ag, err := New(Config{Client: deepseek.New(deepseek.WithAPIKey("t"))})
	if err != nil {
		t.Fatal(err)
	}
	if ag.QueueUserMessage("x") {
		t.Error("accepted outside a round")
	}
	// nil receiver must not panic (defensive: wiring mistakes happen).
	var nilAg *Agent
	if nilAg.QueueUserMessage("x") {
		t.Error("nil receiver accepted")
	}
}

// TestQueueUserMessageCaps covers both caps: oversized text and too many
// pending messages for one turn.
func TestQueueUserMessageCaps(t *testing.T) {
	t.Parallel()
	ag, err := New(Config{Client: deepseek.New(deepseek.WithAPIKey("t"))})
	if err != nil {
		t.Fatal(err)
	}
	ag.beginTurnSession()
	defer ag.endTurnSession()

	if ag.QueueUserMessage(strings.Repeat("x", maxInjectedBytes+1)) {
		t.Error("accepted a message over maxInjectedBytes")
	}
	if ag.QueueUserMessage("   ") {
		t.Error("accepted whitespace-only text")
	}
	for i := 0; i < maxPendingInjections; i++ {
		if !ag.QueueUserMessage("msg") {
			t.Fatalf("rejected message %d under the cap", i)
		}
	}
	if ag.QueueUserMessage("one too many") {
		t.Errorf("accepted message %d over maxPendingInjections", maxPendingInjections+1)
	}
}

// TestInjectionClearedByCancel: Esc cancels the round; whatever was
// queued must not survive into the next one. This is the "Esc stops
// everything includes latent state" rule the TUI's queue/steer fields
// already follow.
func TestInjectionClearedByCancel(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		<-blocked // hold the stream open until the test cancels
	}))
	defer srv.Close()
	defer close(blocked)

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := ag.Prompt(ctx, "hi")
	// Wait for the round to be visibly in flight before queueing.
	if !ag.QueueUserMessage("should be dropped") {
		t.Fatal("could not queue while in flight")
	}
	cancel()
	for range ch {
		// drain; the round must terminate on ctx cancel
	}
	if ag.QueueUserMessage("late") {
		t.Error("QueueUserMessage accepted after the round ended")
	}
	ag.injectMu.Lock()
	left := len(ag.injectQueue)
	ag.injectMu.Unlock()
	if left != 0 {
		t.Errorf("queue left %d entries after cancel, want 0", left)
	}
}
