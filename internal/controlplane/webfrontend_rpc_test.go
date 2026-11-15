package controlplane

// POST /rpc envelope tests (W4.1 acceptance): a table over the whole
// JSON-RPC 2.0 envelope — happy paths with both id kinds, null-result
// member survival, every -32xxx error tier, the app-error range, and
// notification silence. Transport-tier behaviour (auth / 405 / 413)
// and the production-empty-registry -32601 contract get dedicated
// tests. The registry here carries test methods only; the method sets
// themselves are W4.2/W4.3.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/rpc"
)

// newTestRPC builds a web frontend whose /rpc registry carries the
// method family the table drives, plus the server to reach it.
func newTestRPC(t *testing.T) *httptest.Server {
	t.Helper()
	fe, _, _ := newTestWeb(t)
	reg := fe.RPCRegistry()
	reg.Register("test/echo", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p struct{ Text string }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "invalid params: %v", err)
		}
		return map[string]string{"echo": p.Text}, nil
	})
	reg.Register("test/nil", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, nil
	})
	reg.Register("test/internal", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	})
	reg.Register("test/notfound", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: rpc.CodeSessionNotFound, Message: "session abc not found", Data: map[string]string{"sid": "abc"}}
	})
	srv := httptest.NewServer(fe.handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestWeb_RPCEnvelope(t *testing.T) {
	srv := newTestRPC(t)
	post := func(body string) (int, map[string]json.RawMessage, []byte) {
		resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]json.RawMessage
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("response is not a JSON object: %q (%v)", raw, err)
			}
		}
		return resp.StatusCode, m, raw
	}

	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
		wantCode   int    // 0: success (no error member); otherwise the exact error code
		wantResult string // non-empty: assert as raw JSON ("" skip)
		wantID     string // non-empty: assert raw id member
	}{
		{
			name:       "call with number id",
			body:       `{"jsonrpc":"2.0","id":1,"method":"test/echo","params":{"text":"hi"}}`,
			wantStatus: http.StatusOK,
			wantCode:   0,
			wantResult: `{"echo":"hi"}`,
			wantID:     `1`,
		},
		{
			name:       "call with string id round-trips verbatim",
			body:       `{"jsonrpc":"2.0","id":"tab-7","method":"test/echo","params":{"text":"x"}}`,
			wantStatus: http.StatusOK,
			wantCode:   0,
			wantID:     `"tab-7"`,
		},
		{
			name:       "null result keeps its member",
			body:       `{"jsonrpc":"2.0","id":2,"method":"test/nil"}`,
			wantStatus: http.StatusOK,
			wantCode:   0,
			wantResult: `null`,
			wantID:     `2`,
		},
		{
			name:       "garbage body is -32700 with null id",
			body:       `not json`,
			wantStatus: http.StatusOK,
			wantCode:   -32700,
			wantID:     `null`,
		},
		{
			name:       "batch array is wrong shape, not parse error",
			body:       `[{"jsonrpc":"2.0","id":1,"method":"test/echo"}]`,
			wantStatus: http.StatusOK,
			wantCode:   -32600,
			wantID:     `null`,
		},
		{
			name:       "scalar body is wrong shape",
			body:       `42`,
			wantStatus: http.StatusOK,
			wantCode:   -32600,
		},
		{
			name:       "wrong jsonrpc version",
			body:       `{"jsonrpc":"1.0","id":3,"method":"test/echo"}`,
			wantStatus: http.StatusOK,
			wantCode:   -32600,
			wantID:     `3`,
		},
		{
			name:       "missing method",
			body:       `{"jsonrpc":"2.0","id":4}`,
			wantStatus: http.StatusOK,
			wantCode:   -32600,
			wantID:     `4`,
		},
		{
			name:       "unknown method is -32601",
			body:       `{"jsonrpc":"2.0","id":5,"method":"no/such"}`,
			wantStatus: http.StatusOK,
			wantCode:   -32601,
			wantID:     `5`,
		},
		{
			name:       "params the method rejects are -32602",
			body:       `{"jsonrpc":"2.0","id":6,"method":"test/echo","params":"scalar"}`,
			wantStatus: http.StatusOK,
			wantCode:   -32602,
			wantID:     `6`,
		},
		{
			name:       "plain handler error surfaces as -32603",
			body:       `{"jsonrpc":"2.0","id":7,"method":"test/internal"}`,
			wantStatus: http.StatusOK,
			wantCode:   -32603,
			wantID:     `7`,
		},
		{
			name:       "app error keeps its -32001 code",
			body:       `{"jsonrpc":"2.0","id":8,"method":"test/notfound"}`,
			wantStatus: http.StatusOK,
			wantCode:   -32001,
			wantID:     `8`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, m, raw := post(tc.body)

			if tc.wantCode == 0 {
				if _, hasErr := m["error"]; hasErr {
					t.Fatalf("got error member in %s", raw)
				}
				if res, ok := m["result"]; ok {
					if tc.wantResult != "" && string(res) != tc.wantResult {
						t.Errorf("result = %s, want %s", res, tc.wantResult)
					}
				} else {
					t.Errorf("success response must carry a result member: %s", raw)
				}
			} else {
				var e struct {
					Code    int             `json:"code"`
					Message string          `json:"message"`
					Data    json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(m["error"], &e); err != nil {
					t.Fatalf("no error member in %s", raw)
				}
				if e.Code != tc.wantCode {
					t.Errorf("code = %d, want %d (%s)", e.Code, tc.wantCode, raw)
				}
				if e.Message == "" {
					t.Errorf("error message empty: %s", raw)
				}
				if _, hasResult := m["result"]; hasResult {
					t.Errorf("error response must not carry result: %s", raw)
				}
				// App errors carry their structured diagnostics (§3.5).
				if tc.wantCode >= -32099 && tc.wantCode <= -32001 && len(e.Data) == 0 {
					t.Errorf("app error must keep its data member: %s", raw)
				}
			}
			if tc.wantID != "" && string(m["id"]) != tc.wantID {
				t.Errorf("id = %s, want %s", m["id"], tc.wantID)
			}
			if status != tc.wantStatus {
				t.Errorf("HTTP status = %d, want %d", status, tc.wantStatus)
			}
		})
	}

	// A notification (no id) gets no reply at all — spec §4.
	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef",
		"application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"test/echo","params":{"text":"fire and forget"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Errorf("notification = %d %q, want 204 empty", resp.StatusCode, body)
	}
}

