package main

// ACP·P1-2 (card c_5flg7r, backlog ③) end-to-end: TWO sessions crossing
// on ONE `seek acp` stdio connection, against a fake OpenAI-compatible
// backend that scripts by conversation STATE (the body it is sent), not
// request count — so the two interleaved sessions are indistinguishable
// to it and every assertion lands on what seek actually did per session.
//
// The story (acceptance ①② on the real wire):
//
//	new A (dirA) + new B (dirB)      → distinct durable ids, one process
//	prompt A ∥ prompt B (in flight)  → turns serialise on the turn gate;
//	  each session's bash pwd prints ITS directory (cwd axis), each
//	  final answer summarises ITS OWN user messages (history axis)
//	set_mode plan on A only          → A's write denied, B's write lands
//	                                  (workflow axis, plan-vs-default)
//	prompt A again                   → A's history continues across the
//	  B turns: "seen=3" with no beta content (history survives switches)
//
// The lane axis has its real-binary half in the existing single-session
// switch tests; the in-process FourAxes test covers the cross-session
// restore. SEEK_ACP_YOLO=1 keeps bash/write off the approval lane —
// plan's denial is workflow-level and fires under any pref.
//
// Sandbox-only: httptest backend + temp SEEK_HOME/HOME, no keys, no
// network.

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

// multiSessionBackend scripts the LLM from the conversation's own shape:
//
//	last message is a tool result → wrap up, answering with a summary of
//	  the user messages this conversation carries ("seen=<n>:<last>")
//	  — the history assertion rides this
//	last user message says "write attempt" → ONE write tool call
//	  (relative marker.txt — the cwd assertion rides where it lands /
//	  is denied)
//	otherwise (fresh turn) → ONE bash pwd tool call — the cwd assertion
//	  rides its rawOutput
type multiSessionBackend struct {
	srv *httptest.Server
}

func newMultiSessionBackend(t *testing.T) *multiSessionBackend {
	t.Helper()
	b := &multiSessionBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case endsInToolResult(raw):
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(raw, &req)
			users, last := 0, ""
			for _, m := range req.Messages {
				if m.Role == "user" {
					users++
					last = m.Content
				}
			}
			// The summary rides through json.Marshal — the user content
			// carries appended reminders with newlines, and a raw
			// Sprintf would split the SSE frame.
			chunk, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"delta":         map[string]any{"content": fmt.Sprintf("seen=%d:%s", users, last)},
					"finish_reason": nil,
				}},
			})
			_, _ = io.WriteString(w, "data: "+string(chunk)+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":3}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.Contains(lastUserContent(raw), "write attempt"):
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w1","type":"function","function":{"name":"write","arguments":"{\"path\":\"marker.txt\",\"content\":\"multi-session-e2e\"}"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"p1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// frameLog records EVERY frame the child emits (waitWhere drops
// non-matching frames; with two sessions crossing, a frame dropped while
// waiting for the other session's response would be lost to the later
// assertion). wait polls the log until pred matches some frame.
type frameLog struct {
	mu     sync.Mutex
	frames []map[string]json.RawMessage
}

func (l *frameLog) add(m map[string]json.RawMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frames = append(l.frames, m)
}

func (l *frameLog) snapshot() []map[string]json.RawMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]map[string]json.RawMessage, len(l.frames))
	copy(out, l.frames)
	return out
}

