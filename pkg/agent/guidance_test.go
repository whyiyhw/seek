package agent

// Tests for the guidance seam (guidance.go). The invariants:
//
//  1. A hint lands at the same legal boundaries user injections do, and
//     only there — turn 0 is exempt, and the tool-batch pairing is
//     untouched.
//  2. A human break-in supersedes a machine hint at the same boundary.
//     The user's text is the more specific instruction; saying both
//     dilutes it.
//  3. The hint is a user-role message at the tail, so the prefix cache
//     keeps hitting (asserted at the wire level in the second request).
//  4. nil Guidance is a no-op, and the loop still runs.
//  5. The event carries Source + Rule so the TUI can render a hint
//     differently from a human break-in and the trace can count rules.

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

// scriptedGuidance speaks on a fixed set of turns, ignoring the brief —
// the seam is what is under test here, not the rules (internal/guidance
// has its own suite).
type scriptedGuidance struct {
	fireOn map[int]bool
	text   string
	rule   string
	seen   []TurnBrief
}

func (s *scriptedGuidance) Advise(b TurnBrief) string {
	s.seen = append(s.seen, b)
	if s.fireOn[b.Index] {
		return s.text
	}
	return ""
}

func (s *scriptedGuidance) LastAdviceRule() string { return s.rule }

// silentGuidance never speaks (the common case).
type silentGuidance struct{ calls int }

func (s *silentGuidance) Advise(TurnBrief) string { s.calls++; return "" }

// guidanceBackend serves one tool-call turn then plain text turns.
func guidanceBackend(t *testing.T, capture *struct {
	Messages []deepseek.Message `json:"messages"`
}) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if n == 2 && capture != nil {
			_ = json.NewDecoder(r.Body).Decode(capture)
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
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestGuidanceLandsAtToolBoundary: the hint appears in the SECOND
// request, after the tool result, last in the slice — the same shape
// contract the user-injection tests pin, because it is the same seam.
func TestGuidanceLandsAtToolBoundary(t *testing.T) {
	t.Parallel()

	var second struct {
		Messages []deepseek.Message `json:"messages"`
	}
	srv, _ := guidanceBackend(t, &second)

	g := &scriptedGuidance{fireOn: map[int]bool{0: true}, text: "you are looping on go build", rule: "retry-loop"}
	ag, err := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:    deepseek.ModelV41Flash,
		Tools:    tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "body"}),
		Hooks:    hooks.NewRegistry(),
		Guidance: g,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var events []InjectedMessage
	for ev := range ag.Prompt(context.Background(), "start") {
		if e, ok := ev.(InjectedMessage); ok {
			events = append(events, e)
		}
	}

	if len(events) != 1 {
		t.Fatalf("InjectedMessage events = %d, want 1: %+v", len(events), events)
	}
	if events[0].Source != InjectedSourceGuidance {
		t.Errorf("source = %q, want guidance", events[0].Source)
	}
	if events[0].Rule != "retry-loop" {
		t.Errorf("rule = %q, want retry-loop (GuidanceNamer pass-through)", events[0].Rule)
	}

	// The brief handed to the rules must describe the settled turn: one
	// tool call, no prose.
	if len(g.seen) == 0 {
		t.Fatal("Guidance was never consulted")
	}
	b := g.seen[0]
	if len(b.Tools) != 1 || b.Tools[0].Name != "read" {
		t.Errorf("brief tools = %+v, want one read call", b.Tools)
	}
	if b.TextChars != 0 {
		t.Errorf("brief TextChars = %d, want 0 (the tool turn said nothing)", b.TextChars)
	}

	// Wire level: guidance is the last message, after the tool result.
	msgs := second.Messages
	toolIdx, guideIdx := -1, -1
	for i, m := range msgs {
		if m.Role == deepseek.RoleTool {
			toolIdx = i
		}
		if m.Role == deepseek.RoleUser && strings.Contains(m.Content, "you are looping on go build") {
			guideIdx = i
		}
	}
	if toolIdx < 0 || guideIdx < 0 {
		t.Fatalf("second request missing tool (%d) or guidance (%d): %+v", toolIdx, guideIdx, msgs)
	}
	if guideIdx < toolIdx {
		t.Errorf("guidance at %d precedes tool result at %d — orphan tool_calls would 400", guideIdx, toolIdx)
	}
	if guideIdx != len(msgs)-1 {
		t.Errorf("guidance at %d, want last (%d)", guideIdx, len(msgs)-1)
	}
	if msgs[guideIdx].Role != deepseek.RoleUser {
		t.Errorf("guidance role = %q, want user (a system note would sit outside the cache-volatile tail)",
			msgs[guideIdx].Role)
	}
}

