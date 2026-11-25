package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// json_empty_test.go pins the machine-checkable empty-completion signal
// in the `-p --json` stream (card c_jxwsfz). Background: when the
// upstream burns its whole budget on reasoning and returns no content,
// seek self-heals by booking the in-band placeholder marker
// ("[empty: …]") as the assistant text — and the ONLY thing a program
// consumer could see before this change was that marker arriving as an
// ordinary text_delta, with exit 0. A consumer joining deltas therefore
// reported the marker as the answer.
//
// The contract fix: turn_end carries finish_reason, and the placeholder
// turn reports the seek-side value "empty_response" instead of the
// upstream "stop". Human-readable behaviour is unchanged — the marker
// still streams as a delta (the TUI commits deltas).

// captureRunJSONStdout runs fn with os.Stdout redirected into a pipe and
// returns the decoded JSONL lines. Every line is asserted to be valid
// standalone JSON here, so acceptance ③ ("never a half line") is checked
// by every caller without repeating the loop.
func captureRunJSONStdout(t *testing.T, fn func() error) []jsonLine {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	var (
		mu  sync.Mutex
		buf strings.Builder
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b := make([]byte, 4096)
		for {
			n, rerr := r.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	wg.Wait()
	_ = r.Close()

	if runErr != nil {
		t.Fatalf("runJSON: %v", runErr)
	}

	mu.Lock()
	raw := buf.String()
	mu.Unlock()

	var lines []jsonLine
	for _, ln := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if ln == "" {
			continue
		}
		var l jsonLine
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("line is not valid JSON (%v): %q", err, ln)
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		t.Fatal("runJSON emitted no lines")
	}
	return lines
}

// reasoningOnlyThenAnswer scripts the card's reproduction: turn 1 streams
// only reasoning_content and closes with finish=stop and no content
// (max-tokens-eaten thinking); turn 2 answers normally.
func reasoningOnlyThenAnswer(t *testing.T) *httptest.Server {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking until the budget ran out"}}]}`+"\n\n")
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"the real answer"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newJSONTestAgent(t *testing.T, srvURL string) *agent.Agent {
	t.Helper()
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srvURL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}

// Acceptance ① + ③: the reasoning-only placeholder turn must be
// identifiable from the stream alone — turn_end.finish_reason is
// "empty_response" — and every line stays standalone JSON.
func TestRunJSON_EmptyResponseCarriesMachineSignal(t *testing.T) {
	srv := reasoningOnlyThenAnswer(t)
	ag := newJSONTestAgent(t, srv.URL)

	lines := captureRunJSONStdout(t, func() error {
		return runJSON(context.Background(), ag, cache.New(), deepseek.ModelV41Flash,
			false, false, "hi", nil, nil, nil, nil, nil)
	})

	var turns []jsonLine
	markerDelta := false
	agentEnd := false
	for _, l := range lines {
		switch l.Type {
		case "turn_end":
			turns = append(turns, l)
		case "text_delta":
			// Human-readable behaviour unchanged: the marker still rides
			// the stream as an ordinary delta (the TUI commits deltas).
			if !l.Reasoning && strings.Contains(l.Delta, "[empty:") {
				markerDelta = true
			}
		case "agent_end":
			agentEnd = true
		case "error":
			t.Fatalf("unexpected fatal error line: %q", l.Error)
		}
	}

	if len(turns) != 2 {
		t.Fatalf("turn_end count = %d, want 2 (placeholder + recovery): %+v", len(turns), turns)
	}
	if got := turns[0].FinishReason; got != agent.FinishEmptyResponse {
		t.Errorf("placeholder turn_end.finish_reason = %q, want %q — this is the machine signal a program consumer branches on",
			got, agent.FinishEmptyResponse)
	}
	if got := turns[1].FinishReason; got != "stop" {
		t.Errorf("recovery turn_end.finish_reason = %q, want %q", got, "stop")
	}
	if !markerDelta {
		t.Errorf("placeholder marker no longer streams as a text_delta — TUI behaviour regressed: %+v", lines)
	}
	if !agentEnd {
		t.Error("no agent_end line")
	}
}

// Acceptance ②: a normal completion must NOT carry the empty signal, and
// the pre-existing turn_end shape (index/tokens/tool_calls) is unchanged.
func TestRunJSON_NormalCompletionHasNoEmptySignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	t.Cleanup(srv.Close)
	ag := newJSONTestAgent(t, srv.URL)

	lines := captureRunJSONStdout(t, func() error {
		return runJSON(context.Background(), ag, cache.New(), deepseek.ModelV41Flash,
			false, false, "hi", nil, nil, nil, nil, nil)
	})

	for _, l := range lines {
		if l.FinishReason == agent.FinishEmptyResponse {
			t.Fatalf("%s line carries the empty-response signal on a normal run: %+v", l.Type, l)
		}
	}

	want := []string{"agent_start", "turn_start", "text_delta", "turn_end", "agent_end"}
	if got := typeSeq(lines); !sliceEq(got, want) {
		t.Fatalf("type sequence: got %v, want %v (existing event shape must not regress)", got, want)
	}

	te := lines[3]
	if te.Index != 0 || te.PromptTokens != 10 || te.CompletionTokens != 2 {
		t.Errorf("turn_end shape regressed: %+v", te)
	}
	if te.FinishReason != "stop" {
		t.Errorf("normal turn_end.finish_reason = %q, want the upstream %q", te.FinishReason, "stop")
	}
}

// Acceptance ③, second shape: the field is additive — a line without a
// finish classification must not grow a "finish_reason" key, so existing
// consumers' byte expectations stay honest.
func TestRunJSON_FinishReasonOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(jsonLine{Type: "turn_end", Index: 0, PromptTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"finish_reason"`) {
		t.Errorf("empty finish_reason leaked into JSON: %s", raw)
	}
}
