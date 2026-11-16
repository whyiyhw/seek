package methods

// Memory method set tests (W4.3 acceptance, all sandbox: a temporary
// SEEK_HOME carrying fake projects/<id>/{manifest.json,memory.jsonl}
// trees — no real key, no real home):
//
//  1. happy paths: list/get/projects over the fixture, default-project
//     resolution (newest manifest LastSeen), never-null arrays;
//  2. the §3.5 error table: -32602 malformed params/ids, -32002
//     unknown memory, -32003 unknown project, -32601 unknown method
//     (the registry-level row, continuing the W4.2 table style);
//  3. tolerance: corrupt/foreign project dirs are skipped, not fatal.

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

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// assetEnv is the shared W4.3 fixture: one SEEK_HOME with two valid
// projects (alpha: two memories incl. one stale; beta: none, newest
// LastSeen), one corrupt project dir (valid hex name, no manifest),
// and a sessions store whose CWDs bucket into alpha (main + branch)
// and a third no-project hash.
type assetEnv struct {
	home      string
	alphaRoot string
	betaRoot  string
	alphaID   string
	betaID    string
	otherID   string // hash of /nowhere-at-all — sessions bucket here, no project dir
	store     *session.Store
	reg       *rpc.Registry
}

var (
	fixtureTime = time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	staleEntry  = memory.Entry{Name: "aaa-old-decision", Tagline: "first take", Content: "superseded rationale"}
	pinnedEntry = memory.Entry{
		Name:    "zzz-keep-forever",
		Tagline: "load-bearing",
		Content: "the body with\ntwo lines",
		Tags:    []string{"infra"},
		Pinned:  true,
		Stale:   true,
	}
)

// newAssetEnv builds the fixture and wires all four W4.2+W4.3 families
// the way servecli does. Project roots live INSIDE the temp home so
// optional-path stats (project/get's AGENTS.md probe) are testable:
// alpha gets an AGENTS.md, beta gets nothing.
func newAssetEnv(t *testing.T) *assetEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "") // isolate from a dev-machine override

	alphaRoot := filepath.Join(home, "work", "alpha")
	betaRoot := filepath.Join(home, "work", "beta")
	for _, root := range []string{alphaRoot, betaRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(alphaRoot, "AGENTS.md"), []byte("# alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &assetEnv{
		home:      home,
		alphaRoot: alphaRoot,
		betaRoot:  betaRoot,
		alphaID:   paths.ProjectID(alphaRoot),
		betaID:    paths.ProjectID(betaRoot),
		otherID:   paths.ProjectID("/nowhere-at-all"),
	}

	// beta is the newer project (LastSeen later) → the default-project
	// proxy for memory/list|get must resolve to beta.
	writeProjectFixture(t, home, env.alphaID, alphaRoot,
		time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), staleEntry, pinnedEntry)
	writeProjectFixture(t, home, env.betaID, betaRoot,
		time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) /* no entries */)

	// Corrupt project dir: valid 16-hex name, no manifest → skipped by
	// ListProjects, never fatal, never listed.
	if err := os.MkdirAll(filepath.Join(home, "projects", strings.Repeat("f", 16)), 0o755); err != nil {
		t.Fatal(err)
	}

	// Sessions: alpha main + alpha /branch child (forks don't count)
	// + one for a hash with no project dir.
	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	env.store = st
	mains := &session.Session{
		SchemaVersion: 3, ID: "20261115-090000-aaaaaaaaaaaaaaaa",
		Model: "deepseek-chat", CWD: alphaRoot, Turns: 2, ToolCalls: 2,
		Usage: deepseek.Usage{PromptTokens: 1000, CompletionTokens: 200, PromptCacheHitTokens: 600, PromptCacheMissTokens: 400},
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "fix the bug"},
			{Role: deepseek.RoleAssistant, Content: "done"},
		},
	}
	branch := &session.Session{
		SchemaVersion: 3, ID: "20261115-093000-bbbbbbbbbbbbbbbb",
		Model: "deepseek-chat", CWD: alphaRoot, ParentID: mains.ID,
		Messages: []deepseek.Message{{Role: deepseek.RoleUser, Content: "branch"}},
	}
	orphan := &session.Session{
		SchemaVersion: 3, ID: "20261115-100000-cccccccccccccccc",
		Model: "deepseek-chat", CWD: "/nowhere-at-all",
		Usage: deepseek.Usage{PromptTokens: 10, PromptCacheHitTokens: 8, PromptCacheMissTokens: 2},
	}
	for _, s := range []*session.Session{mains, branch, orphan} {
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	reg := rpc.NewRegistry()
	NewMemories().RegisterAll(reg)
	NewProjects(st).RegisterAll(reg)
	NewStats(st).RegisterAll(reg)
	env.reg = reg
	return env
}

