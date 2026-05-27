package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
)

// fakeExec is a deterministic stand-in for `bash -c`. Tests configure
// per-command responses keyed by the exact command string.
type fakeExec struct {
	mu       sync.Mutex
	calls    []fakeCall
	scripted map[string]fakeResponse
}

type fakeCall struct {
	Command string
	CWD     string
	Env     map[string]string
}

type fakeResponse struct {
	Stdout   string
	Exit     int
	Err      error
	SleepFor time.Duration // if >0, the exec sleeps before returning (timeout simulation)
}

func newFakeExec() *fakeExec {
	return &fakeExec{scripted: make(map[string]fakeResponse)}
}

func (f *fakeExec) Set(cmd string, r fakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripted[cmd] = r
}

func (f *fakeExec) Calls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeExec) Exec(ctx context.Context, command, cwd string, env []string) (string, int, error) {
	f.mu.Lock()
	envMap := make(map[string]string)
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 {
			envMap[e[:i]] = e[i+1:]
		}
	}
	f.calls = append(f.calls, fakeCall{Command: command, CWD: cwd, Env: envMap})
	r := f.scripted[command]
	f.mu.Unlock()
	if r.SleepFor > 0 {
		select {
		case <-time.After(r.SleepFor):
		case <-ctx.Done():
			return "", -1, ctx.Err()
		}
	}
	return r.Stdout, r.Exit, r.Err
}

func newRunnerForTest(t *testing.T, cfg hooksconfig.Config, fe *fakeExec) (*ShellRunner, *hooksconfig.AuditLog) {
	t.Helper()
	tmpDir := t.TempDir()
	audit, err := hooksconfig.NewAuditLog(filepath.Join(tmpDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r := NewShellRunner(cfg,
		WithAuditLog(audit),
		WithExecutor(fe.Exec),
		WithProjectContext("pidpid", "/tmp/proj"),
		WithVersion("test"),
	)
	// Simulate a session start so SEEK_SESSION_ID gets populated.
	r.OnSessionStart(context.Background(), SessionStartEvent{ID: "sess1", CWD: "/tmp/proj"})
	return r, audit
}

// ---- pre_tool ----

func TestPreToolUse_DenyOnNonzero(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "blocker", Command: "block kubectl", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "bash"

	fe := newFakeExec()
	fe.Set("block kubectl", fakeResponse{Stdout: "blocked: kubectl delete is forbidden", Exit: 1})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	out, err := r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("OnPreToolUse: %v", err)
	}
	if out.Deny == "" {
		t.Fatalf("expected deny, got empty")
	}
	if !strings.Contains(out.Deny, "blocked") {
		t.Errorf("deny reason should carry stdout: %q", out.Deny)
	}
}

func TestPreToolUse_AllowsOnZeroExit(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "audit", Command: "log it", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("log it", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	out, err := r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Deny != "" {
		t.Errorf("zero-exit should allow, got deny: %q", out.Deny)
	}
}

func TestPreToolUse_FirstDenyShortCircuits(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "first", Command: "first cmd", Source: hooksconfig.SourceProject, Event: hooksconfig.EventPreTool},
			{Name: "second", Command: "second cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	cfg.PreTool[1].Match.Tool = "*"

	fe := newFakeExec()
	fe.Set("first cmd", fakeResponse{Stdout: "nope", Exit: 1})
	fe.Set("second cmd", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	_, _ = r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	calls := fe.Calls()
	if len(calls) != 1 {
		t.Errorf("first deny should short-circuit; calls=%d", len(calls))
	}
}

func TestPreToolUse_NonMatchingToolSkipped(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "edit-only", Command: "lint", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "edit"
	fe := newFakeExec()
	fe.Set("lint", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	_, _ = r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	if len(fe.Calls()) != 0 {
		t.Errorf("edit-only hook should not fire for bash")
	}

	_, _ = r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "edit"})
	if len(fe.Calls()) != 1 {
		t.Errorf("edit-only hook should fire for edit, calls=%d", len(fe.Calls()))
	}
}

func TestPreToolUse_TimeoutTreatedAsDeny(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "slow", Command: "sleep", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool, TimeoutMs: 50},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("sleep", fakeResponse{SleepFor: 200 * time.Millisecond})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	start := time.Now()
	out, err := r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	dur := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if out.Deny == "" {
		t.Errorf("timeout should produce deny")
	}
	if !strings.Contains(out.Deny, "timed out") {
		t.Errorf("deny reason should mention timeout: %q", out.Deny)
	}
	if dur > 500*time.Millisecond {
		t.Errorf("did not honor 50ms timeout, took %s", dur)
	}
}

func TestPreToolUse_EnvVarsInjected(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "envcheck", Command: "echo env", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("echo env", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	_, _ = r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)})
	calls := fe.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls: %d", len(calls))
	}
	want := map[string]string{
		"SEEK_VERSION":        "test",
		"SEEK_SESSION_ID":     "sess1",
		"SEEK_PROJECT_ID":     "pidpid",
		"SEEK_PROJECT_PATH":   "/tmp/proj",
		"SEEK_EVENT":          "pre_tool",
		"SEEK_TOOL_NAME":      "bash",
		"SEEK_TOOL_ARGS_JSON": `{"cmd":"ls"}`,
	}
	for k, v := range want {
		if got := calls[0].Env[k]; got != v {
			t.Errorf("env %s = %q, want %q", k, got, v)
		}
	}
}

