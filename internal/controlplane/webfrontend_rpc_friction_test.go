package controlplane

// The stats/friction httptest round trip (card c_2i6sm6 acceptance):
// the method answering through THIS package's real POST /rpc surface —
// token auth + JSON-RPC envelope + registry dispatch — with the Stats
// family registered onto the frontend's registry the way servecli
// assembles it. No transport logic is re-implemented here; the
// method-level numbers and the §3.5 sid rows are asserted in
// internal/rpc/methods/stats_friction_test.go. All sandbox: a temp
// SEEK_HOME fixture, nothing touches ~/.seek/sessions.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/friction"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/rpc/methods"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestWeb_RPCStatsFriction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	store, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	const sid = "20261117-090000-aaaaaaaaaaaaaaaa"
	denied := fmt.Errorf("%w: user declined write", permission.ErrDenied)
	boom := errors.New("exit status 1")
	fixture := &session.Session{
		SchemaVersion: 3, ID: sid, Model: "deepseek-chat", CWD: "/tmp/proj",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "ship it"},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "c1", Type: "function", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"command":"go test"}`}},
				{ID: "c2", Type: "function", Function: deepseek.ToolCallFunc{Name: "write", Arguments: `{"path":"a.go"}`}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "c1", Content: agent.ToolErrorPrefix + boom.Error()},
			{Role: deepseek.RoleTool, ToolCallID: "c2", Content: agent.ToolErrorPrefix + denied.Error()},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{{
				ID: "c3", Type: "function",
				Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"command":"go test"}`}, // identical retry
			}}},
			{Role: deepseek.RoleTool, ToolCallID: "c3", Content: agent.ToolErrorPrefix + boom.Error()},
			{Role: deepseek.RoleAssistant, Content: "done"},
		},
	}
	if err := store.Save(fixture); err != nil {
		t.Fatal(err)
	}

	fe, _, tok := newTestWeb(t)
	methods.NewStats(store).RegisterAll(fe.RPCRegistry())
	srv := httptest.NewServer(fe.handler())
	t.Cleanup(srv.Close)

	post := func(body string) (int, map[string]json.RawMessage) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/rpc?token="+tok,
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("response is not a JSON object: %q (%v)", raw, err)
		}
		return resp.StatusCode, m
	}

	// Happy path: full envelope {jsonrpc, id, result} carrying the fold.
	status, m := post(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"stats/friction","params":{"sid":%q}}`, sid))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if _, ok := m["error"]; ok {
		t.Fatalf("unexpected error member: %v", m["error"])
	}
	if string(m["id"]) != "7" {
		t.Errorf("id = %s, want 7", m["id"])
	}
	var res struct {
		SID     string           `json:"sid"`
		Summary friction.Summary `json:"summary"`
	}
	if err := json.Unmarshal(m["result"], &res); err != nil {
		t.Fatalf("result does not decode: %v (%s)", err, m["result"])
	}
	want := friction.Summary{
		Turns: 3, ToolCalls: 3,
		Tools: map[string]friction.ToolStat{
			"bash":  {Calls: 2, Errors: 2, Repeats: 1},
			"write": {Calls: 1, Errors: 1, Denied: 1},
		},
	}
	if res.SID != sid || !reflect.DeepEqual(res.Summary, want) {
		t.Errorf("round trip = sid %q %+v, want sid %q %+v", res.SID, res.Summary, sid, want)
	}

	// Unknown sid over HTTP: HTTP 200 + the structured -32001 envelope
	// naming the sid — and demonstrably no panic (the server answered).
	status, m = post(`{"jsonrpc":"2.0","id":8,"method":"stats/friction","params":{"sid":"20260101-000000-0000000000000000"}}`)
	if status != http.StatusOK {
		t.Fatalf("unknown sid status = %d, want 200 (JSON-RPC convention)", status)
	}
	var env struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(m["error"], &env); err != nil {
		t.Fatalf("error member does not decode: %v (%s)", err, m["error"])
	}
	if env.Code != -32001 {
		t.Errorf("code = %d, want -32001", env.Code)
	}
	if env.Data["sid"] != "20260101-000000-0000000000000000" {
		t.Errorf("data = %+v, want the sid named", env.Data)
	}
}
