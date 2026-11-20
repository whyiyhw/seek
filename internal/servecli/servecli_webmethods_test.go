package servecli

// W4.2/W4.3 wiring acceptance: the production `--frontend=web`
// construction carries the asset-plane method sets on its /rpc
// registry, resolving the sessions/projects roots through the serve
// process env (the cards' temporary SEEK_HOME fixture — the same
// ladder the spawned seek subprocesses inherit, so writer and reader
// share one store).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/controlplane"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/paths"
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

// TestBuildFrontendsWebRegistersAssetMethods is the W4.3 sibling: the
// memory/project/stats families answer through the production wiring,
// against a temporary SEEK_HOME carrying one project (manifest +
// memory.jsonl) whose root also anchors the fixture session's CWD —
// so project/list's session bucketing sees it too.
func TestBuildFrontendsWebRegistersAssetMethods(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	root := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := paths.ProjectID(root)
	projDir := filepath.Join(home, "projects", pid)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	man := memory.Manifest{SchemaVersion: memory.SchemaVersion, ProjectID: pid, AbsPath: root, FirstSeen: now, LastSeen: now}
	if b, err := json.Marshal(man); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(filepath.Join(projDir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{Name: "use-stdlib-http", Tagline: "zero-dep asset plane", Content: "net/http is already in the tree"}
	entry.CreatedAt, entry.UpdatedAt, entry.LastRecalledAt = now, now, now
	if b, err := json.Marshal(entry); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(filepath.Join(projDir, "memory.jsonl"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &session.Session{
		SchemaVersion: 3,
		ID:            "20261115-110000-dddddddddddddddd",
		Model:         "deepseek-chat",
		CWD:           root,
		Usage:         deepseek.Usage{PromptTokens: 100, PromptCacheHitTokens: 75, PromptCacheMissTokens: 25},
		Messages:      []deepseek.Message{{Role: deepseek.RoleUser, Content: "asset plane"}},
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

	// memory/projects + memory/list default (the only project) + get.
	res, err := reg.Dispatch(ctx, "memory/projects", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("memory/projects: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), pid) || !strings.Contains(string(b), `"memories":1`) {
		t.Fatalf("memory/projects = %s, want project %s with 1 memory", b, pid)
	}
	res, err = reg.Dispatch(ctx, "memory/get", json.RawMessage(`{"name":"use-stdlib-http"}`))
	if err != nil {
		t.Fatalf("memory/get default project: %v", err)
	}
	if b, _ = json.Marshal(res); !strings.Contains(string(b), "net/http is already in the tree") {
		t.Fatalf("memory/get = %s, want the fixture body", b)
	}

	// project/list sees the session bucketed under the project.
	res, err = reg.Dispatch(ctx, "project/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("project/list: %v", err)
	}
	b, _ = json.Marshal(res)
	if !strings.Contains(string(b), `"sessions":1`) {
		t.Fatalf("project/list = %s, want 1 session under %s", b, pid)
	}

	// stats/cache defaults to the newest session through the env store.
	res, err = reg.Dispatch(ctx, "stats/cache", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("stats/cache: %v", err)
	}
	b, _ = json.Marshal(res)
	if !strings.Contains(string(b), fixture.ID) || !strings.Contains(string(b), `"hit_ratio":0.75`) {
		t.Fatalf("stats/cache = %s, want newest sid + 0.75 hit ratio", b)
	}

	// The §3.5 app codes flow through the production registry too.
	_, err = reg.Dispatch(ctx, "project/get", json.RawMessage(`{"project_id":"0123456789abcdef"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeProjectNotFound {
		t.Errorf("unknown project = %v, want -32003", err)
	}
	_, err = reg.Dispatch(ctx, "memory/get", json.RawMessage(fmt.Sprintf(`{"project_id":%q,"name":"nope"}`, pid)))
	if !errors.As(err, &re) || re.Code != rpc.CodeMemoryNotFound {
		t.Errorf("unknown memory = %v, want -32002", err)
	}
}

// TestBuildFrontendsWebRegistersApprovalMethods is the W5 c_n37tnr
// sibling: the approval hub's two methods answer through the
// production wiring (the hub is frontend-native state, so its methods
// register on the frontend itself, not the internal/rpc/methods
// families). Without this wiring the page's cards would 404 on every
// decide — the pin exists so a registry-line deletion fails here.
func TestBuildFrontendsWebRegistersApprovalMethods(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_SESSIONS_DIR", "")

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

	res, err := reg.Dispatch(ctx, "approval/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("approval/list: %v", err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"approvals":[]`) {
		t.Errorf("empty approval/list = %s, want empty approvals", b)
	}

	_, err = reg.Dispatch(ctx, "approval/decide", json.RawMessage(`{"id":"nope","decision":"y"}`))
	var ae *rpc.Error
	if !errors.As(err, &ae) || ae.Code != rpc.CodeInvalidParams {
		t.Errorf("unknown-id decide = %v, want -32602", err)
	}
}

// TestBuildFrontendsWebRegistersHooksMethods is the W4.6 sibling: the
// hooks family answers through the production wiring — merged
// hooks.toml view over a temporary SEEK_HOME (user + project files),
// audit tail with project attribution via the env-resolved session
// store, follow rejected as -32602 (the one-shot contract).
func TestBuildFrontendsWebRegistersHooksMethods(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	root := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(filepath.Join(root, ".seek"), 0o755); err != nil {
		t.Fatal(err)
	}
	pid := paths.ProjectID(root)
	projDir := filepath.Join(home, "projects", pid)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	man := memory.Manifest{SchemaVersion: memory.SchemaVersion, ProjectID: pid, AbsPath: root, FirstSeen: now, LastSeen: now}
	if b, err := json.Marshal(man); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(filepath.Join(projDir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".seek", "hooks.toml"), []byte(
		"[[pre_tool]]\nname = \"proj-guard\"\ncommand = \"exit 1\"\nmatch = { tool = \"bash\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hooks.toml"), []byte(
		"[[session_start]]\nname = \"user-notify\"\ncommand = \"echo start\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &session.Session{
		SchemaVersion: 3,
		ID:            "20261116-090000-hhhhhhhhhhhhhhhh",
		Model:         "deepseek-chat",
		CWD:           root,
		Messages:      []deepseek.Message{{Role: deepseek.RoleUser, Content: "hooks plane"}},
	}
	if err := st.Save(fixture); err != nil {
		t.Fatal(err)
	}
	audit := fmt.Sprintf(
		`{"ts":"2026-11-16T20:00:00Z","event":"pre_tool","hook":"proj-guard","tool":"bash","session_id":%q,"duration_ms":8,"exit_code":1,"denied":true}`+"\n"+
			`{"ts":"2026-11-16T21:00:00Z","event":"pre_tool","hook":"proj-guard","tool":"bash","session_id":"20261116-210000-iiiiiiiiiiiiiiii","duration_ms":8,"exit_code":1,"denied":true}`+"\n",
		fixture.ID)
	if err := os.WriteFile(filepath.Join(home, "hooks-audit.jsonl"), []byte(audit), 0o644); err != nil {
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

	// hooks/list: the only project is the default; project row first,
	// user row second (dispatch order), shapes as shipped.
	res, err := reg.Dispatch(ctx, "hooks/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("hooks/list: %v", err)
	}
	b, _ := json.Marshal(res)
	want := `[{"name":"proj-guard","event":"pre_tool","command":"exit 1","source":"project","tool":"bash","timeout_ms":5000},{"name":"user-notify","event":"session_start","command":"echo start","source":"user","tool":"*","timeout_ms":5000}]`
	if string(b) != `{"hooks":`+want+`}` {
		t.Fatalf("hooks/list = %s, want %s", b, want)
	}

	// hooks/audit/tail: the project filter keeps the entry the fixture
	// session attributes to this project, drops the gone-session one.
	res, err = reg.Dispatch(ctx, "hooks/audit/tail", json.RawMessage(fmt.Sprintf(`{"project_id":%q}`, pid)))
	if err != nil {
		t.Fatalf("hooks/audit/tail: %v", err)
	}
	b, _ = json.Marshal(res)
	if !strings.Contains(string(b), fixture.ID) || strings.Contains(string(b), "iiiiiiii") {
		t.Fatalf("hooks/audit/tail = %s, want only the fixture session's entry", b)
	}

	// follow=true keeps the one-shot contract honest through the
	// production registry.
	_, err = reg.Dispatch(ctx, "hooks/audit/tail", json.RawMessage(`{"follow":true}`))
	var he *rpc.Error
	if !errors.As(err, &he) || he.Code != rpc.CodeInvalidParams {
		t.Errorf("follow=true = %v, want -32602", err)
	}
}
