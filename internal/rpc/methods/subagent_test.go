package methods

// subagent method set tests (inspect-rpc §6 #12 close-out, all sandbox:
// a temporary SEEK_HOME carrying two fake projects, each with a
// hand-written subagents.jsonl in the on-disk event dialect — no real
// key, no real home):
//
//  1. list: explicit project, default project (newest LastSeen), the
//     status / parent_sid filters, never-null arrays, newest-first order;
//  2. get: the folded row plus the transcript tail (the same information
//     GET /subagents?sub= carries), an active row with no transcript on
//     disk ("" is an answer), the project scan when project_id is absent;
//  3. the §3.5 error table: -32602 bad params/ids, -32003 unknown
//     project, -32006 unknown sub_sid, -32601 unknown method.

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

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/subagent"
)

// subagentEnv is the shared fixture: one SEEK_HOME with two projects
// (alpha holds the rows under test; beta is newer so it is the default
// project proxy and holds one unrelated row) and a registry with the
// subagent family wired the way servecli does.
type subagentEnv struct {
	home    string
	alpha   string
	beta    string
	alphaID string
	betaID  string
	reg     *rpc.Registry
}

// Subagent rows used across the tests.
const (
	subSyncDone = "20261115-090000-aaaaaaaaaaaaaaaa"
	subBgActive = "20261115-091500-bbbbbbbbbbbbbbbb"
	subFailed   = "20261115-093000-cccccccccccccccc"
	subBeta     = "20261115-094500-dddddddddddddddd"
	subUnknown  = "20261115-100000-eeeeeeeeeeeeeeee"
)

// newSubagentEnv builds the fixture: manifests for alpha (older) and
// beta (newer), alpha's index carrying a completed sync row (with a
// transcript), an active background row (no transcript yet) and a
// failed row, beta's index carrying one completed row.
func newSubagentEnv(t *testing.T) *subagentEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	env := &subagentEnv{
		home:  home,
		alpha: filepath.Join(home, "work", "alpha"),
		beta:  filepath.Join(home, "work", "beta"),
	}
	env.alphaID = paths.ProjectID(env.alpha)
	env.betaID = paths.ProjectID(env.beta)
	for _, root := range []string{env.alpha, env.beta} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeProjectFixture(t, home, env.alphaID, env.alpha,
		time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	writeProjectFixture(t, home, env.betaID, env.beta,
		time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))

	writeSubagentIndex(t, env.alpha,
		// Completed sync row with a transcript.
		`{"event":"started","sub_sid":"`+subSyncDone+`","ts":"2026-11-15T09:00:00Z","parent_sid":"sess-A","parent_turn":4,"type":"explore","description":"find handlers"}`,
		`{"event":"completed","sub_sid":"`+subSyncDone+`","ts":"2026-11-15T09:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}`,
		// Background row still running (no transcript on disk yet).
		`{"event":"started","sub_sid":"`+subBgActive+`","ts":"2026-11-15T09:15:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}`,
		// Failed row with a reason.
		`{"event":"started","sub_sid":"`+subFailed+`","ts":"2026-11-15T09:30:00Z","parent_sid":"sess-B","type":"plan","description":"outline"}`,
		`{"event":"failed","sub_sid":"`+subFailed+`","ts":"2026-11-15T09:31:00Z","reason":"max_turns_exceeded"}`,
	)
	writeSubagentIndex(t, env.beta,
		`{"event":"started","sub_sid":"`+subBeta+`","ts":"2026-11-15T09:45:00Z","parent_sid":"sess-C","type":"explore","description":"beta work"}`,
		`{"event":"completed","sub_sid":"`+subBeta+`","ts":"2026-11-15T09:50:00Z","tokens":{"prompt":7,"completion":3,"cache_hit":0}}`,
	)
	writeSubTranscript(t, env.alpha, "sess-A", subSyncDone,
		`{"schema_version":2,"id":"h"}`,
		`{"role":"user","content":"find handlers"}`,
		`{"role":"assistant","content":"first pass"}`,
		`{"role":"assistant","content":"final findings"}`,
	)

	reg := rpc.NewRegistry()
	NewSubagents().RegisterAll(reg)
	env.reg = reg
	return env
}