// wait polls until pred matches a recorded frame and returns it.
func (l *frameLog) wait(t *testing.T, label string, timeout time.Duration, pred func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range l.snapshot() {
			if pred(m) {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s within %s\nframes so far: %s", label, timeout, l.dump())
	return nil
}

func (l *frameLog) dump() string {
	var b strings.Builder
	for _, m := range l.snapshot() {
		raw, _ := json.Marshal(m)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// isUpdateFor matches a session/update notification for one session.
func isUpdateFor(m map[string]json.RawMessage, sid string) bool {
	if !rawEq(m["method"], "session/update") {
		return false
	}
	return strings.Contains(string(m["params"]), `"sessionId":"`+sid+`"`)
}

// lastAgentChunk returns the last agent_message_chunk text recorded for
// the session (the turn's final answer).
func (l *frameLog) lastAgentChunk(t *testing.T, sid string) string {
	t.Helper()
	var text string
	for _, m := range l.snapshot() {
		if !isUpdateFor(m, sid) || !strings.Contains(string(m["params"]), "agent_message_chunk") {
			continue
		}
		var p struct {
			Update struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(m["params"], &p); err == nil && p.Update.Content.Text != "" {
			text = p.Update.Content.Text
		}
	}
	if text == "" {
		t.Fatalf("no agent_message_chunk recorded for %s\n%s", sid, l.dump())
	}
	return text
}

// sessionUpdateContaining waits for a session/update for sid whose params
// contain marker (tool outcome / location assertions).
func (l *frameLog) sessionUpdateContaining(t *testing.T, sid, marker string, contains bool) map[string]json.RawMessage {
	t.Helper()
	return l.wait(t, fmt.Sprintf("update for %s containing %q (contains=%v)", sid, marker, contains), 60*time.Second, func(m map[string]json.RawMessage) bool {
		if !isUpdateFor(m, sid) {
			return false
		}
		if contains {
			return strings.Contains(string(m["params"]), marker)
		}
		return !strings.Contains(string(m["params"]), marker)
	})
}

// awaitPromptResponse waits for the session/prompt response with the id.
func (l *frameLog) awaitPromptResponse(t *testing.T, id string) {
	t.Helper()
	m := l.wait(t, "session/prompt response id="+id, 120*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], id) && (len(m["result"]) > 0 || len(m["error"]) > 0)
	})
	if len(m["error"]) > 0 {
		t.Fatalf("session/prompt %s errored: %s\n%s", id, m["error"], l.dump())
	}
	if !strings.Contains(string(m["result"]), `"end_turn"`) {
		t.Fatalf("session/prompt %s result = %s, want end_turn", id, m["result"])
	}
}

// newSessionOn sends one session/new (the handshake initialize is done
// once by the caller) and returns the sessionId.
func newSessionOn(t *testing.T, c *acpChild, id int, cwd string) string {
	t.Helper()
	c.rpc(t, id, "session/new", map[string]any{"cwd": cwd})
	m := c.waitWhere(t, "session/new response", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], fmt.Sprintf("%d", id)) && len(m["result"]) > 0
	})
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(m["result"], &res); err != nil || res.SessionID == "" {
		t.Fatalf("session/new result = %s (%v)", m["result"], err)
	}
	return res.SessionID
}

