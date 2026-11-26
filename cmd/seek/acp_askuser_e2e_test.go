package main

// The positive half of the ACP ask lane, end to end through the REAL
// binary (acp_askuser.go): a client that advertises
// `clientCapabilities.elicitation.form` gets the question as an
// `elicitation/create` request and its answer reaches the model as the
// tool result.
//
// The negative half already lives in askuser_lane_test.go: the same lane
// with NO capabilities declared must keep answering ErrDisabled. These
// two files pin both sides of the gate — advertising the capability is
// the only thing that flips the lane.
//
// Sandbox-only: httptest backend + temp SEEK_HOME/HOME, no keys, no
// network.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// askBackendWithTool answers the first request with one call to `tool`
// (arguments fixed by the caller), then "done.". Same shape as
// askToolBackend in askuser_lane_test but parameterised on the tool name,
// because the lane is shared by ask_user and propose.
func askBackendWithTool(t *testing.T, tool, args string) *httptest.Server {
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

// askBackendWithQuestion is the ask_user flavour of the above.
func askBackendWithQuestion(t *testing.T, args string) *httptest.Server {
	t.Helper()
	return askBackendWithTool(t, "ask_user", args)
}

const scopeQuestionArgs = `{"question":"Which scope?","options":[{"id":"user","label":"user"},{"id":"project","label":"project"}]}`

// handshakeWithFormElicitation drives initialize (advertising form
// elicitation) + session/new, returning the sessionId.
func (c *acpChild) handshakeWithFormElicitation(t *testing.T, workDir string) string {
	t.Helper()
	c.rpc(t, 1, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":          map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal":    true,
			"elicitation": map[string]any{"form": map[string]any{}},
		},
		"clientInfo": map[string]string{"name": "seek-e2e", "version": "test"},
	})
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

// isElicitationCreate matches an elicitation/create request frame.
func isElicitationCreate(m map[string]json.RawMessage) bool {
	return rawEq(m["method"], "elicitation/create")
}

// answerElicitation replies to one elicitation/create frame with an
// accept carrying content.
func (c *acpChild) answerElicitation(t *testing.T, frame map[string]json.RawMessage, content map[string]any) {
	t.Helper()
	id := frame["id"]
	if len(id) == 0 {
		t.Fatalf("elicitation frame without id: %v", frame)
	}
	body, _ := json.Marshal(map[string]any{"action": "accept", "content": content})
	reply := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, body)
	if _, err := c.stdin.Write([]byte(reply)); err != nil {
		t.Fatalf("write elicitation answer: %v", err)
	}
}

// startPromptForAsk sends the prompt that triggers the fake backend's
// ask_user call.
func (c *acpChild) startPromptForAsk(t *testing.T, sessionID string) {
	t.Helper()
	c.rpc(t, 3, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "问一个问题"}},
	})
}

// TestACPElicitationE2E_ClientAdvertisesForm_QuestionReachesUser is the
// whole point of acp_askuser.go: a client that advertises form
// elicitation gets the question, answers it, and the answer is what the
// MODEL sees in the tool result — not ErrDisabled.
func TestACPElicitationE2E_ClientAdvertisesForm_QuestionReachesUser(t *testing.T) {
	workDir := t.TempDir()
	b := askBackendWithQuestion(t, scopeQuestionArgs)
	c := startACPChild(t, b.URL, workDir)
	sid := c.handshakeWithFormElicitation(t, workDir)

	c.startPromptForAsk(t, sid)

	frame := c.waitWhere(t, "elicitation/create", 60*time.Second, isElicitationCreate)

	// The request must carry the schema shape the SDK validates:
	// session scope + mode "form" + a message + an object schema whose
	// q1 is a titled enum of the two options.
	var p struct {
		SessionID string `json:"sessionId"`
		Mode      string `json:"mode"`
		Message   string `json:"message"`
		Schema    struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type  string `json:"type"`
				Title string `json:"title"`
				OneOf []struct {
					Const string `json:"const"`
					Title string `json:"title"`
				} `json:"oneOf"`
			} `json:"properties"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(frame["params"], &p); err != nil {
		t.Fatalf("elicitation params: %v (%s)", err, frame["params"])
	}
	if p.SessionID != sid || p.Mode != "form" || p.Message == "" {
		t.Fatalf("params = %+v, want session %s / mode form / a message", p, sid)
	}
	if p.Schema.Type != "object" {
		t.Errorf("requestedSchema.type = %q, want object", p.Schema.Type)
	}
	q1, ok := p.Schema.Properties["q1"]
	if !ok {
		t.Fatalf("no q1 field; props = %v", p.Schema.Properties)
	}
	if q1.Type != "string" || len(q1.OneOf) != 2 {
		t.Fatalf("q1 = %+v, want a 2-option titled enum", q1)
	}
	if q1.OneOf[0].Const != "user" || q1.OneOf[1].Const != "project" {
		t.Errorf("q1 options = %+v, want user/project", q1.OneOf)
	}

	c.answerElicitation(t, frame, map[string]any{"q1": "project"})

	// The model's tool result must carry the picked id — that is the
	// observable difference between "the human answered" and the old
	// ErrDisabled path.
	c.awaitToolOutcome(t, `"chosen_ids":["project"]`)
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
}

// TestACPElicitationE2E_DeclineBecomesCancelled pins the decline ending:
// the tool result must be a cancelled answer (the tool layer's own
// contract), and the turn must still end cleanly — a refusal to answer
// is not an error.
func TestACPElicitationE2E_DeclineBecomesCancelled(t *testing.T) {
	workDir := t.TempDir()
	b := askBackendWithQuestion(t, scopeQuestionArgs)
	c := startACPChild(t, b.URL, workDir)
	sid := c.handshakeWithFormElicitation(t, workDir)

	c.startPromptForAsk(t, sid)
	frame := c.waitWhere(t, "elicitation/create", 60*time.Second, isElicitationCreate)

	id := frame["id"]
	if _, err := c.stdin.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"action":"decline"}}`+"\n", id))); err != nil {
		t.Fatalf("write decline: %v", err)
	}

	c.awaitToolOutcome(t, `"cancelled":true`)
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn (a decline must not kill the turn)", reason)
	}
}