// writeSubagentIndex writes lines into project's subagents.jsonl.
func writeSubagentIndex(t *testing.T, project string, lines ...string) {
	t.Helper()
	index, err := paths.SubagentsIndex(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(index), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSubTranscript writes the child's own session JSONL (header line
// then message lines) under the redirected SEEK_HOME.
func writeSubTranscript(t *testing.T, project, parentSid, subSid string, lines ...string) {
	t.Helper()
	dir, err := paths.SubagentSessionDir(project, parentSid, subSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dispatchList runs subagent/list and decodes the result.
func dispatchList(t *testing.T, reg *rpc.Registry, params string) subagentListResult {
	t.Helper()
	res, err := reg.Dispatch(context.Background(), "subagent/list", json.RawMessage(params))
	if err != nil {
		t.Fatalf("subagent/list %s: %v", params, err)
	}
	got, ok := res.(subagentListResult)
	if !ok {
		t.Fatalf("subagent/list result type = %T, want subagentListResult", res)
	}
	return got
}

func TestSubagentList(t *testing.T) {
	env := newSubagentEnv(t)

	// Explicit project: newest-first, three rows, folded state.
	got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q}`, env.alphaID))
	if got.ProjectID != env.alphaID {
		t.Errorf("project_id = %q, want %q", got.ProjectID, env.alphaID)
	}
	if len(got.Subagents) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(got.Subagents), got.Subagents)
	}
	wantOrder := []string{subFailed, subBgActive, subSyncDone}
	for i, sid := range wantOrder {
		if got.Subagents[i].SubSid != sid {
			t.Fatalf("row[%d] = %s, want %s (newest first)", i, got.Subagents[i].SubSid, sid)
		}
	}
	bySid := map[string]subagent.Subagent{}
	for _, s := range got.Subagents {
		bySid[s.SubSid] = s
	}
	if s := bySid[subSyncDone]; s.Status != subagent.StatusCompleted || s.Tokens.Prompt != 100 || s.Description != "find handlers" || s.Type != subagent.TypeExplore {
		t.Errorf("completed row folded wrong: %+v", s)
	}
	if s := bySid[subBgActive]; s.Status != subagent.StatusActive || !s.Background {
		t.Errorf("active row folded wrong: %+v", s)
	}
	if s := bySid[subFailed]; s.Status != subagent.StatusFailed || s.Reason != "max_turns_exceeded" {
		t.Errorf("failed row folded wrong: %+v", s)
	}

	// status filter — the sketch's vocabulary plus "all".
	if got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q,"status":"failed"}`, env.alphaID)); len(got.Subagents) != 1 || got.Subagents[0].SubSid != subFailed {
		t.Errorf("status=failed → %+v, want [%s]", got.Subagents, subFailed)
	}
	if got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q,"status":"active"}`, env.alphaID)); len(got.Subagents) != 1 || got.Subagents[0].SubSid != subBgActive {
		t.Errorf("status=active → %+v, want [%s]", got.Subagents, subBgActive)
	}
	if got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q,"status":"all"}`, env.alphaID)); len(got.Subagents) != 3 {
		t.Errorf("status=all → %d rows, want 3", len(got.Subagents))
	}

	// parent_sid filter.
	if got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q,"parent_sid":"sess-B"}`, env.alphaID)); len(got.Subagents) != 1 || got.Subagents[0].SubSid != subFailed {
		t.Errorf("parent_sid=sess-B → %+v, want [%s]", got.Subagents, subFailed)
	}
	// A filter that matches nothing answers an EMPTY ARRAY, not null.
	got = dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q,"parent_sid":"nope"}`, env.alphaID))
	if got.Subagents == nil {
		t.Error("empty filter result must be [] not null")
	}
	if len(got.Subagents) != 0 {
		t.Errorf("parent_sid=nope → %d rows, want 0", len(got.Subagents))
	}

	// Default project = newest manifest LastSeen = beta.
	got = dispatchList(t, env.reg, `{}`)
	if got.ProjectID != env.betaID || len(got.Subagents) != 1 || got.Subagents[0].SubSid != subBeta {
		t.Errorf("default project → %q %+v, want beta's single row", got.ProjectID, got.Subagents)
	}

	// Params tolerance: unknown fields are ignored (§3.2 stability).
	if got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q,"future_flag":true}`, env.alphaID)); len(got.Subagents) != 3 {
		t.Errorf("unknown param field must be ignored, got %d rows", len(got.Subagents))
	}
}

func TestSubagentListErrors(t *testing.T) {
	env := newSubagentEnv(t)

	cases := []struct {
		name   string
		params string
		code   int
	}{
		{"unknown status", `{"status":"zombie"}`, rpc.CodeInvalidParams},
		{"malformed params", `"not-an-object"`, rpc.CodeInvalidParams},
		{"bad project id", `{"project_id":"../../etc"}`, rpc.CodeInvalidParams},
		{"unknown project", `{"project_id":"0123456789abcdef"}`, rpc.CodeProjectNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := env.reg.Dispatch(context.Background(), "subagent/list", json.RawMessage(c.params))
			var e *rpc.Error
			if !errors.As(err, &e) || e.Code != c.code {
				t.Fatalf("err = %v, want code %d", err, c.code)
			}
		})
	}

	// Unknown method stays -32601 (the registry row).
	_, err := env.reg.Dispatch(context.Background(), "subagent/tree", json.RawMessage(`{}`))
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != rpc.CodeMethodNotFound {
		t.Fatalf("subagent/tree = %v, want -32601 (not shipped: depth-1 cap makes it the flat list)", err)
	}
}

// dispatchGet runs subagent/get and decodes the result.
func dispatchGet(t *testing.T, reg *rpc.Registry, params string) subagentGetResult {
	t.Helper()
	res, err := reg.Dispatch(context.Background(), "subagent/get", json.RawMessage(params))
	if err != nil {
		t.Fatalf("subagent/get %s: %v", params, err)
	}
	got, ok := res.(subagentGetResult)
	if !ok {
		t.Fatalf("subagent/get result type = %T, want subagentGetResult", res)
	}
	return got
}

func TestSubagentGet(t *testing.T) {
	env := newSubagentEnv(t)

	// Explicit project: the folded row + the transcript's LAST
	// assistant entry (the same information GET /subagents?sub= carries).
	got := dispatchGet(t, env.reg, fmt.Sprintf(`{"sub_sid":%q,"project_id":%q}`, subSyncDone, env.alphaID))
	if got.Sub.SubSid != subSyncDone || got.Sub.Status != subagent.StatusCompleted {
		t.Errorf("row = %+v, want the completed row", got.Sub)
	}
	if got.LastAssistant != "final findings" {
		t.Errorf("last_assistant = %q, want the LAST assistant entry", got.LastAssistant)
	}
	if got.ProjectID != env.alphaID || got.ProjectRoot != env.alpha {
		t.Errorf("project = %q %q, want alpha's id/root", got.ProjectID, got.ProjectRoot)
	}

	// Active row whose child has written no transcript yet: "" is an
	// answer, not an error.
	got = dispatchGet(t, env.reg, fmt.Sprintf(`{"sub_sid":%q,"project_id":%q}`, subBgActive, env.alphaID))
	if got.Sub.Status != subagent.StatusActive || got.LastAssistant != "" {
		t.Errorf("active row = %+v %q, want active with empty tail", got.Sub, got.LastAssistant)
	}

	// No project_id: the scan finds the row wherever it lives (alpha's
	// row is NOT in the default project — beta is newer).
	got = dispatchGet(t, env.reg, fmt.Sprintf(`{"sub_sid":%q}`, subSyncDone))
	if got.Sub.SubSid != subSyncDone || got.ProjectID != env.alphaID {
		t.Errorf("scan lookup = %+v (project %q), want alpha's row", got.Sub, got.ProjectID)
	}
	// And beta's row resolves too, from the same scan.
	if got := dispatchGet(t, env.reg, fmt.Sprintf(`{"sub_sid":%q}`, subBeta)); got.ProjectID != env.betaID {
		t.Errorf("beta row resolved to project %q, want %q", got.ProjectID, env.betaID)
	}

	// The failure reason rides along (the panel's "失败原因直显").
	got = dispatchGet(t, env.reg, fmt.Sprintf(`{"sub_sid":%q}`, subFailed))
	if got.Sub.Status != subagent.StatusFailed || got.Sub.Reason != "max_turns_exceeded" {
		t.Errorf("failed row = %+v, want the reason surfaced", got.Sub)
	}
}

func TestSubagentGetErrors(t *testing.T) {
	env := newSubagentEnv(t)

	cases := []struct {
		name   string
		params string
		code   int
	}{
		{"missing sub_sid", `{}`, rpc.CodeInvalidParams},
		{"traversal sub_sid", `{"sub_sid":"../../etc/passwd"}`, rpc.CodeInvalidParams},
		{"malformed params", `[]`, rpc.CodeInvalidParams},
		{"unknown project", fmt.Sprintf(`{"sub_sid":%q,"project_id":"0123456789abcdef"}`, subSyncDone), rpc.CodeProjectNotFound},
		{"unknown sub_sid", fmt.Sprintf(`{"sub_sid":%q}`, subUnknown), rpc.CodeSubagentNotFound},
		// A row that exists in ANOTHER project is not found when the
		// caller pins a project — the id is scoped by the caller's claim.
		{"wrong project", fmt.Sprintf(`{"sub_sid":%q,"project_id":%q}`, subSyncDone, env.betaID), rpc.CodeSubagentNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := env.reg.Dispatch(context.Background(), "subagent/get", json.RawMessage(c.params))
			var e *rpc.Error
			if !errors.As(err, &e) || e.Code != c.code {
				t.Fatalf("err = %v, want code %d", err, c.code)
			}
		})
	}
}

// TestSubagentListEmptyIndex pins the fresh-project case: a project
// with a manifest but no subagents.jsonl answers an empty array (the
// panel renders an empty list, it never sees a failed call).
func TestSubagentListEmptyIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")
	root := filepath.Join(home, "work", "fresh")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFixture(t, home, paths.ProjectID(root), root, time.Now().UTC())

	reg := rpc.NewRegistry()
	NewSubagents().RegisterAll(reg)
	got := dispatchList(t, reg, `{}`)
	if got.Subagents == nil {
		t.Fatal("no index → nil slice, want []")
	}
	if len(got.Subagents) != 0 {
		t.Fatalf("no index → %d rows, want 0", len(got.Subagents))
	}
}

// TestSubagentGetNoProjects pins the machine-with-nothing case: no
// projects on disk at all, so the scan is empty and the answer is
// not-found (never a -32003 blaming a project nobody named).
func TestSubagentGetNoProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	reg := rpc.NewRegistry()
	NewSubagents().RegisterAll(reg)
	_, err := reg.Dispatch(context.Background(), "subagent/get", json.RawMessage(fmt.Sprintf(`{"sub_sid":%q}`, subUnknown)))
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != rpc.CodeSubagentNotFound {
		t.Fatalf("err = %v, want -32006", err)
	}
}

// TestSubagentListMatchesPanelFold pins the "one projection" claim:
// the method's rows are byte-identical to subagent.ListProject's — the
// exact call GET /subagents makes (webfrontend_subagents.go). If a
// future change gives the method its own fold, this fails.
func TestSubagentListMatchesPanelFold(t *testing.T) {
	env := newSubagentEnv(t)
	got := dispatchList(t, env.reg, fmt.Sprintf(`{"project_id":%q}`, env.alphaID))

	direct, err := subagent.ListProject(env.alpha, subagent.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Subagents) != len(direct) {
		t.Fatalf("method rows = %d, panel fold = %d", len(got.Subagents), len(direct))
	}
	for i := range direct {
		if got.Subagents[i] != direct[i] {
			t.Fatalf("row[%d] drifted between the method and the panel fold:\n method=%+v\n panel =%+v",
				i, got.Subagents[i], direct[i])
		}
	}
}
