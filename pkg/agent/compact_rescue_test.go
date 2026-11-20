package agent

// Compact-overflow rescue tests (card c_idisqf; docs/pitfalls.md §
// "Grep tool with broad pattern can deadlock").
//
// The deadlock shape these tests reproduce: /compact's Summarise call
// sends the FULL history plus the summariser prompt. Once that history
// overflows the model's context window the API rejects the rescue call
// itself with the same 400 every other prompt gets — the one request
// that was supposed to shrink the session cannot fit, so the session is
// unrecoverable in-app.
//
// rescueBackend below is an independent oracle: it re-implements the
// bytes→tokens heuristic locally (deliberately NOT calling the
// production estimator) so a bug in the shared estimator cannot make
// these tests lie to themselves. Fixtures are sized well away from
// decision boundaries so ±10% estimator drift cannot flip a verdict.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// rescueModel is unknown to internal/budget, so rescueBudget falls back
// to budget.Default — no global SetLimits mutation needed and the tests
// stay parallel-safe.
const rescueModel = "rescue-fixture-model"

// oracleTokens is the test-side token estimate: ASCII ≈ 4 bytes/token,
// non-ASCII ≈ 1 token/rune. Same formula the production estimator uses,
// re-implemented on purpose (independent oracle).
func oracleTokens(msgs []deepseek.Message) int {
	total := 0
	for _, m := range msgs {
		total += 8 // role + separators + ids
		for _, tc := range m.ToolCalls {
			total += len(tc.ID) + len(tc.Function.Name) + len(tc.Function.Arguments)/4
		}
		wide := 0
		for _, r := range m.Content {
			if r >= 0x80 {
				wide++
			}
		}
		n := len([]rune(m.Content))
		total += (n-wide)/4 + wide
	}
	return total
}

// rescueCall is one captured request: the wire messages plus the token
// count the backend attributed to them.
type rescueCall struct {
	messages []deepseek.Message
	tokens   int
}

// rescueBackend fakes the DeepSeek chat endpoint with a hard context
// budget. A request whose (density-adjusted) token estimate exceeds
// limit gets the same 400 the real API returns for an over-limit
// prompt — the rejection that used to make /compact unrecoverable.
// density > 1 simulates a tokenizer denser than our estimator (the
// estimator-underestimates case that forces the reactive retry).
// The backend also refuses orphaned tool results, exactly as the real
// API does — a rescue that trims history into an invalid shape must
// fail here rather than ship.
type rescueBackend struct {
	mu       sync.Mutex
	limit    int
	density  float64
	calls    []rescueCall
	pairErrs []string
	srv      *httptest.Server
}

func newRescueBackend(t *testing.T, limit int, density float64) *rescueBackend {
	t.Helper()
	b := &rescueBackend{limit: limit, density: density}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req deepseek.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tok := int(float64(oracleTokens(req.Messages)) * density)
		b.mu.Lock()
		b.calls = append(b.calls, rescueCall{messages: req.Messages, tokens: tok})
		b.mu.Unlock()

		if tok > limit {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf(
						"This model's maximum context length is %d tokens. However, you requested %d tokens. Please reduce the length of the messages.", limit, tok),
					"type": "invalid_request_error",
				},
			})
			return
		}
		if pair := checkToolPairing(req.Messages); pair != "" {
			b.mu.Lock()
			b.pairErrs = append(b.pairErrs, pair)
			b.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "invalid message order: " + pair,
					"type":    "invalid_request_error",
				},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-rescue",
			"object": "chat.completion",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "RESCUED-SUMMARY"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110,
			},
		})
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *rescueBackend) callCount(t *testing.T) int {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *rescueBackend) call(t *testing.T, i int) rescueCall {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < 0 || i >= len(b.calls) {
		t.Fatalf("backend call %d not recorded (have %d)", i, len(b.calls))
	}
	return b.calls[i]
}

// checkToolPairing returns a non-empty description if any tool message
// has no preceding assistant message carrying its tool_call_id — the
// orphan shape the real API rejects with a 400.
func checkToolPairing(msgs []deepseek.Message) string {
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Role == deepseek.RoleAssistant {
			for _, tc := range m.ToolCalls {
				seen[tc.ID] = true
			}
		}
		if m.Role == deepseek.RoleTool && !seen[m.ToolCallID] {
			return fmt.Sprintf("tool message with tool_call_id %q has no preceding assistant tool_calls entry", m.ToolCallID)
		}
	}
	return ""
}

