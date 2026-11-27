package main

// Headless lanes must answer ask_user / propose with ErrDisabled — "no
// interactive channel here, rephrase as prose" — NOT ErrNoCallback
// ("… (programming error)").
//
// The distinction is load-bearing, not cosmetic: the two texts tell the
// model different things. ErrDisabled is an instruction (ask in your
// reply instead); ErrNoCallback reads as our bug, and a model that reads
// "programming error" retries the same call.
//
// The regression this pins: askPolicy was born ModeAsk and only the TUI
// path ever installed a callback, so every headless lane (print/json,
// rpc, acp, goal, autopilot, benchmark) hit ErrNoCallback. The fix
// inverts the default — askPolicy starts ModeDisabled and each lane that
// HAS a human opts in at its wiring block. These tests drive the REAL
// binary on two lanes, because the bug lived in the wiring between them.
//
// Card c_7bqiq5 moved the ACP half: an ACP session always has a human, so
// that lane now asks over session/request_permission instead of failing.
// What remains pinned here is the PRINT lane (no human → ErrDisabled) and
// the ACP lane's no-answer ending (timeout → cancelled, never a hang).
//
// Sandbox-only: httptest backend + temp SEEK_HOME/HOME, no keys, no
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
	"testing"
	"time"
)

// askToolBackend answers the first request with one call to `tool`
// (arguments fixed by the caller) and "done." afterwards. It
// discriminates on request count, which is safe here: each child runs
// exactly one prompt, so there is no conversation history to mistake
// for a fresh call.
func askToolBackend(t *testing.T, tool, args string) *httptest.Server {
	t.Helper()
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		w.Header().Set("Content-Type", "text/event-stream")
		if reqs == 1 {
			chunk := fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":null}]}`, tool, args)
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"done."},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":3}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

const (
	// askUserArgs is a schema-valid single-question ask_user call.
	askUserArgs = `{"question":"Which scope?","options":[{"id":"user","label":"user"},{"id":"project","label":"project"}]}`
	// proposeArgs is a schema-valid propose call (done_when is required).
	proposeArgs = `{"problem":"Add a widget.","done_when":["go test ./... is green"],"steps":["Add the widget"]}`

	// disabledText must reach the model; noCallbackText must not.
	disabledText   = "rephrase as a plain-text question"
	noCallbackText = "no callback registered"
)

// askLaneChild is one running print-mode child plus its stdout pump.
type askLaneChild struct {
	cmd    *exec.Cmd
	lines  chan jsonLine
	stderr strings.Builder
	done   chan error
}

// startAskLaneChild runs the compiled binary in print mode (no
// --approval-stdin: ask_user is not permission-gated, so it runs and can
// fail on its own terms).
func startAskLaneChild(t *testing.T, backendURL, workDir string) *askLaneChild {
	t.Helper()
	bin := buildSeekBin(t)
	cmd := exec.Command(bin,
		"-p", "问一个问题",
		"--json",
		"--provider", "compatible",
		"--base-url", backendURL+"/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-save",
		"--no-cron-tick",
		"--no-project-md",
	)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"OPENAI_API_KEY=e2e-key",
		// Same seam as startACPChild: the startup catalogue gate keys off
		// the OpenCode CREDENTIAL (c_nfeloo), so an ambient
		// OPENCODE_API_KEY in the developer's shell must not make these
		// children fetch the catalogue from opencode.ai.
		"OPENCODE_API_KEY=",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	c := &askLaneChild{cmd: cmd, lines: make(chan jsonLine, 64), done: make(chan error, 1)}
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
				continue // non-JSON stdout = contract break; other lanes assert it
			}
			c.lines <- l
		}
	}()
	go func() { c.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
		}
	})
	return c
}

// TestAskUserLane_PrintModeSaysRephrase is the print lane: the tool call
// happens (ask_user is not permission-gated), and the tool result must
// be the actionable ErrDisabled text rather than the programming-error
// text.
func TestAskUserLane_PrintModeSaysRephrase(t *testing.T) {
	for _, tc := range []struct{ name, tool, args string }{
		{"ask_user", "ask_user", askUserArgs},
		{"propose", "propose", proposeArgs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			b := askToolBackend(t, tc.tool, tc.args)
			c := startAskLaneChild(t, b.URL, workDir)

			var sawTool bool
			for !sawTool {
				select {
				case l, ok := <-c.lines:
					if !ok {
						t.Fatalf("stdout closed before %s finished\nstderr:\n%s", tc.tool, c.stderr.String())
					}
					if l.Type != "tool_end" || l.Name != tc.tool {
						continue
					}
					sawTool = true
					if strings.Contains(l.Error, noCallbackText) {
						t.Fatalf("%s error = %q — a headless lane must report ErrDisabled, not a programming error", tc.tool, l.Error)
					}
					if !strings.Contains(l.Error, disabledText) {
						t.Fatalf("%s error = %q, want the ErrDisabled text (%q) so the model rephrases", tc.tool, l.Error, disabledText)
					}
				case <-time.After(60 * time.Second):
					t.Fatalf("no %s tool_end within 60s\nstderr:\n%s", tc.tool, c.stderr.String())
				}
			}
			select {
			case err := <-c.done:
				if err != nil {
					t.Fatalf("child exit: %v\nstderr:\n%s", err, c.stderr.String())
				}
			case <-time.After(60 * time.Second):
				t.Fatalf("child did not exit after agent_end\nstderr:\n%s", c.stderr.String())
			}
		})
	}
}

// TestAskUserLane_ACPTimeoutsToCancelled is the ACP lane's no-ANSWER
// ending (card c_7bqiq5 changed what this lane does: an ACP session
// always has a human, so the question now travels over
// session/request_permission instead of failing as ErrDisabled). A client
// that never answers must not wedge the turn: SEEK_ACP_ASK_TIMEOUT=1
// bounds the wait and the tool result becomes a cancelled answer.
func TestAskUserLane_ACPTimeoutsToCancelled(t *testing.T) {
	workDir := t.TempDir()
	b := askToolBackend(t, "ask_user", askUserArgs)
	c := startACPChild(t, b.URL, workDir, "SEEK_ACP_ASK_TIMEOUT=1")
	sid := c.handshake(t, workDir) // no capabilities → request_permission fallback

	c.rpc(t, 3, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]string{{"type": "text", "text": "问一个问题"}},
	})
	// The question must actually reach the client — that is the whole
	// difference from the pre-c_7bqiq5 ErrDisabled dead end.
	c.waitWhere(t, "session/request_permission", 60*time.Second, isPermissionRequest)

	// Stare at it: the deadline must land as a cancelled tool result, not
	// a hang and not the ErrDisabled text (the human was asked).
	c.awaitToolOutcome(t, `"cancelled":true`)
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn (an unanswered ask must not kill the turn)", reason)
	}
	if strings.Contains(c.stderrText(), noCallbackText) {
		t.Fatalf("lane leaked the programming-error text:\n%s", c.stderrText())
	}
}
