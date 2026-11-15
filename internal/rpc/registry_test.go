package rpc

// Registry tests (W4.1, inspect-rpc §3.2/§3.5): dispatch roundtrip,
// the -32601 contract for unknown methods, error mapping, envelope
// marshaling edge cases (null result must keep its member), and
// concurrent dispatch after the Register phase — the phase split the
// no-lock contract guarantees.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRegistry_DispatchRoundtrip(t *testing.T) {
	r := NewRegistry()
	r.Register("test/echo", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p struct{ Text string }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, Errorf(CodeInvalidParams, "invalid params: %v", err)
		}
		return map[string]string{"echo": p.Text}, nil
	})

	got, err := r.Dispatch(context.Background(), "test/echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	m, ok := got.(map[string]string)
	if !ok || m["echo"] != "hi" {
		t.Errorf("result = %#v, want echo=hi", got)
	}
}

func TestRegistry_DispatchUnknownIs32601(t *testing.T) {
	r := NewRegistry() // empty: the W4.1 production shape
	_, err := r.Dispatch(context.Background(), "session/list", nil)

	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("unknown method must answer *Error, got %T (%v)", err, err)
	}
	if e.Code != -32601 {
		t.Errorf("code = %d, want -32601 (CodeMethodNotFound)", e.Code)
	}
	if !strings.Contains(e.Error(), "session/list") {
		t.Errorf("message %q must name the method", e.Error())
	}
}

func TestRegistry_ParamsFailurePassesCodeThrough(t *testing.T) {
	r := NewRegistry()
	r.Register("test/strict", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p struct{ N int }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, Errorf(CodeInvalidParams, "invalid params: %v", err)
		}
		return p.N, nil
	})
	_, err := r.Dispatch(context.Background(), "test/strict", json.RawMessage(`"scalar"`))
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInvalidParams {
		t.Errorf("params failure = %v, want code -32602", err)
	}
}

func TestRegistry_RegisterNilPanicsAtConstruction(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register(nil) must panic on the construction path")
		}
	}()
	NewRegistry().Register("test/nil", nil)
}

func TestRegistry_ConcurrentDispatch(t *testing.T) {
	// The §3.2 contract: Register phase ends, then concurrent Dispatch
	// is safe. -race is the real assertion here.
	r := NewRegistry()
	r.Register("test/n", func(ctx context.Context, params json.RawMessage) (any, error) {
		return len(params), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if j%2 == 0 {
					if _, err := r.Dispatch(context.Background(), "test/n", json.RawMessage(`{}`)); err != nil {
						t.Errorf("Dispatch: %v", err)
					}
				} else if _, err := r.Dispatch(context.Background(), fmt.Sprintf("none/%d", i), nil); err == nil {
					t.Error("unknown method must error")
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestRequest_Validate(t *testing.T) {
	for name, req := range map[string]Request{
		"ok":            {JSONRPC: "2.0", Method: "a/b"},
		"wrong version": {JSONRPC: "1.0", Method: "a/b"},
		"no method":     {JSONRPC: "2.0"},
	} {
		err := req.Validate()
		if name == "ok" {
			if err != nil {
				t.Errorf("%s: Validate = %v, want nil", name, err)
			}
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeInvalidRequest {
			t.Errorf("%s: Validate = %v, want code -32600", name, err)
		}
	}
	bare := Request{JSONRPC: "2.0", Method: "a/b"}
	if !bare.IsNotification() {
		t.Error("no id = notification")
	}
	withID := Request{JSONRPC: "2.0", Method: "a/b", ID: json.RawMessage("1")}
	if withID.IsNotification() {
		t.Error("id present = not a notification")
	}
}

func TestResponse_MarshalMembers(t *testing.T) {
	// Success: the result member must exist even when the result is
	// nil/zero; the error member must not.
	for name, res := range map[string]any{"nil": nil, "zero": 0} {
		resp, err := NewResult(json.RawMessage("7"), res)
		if err != nil {
			t.Fatalf("%s: NewResult: %v", name, err)
		}
		b, _ := json.Marshal(resp)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if _, ok := m["result"]; !ok {
			t.Errorf("%s: %s — result member missing", name, b)
		}
		if _, ok := m["error"]; ok {
			t.Errorf("%s: %s — must not carry both result and error", name, b)
		}
		if string(m["id"]) != "7" {
			t.Errorf("%s: id = %s, want raw 7", name, m["id"])
		}
	}

	// Error: a falsy-but-real result must survive too (map to error and
	// check only error member exists).
	resp := NewError(json.RawMessage("null"), Errorf(CodeSessionNotFound, "session %s not found", "abc"))
	b, _ := json.Marshal(resp)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	if _, ok := m["result"]; ok {
		t.Errorf("%s: error response must not carry result", b)
	}
	var e struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
	}
	json.Unmarshal(m["error"], &e)
	if e.Code != -32001 || !strings.Contains(e.Msg, "abc") {
		t.Errorf("error = %+v, want -32001 naming the session", e)
	}
}

func TestNewError_Mapping(t *testing.T) {
	// Plain errors become -32603; *Error (wrapped or direct) keeps its
	// code — the contract hosts rely on via errors.As.
	resp := NewError(json.RawMessage("1"), errors.New("boom"))
	if resp.Error.Code != CodeInternalError || resp.Error.Message != "boom" {
		t.Errorf("plain error → %+v, want -32603/boom", resp.Error)
	}
	wrapped := fmt.Errorf("call: %w", Errorf(CodeMemoryNotFound, "no memory"))
	resp = NewError(json.RawMessage("2"), wrapped)
	if resp.Error.Code != CodeMemoryNotFound {
		t.Errorf("wrapped *Error → code %d, want -32002", resp.Error.Code)
	}
}