// overflowHistory builds G exchanges of [user ask, assistant grep
// tool_call, grep-style tool result of toolBytes ASCII bytes], plus a
// final user turn. With G=100, toolBytes=6KiB the estimate lands around
// ~160K tokens — comfortably above budget.Default's 128K so the fixture
// is unambiguously in the deadlock regime.
func overflowHistory(exchanges, toolBytes int) []deepseek.Message {
	pad := strings.Repeat("x", toolBytes)
	var msgs []deepseek.Message
	for i := 0; i < exchanges; i++ {
		msgs = append(msgs,
			deepseek.Message{Role: deepseek.RoleUser, Content: fmt.Sprintf("run grep for pattern foo%d across the repo", i)},
			deepseek.Message{
				Role: deepseek.RoleAssistant,
				ToolCalls: []deepseek.ToolCall{{
					ID:   fmt.Sprintf("call_%d", i),
					Type: "function",
					Function: deepseek.ToolCallFunc{
						Name:      "grep",
						Arguments: fmt.Sprintf(`{"pattern":"foo%d"}`, i),
					},
				}},
			},
			deepseek.Message{Role: deepseek.RoleTool, ToolCallID: fmt.Sprintf("call_%d", i),
				Content: "match line 1: " + pad},
		)
	}
	msgs = append(msgs, deepseek.Message{Role: deepseek.RoleUser, Content: "now summarise what you found"})
	return msgs
}

func newRescueAgent(t *testing.T, url string) *Agent {
	t.Helper()
	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(url)),
		Model:        rescueModel, // unknown to budget → Default window
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ag
}

// TestSummarise_OverflowRescue_Completes is THE reproduction: a history
// that overflows the window must still produce a summary. Before the
// rescue path this fails with the API's "maximum context length" 400 —
// the deadlock: the request whose job was to shrink the session is the
// one that cannot fit.
func TestSummarise_OverflowRescue_Completes(t *testing.T) {
	t.Parallel()
	hist := overflowHistory(100, 6<<10)

	// Fixture sanity: with the summariser prompt appended, the estimate
	// must exceed the full window — this conversation is genuinely in
	// the deadlock regime, not merely near it.
	full := append(append([]deepseek.Message{{Role: deepseek.RoleSystem, Content: "sys"}}, hist...),
		deepseek.Message{Role: deepseek.RoleUser, Content: summariserPrompt})
	if est := oracleTokens(full); est <= budget.Default {
		t.Fatalf("fixture must overflow the window: estimate %d <= %d", est, budget.Default)
	}

	b := newRescueBackend(t, budget.Default, 1.0)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(hist)

	// Timeout guard: a rescue that hangs must fail the test, not the CI
	// job's global timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	summary, _, err := ag.Summarise(ctx)
	if err != nil {
		t.Fatalf("Summarise on overflowed history must complete via rescue, got: %v", err)
	}
	if summary != "RESCUED-SUMMARY" {
		t.Fatalf("unexpected summary %q", summary)
	}
	if n := b.callCount(t); n != 1 {
		t.Fatalf("pre-flight rescue should make the first call fit, saw %d calls", n)
	}
	got := b.call(t, 0)
	if got.tokens > budget.Default {
		t.Fatalf("accepted request still overflows: %d tokens", got.tokens)
	}
	if len(got.messages) >= len(full) {
		t.Fatalf("rescue must shrink the request: %d messages, want < %d", len(got.messages), len(full))
	}
	// The kept tail must still end with the real final user turn and
	// the summariser prompt, and carry the elision note.
	last := got.messages[len(got.messages)-1]
	if last.Role != deepseek.RoleUser || !strings.Contains(last.Content, summariserPrompt[:40]) {
		t.Fatalf("summariser prompt turn must survive: %+v", last)
	}
	if !strings.Contains(last.Content, "[Context rescue:") {
		t.Fatalf("elision note missing from summariser prompt: %q", last.Content[:120])
	}
	prev := got.messages[len(got.messages)-2]
	if prev.Content != "now summarise what you found" {
		t.Fatalf("final real user turn must be kept, got %+v", prev)
	}
	// Pairing was enforced by the backend (any orphan would have 400'd);
	// surface the recorded violations for triage anyway.
	b.mu.Lock()
	pairErrs := append([]string(nil), b.pairErrs...)
	b.mu.Unlock()
	if len(pairErrs) > 0 {
		t.Fatalf("orphaned tool messages in trimmed request: %v", pairErrs[0])
	}
}