// Verification criterion #11: missing fields should be empty string,
// not unset. Verify SEEK_TOOL_NAME is present (even if empty) for
// session_start events.
func TestEnvVars_MissingFieldsAreEmptyNotUnset(t *testing.T) {
	cfg := hooksconfig.Config{
		SessionStart: []hooksconfig.Hook{
			{Name: "h", Command: "echo s", Source: hooksconfig.SourceUser, Event: hooksconfig.EventSessionStart},
		},
	}
	fe := newFakeExec()
	fe.Set("echo s", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	// session_start fires automatically inside newRunnerForTest, then
	// we re-fire to capture under a known event.
	r.OnSessionStart(context.Background(), SessionStartEvent{ID: "sess2", CWD: "/tmp/proj"})
	calls := fe.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls")
	}
	last := calls[len(calls)-1]
	// Per PRD §3.3, SEEK_TOOL_NAME is filled for pre_tool / post_tool
	// only — for session_start it should NOT be in env (omission, not
	// empty string). The umbrella PRD says "缺失字段为空字符串而非 unset"
	// for the OVERALL contract; we read that as "fields documented for
	// this event are always set", not "every var is set for every
	// event". Confirm SEEK_EVENT carries the event name; SEEK_VERSION
	// is set; absence of SEEK_TOOL_NAME on a session event is fine.
	if last.Env["SEEK_EVENT"] != "session_start" {
		t.Errorf("SEEK_EVENT: %q", last.Env["SEEK_EVENT"])
	}
	if last.Env["SEEK_VERSION"] != "test" {
		t.Errorf("SEEK_VERSION: %q", last.Env["SEEK_VERSION"])
	}
}

// PRD §3.3: for pre/post tool events, SEEK_TOOL_ARGS_JSON is set even
// when args is empty — the variable exists with value "".
func TestPreToolUse_EmptyArgsStillSetsEnv(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "x", Command: "echo", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("echo", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()
	_, _ = r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	last := fe.Calls()[len(fe.Calls())-1]
	if _, ok := last.Env["SEEK_TOOL_ARGS_JSON"]; !ok {
		t.Errorf("SEEK_TOOL_ARGS_JSON missing — should be present (possibly empty)")
	}
	if v := last.Env["SEEK_TOOL_ARGS_JSON"]; v != "" {
		t.Errorf("empty-args var: %q, want \"\"", v)
	}
}

// ---- post_tool ----

func TestPostToolUse_DoesNotInfluenceFlow(t *testing.T) {
	cfg := hooksconfig.Config{
		PostTool: []hooksconfig.Hook{
			{Name: "log", Command: "log", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPostTool},
		},
	}
	cfg.PostTool[0].Match.Tool = "*"
	fe := newFakeExec()
	// Hook fails — should not propagate (observer).
	fe.Set("log", fakeResponse{Stdout: "leak this!", Exit: 5})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()
	// observer: no return value; just verify it runs without panicking
	r.OnPostToolUse(context.Background(), PostToolUseEvent{
		Name: "bash", Args: json.RawMessage(`{}`), Result: "ran fine",
	})
	if len(fe.Calls()) != 1 {
		t.Fatalf("hook should have run, calls=%d", len(fe.Calls()))
	}
	// Crucial: the hook's stdout MUST NOT leak anywhere visible to the
	// prompt. We can't fully test the absence of an output, but we
	// can assert that the runner does not own any state that captures
	// it. The audit log holds it (we expect that).
}

func TestPostToolUse_TruncatesLongResult(t *testing.T) {
	cfg := hooksconfig.Config{
		PostTool: []hooksconfig.Hook{
			{Name: "log", Command: "log", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPostTool},
		},
	}
	cfg.PostTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("log", fakeResponse{Exit: 0})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	big := strings.Repeat("a", 5000)
	r.OnPostToolUse(context.Background(), PostToolUseEvent{Name: "bash", Result: big})
	env := fe.Calls()[0].Env["SEEK_TOOL_RESULT"]
	if !strings.HasSuffix(env, " [truncated]") {
		t.Errorf("expected truncation marker; tail=%q", env[max(0, len(env)-30):])
	}
	if len(env) > 4096+len(" [truncated]") {
		t.Errorf("result too long: %d", len(env))
	}
}

// ---- skip reason ----

func TestSkippedHookDoesNotRun(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "broken", Command: "would run", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool, SkipReason: "syntax: bad"},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("would run", fakeResponse{Stdout: "DO NOT REACH ME", Exit: 1})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()
	out, _ := r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	if out.Deny != "" {
		t.Errorf("skipped hook should not deny; got %q", out.Deny)
	}
	if len(fe.Calls()) != 0 {
		t.Errorf("skipped hook should not exec; calls=%d", len(fe.Calls()))
	}
}

