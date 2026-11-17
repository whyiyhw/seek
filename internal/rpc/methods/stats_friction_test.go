package methods

// stats/friction tests (card c_2i6sm6 acceptance): dispatch-level
// behaviour over a fixture sessions store in a temp SEEK_HOME — the
// fold's numbers through the registry, and the sid contract (explicit /
// default-to-newest / unknown → -32001 naming it). The HTTP /rpc round
// trip lives in controlplane's webfrontend_rpc_friction_test.go, where
// the real handler is in-package reachable. All sandbox: temp dirs
// only, nothing touches ~/.seek/sessions.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/whyiyhw/seek/internal/friction"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// frictionFixture writes one session whose transcript exercises every
// counter through the producers' own constants: a denied write (Errors
// + Denied), a failing bash call repeated identically (Calls + Errors +
// Repeats), and a healthy read.
func frictionFixture(t *testing.T, store *session.Store, sid string) {
	t.Helper()
	denied := fmt.Errorf("%w: user declined write", permission.ErrDenied)
	boom := errors.New("exit status 1")
	sess := &session.Session{
		SchemaVersion: 3,
		ID:            sid,
		Model:         "deepseek-chat",
		CWD:           "/tmp/proj",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "ship it"},
			{
				Role: deepseek.RoleAssistant,
				ToolCalls: []deepseek.ToolCall{
					{ID: "c1", Type: "function", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"command":"go test"}`}},
					{ID: "c2", Type: "function", Function: deepseek.ToolCallFunc{Name: "write", Arguments: `{"path":"a.go"}`}},
					{ID: "c3", Type: "function", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"a.go"}`}},
				},
			},
			{Role: deepseek.RoleTool, ToolCallID: "c1", Content: agent.ToolErrorPrefix + boom.Error()},
			{Role: deepseek.RoleTool, ToolCallID: "c2", Content: agent.ToolErrorPrefix + denied.Error()},
			{Role: deepseek.RoleTool, ToolCallID: "c3", Content: "package a"},
			{
				Role: deepseek.RoleAssistant,
				ToolCalls: []deepseek.ToolCall{{
					ID: "c4", Type: "function",
					Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"command":"go test"}`}, // identical retry
				}},
			},
			{Role: deepseek.RoleTool, ToolCallID: "c4", Content: agent.ToolErrorPrefix + boom.Error()},
			{Role: deepseek.RoleAssistant, Content: "done"},
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
}

// wantFrictionSummary is the fixture's expected fold, asserted
// identically at the dispatch level and over the wire.
func wantFrictionSummary() friction.Summary {
	return friction.Summary{
		Turns:     3,
		ToolCalls: 4,
		Tools: map[string]friction.ToolStat{
			"bash":  {Calls: 2, Errors: 2, Repeats: 1},
			"write": {Calls: 1, Errors: 1, Denied: 1},
			"read":  {Calls: 1},
		},
	}
}

func newFrictionEnv(t *testing.T) (*rpc.Registry, *session.Store) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "") // isolate from a dev-machine override

	store, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	frictionFixture(t, store, "20261117-090000-aaaaaaaaaaaaaaaa")
	// A second, newer, tool-free session: default sid must resolve to
	// it, proving the newest-proxy picks by update time, not by content.
	quiet := &session.Session{
		SchemaVersion: 3, ID: "20261117-100000-bbbbbbbbbbbbbbbb",
		Model: "deepseek-chat", CWD: "/tmp/proj",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "just chatting"},
			{Role: deepseek.RoleAssistant, Content: "ok"},
		},
	}
	if err := store.Save(quiet); err != nil {
		t.Fatal(err)
	}

	reg := rpc.NewRegistry()
	NewStats(store).RegisterAll(reg)
	return reg, store
}

func TestStatsFrictionDispatch(t *testing.T) {
	reg, _ := newFrictionEnv(t)
	ctx := context.Background()

	// Explicit sid: full fold through the registry.
	res, err := reg.Dispatch(ctx, "stats/friction",
		json.RawMessage(`{"sid":"20261117-090000-aaaaaaaaaaaaaaaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.(statsFrictionResult)
	if !ok {
		t.Fatalf("result = %T, want statsFrictionResult", res)
	}
	if got.SID != "20261117-090000-aaaaaaaaaaaaaaaa" {
		t.Errorf("sid = %q", got.SID)
	}
	if !reflect.DeepEqual(got.Summary, wantFrictionSummary()) {
		t.Errorf("summary = %+v, want %+v", got.Summary, wantFrictionSummary())
	}

	// Default sid = newest (the quiet session): empty fold, still a
	// well-formed answer.
	res, err = reg.Dispatch(ctx, "stats/friction", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got = res.(statsFrictionResult)
	if got.SID != "20261117-100000-bbbbbbbbbbbbbbbb" {
		t.Errorf("default sid = %q, want the newest session", got.SID)
	}
	if got.Summary.Turns != 1 || got.Summary.ToolCalls != 0 || len(got.Summary.Tools) != 0 {
		t.Errorf("quiet summary = %+v, want Turns 1 and no tool rows", got.Summary)
	}
}

// Unknown-but-well-formed sid: the §3.5 row — structured -32001 naming
// the sid, never a panic (the acceptance's explicit requirement).
func TestStatsFrictionUnknownSID(t *testing.T) {
	reg, _ := newFrictionEnv(t)
	_, err := reg.Dispatch(context.Background(), "stats/friction",
		json.RawMessage(fmt.Sprintf(`{"sid":%q}`, unknownSID)))
	if err == nil {
		t.Fatal("want error, got success")
	}
	var e *rpc.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %T (%v), want *rpc.Error", err, err)
	}
	if e.Code != rpc.CodeSessionNotFound {
		t.Errorf("code = %d, want %d", e.Code, rpc.CodeSessionNotFound)
	}
	if data, ok := e.Data.(map[string]string); !ok || data["sid"] != unknownSID {
		t.Errorf("data = %+v, want sid %q named", e.Data, unknownSID)
	}
}