// TestGuidanceSupersededByUserInjection: when a human break-in lands at
// the same boundary, the hint stays silent. Both are user-role tail
// messages; sending two would dilute the specific one.
func TestGuidanceSupersededByUserInjection(t *testing.T) {
	t.Parallel()

	srv, _ := guidanceBackend(t, nil)
	g := &scriptedGuidance{fireOn: map[int]bool{0: true}, text: "machine hint", rule: "silence-run"}

	hreg := hooks.NewRegistry()
	ag, err := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:    deepseek.ModelV41Flash,
		Tools:    tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "body"}),
		Hooks:    hreg,
		Guidance: g,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Queue from OnPostTurn(0): the same boundary the guidance would use.
	q := &queueOnPostTurn{at: 0, text: "human says stop", ag: ag}
	hreg.Register(q)

	var injected []InjectedMessage
	for ev := range ag.Prompt(context.Background(), "start") {
		if e, ok := ev.(InjectedMessage); ok {
			injected = append(injected, e)
		}
	}

	if len(injected) != 1 {
		t.Fatalf("injected = %d, want exactly 1 (the human's): %+v", len(injected), injected)
	}
	if injected[0].Source != InjectedSourceUser {
		t.Errorf("source = %q, want user — the human wins the boundary", injected[0].Source)
	}
	for _, m := range ag.Messages() {
		if strings.Contains(m.Content, "machine hint") {
			t.Error("guidance was appended despite a human injection at the same boundary")
		}
	}
}

// alwaysGuidance speaks on every consultation — the worst case for the
// turn-extension question.
type alwaysGuidance struct{ calls int }

func (s *alwaysGuidance) Advise(TurnBrief) string { s.calls++; return "keep going" }

// TestGuidanceNeverExtendsTheRound pins the rule the first implementation
// got wrong: a machine hint must not turn "the model decided it is done"
// into another turn. The settle point is excluded for guidance precisely
// because the continuation produces a fresh brief that qualifies again —
// measured as 397 consecutive injections (and MaxTurns exhausted) before
// the fix.
func TestGuidanceNeverExtendsTheRound(t *testing.T) {
	t.Parallel()

	srv, calls := guidanceBackend(t, nil)
	g := &alwaysGuidance{}
	ag, err := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:    deepseek.ModelV41Flash,
		Tools:    tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "body"}),
		Guidance: g,
		MaxTurns: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var turns int
	for ev := range ag.Prompt(context.Background(), "go") {
		if _, ok := ev.(TurnEnd); ok {
			turns++
		}
	}

	// The scripted backend needs exactly two model calls: one tool call,
	// one final answer. A guidance hint may accompany the transition from
	// turn 0 to turn 1, but it must not buy a third call.
	if got := calls.Load(); got != 2 {
		t.Errorf("backend calls = %d, want 2 — guidance extended the round (turns settled: %d)", got, turns)
	}
	if turns != 2 {
		t.Errorf("settled turns = %d, want 2", turns)
	}
}

// TestGuidanceNilIsNoOp: the default configuration must be inert.
func TestGuidanceNilIsNoOp(t *testing.T) {
	t.Parallel()
	srv, calls := guidanceBackend(t, nil)
	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
		Tools:  tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "body"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var injected int
	for ev := range ag.Prompt(context.Background(), "go") {
		if _, ok := ev.(InjectedMessage); ok {
			injected++
		}
	}
	if injected != 0 {
		t.Errorf("nil Guidance produced %d injections", injected)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("backend calls = %d, want 2 (a no-op guidance must not add or drop turns)", got)
	}
}

// TestGuidanceSilentDoesNotChangeTheLoop: a Guidance that never speaks
// must be byte-identical in behaviour to nil.
func TestGuidanceSilentDoesNotChangeTheLoop(t *testing.T) {
	t.Parallel()
	srv, calls := guidanceBackend(t, nil)
	g := &silentGuidance{}
	ag, err := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:    deepseek.ModelV41Flash,
		Tools:    tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "body"}),
		Guidance: g,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ag.Prompt(context.Background(), "go") {
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("backend calls = %d, want 2", got)
	}
	if g.calls == 0 {
		t.Error("Guidance was never consulted — the seam is dead")
	}
}

// TestGuidanceTurnZeroExempt: the user's own prompt owns turn 0.
func TestGuidanceTurnZeroExempt(t *testing.T) {
	t.Parallel()
	srv, _ := guidanceBackend(t, nil)
	// Fire on every turn, including 0. Turn 0 must still be exempt —
	// nothing may precede the user's own prompt.
	g := &scriptedGuidance{fireOn: map[int]bool{0: true, 1: true, 2: true}, text: "hint"}
	ag, err := New(Config{
		Client:   deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:    deepseek.ModelV41Flash,
		Tools:    tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "body"}),
		Guidance: g,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ag.Prompt(context.Background(), "user prompt") {
	}
	msgs := ag.Messages()
	// Find the user's own prompt and assert nothing user-role precedes
	// it. (No SystemPrompt is configured here, so it is index 0.)
	for i, m := range msgs {
		if m.Role == deepseek.RoleUser && strings.Contains(m.Content, "user prompt") {
			for j := 0; j < i; j++ {
				if msgs[j].Role == deepseek.RoleUser {
					t.Fatalf("user-role message at %d precedes the user's own prompt at %d: %q",
						j, i, msgs[j].Content)
				}
			}
			return
		}
	}
	t.Fatal("user prompt not found in history")
}