// writeProjectFixture lays down one project dir: manifest.json with
// controlled FirstSeen/LastSeen + optional memory.jsonl entries.
func writeProjectFixture(t *testing.T, home, id, root string, lastSeen time.Time, entries ...memory.Entry) {
	t.Helper()
	dir := filepath.Join(home, "projects", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	man := memory.Manifest{
		SchemaVersion: memory.SchemaVersion,
		ProjectID:     id,
		AbsPath:       root,
		FirstSeen:     lastSeen.Add(-24 * time.Hour),
		LastSeen:      lastSeen,
	}
	b, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		return
	}
	var buf strings.Builder
	for i := range entries {
		e := entries[i]
		e.SchemaVersion = memory.SchemaVersion
		e.CreatedAt = fixtureTime
		e.UpdatedAt = fixtureTime
		e.LastRecalledAt = fixtureTime
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.jsonl"), []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryList(t *testing.T) {
	env := newAssetEnv(t)

	// Explicit project: name-sorted, stale included and flagged.
	res, err := env.reg.Dispatch(context.Background(), "memory/list", json.RawMessage(fmt.Sprintf(`{"project_id":%q}`, env.alphaID)))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(memoryListResult)
	if len(got.Memories) != 2 {
		t.Fatalf("memories = %d, want 2 (%+v)", len(got.Memories), got)
	}
	if got.Memories[0].Name != staleEntry.Name || got.Memories[1].Name != pinnedEntry.Name {
		t.Errorf("order = %s %s, want name-sorted", got.Memories[0].Name, got.Memories[1].Name)
	}
	if !got.Memories[1].Pinned || !got.Memories[1].Stale {
		t.Errorf("flags lost: %+v", got.Memories[1])
	}
	if got.Memories[0].UpdatedAt != fixtureTime {
		t.Errorf("updated_at = %v, want fixture time", got.Memories[0].UpdatedAt)
	}

	// Default project: newest LastSeen = beta (empty but existing).
	res, err = env.reg.Dispatch(context.Background(), "memory/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"memories":[]}` {
		t.Errorf("default project (beta, empty) = %s, want {\"memories\":[]}", b)
	}

	// No params member at all is the zero value → default project.
	if _, err := env.reg.Dispatch(context.Background(), "memory/list", nil); err != nil {
		t.Errorf("absent params: %v", err)
	}
}

func TestMemoryGet(t *testing.T) {
	env := newAssetEnv(t)

	params := fmt.Sprintf(`{"project_id":%q,"name":%q}`, env.alphaID, pinnedEntry.Name)
	res, err := env.reg.Dispatch(context.Background(), "memory/get", json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(memoryGetResult)
	if got.Name != pinnedEntry.Name || got.Content != pinnedEntry.Content || got.Tagline != pinnedEntry.Tagline {
		t.Errorf("entry drifted: %+v", got)
	}
	if !got.Pinned || !got.Stale {
		t.Errorf("flags lost: %+v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "infra" {
		t.Errorf("tags = %v, want [infra]", got.Tags)
	}
	if got.CreatedAt != fixtureTime || got.LastRecalledAt != fixtureTime || got.RecallCount != 0 {
		t.Errorf("timestamps drifted: %+v", got)
	}
	// Tags on a tagless entry must marshal as [], never null.
	res, err = env.reg.Dispatch(context.Background(), "memory/get",
		json.RawMessage(fmt.Sprintf(`{"project_id":%q,"name":%q}`, env.alphaID, staleEntry.Name)))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), `"tags":null`) {
		t.Errorf("tagless entry marshals tags null: %s", b)
	}
}

func TestMemoryProjects(t *testing.T) {
	env := newAssetEnv(t)

	res, err := env.reg.Dispatch(context.Background(), "memory/projects", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(memoryProjectsResult)
	if len(got.Projects) != 2 { // the corrupt hex dir is skipped
		t.Fatalf("projects = %d, want 2 (%+v)", len(got.Projects), got)
	}
	// Newest LastSeen first: beta (11-14) before alpha (11-01).
	if got.Projects[0].ProjectID != env.betaID || got.Projects[1].ProjectID != env.alphaID {
		t.Errorf("order = %s %s, want beta alpha (newest first)", got.Projects[0].ProjectID, got.Projects[1].ProjectID)
	}
	if got.Projects[0].Memories != 0 || got.Projects[1].Memories != 2 {
		t.Errorf("memories counts = %d %d, want 0 2", got.Projects[0].Memories, got.Projects[1].Memories)
	}
	if got.Projects[1].Root != env.alphaRoot {
		t.Errorf("root = %q, want %q", got.Projects[1].Root, env.alphaRoot)
	}

	// Empty SEEK_HOME (no projects dir at all): an empty array, never
	// an error, never null.
	t.Setenv("SEEK_HOME", t.TempDir())
	res, err = env.reg.Dispatch(context.Background(), "memory/projects", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"projects":[]}` {
		t.Errorf("empty home = %s, want {\"projects\":[]}", b)
	}
}

// The §3.5 error table for the memory family (plus the registry-level
// -32601 row continuing the W4.2 style): malformed params and ids are
// -32602; a well-formed but absent memory is -32002 naming it in data;
// an absent project is -32003.
func TestMemoryErrorCodes(t *testing.T) {
	env := newAssetEnv(t)
	const unknownProject = "0123456789abcdef" // well-formed, not on disk

	cases := []struct {
		name   string
		method string
		params string
		want   int
	}{
		{"unknown method", "memory/purge", `{}`, rpc.CodeMethodNotFound},

		{"list: scalar params", "memory/list", `42`, rpc.CodeInvalidParams},
		{"list: array params", "memory/list", `["x"]`, rpc.CodeInvalidParams},
		{"list: short project_id", "memory/list", `{"project_id":"abc"}`, rpc.CodeInvalidParams},
		{"list: non-hex project_id", "memory/list", `{"project_id":"zzzzzzzzzzzzzzzz"}`, rpc.CodeInvalidParams},
		{"list: traversal project_id", "memory/list", `{"project_id":"../../etc/passwd"}`, rpc.CodeInvalidParams},
		{"list: uppercase project_id", "memory/list", `{"project_id":"AAAAAAAAAAAAAAAA"}`, rpc.CodeInvalidParams},
		{"list: unknown project", "memory/list", fmt.Sprintf(`{"project_id":%q}`, unknownProject), rpc.CodeProjectNotFound},

		{"get: missing name", "memory/get", `{}`, rpc.CodeInvalidParams},
		{"get: empty name", "memory/get", `{"name":""}`, rpc.CodeInvalidParams},
		{"get: null name", "memory/get", `{"name":null}`, rpc.CodeInvalidParams},
		{"get: array params", "memory/get", `["name"]`, rpc.CodeInvalidParams},
		{"get: bad project_id", "memory/get", `{"project_id":"x","name":"n"}`, rpc.CodeInvalidParams},
		{"get: unknown project", "memory/get", fmt.Sprintf(`{"project_id":%q,"name":"n"}`, unknownProject), rpc.CodeProjectNotFound},
		{"get: unknown memory", "memory/get", fmt.Sprintf(`{"project_id":%q,"name":"nope"}`, env.alphaID), rpc.CodeMemoryNotFound},

		{"projects: scalar params", "memory/projects", `"x"`, rpc.CodeInvalidParams},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.reg.Dispatch(context.Background(), tc.method, json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("want error, got success")
			}
			var e *rpc.Error
			if !errors.As(err, &e) {
				t.Fatalf("error = %T (%v), want *rpc.Error", err, err)
			}
			if e.Code != tc.want {
				t.Errorf("code = %d, want %d (%v)", e.Code, tc.want, err)
			}
			switch tc.want {
			case rpc.CodeMemoryNotFound:
				data, ok := e.Data.(map[string]string)
				if !ok || data["name"] == "" || data["project_id"] == "" {
					t.Errorf("memory error must name project_id+name in data: %+v", e)
				}
			case rpc.CodeProjectNotFound:
				data, ok := e.Data.(map[string]string)
				if !ok || data["project_id"] == "" {
					t.Errorf("project error must name project_id in data: %+v", e)
				}
			}
		})
	}
}

// With no projects on disk, the default-project proxy has nothing to
// resolve to: -32003, not an internal error and not an empty success.
func TestMemoryDefaultProjectNoneOnDisk(t *testing.T) {
	env := newAssetEnv(t)
	t.Setenv("SEEK_HOME", t.TempDir())

	for _, method := range []string{"memory/list", "memory/get"} {
		params := json.RawMessage(`{}`)
		if method == "memory/get" {
			params = json.RawMessage(`{"name":"any"}`)
		}
		_, err := env.reg.Dispatch(context.Background(), method, params)
		var e *rpc.Error
		if !errors.As(err, &e) || e.Code != rpc.CodeProjectNotFound {
			t.Errorf("%s on empty home = %v, want -32003", method, err)
		}
	}
}
