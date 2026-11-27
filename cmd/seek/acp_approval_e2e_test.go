package main

// ACP·P0-2 acceptance ① end to end through the REAL binary (backlog ①):
// `seek acp` against the same fake OpenAI-compatible backend the print
// lane's approval e2e uses (approval_e2e_test.go), with the lane pinned
// via env (SEEK_ACP_PROVIDER=glm + GLM_BASE_URL — flags can't reach the
// ACP lane). The test IS the ACP client: it drives
// initialize/session/new/session/prompt, then answers the
// session/request_permission frame seek sends mid-turn.
//
// Four lanes:
//
//	① allow_once → the gated bash RUNS (tool_call_update carries the
//	   echoed marker) and the turn ends end_turn.
//	② reject_once → the tool result is the ErrDenied text; the run
//	   still completes cleanly (a denial never kills the turn).
//	③ timeout (SEEK_ACP_PERMISSION_TIMEOUT=1, no answer) → default
//	   DENY after the deadline; the run completes.
//	④ SEEK_ACP_YOLO=1 → no permission frame is ever sent (the
//	   short-circuit's priority); bash just runs. P0-2 ③.
//
// Sandbox-only: httptest backend + temp SEEK_HOME/HOME, no keys, no
// network.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// acpChild is one running `seek acp` plus the ndJson client handles.
type acpChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan map[string]json.RawMessage // every stdout ndJson line

	stderrMu sync.Mutex
	stderr   strings.Builder
	done     chan error
}

// stderrText snapshots the child's stderr so far (the pump goroutine
// keeps writing; failure messages read it concurrently).
func (c *acpChild) stderrText() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return c.stderr.String()
}

// stderrWriter serialises writes into c.stderr (exec's pump runs on
// its own goroutine; strings.Builder is not concurrency-safe).
type stderrWriter struct{ c *acpChild }

func (w stderrWriter) Write(p []byte) (int, error) {
	w.c.stderrMu.Lock()
	defer w.c.stderrMu.Unlock()
	return w.c.stderr.Write(p)
}

// startACPChild runs the compiled binary on the ACP lane against the
// fake backend. extraEnv is appended after the isolating baseline.
func startACPChild(t *testing.T, backendURL, workDir string, extraEnv ...string) *acpChild {
	t.Helper()
	bin := buildSeekBin(t)
	cmd := exec.Command(bin, "acp")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"SEEK_ACP_PROVIDER=glm",
		"SEEK_ACP_MODEL=e2e-model",
		"GLM_API_KEY=e2e-key",
		"GLM_BASE_URL="+backendURL+"/v1/chat/completions",
		"ANTHROPIC_API_KEY=",
		"OPENAI_API_KEY=",
		"GEMINI_API_KEY=",
		// The startup catalogue gate now keys off the CREDENTIAL, not the
		// launch lane (c_nfeloo): an ambient OPENCODE_API_KEY in the
		// developer's shell would otherwise make these children read the
		// catalogue cache and refresh from opencode.ai. SEEK_HOME/HOME
		// already isolate the stored browser login; this closes the env
		// half of the same seam.
		"OPENCODE_API_KEY=",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	c := &acpChild{cmd: cmd, stdin: stdin, frames: make(chan map[string]json.RawMessage, 128), done: make(chan error, 1)}
	cmd.Stderr = stderrWriter{c}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() {
		defer close(c.frames)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue // non-JSON stdout = contract break; smoke asserts it
			}
			c.frames <- m
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

// rpc writes one JSON-RPC request line.
func (c *acpChild) rpc(t *testing.T, id int, method string, params any) {
	t.Helper()
	pb, _ := json.Marshal(params)
	line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`+"\n", id, method, pb)
	if _, err := c.stdin.Write([]byte(line)); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
}

// waitWhere scans frames until pred matches (other frames are dropped);
// label names the wait in failure messages.
func (c *acpChild) waitWhere(t *testing.T, label string, timeout time.Duration, pred func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-c.frames:
			if !ok {
				t.Fatalf("stdout closed while waiting for %s\nstderr:\n%s", label, c.stderrText())
			}
			if pred(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s within %s\nstderr:\n%s", label, timeout, c.stderrText())
		}
	}
}

// handshake drives initialize + session/new and returns the sessionId.
func (c *acpChild) handshake(t *testing.T, workDir string) string {
	t.Helper()
	c.rpc(t, 1, "initialize", map[string]any{"protocolVersion": 1})
	c.waitWhere(t, "initialize response", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], "1")
	})
	c.rpc(t, 2, "session/new", map[string]any{"cwd": workDir})
	m := c.waitWhere(t, "session/new response", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], "2")
	})
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(m["result"], &res); err != nil || res.SessionID == "" {
		t.Fatalf("session/new result = %s (%v)", m["result"], err)
	}
	return res.SessionID
}

func rawEq(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == want
	}
	var n json.Number // the server echoes numeric ids verbatim
	return json.Unmarshal(raw, &n) == nil && n.String() == want
}

// isPermissionRequest matches a session/request_permission frame.
func isPermissionRequest(m map[string]json.RawMessage) bool {
	return rawEq(m["method"], "session/request_permission")
}

// answerPermission replies to one request_permission frame with the
// given optionId (v1 schema's nested selected outcome).
func (c *acpChild) answerPermission(t *testing.T, frame map[string]json.RawMessage, optionID string) {
	t.Helper()
	id := frame["id"]
	if len(id) == 0 {
		t.Fatalf("permission frame without id: %v", frame)
	}
	reply := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":{"outcome":"selected","optionId":%q}}}`+"\n", id, optionID)
	if _, err := c.stdin.Write([]byte(reply)); err != nil {
		t.Fatalf("write permission answer: %v", err)
	}
}

