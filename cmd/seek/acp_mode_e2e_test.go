package main

// ACP·P2 acceptance ③ — the two-state fixture, end to end through the
// REAL binary: one `seek acp` process, one session, both session-mode
// states driven over the protocol, with the SAME fake-LLM script (a
// write tool call) proving the prompt behaves differently per state.
//
//	state plan   (session/set_mode plan):  the write is denied by the
//	  plan-analyze gate WITHOUT a permission request (PlanAnalyze is a
//	  terminal read-only rule set — askFn is never consulted), the file
//	  does NOT land on disk (验收③ "plan 态走 propose 不落盘"), and the
//	  LLM request carries the plan-analyze mode reminder (the prefix-
//	  cache-safe per-turn label, i.e. the propose instruction).
//	state default (session/set_mode default): the same write EXECUTES
//	  (prefGate classes an inside-cwd write as not dangerous, and the
//	  workflow axis no longer vetoes it) — the file EXISTS on disk and
//	  the request no longer carries a plan reminder.
//
// The contrast is the point: mode is the only variable — same session,
// same model, same tool call.
//
// Sandbox-only: httptest backend + temp SEEK_HOME/HOME, no keys, no
// network. SEEK_ACP_PERMISSION_TIMEOUT=3 keeps a wrongly-asked
// permission (a plan-state regression) a fast denial instead of a
// 600 s hang.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// modeBackend scripts the LLM by conversation STATE, not request count
// (turn 2's first request legitimately carries turn 1's tool result in
// history, so "history mentions a tool" is not a discriminator), and
// records every request body so the fixture can assert what the MODEL
// saw per state:
//
//	request whose LAST message is the fresh user turn → the SAME
//	write tool call (the only variable under test is the mode)
//	request ending in a tool result (the write came back) → "done."
type modeBackend struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   int
	bodies []string
}

func (b *modeBackend) body(i int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bodies[i]
}

func (b *modeBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reqs
}

// endsInToolResult reports whether the request's last message is a
// tool result (the write already came back and the model must wrap up).
func endsInToolResult(body []byte) bool {
	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		return false // unparsable → treat as a fresh turn (asks for the write)
	}
	return req.Messages[len(req.Messages)-1].Role == "tool"
}

// lastUserContent returns the content of the request's LAST user
// message. The mode reminder rides the CURRENT turn's user message
// (history is immutable — prefix cache), so "does this turn carry the
// plan reminder" must be asked of exactly this message, not of the
// whole body: turn 1's reminder stays in turn 2's history forever.
func lastUserContent(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	return ""
}