// TestSummarise_OverflowReactiveRetry covers the estimator-drift case:
// the backend's tokenizer is 1.3× denser than the estimator, so the
// pre-flight check believes the request fits while the API disagrees.
// The reactive one-shot retry at half budget must still land the
// summary — without it, any estimator underestimate reinstates the
// deadlock.
func TestSummarise_OverflowReactiveRetry(t *testing.T) {
	t.Parallel()
	// ~111K estimated: under the 124K pre-flight budget (128K − 4K
	// reserve) yet 1.3×111K ≈ 144K over the backend's 128K.
	hist := overflowHistory(70, 6<<10)
	full := append(append([]deepseek.Message{{Role: deepseek.RoleSystem, Content: "sys"}}, hist...),
		deepseek.Message{Role: deepseek.RoleUser, Content: summariserPrompt})
	est := oracleTokens(full)
	if est > budget.Default-4096 {
		t.Fatalf("fixture must pass the pre-flight check, estimate %d", est)
	}
	if int(float64(est)*1.3) <= budget.Default {
		t.Fatalf("fixture must be rejected by the dense backend, 1.3×%d", est)
	}

	b := newRescueBackend(t, budget.Default, 1.3)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	summary, _, err := ag.Summarise(ctx)
	if err != nil {
		t.Fatalf("reactive retry must rescue estimator drift, got: %v", err)
	}
	if summary != "RESCUED-SUMMARY" {
		t.Fatalf("unexpected summary %q", summary)
	}
	if n := b.callCount(t); n != 2 {
		t.Fatalf("want exactly 2 calls (full attempt + trimmed retry), saw %d", n)
	}
	first, second := b.call(t, 0), b.call(t, 1)
	if len(second.messages) >= len(first.messages) {
		t.Fatalf("retry must be trimmed: %d messages vs %d", len(second.messages), len(first.messages))
	}
	if second.tokens > budget.Default {
		t.Fatalf("retry still overflows: %d tokens", second.tokens)
	}
	if last := second.messages[len(second.messages)-1]; !strings.Contains(last.Content, "[Context rescue:") {
		t.Fatalf("retry must carry the elision note: %q", last.Content[:120])
	}
}

// TestSummarise_NormalPath_BytesUnchanged is the prefix-cache guard:
// when the history fits, the request bytes must be EXACTLY the
// pre-rescue shape — system prompt, history verbatim, summariser prompt
// appended. No markers, no reordering, no trimming. This must hold
// before AND after the rescue landed.
func TestSummarise_NormalPath_BytesUnchanged(t *testing.T) {
	t.Parallel()
	hist := overflowHistory(3, 200) // tiny conversation, nowhere near any budget

	b := newRescueBackend(t, budget.Default, 1.0)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := ag.Summarise(ctx); err != nil {
		t.Fatalf("Summarise: %v", err)
	}
	if n := b.callCount(t); n != 1 {
		t.Fatalf("a fitting history needs exactly one call, saw %d", n)
	}
	got := b.call(t, 0).messages

	expected := []deepseek.Message{{Role: deepseek.RoleSystem, Content: "sys"}}
	expected = append(expected, hist...)
	expected = append(expected, deepseek.Message{Role: deepseek.RoleUser, Content: summariserPrompt})

	want, _ := json.Marshal(expected)
	have, _ := json.Marshal(got)
	if !bytes.Equal(want, have) {
		t.Fatalf("normal-path request bytes changed:\nwant %s\nhave %s", want, have)
	}
	if bytes.Contains(have, []byte("compact rescue")) || bytes.Contains(have, []byte("Context rescue")) {
		t.Fatalf("rescue markers leaked into a fitting request: %s", have)
	}
}

