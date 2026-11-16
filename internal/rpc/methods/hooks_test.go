package methods

// Hooks method set tests (W4.6 acceptance, all sandbox: the shared
// newAssetEnv fixture plus hooks.toml / hooks-audit.jsonl files written
// under its temporary SEEK_HOME — no real key, no real home):
//
//  1. hooks/list: merged project+user view in dispatch order, default
//     project resolution, user-plane-only when no projects exist,
//     never-null arrays;
//  2. hooks/audit/tail: from_ts cutoff (unparseable entry ts kept,
//     CLI parity), limit tail semantics + bounds, follow rejection,
//     project attribution via the session store (fork/gone/traversal
//     sids degrade, never fail), ENOENT audit log;
//  3. the §3.5 error table: -32602 malformed params/ids/timestamp,
//     -32003 unknown project.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
)

// alphaHooksTOML / userHooksTOML are chosen so the merged view crosses
// every axis the contract names: two sources, two event groups, an
// explicit tool match and an explicit timeout against the defaults.
const (
	alphaHooksTOML = `
[[pre_tool]]
name = "alpha-lint"
command = "gofmt -l ."
timeout_ms = 250

[[session_end]]
name = "alpha-report"
command = "echo done"
`
	userHooksTOML = `
[[pre_tool]]
name = "user-deny"
command = "exit 1"
match = { tool = "bash" }

[[session_start]]
name = "user-notify"
command = "echo start"
`
)

