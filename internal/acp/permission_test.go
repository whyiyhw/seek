package acp

// Permission-lane fixtures (P0-2, feature-acp.md backlog ①): the
// outcome decoder table plus the transport-level three states of one
// agent→client Call — answered (allow / reject), timed out, and the
// no-answer endings (disconnect, client JSON-RPC error, $/
// cancel_request). The policy-level mapping (always→SetAlwaysAllow
// etc.) is covered in cmd/seek/acp_approval_test.go; here we pin the
// wire behaviour the asker leans on.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestParsePermissionOutcome(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want PermissionDecision
	}{
		{"selected allow_once (v1 schema)", `{"outcome":{"outcome":"selected","optionId":"allow_once"}}`, DecisionAllowOnce},
		{"selected allow_always (v1 schema)", `{"outcome":{"outcome":"selected","optionId":"allow_always"}}`, DecisionAllowAlways},
		{"selected reject_once (v1 schema)", `{"outcome":{"outcome":"selected","optionId":"reject_once"}}`, DecisionReject},
		{"cancelled (v1 schema)", `{"outcome":{"outcome":"cancelled"}}`, DecisionCancelled},
		{"legacy bare string allow_once", `{"outcome":"allow_once"}`, DecisionAllowOnce},
		{"legacy bare string allow_always", `{"outcome":"allow_always"}`, DecisionAllowAlways},
		{"legacy bare string reject_once", `{"outcome":"reject_once"}`, DecisionReject},
		{"unknown optionId echo denies", `{"outcome":{"outcome":"selected","optionId":"reject_always"}}`, DecisionReject},
		{"junk json", `not json`, DecisionCancelled},
		{"empty body", `{}`, DecisionCancelled},
	}
	for _, c := range cases {
		if got := ParsePermissionOutcome([]byte(c.raw)); got != c.want {
			t.Errorf("%s: ParsePermissionOutcome(%s) = %v, want %v", c.name, c.raw, got, c.want)
		}
	}
}

// startCallServer wires a Server over pipes and hands back BOTH sides,
// so a test can drive agent→client Calls and script the client replies.
func startCallServer(t *testing.T) (*Server, *json.Encoder, *json.Decoder) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv := NewServer(c2sR, s2cW, fakeBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); c2sW.Close(); s2cW.Close() })
	return srv, json.NewEncoder(c2sW), json.NewDecoder(s2cR)
}

// readRequest reads one agent→client REQUEST frame and returns its id
// (as the raw JSON) plus the decoded envelope.
func readRequest(t *testing.T, dec *json.Decoder) (json.RawMessage, request) {
	t.Helper()
	var frame request
	if err := dec.Decode(&frame); err != nil {
		t.Fatalf("read agent→client frame: %v", err)
	}
	if frame.Method == "" {
		t.Fatalf("expected a request frame, got a reply/notification: %+v", frame)
	}
	if frame.ID == nil {
		t.Fatalf("request frame without id: %+v", frame)
	}
	return *frame.ID, frame
}

func TestCall_AllowRoundTrip(t *testing.T) {
	srv, enc, dec := startCallServer(t)
	type res struct {
		CallErr error
		Raw     json.RawMessage
	}
	got := make(chan res, 1)
	go func() {
		raw, err := srv.Call(context.Background(), "session/request_permission", RequestPermissionParams{
			SessionID: "s1",
			ToolCall:  PermissionToolCall{ToolCallID: "perm-1", Kind: "execute", Title: "run a shell command"},
			Options:   PermissionOptions(),
		}, 5*time.Second)
		got <- res{CallErr: err, Raw: raw}
	}()

	id, frame := readRequest(t, dec)
	if frame.Method != "session/request_permission" {
		t.Fatalf("method = %q", frame.Method)
	}
	var p RequestPermissionParams
	if err := json.Unmarshal(frame.Params, &p); err != nil {
		t.Fatalf("params: %v", err)
	}
	if p.SessionID != "s1" || p.ToolCall.Kind != "execute" || len(p.Options) != 3 ||
		p.Options[0].OptionID != OptionAllowOnce || p.Options[1].OptionID != OptionAllowAlways || p.Options[2].OptionID != OptionRejectOnce {
		t.Fatalf("params = %+v, want session s1 / execute / the standard three options", p)
	}
	// Client answers: selected allow_once.
	_ = enc.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}}})

	r := <-got
	if r.CallErr != nil {
		t.Fatalf("Call: %v", r.CallErr)
	}
	if d := ParsePermissionOutcome(r.Raw); d != DecisionAllowOnce {
		t.Fatalf("outcome = %v, want allow_once", d)
	}
}

