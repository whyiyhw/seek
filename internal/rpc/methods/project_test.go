package methods

// Project method set tests (W4.3 acceptance): list/get over the
// shared assetEnv fixture — session bucketing (forks skipped, orphan
// hashes invisible), optional AGENTS.md/hooks paths appearing only
// when the file exists, the §3.5 error table.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/whyiyhw/seek/internal/rpc"
)

func TestProjectList(t *testing.T) {
	env := newAssetEnv(t)

	res, err := env.reg.Dispatch(context.Background(), "project/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(projectListResult)
	if len(got.Projects) != 2 { // corrupt hex dir skipped
		t.Fatalf("projects = %d, want 2 (%+v)", len(got.Projects), got)
	}
	// ListProjects' own order: id-sorted, asserted here so the browse
	// views relying on it fail loudly if it ever drifts.
	var order []string
	byID := map[string]projectListItem{}
	for _, p := range got.Projects {
		order = append(order, p.ProjectID)
		byID[p.ProjectID] = p
	}
	if order[0] > order[1] {
		t.Errorf("order = %v, want id-sorted", order)
	}
	alpha := byID[env.alphaID]
	if alpha.Sessions != 1 { // main counts, the /branch fork does not
		t.Errorf("alpha sessions = %d, want 1 (fork skipped)", alpha.Sessions)
	}
	if alpha.Memories != 2 || alpha.Root != env.alphaRoot {
		t.Errorf("alpha row drifted: %+v", alpha)
	}
	if beta := byID[env.betaID]; beta.Sessions != 0 || beta.Memories != 0 {
		t.Errorf("beta row drifted: %+v", beta)
	}
	// The /nowhere-at-all session buckets to its own hash and has no
	// project dir: invisible, never a phantom row.
	for _, p := range got.Projects {
		if p.ProjectID == env.otherID {
			t.Errorf("phantom project row for session-only hash %s", env.otherID)
		}
	}
}

func TestProjectGet(t *testing.T) {
	env := newAssetEnv(t)

	res, err := env.reg.Dispatch(context.Background(), "project/get", json.RawMessage(fmt.Sprintf(`{"project_id":%q}`, env.alphaID)))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(projectGetResult)
	if got.ProjectID != env.alphaID || got.Root != env.alphaRoot {
		t.Errorf("identity drifted: %+v", got)
	}
	if got.Sessions != 1 || got.Memories != 2 {
		t.Errorf("counts drifted: %+v", got)
	}
	if got.FirstSeen.IsZero() || got.LastSeen.IsZero() {
		t.Errorf("manifest timestamps lost: %+v", got)
	}
	// alpha has AGENTS.md on disk, no hooks.toml.
	if got.AgentsMDPath != filepath.Join(env.alphaRoot, "AGENTS.md") {
		t.Errorf("agents_md_path = %q, want alpha's AGENTS.md", got.AgentsMDPath)
	}
	if got.HooksTOMLPath != "" {
		t.Errorf("hooks_toml_path = %q, want absent (no file)", got.HooksTOMLPath)
	}

	// beta: neither file → both paths absent, still a valid get.
	res, err = env.reg.Dispatch(context.Background(), "project/get", json.RawMessage(fmt.Sprintf(`{"project_id":%q}`, env.betaID)))
	if err != nil {
		t.Fatal(err)
	}
	if beta := res.(projectGetResult); beta.AgentsMDPath != "" || beta.HooksTOMLPath != "" {
		t.Errorf("beta optional paths = %q %q, want absent", beta.AgentsMDPath, beta.HooksTOMLPath)
	}

	// hooks.toml appears once it exists on disk.
	hooks := filepath.Join(env.betaRoot, ".seek", "hooks.toml")
	if err := os.MkdirAll(filepath.Dir(hooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte("[hooks]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = env.reg.Dispatch(context.Background(), "project/get", json.RawMessage(fmt.Sprintf(`{"project_id":%q}`, env.betaID)))
	if err != nil {
		t.Fatal(err)
	}
	if beta := res.(projectGetResult); beta.HooksTOMLPath != hooks {
		t.Errorf("hooks_toml_path = %q, want %q", beta.HooksTOMLPath, hooks)
	}
}

// A manifest whose AbsPath no longer exists on disk (project moved or
// deleted): metadata still reads, optional paths just stay absent —
// the manifest is the record, the tree is decoration.
func TestProjectGetRootMissingOnDisk(t *testing.T) {
	env := newAssetEnv(t)
	t.Setenv("SEEK_HOME", t.TempDir())
	writeProjectFixture(t, os.Getenv("SEEK_HOME"), "0123456789abcdef", "/gone/away",
		fixtureTime)

	res, err := env.reg.Dispatch(context.Background(), "project/get", json.RawMessage(`{"project_id":"0123456789abcdef"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(projectGetResult)
	if got.Root != "/gone/away" || got.AgentsMDPath != "" || got.HooksTOMLPath != "" {
		t.Errorf("moved project = %+v, want root recorded, no optional paths", got)
	}
}

func TestProjectErrorCodes(t *testing.T) {
	env := newAssetEnv(t)

	cases := []struct {
		name   string
		method string
		params string
		want   int
	}{
		{"list: scalar params", "project/list", `7`, rpc.CodeInvalidParams},
		{"list: array params", "project/list", `[]`, rpc.CodeInvalidParams},
		{"list: unknown field tolerated", "project/list", `{"future_flag":true}`, -1}, // 0: success

		{"get: missing project_id", "project/get", `{}`, rpc.CodeInvalidParams},
		{"get: empty project_id", "project/get", `{"project_id":""}`, rpc.CodeInvalidParams},
		{"get: short project_id", "project/get", `{"project_id":"abc"}`, rpc.CodeInvalidParams},
		{"get: traversal project_id", "project/get", `{"project_id":"../../../../etc"}`, rpc.CodeInvalidParams},
		{"get: scalar params", "project/get", `"alpha"`, rpc.CodeInvalidParams},
		{"get: unknown project", "project/get", `{"project_id":"0123456789abcdef"}`, rpc.CodeProjectNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.reg.Dispatch(context.Background(), tc.method, json.RawMessage(tc.params))
			if tc.want == -1 {
				if err != nil {
					t.Errorf("tolerated params errored: %v", err)
				}
				return
			}
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
			if tc.want == rpc.CodeProjectNotFound {
				data, ok := e.Data.(map[string]string)
				if !ok || data["project_id"] == "" {
					t.Errorf("project error must name project_id in data: %+v", e)
				}
			}
		})
	}
}