func newModeBackend(t *testing.T) *modeBackend {
	t.Helper()
	b := &modeBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.reqs++
		b.bodies = append(b.bodies, string(raw))
		b.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if !endsInToolResult(raw) {
			// Fresh user turn → ask for the write.
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w1","type":"function","function":{"name":"write","arguments":"{\"path\":\"plan-e2e.txt\",\"content\":\"written-by-e2e\"}"}}]},"finish_reason":null}]}`+"\n\n")
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

// setMode sends session/set_mode and asserts the contract: a
// current_mode_update notification FIRST, then a `{}` response.
func (c *acpChild) setMode(t *testing.T, id int, sessionID, mode string) {
	t.Helper()
	c.rpc(t, id, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": mode})
	notif := c.waitWhere(t, "current_mode_update("+mode+")", 15*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["method"], "session/update") && strings.Contains(string(m["params"]), "current_mode_update") &&
			strings.Contains(string(m["params"]), `"currentModeId":"`+mode+`"`)
	})
	if !strings.Contains(string(notif["params"]), `"sessionId":"`+sessionID+`"`) {
		t.Fatalf("current_mode_update sessionId mismatch: %s", notif["params"])
	}
	resp := c.waitWhere(t, "set_mode response", 15*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], fmt.Sprint(id))
	})
	if string(resp["result"]) != "{}" {
		t.Fatalf("set_mode(%s) result = %s, want {}", mode, resp["result"])
	}
}

// TestACPModeE2E_PlanDeniesDefaultWrites is the two-state fixture
// (acceptance ③): set plan → prompt → denied + no disk write + plan
// reminder in the LLM request; exit plan → same prompt → permission
// frame → allow_once → disk write, no reminder.
func TestACPModeE2E_PlanDeniesDefaultWrites(t *testing.T) {
	workDir := t.TempDir()
	b := newModeBackend(t)
	c := startACPChild(t, b.srv.URL, workDir, "SEEK_ACP_PERMISSION_TIMEOUT=3")

	// Handshake inline (not c.handshake) because the fixture also
	// asserts session/new's `modes` capability advertisement.
	c.rpc(t, 1, "initialize", map[string]any{"protocolVersion": 1})
	c.waitWhere(t, "initialize response", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], "1")
	})
	c.rpc(t, 2, "session/new", map[string]any{"cwd": workDir})
	newRes := c.waitWhere(t, "session/new response", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], "2")
	})
	var ns struct {
		SessionID string `json:"sessionId"`
		Modes     struct {
			CurrentModeID  string `json:"currentModeId"`
			AvailableModes []struct {
				ID string `json:"id"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(newRes["result"], &ns); err != nil {
		t.Fatalf("session/new result: %v (%s)", err, newRes["result"])
	}
	if ns.SessionID == "" {
		t.Fatalf("session/new gave no sessionId: %s", newRes["result"])
	}
	if ns.Modes.CurrentModeID != "default" || len(ns.Modes.AvailableModes) != 2 ||
		ns.Modes.AvailableModes[0].ID != "default" || ns.Modes.AvailableModes[1].ID != "plan" {
		t.Fatalf("session/new modes = %s, want current=default + [default, plan]", newRes["result"])
	}
	sid := ns.SessionID
	target := filepath.Join(workDir, "plan-e2e.txt")

	// --- state 1: plan ------------------------------------------------
	c.setMode(t, 3, sid, "plan")

	c.rpc(t, 4, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]string{{"type": "text", "text": "请写一个文件"}},
	})
	// The plan-analyze gate denies WITHOUT consulting the client: the
	// denial text steers the model to propose instead ("走 propose"),
	// and no permission frame may appear (a regression would deny via
	// the 3 s timeout with the wrong text and fail the marker wait).
	c.awaitToolOutcome(t, "plan mode: write is not allowed")
	if reason := c.awaitTurnEndID(t, 4); reason != "end_turn" {
		t.Fatalf("plan-state stopReason = %q, want end_turn (a denial never kills the turn)", reason)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("plan state wrote to disk: %s exists (err=%v)", target, err)
	}
	if uc := lastUserContent([]byte(b.body(0))); !strings.Contains(uc, "[Mode: plan-analyze") {
		t.Fatalf("plan-state turn's user message lacks the plan-analyze reminder:\n%.400s", uc)
	}

	// --- state 2: default (退出 plan) ----------------------------------
	c.setMode(t, 5, sid, "default")
	turn2First := b.count() // turn 2's first LLM request index

	c.rpc(t, 6, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]string{{"type": "text", "text": "请写一个文件"}},
	})
	// The same write now EXECUTES: prefGate classes an inside-cwd write
	// as not dangerous (no ask), and the workflow no longer vetoes it —
	// the file lands on disk. (An outside-cwd write would additionally
	// ride the session/request_permission lane; that axis is the
	// approval e2e's subject, not this fixture's.)
	c.awaitToolOutcome(t, "wrote 14 bytes to")
	if reason := c.awaitTurnEndID(t, 6); reason != "end_turn" {
		t.Fatalf("default-state stopReason = %q, want end_turn", reason)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "written-by-e2e" {
		t.Fatalf("default state did not write the file: %s err=%v content=%q", target, err, got)
	}
	if uc := lastUserContent([]byte(b.body(turn2First))); strings.Contains(uc, "[Mode: plan") {
		t.Fatalf("default-state turn's user message still carries a plan reminder:\n%.400s", uc)
	}
}

// awaitTurnEndID is awaitTurnEnd for an arbitrary prompt id (this
// fixture uses several ids in one session).
func (c *acpChild) awaitTurnEndID(t *testing.T, id int) string {
	t.Helper()
	m := c.waitWhere(t, "session/prompt response", 90*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], fmt.Sprint(id))
	})
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(m["result"], &res); err != nil {
		t.Fatalf("prompt response = %s (%v)", m["result"], err)
	}
	return res.StopReason
}
