package controlplane

// c_4doebx close-out (inspect-rpc §6 #12): subagent/list|get ride the
// /rpc plane, and GET /subagents stays mounted as the compatibility口.
// The two must be ONE projection — the same disk fold, the same row
// bytes — or the panel and any external consumer would disagree about
// the same subagent. This test pins that agreement over a real
// subagents.jsonl, plus the fact that both routes exist side by side
// (removing either is a deliberate act, not a refactor side effect).
//
// The registry is wired here the way servecli wires it; importing the
// methods package from a controlplane test is fine (methods imports rpc
// and the data packages, never controlplane — no cycle).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/rpc/methods"
	"github.com/whyiyhw/seek/internal/subagent"
)

func TestWeb_SubagentMethodsMatchCompatEndpoint(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fe, project := newBoundWebWithIndex(t, fe,
		`{"event":"started","sub_sid":"done-1","ts":"2026-01-01T10:00:00Z","parent_sid":"sess-A","parent_turn":2,"type":"explore","description":"find handlers"}`,
		`{"event":"completed","sub_sid":"done-1","ts":"2026-01-01T10:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}`,
		`{"event":"started","sub_sid":"bg-1","ts":"2026-01-02T09:00:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}`,
	)
	seedTranscript(t, project, "sess-A", "done-1",
		`{"schema_version":2,"id":"h"}`,
		`{"role":"user","content":"find handlers"}`,
		`{"role":"assistant","content":"compat tail"}`,
	)
	// The production wiring (servecli) hangs the family on the registry;
	// mirror it so /rpc answers for real.
	methods.NewSubagents().RegisterAll(fe.RPCRegistry())
	// A project record exists in production because a seek child ran in
	// the directory (memory.LoadOrCreate on session start) — the /rpc
	// family resolves project_id through that record, so the fixture
	// must have one. LoadOrCreate is the production path.
	if _, err := memory.LoadOrCreate(project); err != nil {
		t.Fatalf("LoadOrCreate(%s): %v", project, err)
	}
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	projectID := paths.ProjectID(project)

	// /rpc subagent/list, decoded through the wire (the shape a panel
	// consumer sees).
	callRPC := func(t *testing.T, method string, params any) map[string]json.RawMessage {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
		})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef",
			"application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s transport status = %d, want 200", method, resp.StatusCode)
		}
		var env struct {
			Result map[string]json.RawMessage `json:"result"`
			Error  *rpc.Error                 `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("%s decode: %v", method, err)
		}
		if env.Error != nil {
			t.Fatalf("%s error: %+v", method, env.Error)
		}
		return env.Result
	}

	listRes := callRPC(t, "subagent/list", map[string]string{"project_id": projectID})
	var viaRPC []subagent.Subagent
	if err := json.Unmarshal(listRes["subagents"], &viaRPC); err != nil {
		t.Fatalf("subagents field: %v", err)
	}
	var rpcProject string
	if err := json.Unmarshal(listRes["project_id"], &rpcProject); err != nil || rpcProject != projectID {
		t.Fatalf("project_id = %q (%v), want %q", rpcProject, err, projectID)
	}

	// GET /subagents — the compatibility endpoint, same project.
	resp, rows := getSubagents(t, srv, "?token=test-token-0123456789abcdef")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /subagents = %d, want 200 (the compat口 must keep answering)", resp.StatusCode)
	}
	if len(viaRPC) != len(rows) || len(viaRPC) != 2 {
		t.Fatalf("rows differ: /rpc %d, /subagents %d, want 2 each", len(viaRPC), len(rows))
	}
	for i := range rows {
		if viaRPC[i].SubSid != rows[i].SubSid || string(viaRPC[i].Status) != rows[i].Status {
			t.Fatalf("row[%d] differs between /rpc (%+v) and /subagents (%+v)", i, viaRPC[i], rows[i])
		}
	}

	// /rpc subagent/get — the detail half, same information as ?sub=.
	getRes := callRPC(t, "subagent/get", map[string]string{"sub_sid": "done-1", "project_id": projectID})
	var detail struct {
		Sub           subagent.Subagent `json:"sub"`
		LastAssistant string            `json:"last_assistant"`
	}
	raw, err := json.Marshal(getRes)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	// ?sub= on the compat endpoint for the same id.
	dresp, err := http.Get(srv.URL + "/subagents?token=test-token-0123456789abcdef&sub=done-1")
	if err != nil {
		t.Fatal(err)
	}
	defer dresp.Body.Close()
	var compat subagentDetailBody
	if err := json.NewDecoder(dresp.Body).Decode(&compat); err != nil {
		t.Fatal(err)
	}
	if detail.Sub.SubSid != compat.Sub.SubSid || string(detail.Sub.Status) != compat.Sub.Status {
		t.Fatalf("detail row differs: /rpc %+v, /subagents %+v", detail.Sub, compat.Sub)
	}
	if detail.LastAssistant != compat.LastAssistant || detail.LastAssistant != "compat tail" {
		t.Fatalf("last_assistant differs: /rpc %q, /subagents %q, want %q",
			detail.LastAssistant, compat.LastAssistant, "compat tail")
	}

	// Unknown id answers -32006 on /rpc; the compat endpoint keeps its
	// 404 (different transport conventions, same truth).
	bad := callRPCError(t, srv, "subagent/get", map[string]string{"sub_sid": "nope"})
	if bad == nil || bad.Code != rpc.CodeSubagentNotFound {
		t.Fatalf("unknown sub_sid error = %+v, want -32006", bad)
	}
}

// callRPCError posts a method expected to fail and returns the error
// envelope (nil when the call unexpectedly succeeded).
func callRPCError(t *testing.T, srv *httptest.Server, method string, params any) *rpc.Error {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Error *rpc.Error `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return env.Error
}

// TestWeb_StateCarriesProjectID pins the /state addition the panel
// relies on: the bound project's 16-hex id (the /rpc families' key),
// empty when unbound — never a hash of "" that would look like a real
// project.
func TestWeb_StateCarriesProjectID(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	get := func() map[string]any {
		t.Helper()
		resp, err := http.Get(srv.URL + "/state?token=test-token-0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if m := get(); m["bound"] != false || m["project_id"] != "" {
		t.Errorf("unbound /state = %v, want project_id empty", m)
	}
	project := t.TempDir()
	if err := fe.store.Put(Binding{
		ConvID:      webConvID(),
		ProjectPath: project,
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if m := get(); m["bound"] != true || m["project_id"] != paths.ProjectID(project) {
		t.Errorf("bound /state project_id = %v, want %q", m["project_id"], paths.ProjectID(project))
	}
}