// writeHooksFiles lays down project hooks under root/.seek/ and user
// hooks under the fixture home; either may be skipped to test the
// missing-file steady state (ENOENT is "no hooks", never an error).
func writeHooksFiles(t *testing.T, home, root, projectTOML, userTOML string) {
	t.Helper()
	if projectTOML != "" {
		dir := filepath.Join(root, ".seek")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hooks.toml"), []byte(projectTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if userTOML != "" {
		if err := os.WriteFile(filepath.Join(home, "hooks.toml"), []byte(userTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newHooksEnv is newAssetEnv plus a Hooks-only registry and the
// fixture's hook files (alpha project + user; beta — the default
// project — gets none).
func newHooksEnv(t *testing.T) *assetEnv {
	t.Helper()
	env := newAssetEnv(t)
	writeHooksFiles(t, env.home, env.alphaRoot, alphaHooksTOML, userHooksTOML)
	reg := rpc.NewRegistry()
	NewHooks(env.store).RegisterAll(reg)
	env.reg = reg
	return env
}

func dispatchHooks(t *testing.T, reg *rpc.Registry, method, params string) (any, error) {
	t.Helper()
	return reg.Dispatch(context.Background(), method, json.RawMessage(params))
}

func wantRPCCode(t *testing.T, err error, code int, contains string) {
	t.Helper()
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want code %d", err, code)
	}
	if contains != "" && !strings.Contains(e.Message, contains) {
		t.Errorf("message %q does not contain %q", e.Message, contains)
	}
}

func TestHooksList_MergedDispatchOrder(t *testing.T) {
	env := newHooksEnv(t)

	res, err := dispatchHooks(t, env.reg, "hooks/list", fmt.Sprintf(`{"project_id":%q}`, env.alphaID))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(hooksListResult)
	// Summarize order: event groups (pre_tool → … → session_end),
	// project before user within a group — the dispatch order.
	want := []hooksListItem{
		{Name: "alpha-lint", Event: "pre_tool", Command: "gofmt -l .", Source: "project", Tool: "*", TimeoutMs: 250},
		{Name: "user-deny", Event: "pre_tool", Command: "exit 1", Source: "user", Tool: "bash", TimeoutMs: 5000},
		{Name: "user-notify", Event: "session_start", Command: "echo start", Source: "user", Tool: "*", TimeoutMs: 5000},
		{Name: "alpha-report", Event: "session_end", Command: "echo done", Source: "project", Tool: "*", TimeoutMs: 5000},
	}
	if len(got.Hooks) != len(want) {
		t.Fatalf("hooks = %+v, want %d rows", got.Hooks, len(want))
	}
	for i, w := range want {
		if got.Hooks[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got.Hooks[i], w)
		}
	}
}

func TestHooksList_DefaultProjectAndErrors(t *testing.T) {
	env := newHooksEnv(t) // beta is the newest manifest → default project, no project file

	// Default: beta has no hooks.toml, so the user plane stands alone.
	res, err := dispatchHooks(t, env.reg, "hooks/list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	got := res.(hooksListResult)
	if len(got.Hooks) != 2 || got.Hooks[0].Name != "user-deny" || got.Hooks[1].Name != "user-notify" {
		t.Fatalf("default-project hooks = %+v, want the two user rows", got.Hooks)
	}

	// Absent params entirely is the same call.
	res, err = dispatchHooks(t, env.reg, "hooks/list", ``)
	if err != nil {
		t.Fatal(err)
	}
	if l := len(res.(hooksListResult).Hooks); l != 2 {
		t.Fatalf("no-params hooks = %d rows, want 2", l)
	}

	_, err = dispatchHooks(t, env.reg, "hooks/list", `{"project_id":"0123456789abcdef"}`)
	wantRPCCode(t, err, rpc.CodeProjectNotFound, "")

	_, err = dispatchHooks(t, env.reg, "hooks/list", `{"project_id":"zzz"}`)
	wantRPCCode(t, err, rpc.CodeInvalidParams, "16 hex")

	_, err = dispatchHooks(t, env.reg, "hooks/list", `[]`)
	wantRPCCode(t, err, rpc.CodeInvalidParams, "")
}

func TestHooksList_NoFilesNoProjects(t *testing.T) {
	// A machine with no hooks files at all answers an empty array, not
	// null; and with no projects either, the user plane (also absent)
	// still degrades to empty rather than -32003.
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")
	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	reg := rpc.NewRegistry()
	NewHooks(st).RegisterAll(reg)

	res, err := dispatchHooks(t, reg, "hooks/list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	got := res.(hooksListResult)
	if got.Hooks == nil || len(got.Hooks) != 0 {
		t.Fatalf("hooks = %#v, want empty non-nil", got.Hooks)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"hooks":[]`) {
		t.Fatalf("json = %s, want \"hooks\":[]", b)
	}
}

// ---------- hooks/audit/tail ----------

const (
	sidAlpha  = "20261115-090000-aaaaaaaaaaaaaaaa" // fixture: CWD = alphaRoot
	sidOrphan = "20261115-100000-cccccccccccccccc" // fixture: CWD = /nowhere-at-all
	sidGone   = "20261115-235999-dddddddddddddddd" // no transcript anywhere
	sidEvil   = "../../etc/passwd"                 // traversal-shaped; must never reach the fs
)

var auditFixture = []string{
	// e1, e3, e5 attribute to alpha; e2 to the /nowhere hash; e4 to a
	// gone session; e5 carries an unparseable ts (kept under from_ts,
	// CLI parity); the evil-sid line and a corrupt line exercise the
	// tolerance rules.
	`{"ts":"2026-11-15T20:00:00Z","event":"pre_tool","hook":"alpha-lint","tool":"edit","session_id":"` + sidAlpha + `","duration_ms":12,"exit_code":0,"denied":false}`,
	`{"ts":"2026-11-15T21:00:00Z","event":"post_tool","hook":"user-deny","session_id":"` + sidOrphan + `","duration_ms":5,"exit_code":0,"denied":false}`,
	`{"ts":"2026-11-15T22:00:00Z","event":"pre_tool","hook":"alpha-lint","tool":"bash","session_id":"` + sidAlpha + `","duration_ms":30,"exit_code":1,"denied":true}`,
	`{"ts":"2026-11-15T23:00:00Z","event":"session_start","hook":"user-notify","session_id":"` + sidGone + `","duration_ms":2,"exit_code":0,"denied":false}`,
	`{"ts":"not-a-time","event":"pre_prompt","hook":"x","session_id":"` + sidAlpha + `","duration_ms":0,"exit_code":0,"denied":false}`,
	`{"ts":"2026-11-15T23:30:00Z","event":"session_end","hook":"evil","session_id":"` + sidEvil + `","duration_ms":0,"exit_code":0,"denied":false}`,
	`{oops — corrupt line, skipped by ReadAuditLog`,
}

func writeAuditLog(t *testing.T, home string, lines []string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "hooks-audit.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newAuditEnv(t *testing.T) *assetEnv {
	t.Helper()
	env := newHooksEnv(t)
	writeAuditLog(t, env.home, auditFixture)
	return env
}

func auditEntries(t *testing.T, res any) []hooksconfig.AuditEntry {
	t.Helper()
	got, ok := res.(hooksAuditResult)
	if !ok {
		t.Fatalf("result = %T, want hooksAuditResult", res)
	}
	if got.Entries == nil {
		t.Fatal("entries = nil, want non-nil")
	}
	return got.Entries
}

func TestHooksAuditTail_PlainTail(t *testing.T) {
	env := newAuditEnv(t)

	// No filter: every parseable entry, file order (oldest→newest);
	// the corrupt line never appears.
	res, err := dispatchHooks(t, env.reg, "hooks/audit/tail", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	entries := auditEntries(t, res)
	if len(entries) != 6 {
		t.Fatalf("entries = %d, want 6 (corrupt line skipped): %+v", len(entries), entries)
	}
	if entries[0].Hook != "alpha-lint" || entries[5].Hook != "evil" {
		t.Errorf("order = %s…%s, want file order", entries[0].Hook, entries[5].Hook)
	}

	// from_ts keeps entries at-or-after the cutoff and KEEPS the
	// unparseable-ts entry (fail-open, `seek hooks audit --since` rule).
	res, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"from_ts":"2026-11-15T21:30:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	entries = auditEntries(t, res)
	if len(entries) != 4 || entries[0].TS != "2026-11-15T22:00:00Z" || entries[2].TS != "not-a-time" {
		t.Fatalf("from_ts entries = %+v, want e3,e4,e5(unparsed kept),evil", entries)
	}

	// limit tails the FILTERED set, window in chronological order.
	res, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"from_ts":"2026-11-15T21:30:00Z","limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	entries = auditEntries(t, res)
	if len(entries) != 2 || entries[0].TS != "not-a-time" || entries[1].TS != "2026-11-15T23:30:00Z" {
		t.Fatalf("limit tail = %+v, want last two of the filtered set", entries)
	}
}

func TestHooksAuditTail_DefaultLimitCapsTail(t *testing.T) {
	env := newHooksEnv(t)
	var lines []string
	for i := 0; i < hooksAuditDefaultLimit+5; i++ {
		lines = append(lines, fmt.Sprintf(`{"ts":"2026-11-15T%02d:00:00Z","event":"pre_tool","hook":"h%d","session_id":%q,"duration_ms":1,"exit_code":0,"denied":false}`, i%24, i, sidAlpha))
	}
	writeAuditLog(t, env.home, lines)

	res, err := dispatchHooks(t, env.reg, "hooks/audit/tail", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	entries := auditEntries(t, res)
	if len(entries) != hooksAuditDefaultLimit {
		t.Fatalf("entries = %d, want the default cap %d", len(entries), hooksAuditDefaultLimit)
	}
	if entries[0].Hook != "h5" {
		t.Errorf("first = %s, want h5 (the oldest beyond the cap drops off the tail)", entries[0].Hook)
	}
}

func TestHooksAuditTail_ProjectAttribution(t *testing.T) {
	env := newAuditEnv(t)

	// alpha: the two alpha-session entries + the unparseable-ts one
	// (same session); orphan and gone sessions never match.
	res, err := dispatchHooks(t, env.reg, "hooks/audit/tail", fmt.Sprintf(`{"project_id":%q}`, env.alphaID))
	if err != nil {
		t.Fatal(err)
	}
	entries := auditEntries(t, res)
	if len(entries) != 3 || entries[0].TS != "2026-11-15T20:00:00Z" || entries[2].TS != "not-a-time" {
		t.Fatalf("alpha entries = %+v, want the 3 alpha-session rows", entries)
	}

	// The /nowhere hash buckets without a project dir — attribution is
	// pure header-CWD bucketing, project/list's rule.
	res, err = dispatchHooks(t, env.reg, "hooks/audit/tail", fmt.Sprintf(`{"project_id":%q}`, env.otherID))
	if err != nil {
		t.Fatal(err)
	}
	entries = auditEntries(t, res)
	if len(entries) != 1 || entries[0].SessionID != sidOrphan {
		t.Fatalf("nowhere entries = %+v, want the orphan row", entries)
	}

	// The traversal-shaped sid degrades to unattributable — no
	// filesystem reach, no failed call. At this cutoff the only other
	// survivor is the fail-open unparseable-ts alpha entry.
	res, err = dispatchHooks(t, env.reg, "hooks/audit/tail", fmt.Sprintf(`{"project_id":%q,"from_ts":"2026-11-15T23:15:00Z"}`, env.alphaID))
	if err != nil {
		t.Fatal(err)
	}
	if entries := auditEntries(t, res); len(entries) != 1 || entries[0].TS != "not-a-time" {
		t.Fatalf("traversal-sid entries = %+v, want only the fail-open alpha row", entries)
	}

	_, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"project_id":"nothex"}`)
	wantRPCCode(t, err, rpc.CodeInvalidParams, "16 hex")
}

func TestHooksAuditTail_ErrorTable(t *testing.T) {
	env := newAuditEnv(t)

	_, err := dispatchHooks(t, env.reg, "hooks/audit/tail", `{"follow":true}`)
	wantRPCCode(t, err, rpc.CodeInvalidParams, "follow=true")

	// Explicit false is the documented no-op, not an error.
	if _, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"follow":false}`); err != nil {
		t.Fatalf("follow=false: %v", err)
	}

	_, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"from_ts":"yesterday"}`)
	wantRPCCode(t, err, rpc.CodeInvalidParams, "RFC3339")

	for _, bad := range []string{`0`, `1001`, `-1`} {
		_, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"limit":`+bad+`}`)
		wantRPCCode(t, err, rpc.CodeInvalidParams, "limit")
	}

	_, err = dispatchHooks(t, env.reg, "hooks/audit/tail", `{"limit":"50"}`)
	wantRPCCode(t, err, rpc.CodeInvalidParams, "")
}

func TestHooksAuditTail_NoLogFile(t *testing.T) {
	env := newHooksEnv(t) // no hooks-audit.jsonl written

	res, err := dispatchHooks(t, env.reg, "hooks/audit/tail", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	got := res.(hooksAuditResult)
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want empty non-nil (no audit data yet)", got.Entries)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"entries":[]`) {
		t.Fatalf("json = %s, want \"entries\":[]", b)
	}
}