// TestSummarise_NormalPath_NearLimit_Untrimmed guards against premature
// rescue: at ~70% of the pre-flight budget the request must still go
// out untrimmed (estimator conservatism must not quietly degrade every
// large-but-fitting compact).
func TestSummarise_NormalPath_NearLimit_Untrimmed(t *testing.T) {
	t.Parallel()
	// ~80K estimated ≈ 64% of the 124K pre-flight budget.
	hist := overflowHistory(50, 6<<10)
	full := append(append([]deepseek.Message{{Role: deepseek.RoleSystem, Content: "sys"}}, hist...),
		deepseek.Message{Role: deepseek.RoleUser, Content: summariserPrompt})
	if est := oracleTokens(full); est > (budget.Default-4096)*70/100 {
		t.Fatalf("fixture must be comfortably inside the budget, estimate %d", est)
	}

	b := newRescueBackend(t, budget.Default, 1.0)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := ag.Summarise(ctx); err != nil {
		t.Fatalf("Summarise: %v", err)
	}
	if n := b.callCount(t); n != 1 {
		t.Fatalf("fitting history needs exactly one call, saw %d", n)
	}
	got := b.call(t, 0).messages
	if len(got) != len(full) {
		t.Fatalf("near-limit request was trimmed: %d messages, want %d", len(got), len(full))
	}
	if last := got[len(got)-1]; strings.Contains(last.Content, "[Context rescue:") {
		t.Fatalf("premature rescue note: %q", last.Content[:120])
	}
}

// TestSummarise_RescueStage1_TruncatesGiantBody: a single oversized
// paste (600KB) overflows on its own; stage 1 caps the body in place
// instead of dropping the exchange — content shrinks, message count
// does not, so no information about conversation SHAPE is lost and no
// tool pairing can break.
func TestSummarise_RescueStage1_TruncatesGiantBody(t *testing.T) {
	t.Parallel()
	paste := strings.Repeat("p", 600<<10)
	hist := []deepseek.Message{
		{Role: deepseek.RoleUser, Content: paste},
		{Role: deepseek.RoleAssistant, Content: "received, analysing"},
		{Role: deepseek.RoleUser, Content: "what did you find"},
	}

	b := newRescueBackend(t, budget.Default, 1.0)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := ag.Summarise(ctx); err != nil {
		t.Fatalf("Summarise with giant body: %v", err)
	}
	if n := b.callCount(t); n != 1 {
		t.Fatalf("stage-1 rescue should fit on the first call, saw %d", n)
	}
	got := b.call(t, 0).messages
	if len(got) != 5 { // sys + 3 history + summariser prompt
		t.Fatalf("stage 1 must not drop messages: %d, want 5", len(got))
	}
	body := got[1].Content
	if len(body) > (8<<10)+512 { // cap + marker + note headroom
		t.Fatalf("giant body not capped: %d bytes", len(body))
	}
	if !strings.Contains(body, "[compact rescue: truncated") {
		t.Fatalf("truncation marker missing: %q", body[:120])
	}
	if !strings.HasPrefix(body, "pppp") {
		t.Fatalf("keep-head violated: %q", body[:40])
	}
}