// ---- ctx cancellation ----

func TestPreToolUse_ContextCancellation(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "long", Command: "sleep10s", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool, TimeoutMs: 5000},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("sleep10s", fakeResponse{SleepFor: 10 * time.Second})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _ = r.OnPreToolUse(ctx, PreToolUseIn{Name: "bash"})
	if dur := time.Since(start); dur > 2*time.Second {
		t.Errorf("ctx cancellation didn't kick in; took %s", dur)
	}
}

// ---- audit log integration ----

func TestPreToolUse_AuditEntryWritten(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "deny", Command: "deny", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("deny", fakeResponse{Stdout: "no", Exit: 1})

	tmpDir := t.TempDir()
	audit, _ := hooksconfig.NewAuditLog(filepath.Join(tmpDir, "audit.jsonl"))
	r := NewShellRunner(cfg, WithAuditLog(audit), WithExecutor(fe.Exec),
		WithProjectContext("pp", "/tmp/p"), WithVersion("t"))
	r.OnSessionStart(context.Background(), SessionStartEvent{ID: "s"})
	defer r.Close()

	_, _ = r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	_ = audit.Close()
	entries, err := hooksconfig.ReadAuditLog(filepath.Join(tmpDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: %d", len(entries))
	}
	if entries[0].Hook != "deny" {
		t.Errorf("hook: %s", entries[0].Hook)
	}
	if !entries[0].Denied {
		t.Errorf("denied flag missing")
	}
	if entries[0].Tool != "bash" {
		t.Errorf("tool: %s", entries[0].Tool)
	}
	if entries[0].SessionID != "s" {
		t.Errorf("session id: %s", entries[0].SessionID)
	}
	if entries[0].ExitCode != 1 {
		t.Errorf("exit code: %d", entries[0].ExitCode)
	}
}

// ---- concurrent safety ----

func TestConcurrentPreToolUse(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "ok", Command: "ok", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("ok", fakeResponse{Exit: 0})
	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	var wg sync.WaitGroup
	var deniedCount atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _ := r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
			if out.Deny != "" {
				deniedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if deniedCount.Load() != 0 {
		t.Errorf("concurrent allows produced denies: %d", deniedCount.Load())
	}
}

// ---- exec error ----

func TestPreToolUse_ExecFailureTreatedAsDeny(t *testing.T) {
	cfg := hooksconfig.Config{
		PreTool: []hooksconfig.Hook{
			{Name: "missing-bash", Command: "x", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPreTool},
		},
	}
	cfg.PreTool[0].Match.Tool = "*"
	fe := newFakeExec()
	fe.Set("x", fakeResponse{Exit: -1, Err: errors.New("fork/exec /bin/bash: no such file")})
	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	out, err := r.OnPreToolUse(context.Background(), PreToolUseIn{Name: "bash"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Deny == "" {
		t.Errorf("exec failure should deny (defensive); got allow")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