// runOneTurn sends the prompt that triggers the fake backend's single
// bash call and returns after the turn's response frame arrives. The
// caller handles the permission frame IN BETWEEN (prompt responses are
// async: they land only when the turn ends).
func (c *acpChild) runOneTurn(t *testing.T, sessionID string, handlePerm func(frame map[string]json.RawMessage)) {
	t.Helper()
	c.rpc(t, 3, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "请跑一次 echo"}},
	})
	if handlePerm != nil {
		frame := c.waitWhere(t, "session/request_permission", 60*time.Second, isPermissionRequest)
		// The frame must carry the standard menu + the session + an
		// execute-kind subject (the gated action is bash).
		var p struct {
			SessionID string `json:"sessionId"`
			ToolCall  struct {
				Kind string `json:"kind"`
			} `json:"toolCall"`
			Options []struct {
				OptionID string `json:"optionId"`
			} `json:"options"`
		}
		if err := json.Unmarshal(frame["params"], &p); err != nil {
			t.Fatalf("permission params: %v (%s)", err, frame["params"])
		}
		if p.SessionID != sessionID || p.ToolCall.Kind != "execute" || len(p.Options) != 3 {
			t.Fatalf("permission params = %+v, want session %s / execute / 3 options", p, sessionID)
		}
		handlePerm(frame)
	}
}

// awaitTurnEnd waits for the prompt response (id 3) and returns its
// stopReason.
func (c *acpChild) awaitTurnEnd(t *testing.T) string {
	t.Helper()
	m := c.waitWhere(t, "session/prompt response", 90*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], "3")
	})
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(m["result"], &res); err != nil {
		t.Fatalf("prompt response = %s (%v)", m["result"], err)
	}
	return res.StopReason
}

// awaitToolOutcome waits for the bash tool_call_update whose rawOutput
// contains marker, and returns it.
func (c *acpChild) awaitToolOutcome(t *testing.T, marker string) {
	t.Helper()
	c.waitWhere(t, "tool_call_update containing "+marker, 60*time.Second, func(m map[string]json.RawMessage) bool {
		if !rawEq(m["method"], "session/update") || !strings.Contains(string(m["params"]), "tool_call_update") {
			return false
		}
		return strings.Contains(string(m["params"]), marker)
	})
}

// assertNoPermissionFrameEver drains any frames still in flight and
// fails if any is a permission request (the yolo lane must never send
// one).
func (c *acpChild) assertNoPermissionFrameEver(t *testing.T) {
	t.Helper()
	// Give late frames a moment, then drain.
	time.Sleep(500 * time.Millisecond)
	for {
		select {
		case m, ok := <-c.frames:
			if !ok {
				return
			}
			if isPermissionRequest(m) {
				t.Fatalf("permission frame sent on the yolo lane: %v", m)
			}
		default:
			return
		}
	}
}

// Lane ①: allow_once releases the gated bash call.
func TestACPApprovalE2E_AllowOnceRunsTool(t *testing.T) {
	workDir := t.TempDir()
	b := newApprovalBackend(t)
	c := startACPChild(t, b.srv.URL, workDir)
	sid := c.handshake(t, workDir)

	c.runOneTurn(t, sid, func(frame map[string]json.RawMessage) {
		c.answerPermission(t, frame, "allow_once")
	})
	c.awaitToolOutcome(t, "approval-e2e")
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
}

// Lane ②: reject_once denies; the turn still completes.
func TestACPApprovalE2E_RejectDeniesButCompletes(t *testing.T) {
	workDir := t.TempDir()
	b := newApprovalBackend(t)
	c := startACPChild(t, b.srv.URL, workDir)
	sid := c.handshake(t, workDir)

	c.runOneTurn(t, sid, func(frame map[string]json.RawMessage) {
		c.answerPermission(t, frame, "reject_once")
	})
	// The ErrDenied text is the tool result the model sees; the turn
	// must still end cleanly.
	c.awaitToolOutcome(t, "permission denied")
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn (a denial must not kill the turn)", reason)
	}
}

// Lane ③: no answer at all → the (1s) deadline denies by default and
// the run completes — never a hang.
func TestACPApprovalE2E_TimeoutDefaultsToDeny(t *testing.T) {
	workDir := t.TempDir()
	b := newApprovalBackend(t)
	c := startACPChild(t, b.srv.URL, workDir, "SEEK_ACP_PERMISSION_TIMEOUT=1")
	sid := c.handshake(t, workDir)

	c.runOneTurn(t, sid, func(frame map[string]json.RawMessage) {
		// Stare in silence: the request must time out into a denial.
	})
	c.awaitToolOutcome(t, "permission denied")
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
}

// Lane ④: SEEK_ACP_YOLO=1 short-circuits first — no permission frame
// is ever sent (P0-2 ③), bash just runs.
func TestACPApprovalE2E_YoloNeverAsks(t *testing.T) {
	workDir := t.TempDir()
	b := newApprovalBackend(t)
	c := startACPChild(t, b.srv.URL, workDir, "SEEK_ACP_YOLO=1")
	sid := c.handshake(t, workDir)

	c.runOneTurn(t, sid, nil) // no handler: a permission frame here fails the wait
	c.awaitToolOutcome(t, "approval-e2e")
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
	c.assertNoPermissionFrameEver(t)
}
