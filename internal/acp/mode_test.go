package acp

// Session-mode transport tests (ACP·P2, backlog ④): session/set_mode
// dispatch, the current_mode_update notification, the between-turns
// rejection, the no-ModeSwitcher -32601, and the `modes` capability
// echo on session/new. The prompt-behavior half of the feature (plan
// state denies writes, no disk write, propose reminder rides the
// prompt) lives in cmd/seek's real-binary e2e (acp_mode_e2e_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// modeFake is a fakeBackend that opts INTO ModeSwitcher. fakeBackend
// itself deliberately does not implement it — half these tests need a
// backend that never heard of modes (the -32601 lane), which is exactly
// what a bare fakeBackend now is.
type modeFake struct {
	fakeBackend
	setMode func(p SetModeParams) error
}

func (f modeFake) SetMode(p SetModeParams) error {
	if f.setMode != nil {
		return f.setMode(p)
	}
	return nil
}

// planModes is the two-mode table seek serves, mirrored from
// cmd/seek's acpSessionModes for wire-shape assertions.
func planModes(current string) *SessionModeState {
	return &SessionModeState{
		CurrentModeID: current,
		AvailableModes: []SessionMode{
			{ID: ModeDefault, Name: "Default", Description: "Normal operation — reads and writes follow the permission preference."},
			{ID: ModePlan, Name: "Plan", Description: "Read-only analysis: explore, then propose a plan; writes are denied until you switch back."},
		},
	}
}

func TestServer_SetMode_HappyPath_NotifiesThenResponds(t *testing.T) {
	var got SetModeParams
	enc, dec := startServer(t, modeFake{setMode: func(p SetModeParams) error {
		got = p
		return nil
	}})
	send(t, enc, 1, "session/set_mode", SetModeParams{SessionID: "sess-1", ModeID: ModePlan})

	// Notification first — the response carries no mode info (schema:
	// _meta only), so current_mode_update is the client's only
	// confirmation and must not be reordered behind the response.
	notif := readMsg(t, dec)
	var method string
	if err := json.Unmarshal(notif["method"], &method); err != nil || method != "session/update" {
		t.Fatalf("first frame after set_mode = %v, want session/update notification", notif)
	}
	var su struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			CurrentModeID string `json:"currentModeId"`
		} `json:"update"`
	}
	if err := json.Unmarshal(notif["params"], &su); err != nil {
		t.Fatalf("notification params: %v (%s)", err, notif["params"])
	}
	if su.SessionID != "sess-1" || su.Update.SessionUpdate != "current_mode_update" || su.Update.CurrentModeID != ModePlan {
		t.Fatalf("notification = %+v, want sess-1 / current_mode_update / %s", su, ModePlan)
	}

	resp := readMsg(t, dec)
	var id int
	if err := json.Unmarshal(resp["id"], &id); err != nil || id != 1 {
		t.Fatalf("second frame = %v, want the set_mode response (id 1)", resp)
	}
	if string(resp["result"]) != "{}" {
		t.Fatalf("set_mode result = %s, want {} (schema response is _meta-only)", resp["result"])
	}
	if got.SessionID != "sess-1" || got.ModeID != ModePlan {
		t.Fatalf("backend saw %+v, want sess-1/plan", got)
	}
}

func TestServer_SetMode_BackendErrorIsMinus32000(t *testing.T) {
	enc, dec := startServer(t, modeFake{setMode: func(p SetModeParams) error {
		return errors.New("unknown mode \"bogus\" (available: default, plan)")
	}})
	send(t, enc, 1, "session/set_mode", SetModeParams{SessionID: "sess-1", ModeID: "bogus"})
	resp := readMsg(t, dec)
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp["error"], &e); err != nil {
		t.Fatalf("no error frame: %v", resp)
	}
	if e.Code != -32000 || !strings.Contains(e.Message, "available") {
		t.Fatalf("error = %+v, want -32000 naming the available modes", e)
	}
}