// TestWeb_RPCUnknownMethodOnProductionRegistry is the literal W4.1
// acceptance sentence: with the production wiring — NewWeb, zero
// methods registered — every method call answers -32601.
func TestWeb_RPCUnknownMethodOnProductionRegistry(t *testing.T) {
	fe, _, _ := newTestWeb(t) // no Register calls: the shipped shape
	if fe.RPCRegistry() == nil {
		t.Fatal("NewWeb must build the registry")
	}
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef",
		"application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"session/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		ID     int             `json:"id"`
		Error  *rpc.Error      `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Errorf("error = %+v, want code -32601", env.Error)
	}
	if env.ID != 1 {
		t.Errorf("id = %d, want 1 echoed", env.ID)
	}
}

func TestWeb_RPCAuthAndGuards(t *testing.T) {
	srv := newTestRPC(t)
	call := func(method, token string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+"/rpc"+token, strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := call(http.MethodPost, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /rpc without token = %d, want 401", resp.StatusCode)
	}
	if resp := call(http.MethodPost, "?token=wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /rpc wrong token = %d, want 401", resp.StatusCode)
	}
	if resp := call(http.MethodGet, "?token=test-token-0123456789abcdef"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /rpc = %d, want 405", resp.StatusCode)
	}

	// Bearer passes the same gate (the /msg precedent).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"no/such"}`))
	req.Header.Set("Authorization", "Bearer test-token-0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST /rpc with Bearer = %d, want 200 (envelope carries the outcome)", resp.StatusCode)
	}
}

func TestWeb_RPCBodyTooLarge(t *testing.T) {
	srv := newTestRPC(t)
	big := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"test/echo","params":{"text":"%s"}}`,
		strings.Repeat("x", 1<<20))
	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef",
		"application/json", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", resp.StatusCode)
	}
}
