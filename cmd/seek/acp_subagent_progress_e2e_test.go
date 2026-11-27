package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// subagentProgressBackend drives a real `seek acp` child through one turn
// that spawns a sync subagent. Three model calls:
//  1. parent → one `agent` tool call
//  2. child  → a text delta containing the progress marker (the child's
//     system prompt names it "You are a subagent spawned by the parent
//     agent for", which distinguishes it from the parent's request)
//  3. parent → final answer after the tool result lands
//
// Sandbox-only: httptest backend, no keys, no network.
type subagentProgressBackend struct{ srv *httptest.Server }

const subagentProgressMarker = "SUBAGENT_PROGRESS_E2E"

func newSubagentProgressBackend(t *testing.T) *subagentProgressBackend {
	t.Helper()
	b := &subagentProgressBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.Contains(string(raw), "You are a subagent spawned by the parent agent for"):
			// Child model streams one content delta; the Runner's pump
			// coalesces it and flushes at TurnEnd into the parent's ACP
			// stream as agent_thought_chunk.
			chunk, _ := jsonMarshalSSE(map[string]any{
				"choices": []map[string]any{{
					"delta":         map[string]any{"content": "child says " + subagentProgressMarker},
					"finish_reason": nil,
				}},
			})
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case endsInToolResult(raw):
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"done."},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":3}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			args, _ := jsonMarshalSSE(map[string]any{"description": "scan deps", "prompt": "scan the deps"})
			chunk, _ := jsonMarshalSSE(map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{"tool_calls": []map[string]any{{
						"index": 0, "id": "a1", "type": "function",
						"function": map[string]any{"name": "agent", "arguments": string(args)},
					}}},
					"finish_reason": nil,
				}},
			})
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// jsonMarshalSSE is a tiny local helper so the SSE fixtures above stay
// readable — a raw JSON string with nested quotes is easy to get wrong.
func jsonMarshalSSE(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// TestACPSubagentProgressE2E: the child's intermediate text reaches the
// parent ACP session as a STANDARD agent_thought_chunk frame carrying the
// sub-sid prefix — the only process-text variant paseo renders from ACP.
func TestACPSubagentProgressE2E(t *testing.T) {
	workDir := t.TempDir()
	backend := newSubagentProgressBackend(t)
	c := startACPChild(t, backend.srv.URL, workDir, "SEEK_ACP_YOLO=1")
	sid := c.handshake(t, workDir)

	// Record every frame from here on (waitWhere consumes frames, so the
	// drain starts only after the handshake completes).
	log := &frameLog{}
	go func() {
		for m := range c.frames {
			log.add(m)
		}
	}()

	c.rpc(t, 10, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]string{{"type": "text", "text": "spawn a child"}},
	})
	log.awaitPromptResponse(t, "10")

	// The progress frame must be an agent_thought_chunk for the PARENT
	// session, carrying the child's text. A bare agent_message_chunk would
	// land in the answer area and mix with the parent's own final text.
	frame := log.wait(t, "agent_thought_chunk with subagent progress", 30*time.Second, func(m map[string]json.RawMessage) bool {
		if !isUpdateFor(m, sid) || !strings.Contains(string(m["params"]), `"agent_thought_chunk"`) {
			return false
		}
		return strings.Contains(string(m["params"]), subagentProgressMarker)
	})
	params := string(frame["params"])
	if !strings.Contains(params, "child says "+subagentProgressMarker) {
		t.Fatalf("progress frame did not carry the child's text: %s", params)
	}
	// Attribution: a "[<sub-sid>] " prefix must be present so interleaved
	// sibling spawns stay distinguishable in paseo. sub-sids are
	// "<timestamp>-<hash>" (see subagent's id generator), not a fixed word.
	if !strings.Contains(params, `text":"[`) || !strings.Contains(params, "] child says") {
		t.Fatalf("progress frame missing sub-sid prefix: %s", params)
	}
}