func TestServer_SetMode_BadParamsIsMinus32602(t *testing.T) {
	enc, dec := startServer(t, modeFake{})
	send(t, enc, 1, "session/set_mode", json.RawMessage(`{"sessionId":42}`))
	resp := readMsg(t, dec)
	var e struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(resp["error"], &e); err != nil || e.Code != -32602 {
		t.Fatalf("resp = %v (%v), want -32602 for unparsable params", resp, err)
	}
}

func TestServer_SetMode_NoModeSwitcherIsMinus32601(t *testing.T) {
	// A backend that never heard of modes answers the standard
	// unknown-method error — same as any method seek doesn't serve —
	// so clients hide their mode switcher rather than retry.
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 1, "session/set_mode", SetModeParams{SessionID: "sess-1", ModeID: ModePlan})
	resp := readMsg(t, dec)
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp["error"], &e); err != nil {
		t.Fatalf("no error frame: %v", resp)
	}
	if e.Code != -32601 || !strings.Contains(e.Message, "no session modes") {
		t.Fatalf("error = %+v, want -32601 naming missing mode support", e)
	}
}

func TestServer_SetMode_RejectedWhilePromptInFlight(t *testing.T) {
	released := make(chan struct{})
	enc, dec := startServer(t, modeFake{fakeBackend{prompt: func(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
		select {
		case <-released:
			return PromptResult{StopReason: "end_turn"}, nil
		case <-ctx.Done():
			return PromptResult{}, ctx.Err()
		}
	}}, nil})
	send(t, enc, 1, "session/prompt", PromptParams{SessionID: "sess-1", Prompt: []ContentBlock{{Type: "text", Text: "hold"}}})
	send(t, enc, 2, "session/set_mode", SetModeParams{SessionID: "sess-1", ModeID: ModePlan})
	resp := readMsg(t, dec)
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp["error"], &e); err != nil || e.Code != -32000 {
		t.Fatalf("resp = %v (%v), want -32000 rejection while a turn runs", resp, err)
	}
	if !strings.Contains(e.Message, "between turns") {
		t.Fatalf("error message = %q, want the between-turns guidance", e.Message)
	}
	close(released)
	// The in-flight turn finishes normally — the rejection killed
	// nothing (denials are results, never fatal). No other frames are
	// pending (the fake prompt sends no updates), so the next frame is
	// the prompt response itself.
	promptResp := readMsg(t, dec)
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(promptResp["result"], &res); err != nil || res.StopReason != "end_turn" {
		t.Fatalf("prompt result = %s (%v), want end_turn after the mode rejection", promptResp["result"], err)
	}
}

func TestServer_NewSessionEchoesModes(t *testing.T) {
	enc, dec := startServer(t, modeFake{fakeBackend{modes: planModes(ModeDefault)}, nil})
	send(t, enc, 1, "session/new", NewSessionParams{Cwd: "/tmp"})
	resp := readMsg(t, dec)
	var res struct {
		Modes *SessionModeState `json:"modes"`
	}
	if err := json.Unmarshal(resp["result"], &res); err != nil {
		t.Fatalf("session/new result: %v (%s)", err, resp["result"])
	}
	if res.Modes == nil {
		t.Fatalf("session/new result carries no modes: %s", resp["result"])
	}
	if res.Modes.CurrentModeID != ModeDefault || len(res.Modes.AvailableModes) != 2 {
		t.Fatalf("modes = %+v, want current=default with 2 available modes", res.Modes)
	}
	if res.Modes.AvailableModes[1].ID != ModePlan || res.Modes.AvailableModes[1].Description == "" {
		t.Fatalf("plan mode entry = %+v, want id=plan with a description", res.Modes.AvailableModes[1])
	}
}

func TestServer_NewSessionWithoutModesOmitsField(t *testing.T) {
	// Pre-P2 backends (and every fake that never opted in) must keep
	// producing byte-identical session/new results: `modes` is omitempty
	// and nil, never `"modes":null`.
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 1, "session/new", NewSessionParams{Cwd: "/tmp"})
	resp := readMsg(t, dec)
	if strings.Contains(string(resp["result"]), "modes") {
		t.Fatalf("session/new result mentions modes without support: %s", resp["result"])
	}
}