func TestCall_TimeoutDeniesByNoReply(t *testing.T) {
	srv, _, dec := startCallServer(t)
	got := make(chan error, 1)
	go func() {
		_, err := srv.Call(context.Background(), "session/request_permission", RequestPermissionParams{}, 50*time.Millisecond)
		got <- err
	}()
	// Client never answers: the frame still arrives (proves the request
	// was sent), then silence until the deadline.
	readRequest(t, dec)
	select {
	case err := <-got:
		if err != ErrCallTimeout {
			t.Fatalf("Call err = %v, want ErrCallTimeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return on timeout")
	}
}

func TestCall_DisconnectFailsPending(t *testing.T) {
	// Close the client→server read side while a Call is pending — Serve
	// sees EOF and must resolve the waiter with ErrDisconnected, not
	// leave it hanging until the timeout. (io.Pipe writes block until
	// read, so the request frame is consumed first.)
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv := NewServer(c2sR, s2cW, fakeBackend{})
	go srv.Serve(context.Background())
	t.Cleanup(func() { c2sW.Close(); s2cW.Close() })
	dec := json.NewDecoder(s2cR)

	got := make(chan error, 1)
	go func() {
		_, err := srv.Call(context.Background(), "session/request_permission", RequestPermissionParams{}, time.Hour)
		got <- err
	}()
	readRequest(t, dec) // the frame left the process before the vanishing
	c2sR.Close()        // client vanished mid-prompt

	select {
	case err := <-got:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("Call err = %v, want ErrDisconnected", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending Call survived the read loop's exit")
	}
}

func TestCall_ClientErrorDenies(t *testing.T) {
	srv, enc, dec := startCallServer(t)
	got := make(chan error, 1)
	go func() {
		_, err := srv.Call(context.Background(), "session/request_permission", RequestPermissionParams{}, 5*time.Second)
		got <- err
	}()
	// A headless driver that doesn't implement the method answers -32601.
	id, _ := readRequest(t, dec)
	_ = enc.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{"2.0", id, rpcError{Code: -32601, Message: "method not found"}})
	err := <-got
	if err == nil {
		t.Fatal("client error frame must surface as a Call error")
	}
}

func TestCall_CancelRequestNotification(t *testing.T) {
	srv, enc, dec := startCallServer(t)
	got := make(chan error, 1)
	go func() {
		_, err := srv.Call(context.Background(), "session/request_permission", RequestPermissionParams{}, time.Hour)
		got <- err
	}()
	id, _ := readRequest(t, dec)
	// The user closed the permission prompt: client cancels our request.
	_ = enc.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", "$/cancel_request", append([]byte(`{"id":`), append(id, '}')...)})
	err := <-got
	if !errors.Is(err, ErrCallCancelled) {
		t.Fatalf("Call err = %v, want ErrCallCancelled", err)
	}
}

func TestCall_CtxCancelUnblocks(t *testing.T) {
	srv, _, dec := startCallServer(t)
	// Drain agent→client frames in the background or the request write
	// blocks on the synchronous pipe (io.Pipe semantics; a real client
	// always reads). This test only cares that the call unblocks.
	go func() {
		for {
			var v json.RawMessage
			if dec.Decode(&v) != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := srv.Call(ctx, "session/request_permission", RequestPermissionParams{}, time.Hour)
		got <- err
	}()
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, ErrCallCancelled) {
			t.Fatalf("Call err = %v, want ErrCallCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return on ctx cancel")
	}
}

// TestServe_ResponseNotDispatchedAsMethod pins the frame-routing rule
// the read loop now relies on: a reply (id, no method) must go to its
// waiter, NOT fall into dispatch's default case (which would answer a
// response with a -32601 error — protocol noise on every permission
// answer).
func TestServe_ResponseNotDispatchedAsMethod(t *testing.T) {
	_, enc, dec := startCallServer(t)
	// A stray reply with no pending Call: silently ignored, no error
	// frame written back.
	_ = enc.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{"2.0", json.RawMessage(`"p99"`), json.RawMessage(`{"outcome":{"outcome":"cancelled"}}`)})

	// Then a normal request still works — the loop is alive.
	send(t, enc, 7, "initialize", InitializeParams{ProtocolVersion: 1})
	msg := readMsg(t, dec)
	var res InitializeResult
	if err := json.Unmarshal(msg["result"], &res); err != nil || res.ProtocolVersion != 1 {
		t.Fatalf("initialize after stray reply: %v (%v)", err, msg)
	}
}
