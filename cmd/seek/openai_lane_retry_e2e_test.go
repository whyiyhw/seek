package main

// Card c_koutux acceptance ⑤, end to end through the REAL binary on the
// OpenCode lane — the lane whose single HTTP/2 PROTOCOL_ERROR killed an
// entire unattended round.
//
// A local OpenAI-compatible fake backend kills the FIRST attempt and
// answers normally on the second. Two kill shapes, mirroring the two
// defects the card fixed:
//
//	① open   — the socket dies before any status line (c.http.Do fails;
//	            the open-phase retry must re-issue).
//	② body   — SSE headers land, then the socket dies before a single
//	            event (nothing visible, so pumpStream must re-open once).
//
// The round must survive (text + finish_reason=stop + agent_end, exit 0,
// no `error` line), the server must see exactly 2 attempts, and
// SEEK_LLM_TRACE=1 must print exactly one retry line — the operator-visible
// record of a silent recovery.
//
// Sandbox-only: httptest backend + a temp SEEK_HOME/HOME, no key, no
// network (--base-url points at loopback).

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// retryBackend returns 200 + a normal stream on every request except the
// first, which it kills in the shape selected by breakAfterHeaders.
type retryBackend struct {
	srv               *httptest.Server
	hits              atomic.Int32
	breakAfterHeaders bool
}

func newRetryBackend(t *testing.T, breakAfterHeaders bool) *retryBackend {
	t.Helper()
	b := &retryBackend{breakAfterHeaders: breakAfterHeaders}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if b.hits.Add(1) == 1 {
			if b.breakAfterHeaders {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			// Hijack + close is as close to a peer RST as httptest
			// gets: no status line when breakAfterHeaders is false,
			// headers-then-EOF when it is true.
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = c.Close()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"recovered"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// TestOpenAILaneRetryE2E is acceptance ⑤: one transient break on the
// OpenCode lane must not kill the round.
func TestOpenAILaneRetryE2E(t *testing.T) {
	for _, tc := range []struct {
		name              string
		breakAfterHeaders bool
	}{
		{"open", false},
		{"body", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			b := newRetryBackend(t, tc.breakAfterHeaders)

			bin := buildSeekBin(t)
			cmd := exec.Command(bin,
				"-p", "hi",
				"--json",
				"--provider", "opencode",
				"--base-url", b.srv.URL+"/v1/chat/completions",
				"--model", "e2e-model",
				"--no-save",
				"--no-cron-tick",
				"--no-project-md",
			)
			cmd.Dir = workDir
			cmd.Env = append(os.Environ(),
				"SEEK_HOME="+workDir,
				"HOME="+workDir,
				// Without a key the lane would go for the stored browser
				// login and fail before reaching the fake endpoint.
				"OPENCODE_API_KEY=e2e-key",
				// The retry is silent by design; this is the opt-in
				// record the acceptance point looks for.
				"SEEK_LLM_TRACE=1",
				"SEEK_NO_WEBFETCH=1",
				"SEEK_NO_WEBSEARCH=1",
				"SEEK_AUTO_DISTILL=0",
			)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatalf("stdout pipe: %v", err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}

			var (
				lines       []jsonLine
				sawText     bool
				sawFinish   string
				sawError    bool
				sawAgentEnd bool
			)
			sc := bufio.NewScanner(stdout)
			sc.Buffer(make([]byte, 0, 4096), 1<<20)
			for sc.Scan() {
				var l jsonLine
				if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
					t.Fatalf("non-JSON stdout line %q: %v", sc.Text(), err)
				}
				lines = append(lines, l)
				switch l.Type {
				case "text_delta":
					if l.Delta == "recovered" {
						sawText = true
					}
				case "turn_end":
					sawFinish = l.FinishReason
				case "error":
					sawError = true
				case "agent_end":
					sawAgentEnd = true
				}
			}

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("child exit: %v (one transient break must not kill the round)\nstderr:\n%s", err, stderr.String())
				}
			case <-time.After(120 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("child did not exit within 120s\nstderr:\n%s", stderr.String())
			}

			if got := b.hits.Load(); got != 2 {
				t.Errorf("fake endpoint saw %d attempts, want 2 (one retry)", got)
			}
			if sawError {
				t.Errorf("the retried turn surfaced a `error` line; want a clean round: %+v", lines)
			}
			if !sawText {
				t.Errorf("the second attempt's text never reached the stream: %+v", lines)
			}
			if sawFinish != "stop" {
				t.Errorf("turn_end finish_reason = %q, want stop", sawFinish)
			}
			if !sawAgentEnd {
				t.Errorf("no agent_end line — the round did not settle: %+v", lines)
			}
			// Exactly one retry record: a silent recovery is
			// undebuggable, a noisy one hides real trouble.
			if got := strings.Count(stderr.String(), "[llm-trace] retry"); got != 1 {
				t.Errorf("stderr retry lines = %d, want exactly 1\nstderr:\n%s", got, stderr.String())
			}
		})
	}
}
