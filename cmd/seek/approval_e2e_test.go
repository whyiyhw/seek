package main

// W5 card c_n37tnr child-half acceptance, end to end through the REAL
// binary: `seek -p … --json --approval-stdin` against a fake
// OpenAI-compatible backend that asks for one bash call. The test acts
// as the plane: it watches the child's stdout for the
// approval_request line, answers with one approval_decision line on
// the child's stdin, and asserts the child then RUNS the tool and
// finishes — i.e. the gated action was released by the decision, not
// by --yolo.
//
// Two lanes:
//
//	① "y"   → the tool executes, the run completes (exit 0).
//	② EOF   → no decision arrives; the request is withdrawn
//	          (approval_cancel) and the tool result is the usual
//	          ErrDenied text, so the run still terminates cleanly
//	          (print mode's default posture is preserved).
//
// Sandbox-only: httptest backend + a temp SEEK_HOME/HOME, no keys, no
// network.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// approvalBackend is a fake chat backend that asks for exactly one bash
// tool call on the first request and answers "done" afterwards.
type approvalBackend struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs int
}

func newApprovalBackend(t *testing.T) *approvalBackend {
	t.Helper()
	b := &approvalBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.reqs++
		n := b.reqs
		b.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// One bash tool call — the gated action the human must
			// release.
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo approval-e2e\"}"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"done."},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":3}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// approvalChild is one running child plus the handles the test drives.
type approvalChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan jsonLine // every stdout JSONL line, as it lands
	stderr strings.Builder
	done   chan error
}

// startApprovalChild runs the compiled binary in print mode with
// --approval-stdin and a writable stdin pipe (the plane's shape).
func startApprovalChild(t *testing.T, backendURL, workDir string, extra ...string) *approvalChild {
	t.Helper()
	bin := buildSeekBin(t)
	args := []string{
		"-p", "请跑一次 echo",
		"--json",
		"--approval-stdin",
		"--provider", "compatible",
		"--base-url", backendURL + "/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-save",
		"--no-cron-tick",
		"--no-project-md",
	}
	args = append(args, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"OPENAI_API_KEY=e2e-key",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	c := &approvalChild{cmd: cmd, stdin: stdin, lines: make(chan jsonLine, 64), done: make(chan error, 1)}
	cmd.Stderr = &c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() {
		defer close(c.lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			var l jsonLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				continue // non-JSON stdout would be a contract break; other lanes assert it
			}
			c.lines <- l
		}
	}()
	go func() { c.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
		}
	})
	return c
}

// waitLine blocks until a line of type typ arrives (or fails).
// typ == l2typAny returns the next line of any type.
func (c *approvalChild) waitLine(t *testing.T, typ string, timeout time.Duration) jsonLine {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case l, ok := <-c.lines:
			if !ok {
				t.Fatalf("stdout closed while waiting for %q\nstderr:\n%s", typ, c.stderr.String())
			}
			if l.Type == typ || typ == l2typAny {
				return l
			}
		case <-deadline:
			t.Fatalf("no %q line within %s\nstderr:\n%s", typ, timeout, c.stderr.String())
		}
	}
}

// TestApprovalStdinE2E_AllowedRunsTool is lane ①: the decision line
// releases the gated bash call, the tool's output reaches the stream,
// and the run completes.
func TestApprovalStdinE2E_AllowedRunsTool(t *testing.T) {
	workDir := t.TempDir()
	b := newApprovalBackend(t)
	c := startApprovalChild(t, b.srv.URL, workDir)

	req := c.waitLine(t, "approval_request", 60*time.Second)
	if req.ReqID == "" || req.Kind != "bash" || !strings.Contains(req.Command, "echo approval-e2e") {
		t.Fatalf("approval_request = %+v, want req_id + kind=bash + the echoed command", req)
	}
	if _, err := fmt.Fprintf(c.stdin, `{"type":"approval_decision","req_id":%q,"decision":"y"}`+"\n", req.ReqID); err != nil {
		t.Fatalf("write decision: %v", err)
	}

	// The tool actually ran: its tool_end carries the command's output.
	var sawTool bool
	for !sawTool {
		l := c.waitLine(t, l2typAny, 60*time.Second)
		if l.Type == "tool_end" && l.Name == "bash" {
			if l.Error != "" {
				t.Fatalf("bash tool_end error = %q (the decision did not release the gate)", l.Error)
			}
			if !strings.Contains(l.Result, "approval-e2e") {
				t.Fatalf("bash result = %q, want the echoed marker", l.Result)
			}
			sawTool = true
		}
	}
	c.waitLine(t, "agent_end", 60*time.Second)
	if err := <-c.done; err != nil {
		t.Fatalf("child exit: %v\nstderr:\n%s", err, c.stderr.String())
	}
}

// TestApprovalStdinE2E_EOFDeniesAndWithdraws is lane ②: with no
// decider (stdin closed), the card is withdrawn and the tool is
// denied — the run still finishes cleanly, exactly like pre-W5 print
// mode's posture.
func TestApprovalStdinE2E_EOFDeniesAndWithdraws(t *testing.T) {
	workDir := t.TempDir()
	b := newApprovalBackend(t)
	c := startApprovalChild(t, b.srv.URL, workDir)

	req := c.waitLine(t, "approval_request", 60*time.Second)
	_ = c.stdin.Close() // no human attached

	cancel := c.waitLine(t, "approval_cancel", 30*time.Second)
	if cancel.ReqID != req.ReqID {
		t.Errorf("approval_cancel req_id = %q, want %q", cancel.ReqID, req.ReqID)
	}

	var sawDenied bool
	for !sawDenied {
		l := c.waitLine(t, l2typAny, 60*time.Second)
		if l.Type == "tool_end" && l.Name == "bash" {
			// ErrDenied surfaces as the tool result string (the model-
			// visible denial), never a fatal error line.
			if l.Error == "" && !strings.Contains(l.Result, "bash") {
				t.Fatalf("bash tool_end = %+v, want a denial result", l)
			}
			sawDenied = true
		}
	}
	c.waitLine(t, "agent_end", 60*time.Second)
	if err := <-c.done; err != nil {
		t.Fatalf("child exit: %v (a denial must not kill the run)\nstderr:\n%s", err, c.stderr.String())
	}
}

// l2typAny is the "any line" pseudo-type for waitLine: it matches
// every line, so a loop can inspect the full stream.
const l2typAny = "\x00any"
