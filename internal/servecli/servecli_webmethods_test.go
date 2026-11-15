package servecli

// W4.2 wiring acceptance: the production `--frontend=web` construction
// carries the session method set on its /rpc registry, resolving the
// sessions root through the serve process env (the card's temporary
// SEEK_HOME fixture — the same ladder the spawned seek subprocesses
// inherit, so writer and reader share one store).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/whyiyhw/seek/internal/controlplane"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestBuildFrontendsWebRegistersSessionMethods(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "") // isolate from a dev-machine override

	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &session.Session{
		SchemaVersion: 3,
		ID:            "20261115-090000-aaaaaaaaaaaaaaaa",
		Model:         "deepseek-chat",
		CWD:           "/tmp/proj",
		Turns:         1,
		ToolCalls:     1,
		Usage:         deepseek.Usage{PromptTokens: 10, CompletionTokens: 5},
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "hello from the fixture"},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Type: "function", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"main.go"}`}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "call_1", Content: "package main"},
			{Role: deepseek.RoleAssistant, Content: "done"},
		},
	}
	if err := st.Save(fixture); err != nil {
		t.Fatal(err)
	}

	fes, err := buildFrontends([]string{"web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fe, ok := fes[0].(*controlplane.WebFrontend)
	if !ok {
		t.Fatalf("web frontend = %T, want *controlplane.WebFrontend", fes[0])
	}
	reg := fe.RPCRegistry()
	ctx := context.Background()

	// session/list sees the fixture through the env-resolved root.
	res, err := reg.Dispatch(ctx, "session/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("session/list: %v", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Sessions []struct {
			SID   string `json:"sid"`
			Title string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SID != fixture.ID || list.Sessions[0].Title != "hello from the fixture" {
		t.Fatalf("fixture not listed: %s", b)
	}

	// session/messages pages the fixture transcript.
	res, err = reg.Dispatch(ctx, "session/messages", json.RawMessage(`{"sid":"20261115-090000-aaaaaaaaaaaaaaaa","limit":2}`))
	if err != nil {
		t.Fatalf("session/messages: %v", err)
	}
	b, _ = json.Marshal(res)
	var msgs struct {
		Messages   []json.RawMessage `json:"messages"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(b, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs.Messages) != 2 || msgs.NextCursor != "2" {
		t.Fatalf("first page = %s, want 2 messages with next_cursor 2", b)
	}

	// Unknown sessions still answer the §3.5 app code through the
	// production registry.
	_, err = reg.Dispatch(ctx, "session/get", json.RawMessage(`{"sid":"20260101-000000-0000000000000000"}`))
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != rpc.CodeSessionNotFound {
		t.Errorf("unknown session = %v, want -32001", err)
	}
}