// TestACPMultiSessionE2E_TwoSessionsCrossing is the acceptance fixture:
// two sessions, concurrent prompts, four axes asserted on the real wire.
func TestACPMultiSessionE2E_TwoSessionsCrossing(t *testing.T) {
	backend := newMultiSessionBackend(t)
	// Yolo lane: bash/write never reach the approval lane; plan's
	// workflow denial is pref-independent.
	c := startACPChild(t, backend.srv.URL, t.TempDir(), "SEEK_ACP_YOLO=1")

	dirA, dirB := t.TempDir(), t.TempDir()
	resA, errA := filepath.EvalSymlinks(dirA)
	resB, errB := filepath.EvalSymlinks(dirB)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}

	c.rpc(t, 1, "initialize", map[string]any{"protocolVersion": 1})
	c.waitWhere(t, "initialize response", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return rawEq(m["id"], "1")
	})
	sidA := newSessionOn(t, c, 2, dirA)
	sidB := newSessionOn(t, c, 3, dirB)
	if sidA == sidB {
		t.Fatalf("second session/new returned the same id %q — must mint a new session", sidA)
	}
	for sid, which := range map[string]string{sidA: "A", sidB: "B"} {
		if len(sid) < 10 || !strings.Contains(sid, "-") {
			t.Fatalf("session %s id %q does not look durable", which, sid)
		}
	}

	// Take over the frame stream: everything the child emits lands in
	// the log (waitWhere drops frames — not acceptable with two
	// sessions crossing). The drain goroutine ends when the cleanup
	// closes the child; nothing waits on it (a failure-path defer that
	// blocked on the drain would deadlock before cleanup could run).
	log := &frameLog{}
	go func() {
		for m := range c.frames {
			log.add(m)
		}
	}()

	// ② Two prompts in flight at once — turns serialise on the gate.
	c.rpc(t, 10, "session/prompt", map[string]any{
		"sessionId": sidA,
		"prompt":    []map[string]string{{"type": "text", "text": "alpha-one"}},
	})
	c.rpc(t, 11, "session/prompt", map[string]any{
		"sessionId": sidB,
		"prompt":    []map[string]string{{"type": "text", "text": "beta-one"}},
	})
	log.awaitPromptResponse(t, "10")
	log.awaitPromptResponse(t, "11")

	// cwd axis, on the wire: each session's bash pwd printed ITS dir.
	mA := log.sessionUpdateContaining(t, sidA, resA, true)
	mB := log.sessionUpdateContaining(t, sidB, resB, true)
	if !strings.Contains(string(mA["params"]), `"tool_call_update"`) || !strings.Contains(string(mB["params"]), `"tool_call_update"`) {
		t.Fatalf("pwd outcome did not arrive as a tool_call_update:\n%s", log.dump())
	}
	if strings.Contains(string(mA["params"]), resB) {
		t.Fatalf("session A's turn saw B's directory — cwd cross-talk:\n%s", log.dump())
	}
	if strings.Contains(string(mB["params"]), resA) {
		t.Fatalf("session B's turn saw A's directory — cwd cross-talk:\n%s", log.dump())
	}

	// history axis, on the wire: each final answer summarises ITS OWN
	// user messages (backend counts them from the request body).
	if got := log.lastAgentChunk(t, sidA); !strings.Contains(got, "seen=1:alpha-one") {
		t.Fatalf("A's final answer = %q, want seen=1:alpha-one (its own history only)", got)
	}
	if got := log.lastAgentChunk(t, sidB); !strings.Contains(got, "seen=1:beta-one") {
		t.Fatalf("B's final answer = %q, want seen=1:beta-one (its own history only)", got)
	}

	// workflow axis: plan on A only. A's write is denied by the
	// plan-analyze gate; B's identical write executes in ITS directory.
	c.rpc(t, 6, "session/set_mode", map[string]any{"sessionId": sidA, "modeId": "plan"})
	log.wait(t, "current_mode_update(plan)", 60*time.Second, func(m map[string]json.RawMessage) bool {
		return isUpdateFor(m, sidA) && strings.Contains(string(m["params"]), `"currentModeId":"plan"`)
	})

	c.rpc(t, 12, "session/prompt", map[string]any{
		"sessionId": sidA,
		"prompt":    []map[string]string{{"type": "text", "text": "write attempt alpha"}},
	})
	log.awaitPromptResponse(t, "12")
	log.sessionUpdateContaining(t, sidA, "plan mode: write is not allowed", true)
	if _, err := os.Stat(filepath.Join(dirA, "marker.txt")); err == nil {
		t.Fatalf("plan-mode session A must not write marker.txt into %s", dirA)
	}

	c.rpc(t, 13, "session/prompt", map[string]any{
		"sessionId": sidB,
		"prompt":    []map[string]string{{"type": "text", "text": "write attempt beta"}},
	})
	log.awaitPromptResponse(t, "13")
	log.sessionUpdateContaining(t, sidB, "plan mode: write is not allowed", false)
	if _, err := os.Stat(filepath.Join(dirB, "marker.txt")); err != nil {
		t.Fatalf("default-mode session B's write must land in %s: %v", dirB, err)
	}

	// history axis, continued: A's next turn sees A's OWN three user
	// messages (alpha-one, write attempt alpha, alpha-three) — the beta
	// turns never leak in, and A's history survived the B switches.
	c.rpc(t, 14, "session/prompt", map[string]any{
		"sessionId": sidA,
		"prompt":    []map[string]string{{"type": "text", "text": "alpha-three"}},
	})
	log.awaitPromptResponse(t, "14")
	if got := log.lastAgentChunk(t, sidA); !strings.Contains(got, "seen=3:") || !strings.Contains(got, "alpha-three") {
		t.Fatalf("A's continued answer = %q, want seen=3:…alpha-three", got)
	}
	if strings.Contains(log.lastAgentChunk(t, sidA), "beta") {
		t.Fatalf("beta content leaked into A's history:\n%s", log.dump())
	}
}