// TestSummarise_RescueMultiToolBatchKeepsPairing: cutting history whose
// exchanges carry MULTI-tool batches must never strand a tool result
// without its assistant parent — the backend enforces the same pairing
// rule the real API does, so an invalid cut fails here rather than at
// the user's next 400.
func TestSummarise_RescueMultiToolBatchKeepsPairing(t *testing.T) {
	t.Parallel()
	var hist []deepseek.Message
	for i := 0; i < 60; i++ {
		calls := []deepseek.ToolCall{
			{ID: fmt.Sprintf("a_%d", i), Type: "function", Function: deepseek.ToolCallFunc{Name: "grep", Arguments: `{"pattern":"x"}`}},
			{ID: fmt.Sprintf("b_%d", i), Type: "function", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"y"}`}},
		}
		hist = append(hist,
			deepseek.Message{Role: deepseek.RoleUser, Content: fmt.Sprintf("step %d", i)},
			deepseek.Message{Role: deepseek.RoleAssistant, ToolCalls: calls},
			deepseek.Message{Role: deepseek.RoleTool, ToolCallID: calls[0].ID,
				Content: "match: " + strings.Repeat("x", 6<<10)},
			deepseek.Message{Role: deepseek.RoleTool, ToolCallID: calls[1].ID,
				Content: "file: " + strings.Repeat("y", 6<<10)},
		)
	}
	hist = append(hist, deepseek.Message{Role: deepseek.RoleUser, Content: "wrap up"})

	b := newRescueBackend(t, budget.Default, 1.0)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := ag.Summarise(ctx); err != nil {
		t.Fatalf("Summarise over multi-tool history: %v", err)
	}
	b.mu.Lock()
	pairErrs := append([]string(nil), b.pairErrs...)
	b.mu.Unlock()
	if len(pairErrs) > 0 {
		t.Fatalf("orphaned tool message: %v", pairErrs[0])
	}
	// Every kept tool batch must be complete or dropped whole-ish: any
	// assistant tool_calls message present must have all its results.
	got := b.call(t, 0).messages
	want := map[string]bool{}
	for _, m := range got {
		if m.Role == deepseek.RoleAssistant {
			for _, tc := range m.ToolCalls {
				want[tc.ID] = true
			}
		}
	}
	for _, m := range got {
		if m.Role == deepseek.RoleTool && !want[m.ToolCallID] {
			t.Fatalf("orphan tool result %s survived the cut", m.ToolCallID)
		}
	}
}

// TestSummarise_CancelledCtx_ReturnsPromptly: the rescue path must
// honour ctx cancellation — a hung summarise call must not outlive its
// caller's patience (the TUI bounds it at 60s; the test bounds tighter).
func TestSummarise_CancelledCtx_ReturnsPromptly(t *testing.T) {
	t.Parallel()
	b := newRescueBackend(t, budget.Default, 1.0)
	ag := newRescueAgent(t, b.srv.URL)
	ag.Reset(overflowHistory(100, 6<<10))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the call
	start := time.Now()
	_, _, err := ag.Summarise(ctx)
	if err == nil {
		t.Fatal("cancelled ctx must error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled call took %v to return", elapsed)
	}
}

// --- second-tier (llm.Provider) path ---

// rescueProvider fakes a second-tier provider with the same oracle
// budget: over-limit requests get a stream whose only event is an
// ErrorEvent carrying the provider-flavoured overflow message.
type rescueProvider struct {
	mu      sync.Mutex
	limit   int
	density float64
	reqs    []llm.ChatRequest
}

func (p *rescueProvider) Name() string { return "rescue" }

func (p *rescueProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()

	var msgs []deepseek.Message
	for _, m := range req.Messages {
		dm := deepseek.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			dm.ToolCalls = append(dm.ToolCalls, deepseek.ToolCall{
				ID: tc.ID, Type: "function",
				Function: deepseek.ToolCallFunc{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		msgs = append(msgs, dm)
	}
	ch := make(chan llm.Event, 2)
	if tok := int(float64(oracleTokens(msgs)) * p.density); tok > p.limit {
		ch <- llm.ErrorEvent{Err: errors.New(
			"provider api error: This model's maximum context length is " +
				fmt.Sprint(p.limit) + " tokens, request exceeded it")}
		close(ch)
		return ch, nil
	}
	ch <- llm.TextDelta{Delta: "LLM-SUMMARY"}
	ch <- llm.TurnDone{FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func (p *rescueProvider) count(t *testing.T) int {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

func (p *rescueProvider) last(t *testing.T) llm.ChatRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		t.Fatal("provider saw no request")
	}
	return p.reqs[len(p.reqs)-1]
}

// TestSummariseLLM_OverflowReactiveRetry: the second-tier lane shares
// the deadlock shape (full history through ChatStream) and the rescue —
// pre-flight passes at 1.0×, the dense backend rejects, the reactive
// retry lands it.
func TestSummariseLLM_OverflowReactiveRetry(t *testing.T) {
	t.Parallel()
	// ~111K estimated: pre-flight (124K budget) passes, 1.3× rejects.
	hist := overflowHistory(70, 6<<10)
	prov := &rescueProvider{limit: budget.Default, density: 1.3}
	ag, err := New(Config{Provider: prov, Model: rescueModel, SystemPrompt: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	summary, _, err := ag.Summarise(ctx)
	if err != nil {
		t.Fatalf("llm-path rescue: %v", err)
	}
	if summary != "LLM-SUMMARY" {
		t.Fatalf("unexpected summary %q", summary)
	}
	if n := prov.count(t); n != 2 {
		t.Fatalf("want 2 provider calls, saw %d", n)
	}
	last := prov.last(t)
	if n := len(last.Messages); n >= len(hist)+2 {
		t.Fatalf("retry must be trimmed: %d messages", n)
	}
	if l := last.Messages[len(last.Messages)-1]; !strings.Contains(l.Content, "[Context rescue:") {
		t.Fatalf("retry must carry the elision note: %q", l.Content[:120])
	}
}

// TestSummariseLLM_NormalPath_BytesUnchanged: the second-tier guard —
// fitting history goes through msgsToLLM verbatim plus the summariser
// prompt, no rescue artefacts.
func TestSummariseLLM_NormalPath_BytesUnchanged(t *testing.T) {
	t.Parallel()
	hist := overflowHistory(3, 200)
	prov := &rescueProvider{limit: budget.Default, density: 1.0}
	ag, err := New(Config{Provider: prov, Model: rescueModel, SystemPrompt: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	ag.Reset(hist)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := ag.Summarise(ctx); err != nil {
		t.Fatalf("Summarise: %v", err)
	}
	got := prov.last(t).Messages

	expected := []llm.Message{{Role: deepseek.RoleSystem, Content: "sys"}}
	for _, m := range hist {
		expected = append(expected, llm.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, ToolName: m.Name})
		for _, tc := range m.ToolCalls {
			expected[len(expected)-1].ToolCalls = append(expected[len(expected)-1].ToolCalls,
				llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
	}
	expected = append(expected, llm.Message{Role: deepseek.RoleUser, Content: summariserPrompt})

	if len(got) != len(expected) {
		t.Fatalf("message count changed: %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i].Role != expected[i].Role || got[i].Content != expected[i].Content ||
			got[i].ToolCallID != expected[i].ToolCallID || len(got[i].ToolCalls) != len(expected[i].ToolCalls) {
			t.Fatalf("message %d changed:\ngot  %+v\nwant %+v", i, got[i], expected[i])
		}
		for j := range expected[i].ToolCalls {
			if got[i].ToolCalls[j] != expected[i].ToolCalls[j] {
				t.Fatalf("message %d tool call %d changed: %+v vs %+v", i, j, got[i].ToolCalls[j], expected[i].ToolCalls[j])
			}
		}
	}
}

// --- unit tests for the rescue primitives ---

// TestTrimForRescue_Fits_ReturnsIdenticalSlice: a fitting request must
// come back as the SAME slice (no copy, no byte change) with stats
// inactive — this is what keeps the normal path byte-identical.
func TestTrimForRescue_Fits_ReturnsIdenticalSlice(t *testing.T) {
	t.Parallel()
	msgs := overflowHistory(3, 200)
	out, stats := trimForRescue(msgs, estimateTokens(msgs)+1)
	if stats.active {
		t.Fatal("stats.active must be false when the estimate fits")
	}
	if len(out) != len(msgs) {
		t.Fatalf("length changed: %d vs %d", len(out), len(msgs))
	}
	for i := range msgs {
		if &out[i] != &msgs[i] {
			t.Fatalf("slice was copied at index %d — fitting input must pass through untouched", i)
		}
	}
}

// TestTrimForRescue_NeverMutatesInput: the trim works on a clone — the
// agent's own history (the caller's slice) must survive byte-identical.
func TestTrimForRescue_NeverMutatesInput(t *testing.T) {
	t.Parallel()
	msgs := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: strings.Repeat("u", 20<<10)},
		{Role: deepseek.RoleAssistant, Content: "ok"},
	}
	snapshot := make([]deepseek.Message, len(msgs))
	copy(snapshot, msgs)
	_, stats := trimForRescue(msgs, 1024)
	if !stats.active {
		t.Fatal("fixture must trigger trimming")
	}
	for i := range msgs {
		if msgs[i].Content != snapshot[i].Content {
			t.Fatalf("input mutated at %d", i)
		}
	}
}

// TestTrimForRescue_DropOldestKeepsTail: under budget pressure the
// system prompt and the newest messages survive; the first kept
// message after the head is never a tool result (orphan rule).
func TestTrimForRescue_DropOldestKeepsTail(t *testing.T) {
	t.Parallel()
	hist := overflowHistory(20, 512) // ~2.7K tokens total
	withPrompt := append(append([]deepseek.Message{{Role: deepseek.RoleSystem, Content: "sys"}}, hist...),
		deepseek.Message{Role: deepseek.RoleUser, Content: summariserPrompt})
	// Budget for roughly the last exchange + prompt.
	budgetTokens := 200
	out, stats := trimForRescue(withPrompt, budgetTokens)
	if !stats.active || stats.dropped == 0 {
		t.Fatalf("expected drops, stats=%+v", stats)
	}
	if estimateTokens(out) > budgetTokens {
		t.Fatalf("trimmed request still over budget: %d > %d", estimateTokens(out), budgetTokens)
	}
	if out[0].Role != deepseek.RoleSystem || out[0].Content != "sys" {
		t.Fatalf("system prompt must be kept verbatim: %+v", out[0])
	}
	if out[1].Role == deepseek.RoleTool {
		t.Fatalf("cut left an orphaned tool result at the head of the kept tail")
	}
	if last := out[len(out)-1]; last.Content != summariserPrompt {
		t.Fatalf("summariser prompt must be the kept last message: %q", last.Content[:80])
	}
	if n := len(out); n < 3 { // system + at least one exchange + prompt
		t.Fatalf("tail kept too little: %d messages", n)
	}
}

// TestTrimForRescue_Irreducible_KeepsStage1: with only [system, prompt]
// left there is nothing droppable — the stage-1 result comes back with
// dropped=0 rather than an empty or system-less slice.
func TestTrimForRescue_Irreducible_KeepsStage1(t *testing.T) {
	t.Parallel()
	msgs := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: summariserPrompt},
	}
	out, stats := trimForRescue(msgs, 10) // absurdly small budget
	if !stats.active {
		t.Fatal("fixture must trigger trimming")
	}
	if stats.dropped != 0 {
		t.Fatalf("irreducible input must not drop: %+v", stats)
	}
	if len(out) != len(msgs) || out[0].Role != deepseek.RoleSystem {
		t.Fatalf("stage-1 result must keep system+prompt: %d messages", len(out))
	}
}

// TestEstimateTokens_MixedScripts: ASCII ≈ 4 bytes/token, wide runes ≈
// 1 token each — the conservative CJK bias that makes Chinese-heavy
// histories err high (rescue early) instead of low (deadlock).
func TestEstimateTokens_MixedScripts(t *testing.T) {
	t.Parallel()
	ascii := deepseek.Message{Role: deepseek.RoleUser, Content: strings.Repeat("a", 4000)}
	if got := estimateTokens([]deepseek.Message{ascii}); got != 8+1000 {
		t.Fatalf("ascii estimate %d, want 1008", got)
	}
	cjk := deepseek.Message{Role: deepseek.RoleUser, Content: strings.Repeat("中", 1000)}
	if got := estimateTokens([]deepseek.Message{cjk}); got != 8+1000 {
		t.Fatalf("cjk estimate %d, want 1008 (1/rune conservative)", got)
	}
}

// TestIsContextOverflowError: loose match across provider phrasings,
// strict exclusion of upstream outages and transport noise.
func TestIsContextOverflowError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deepseek phrasing", errors.New("deepseek api error: invalid_request_error: This model's maximum context length is 131072 tokens. However, you requested 140000 tokens."), true},
		{"openai phrasing", errors.New("context_length_exceeded: maximum context length is 8192 tokens"), true},
		{"anthropic phrasing", errors.New("prompt is too long: 200000 tokens > 190000 maximum"), true},
		{"generic window", errors.New("input length exceeds context window"), true},
		{"quota 429", errors.New("deepseek api error: rate limit: quota exceeded"), false},
		{"5xx", &deepseek.APIError{StatusCode: 503, Message: "maximum context upstream blip"}, false},
		{"transport", errors.New("connection reset by peer"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isContextOverflowError(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRescueBudget: window minus output reserve (never below
// cfg.MaxTokens), floor 1024 for pathological windows.
func TestRescueBudget(t *testing.T) {
	t.Parallel()
	if got := rescueBudget("deepseek-flash", 0); got != 1_000_000-4096 {
		t.Fatalf("known model budget %d", got)
	}
	if got := rescueBudget("rescue-fixture-model", 8192); got != 128_000-8192 {
		t.Fatalf("maxTokens must lift the reserve: %d", got)
	}
	if got := rescueBudget("rescue-fixture-model", 0); got != 128_000-4096 {
		t.Fatalf("default window budget %d", got)
	}
	if got := rescueBudget("rescue-fixture-model", 500_000); got != 1024 {
		t.Fatalf("floor %d", got)
	}
}