// TestACPElicitationE2E_TimeoutBecomesCancelled is the never-hang guard:
// a client that advertises the capability but never answers must not
// wedge the turn. SEEK_ACP_ASK_TIMEOUT=1 bounds the wait; the tool then
// sees a cancelled answer and the run completes.
func TestACPElicitationE2E_TimeoutBecomesCancelled(t *testing.T) {
	workDir := t.TempDir()
	b := askBackendWithQuestion(t, scopeQuestionArgs)
	c := startACPChild(t, b.URL, workDir, "SEEK_ACP_ASK_TIMEOUT=1")
	sid := c.handshakeWithFormElicitation(t, workDir)

	c.startPromptForAsk(t, sid)
	// Stare in silence at the request; it must time out into a cancel.
	c.waitWhere(t, "elicitation/create", 60*time.Second, isElicitationCreate)

	c.awaitToolOutcome(t, `"cancelled":true`)
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
	if !strings.Contains(c.stderrText(), "ask_user unanswered") {
		t.Errorf("stderr should record why the picker never appeared:\n%s", c.stderrText())
	}
}

// TestACPElicitationE2E_NoCapabilityStaysDisabled is the gate's negative
// side, at the wire level: the same fake backend, a client that declares
// nothing, and NO elicitation/create frame may ever be sent — the tool
// result is ErrDisabled, exactly as before this lane existed.
func TestACPElicitationE2E_NoCapabilityStaysDisabled(t *testing.T) {
	workDir := t.TempDir()
	b := askBackendWithQuestion(t, scopeQuestionArgs)
	c := startACPChild(t, b.URL, workDir)
	sid := c.handshake(t, workDir) // no clientCapabilities

	c.startPromptForAsk(t, sid)
	c.awaitToolOutcome(t, "rephrase as a plain-text question")

	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
	c.assertNoElicitationFrameEver(t)
}

// TestACPElicitationE2E_ProposeAnswersThroughForm covers the OTHER tool
// that reads this lane. propose calls Policy.Ask (v1 single-question),
// not AskBatch — so this pins the delegation path and the four-option
// plan menu, whose ids the propose tool switches on
// (approve / approve_batch / adjust / cancel). Picking "adjust" is the
// interesting one: it must reach the sink as a plan rejection, which the
// tool result spells out.
func TestACPElicitationE2E_ProposeAnswersThroughForm(t *testing.T) {
	workDir := t.TempDir()
	const proposeArgs = `{"problem":"Add a widget.","done_when":["go test ./... is green"],"steps":["Add the widget"]}`
	b := askBackendWithTool(t, "propose", proposeArgs)
	c := startACPChild(t, b.URL, workDir)
	sid := c.handshakeWithFormElicitation(t, workDir)

	c.startPromptForAsk(t, sid)
	frame := c.waitWhere(t, "elicitation/create (propose)", 60*time.Second, isElicitationCreate)

	// The form must carry the four plan options as consts, plus the
	// free-text twin — propose's question text is long (problem + steps),
	// so asserting on the option ids is the stable part.
	var p struct {
		Schema struct {
			Properties map[string]struct {
				Title string `json:"title"`
				OneOf []struct {
					Const string `json:"const"`
				} `json:"oneOf"`
			} `json:"properties"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(frame["params"], &p); err != nil {
		t.Fatalf("elicitation params: %v", err)
	}
	q1, ok := p.Schema.Properties["q1"]
	if !ok {
		t.Fatalf("no q1; props = %v", p.Schema.Properties)
	}
	if len(q1.OneOf) != 4 {
		t.Fatalf("q1.oneOf = %+v, want propose's four plan options", q1.OneOf)
	}
	want := []string{"approve", "approve_batch", "adjust", "cancel"}
	for i, id := range want {
		if q1.OneOf[i].Const != id {
			t.Errorf("option %d = %q, want %q", i, q1.OneOf[i].Const, id)
		}
	}

	c.answerElicitation(t, frame, map[string]any{"q1": "adjust"})

	// "adjust" without free text still routes to the plan-rejection path;
	// the tool result names it.
	c.awaitToolOutcome(t, "adjust")
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
}

// assertNoElicitationFrameEver drains any frames still in flight and
// fails if any is an elicitation request.
func (c *acpChild) assertNoElicitationFrameEver(t *testing.T) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	for {
		select {
		case m, ok := <-c.frames:
			if !ok {
				return
			}
			if isElicitationCreate(m) {
				t.Fatalf("elicitation frame sent to a client that declared no capability: %v", m)
			}
		default:
			return
		}
	}
}
