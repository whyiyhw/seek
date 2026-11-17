package hooks

// pre_prompt_inject dispatch coverage (feature-hooks-injection §3.4 +
// §4.5, milestone M-O3.2). The contracts under test:
//
//   - authorized stdout becomes exactly one user-role Prepend message
//     per hook, wrapped in <context type="hook" name="...">, FIFO in
//     declaration order (project before user — Merge order);
//   - the face NEVER rewrites UserText (D17 veto) and NEVER errors —
//     the deny paths below all block the INJECTION, not the turn;
//   - max_bytes hard-truncation + " [truncated]" wire marker + audit
//     row carrying injected_bytes (post-truncation byte count) and a
//     truncation reason;
//   - failure family (non-zero exit / timeout / static skip / empty
//     stdout): stdout dropped, hook skipped, next hook still runs.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func injectCfg(hooks ...hooksconfig.Hook) hooksconfig.Config {
	return hooksconfig.Config{PrePromptInject: hooks}
}

func TestPrePromptInject_StdoutBecomesContextPrepend(t *testing.T) {
	cfg := injectCfg(hooksconfig.Hook{
		Name: "ci-status", Command: "cat ctx", Source: hooksconfig.SourceUser,
		Event: hooksconfig.EventPrePromptInject,
	})
	fe := newFakeExec()
	fe.Set("cat ctx", fakeResponse{Stdout: "CI: green\nlast build 5m ago\n"})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	out, err := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{UserText: "fix the flaky test"})
	if err != nil {
		t.Fatalf("OnPrePrompt: %v", err)
	}
	// D17 veto: the shell face never rewrites the user's message.
	if out.UserText != "" {
		t.Errorf("UserText = %q, want empty (no rewrite)", out.UserText)
	}
	if len(out.Prepend) != 1 {
		t.Fatalf("Prepend = %d messages, want 1", len(out.Prepend))
	}
	m := out.Prepend[0]
	if m.Role != deepseek.RoleUser {
		t.Errorf("role = %s, want user (channel D shape)", m.Role)
	}
	want := "<context type=\"hook\" name=\"ci-status\">\nCI: green\nlast build 5m ago\n</context>"
	if m.Content != want {
		t.Errorf("content = %q\nwant      %q", m.Content, want)
	}
}

func TestPrePromptInject_FIFODeclarationOrder(t *testing.T) {
	cfg := injectCfg(
		hooksconfig.Hook{Name: "a", Command: "cmd a", Source: hooksconfig.SourceProject, Event: hooksconfig.EventPrePromptInject},
		hooksconfig.Hook{Name: "b", Command: "cmd b", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPrePromptInject},
	)
	fe := newFakeExec()
	fe.Set("cmd a", fakeResponse{Stdout: "from project"})
	fe.Set("cmd b", fakeResponse{Stdout: "from user"})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	out, _ := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
	if len(out.Prepend) != 2 {
		t.Fatalf("Prepend = %d, want 2", len(out.Prepend))
	}
	if !strings.Contains(out.Prepend[0].Content, "from project") || !strings.Contains(out.Prepend[1].Content, "from user") {
		t.Errorf("FIFO order broken: %q then %q", out.Prepend[0].Content, out.Prepend[1].Content)
	}
}

func TestPrePromptInject_MaxBytesTruncation(t *testing.T) {
	cfg := injectCfg(hooksconfig.Hook{
		Name: "big", Command: "big cmd", Source: hooksconfig.SourceUser,
		Event: hooksconfig.EventPrePromptInject, MaxBytes: 16,
	})
	fe := newFakeExec()
	body := strings.Repeat("x", 100)
	fe.Set("big cmd", fakeResponse{Stdout: body})

	r, audit := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	out, _ := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("Prepend = %d, want 1", len(out.Prepend))
	}
	wantBody := strings.Repeat("x", 16) + truncationMarker
	if !strings.HasSuffix(out.Prepend[0].Content, wantBody+"\n</context>") {
		t.Errorf("truncated tail = %q", out.Prepend[0].Content)
	}
	// Audit row: post-truncation byte count (marker included) + reason.
	entries := readAuditT(t, audit.Path())
	if len(entries) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Event != hooksconfig.EventPrePromptInject || e.Hook != "big" {
		t.Errorf("audit row = %+v", e)
	}
	if e.InjectedBytes != len(wantBody) {
		t.Errorf("injected_bytes = %d, want %d (max + marker)", e.InjectedBytes, len(wantBody))
	}
	if !strings.Contains(e.Reason, "truncated at max_bytes=16") {
		t.Errorf("truncation reason = %q", e.Reason)
	}
}

// Under-budget and exactly-at-budget output must pass through
// untruncated — the boundary is `len > max`, not `>=`.
func TestPrePromptInject_AtBudgetNotTruncated(t *testing.T) {
	for _, tc := range []struct {
		name string
		len  int
	}{
		{"under", 15},
		{"exact", 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := injectCfg(hooksconfig.Hook{
				Name: "edge", Command: "edge cmd", Source: hooksconfig.SourceUser,
				Event: hooksconfig.EventPrePromptInject, MaxBytes: 16,
			})
			fe := newFakeExec()
			fe.Set("edge cmd", fakeResponse{Stdout: strings.Repeat("y", tc.len)})

			r, audit := newRunnerForTest(t, cfg, fe)
			defer r.Close()

			out, _ := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
			if len(out.Prepend) != 1 {
				t.Fatalf("Prepend = %d, want 1", len(out.Prepend))
			}
			if strings.Contains(out.Prepend[0].Content, truncationMarker) {
				t.Errorf("at-budget output must not carry the marker")
			}
			if e := readAuditT(t, audit.Path())[0]; e.Reason != "" || e.InjectedBytes != tc.len {
				t.Errorf("audit = %+v, want no reason and injected_bytes=%d", e, tc.len)
			}
		})
	}
}

// The deny family: every path below blocks the INJECTION only — no
// error, no turn block, later hooks still run, stdout never leaks into
// the prompt bytes.
func TestPrePromptInject_DenyPaths(t *testing.T) {
	t.Run("nonzero exit skips hook stdout", func(t *testing.T) {
		cfg := injectCfg(
			hooksconfig.Hook{Name: "boom", Command: "boom cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPrePromptInject},
			hooksconfig.Hook{Name: "ok", Command: "ok cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventPrePromptInject},
		)
		fe := newFakeExec()
		fe.Set("boom cmd", fakeResponse{Stdout: "SHOULD NOT ENTER PROMPT", Exit: 1})
		fe.Set("ok cmd", fakeResponse{Stdout: "fine"})

		r, audit := newRunnerForTest(t, cfg, fe)
		defer r.Close()

		out, err := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
		if err != nil {
			t.Fatalf("failed hook must not error the turn: %v", err)
		}
		if len(out.Prepend) != 1 || !strings.Contains(out.Prepend[0].Content, "fine") {
			t.Fatalf("failed hook's stdout must be dropped, later hook still injects: %+v", out.Prepend)
		}
		for _, m := range out.Prepend {
			if strings.Contains(m.Content, "SHOULD NOT ENTER PROMPT") {
				t.Errorf("denied stdout leaked into prompt bytes")
			}
		}
		entries := readAuditT(t, audit.Path())
		if len(entries) != 2 || entries[0].ExitCode != 1 || entries[0].InjectedBytes != 0 {
			t.Errorf("audit rows = %+v, want first=exit1/0 bytes, second ok", entries)
		}
	})

	t.Run("timeout skips hook stdout", func(t *testing.T) {
		cfg := injectCfg(hooksconfig.Hook{
			Name: "slow", Command: "slow cmd", Source: hooksconfig.SourceUser,
			Event: hooksconfig.EventPrePromptInject, TimeoutMs: 50,
		})
		fe := newFakeExec()
		fe.Set("slow cmd", fakeResponse{Stdout: "late", SleepFor: 200 * time.Millisecond})

		r, _ := newRunnerForTest(t, cfg, fe)
		defer r.Close()

		start := time.Now()
		out, err := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
		if err != nil {
			t.Fatalf("timeout must not error the turn: %v", err)
		}
		if len(out.Prepend) != 0 {
			t.Errorf("timed-out hook must not inject: %+v", out.Prepend)
		}
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("timeout not honored: %s", d)
		}
	})

	t.Run("static skip never execs", func(t *testing.T) {
		cfg := injectCfg(hooksconfig.Hook{
			Name: "broken", Command: "never runs", Source: hooksconfig.SourceUser,
			Event: hooksconfig.EventPrePromptInject, SkipReason: "syntax error near token",
		})
		fe := newFakeExec()
		fe.Set("never runs", fakeResponse{Stdout: "DO NOT REACH"})

		r, audit := newRunnerForTest(t, cfg, fe)
		defer r.Close()

		out, _ := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
		if len(out.Prepend) != 0 || len(fe.Calls()) != 0 {
			t.Errorf("skipped hook must not exec or inject: %+v calls=%d", out.Prepend, len(fe.Calls()))
		}
		if e := readAuditT(t, audit.Path())[0]; !strings.Contains(e.Reason, "skipped") {
			t.Errorf("skip audit reason = %q", e.Reason)
		}
	})

	t.Run("empty stdout injects no message", func(t *testing.T) {
		cfg := injectCfg(hooksconfig.Hook{
			Name: "quiet", Command: "quiet cmd", Source: hooksconfig.SourceUser,
			Event: hooksconfig.EventPrePromptInject,
		})
		fe := newFakeExec()
		fe.Set("quiet cmd", fakeResponse{Stdout: ""})

		r, audit := newRunnerForTest(t, cfg, fe)
		defer r.Close()

		out, _ := r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
		if len(out.Prepend) != 0 {
			t.Errorf("empty stdout must not produce an empty <context>: %+v", out.Prepend)
		}
		if e := readAuditT(t, audit.Path())[0]; e.InjectedBytes != 0 {
			t.Errorf("audit injected_bytes = %d, want 0", e.InjectedBytes)
		}
	})
}

func TestPrePromptInject_EnvCarriesEvent(t *testing.T) {
	cfg := injectCfg(hooksconfig.Hook{
		Name: "env", Command: "env cmd", Source: hooksconfig.SourceUser,
		Event: hooksconfig.EventPrePromptInject,
	})
	fe := newFakeExec()
	fe.Set("env cmd", fakeResponse{Stdout: "ok"})

	r, _ := newRunnerForTest(t, cfg, fe)
	defer r.Close()

	_, _ = r.InjectHook().OnPrePrompt(context.Background(), PrePromptIn{})
	calls := fe.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Env["SEEK_EVENT"] != hooksconfig.EventPrePromptInject {
		t.Errorf("SEEK_EVENT = %q", calls[0].Env["SEEK_EVENT"])
	}
	if calls[0].Env["SEEK_SESSION_ID"] != "sess1" {
		t.Errorf("SEEK_SESSION_ID = %q", calls[0].Env["SEEK_SESSION_ID"])
	}
}

// The face registers into the PrePrompt slot only, and lands BEHIND an
// in-process hook registered earlier — the "in-process hooks 之后"
// ordering main.go relies on (memory snapshot / background notices
// first, shell context after).
func TestPrePromptInject_RegistryOrderAfterInProcess(t *testing.T) {
	reg := NewRegistry()
	inProcess := &fixedPrependHook{content: "<context source=\"memory.soul\">\nsnapshot\n</context>"}
	reg.Register(inProcess)

	fe := newFakeExec()
	fe.Set("sh ctx", fakeResponse{Stdout: "shell context"})
	runner := NewShellRunner(injectCfg(hooksconfig.Hook{
		Name: "ci", Command: "sh ctx", Source: hooksconfig.SourceUser,
		Event: hooksconfig.EventPrePromptInject,
	}), WithExecutor(fe.Exec))
	defer runner.Close()
	reg.Register(runner.InjectHook())

	s := reg.Stack()
	if len(s.PrePrompt) != 2 || len(s.PreToolUse) != 0 {
		t.Fatalf("PrePrompt stack = %v (want 2), PreToolUse = %v (want 0)", s.PrePrompt, s.PreToolUse)
	}
	if s.PrePrompt[0] != "*hooks.fixedPrependHook" || s.PrePrompt[1] != "*hooks.InjectRunner" {
		t.Errorf("PrePrompt order = %v, want in-process first", s.PrePrompt)
	}

	out, err := reg.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Prepend) != 2 {
		t.Fatalf("Prepend = %d, want 2", len(out.Prepend))
	}
	if !strings.HasPrefix(out.Prepend[0].Content, "<context source=\"memory.soul\">") {
		t.Errorf("in-process context must land first: %q", out.Prepend[0].Content)
	}
	if !strings.HasPrefix(out.Prepend[1].Content, "<context type=\"hook\"") {
		t.Errorf("shell context must land second: %q", out.Prepend[1].Content)
	}
	if out.UserText != "hi" {
		t.Errorf("UserText = %q, want unchanged", out.UserText)
	}
}

// HasInjectHooks gates the Registry slot: false with no config, with
// all-skipped config, and on a nil runner.
func TestHasInjectHooks(t *testing.T) {
	var nilRunner *ShellRunner
	if nilRunner.HasInjectHooks() {
		t.Errorf("nil runner must report false")
	}
	if NewShellRunner(hooksconfig.Config{}).HasInjectHooks() {
		t.Errorf("empty config must report false")
	}
	allSkipped := injectCfg(hooksconfig.Hook{Name: "s", Command: "c", SkipReason: "bad"})
	if NewShellRunner(allSkipped).HasInjectHooks() {
		t.Errorf("all-skipped config must report false")
	}
	one := injectCfg(hooksconfig.Hook{Name: "ok", Command: "c"})
	if !NewShellRunner(one).HasInjectHooks() {
		t.Errorf("one live hook must report true")
	}
	// A config with only OTHER events must not flip the inject gate.
	other := hooksconfig.Config{PreTool: []hooksconfig.Hook{{Name: "n", Command: "c"}}}
	if NewShellRunner(other).HasInjectHooks() {
		t.Errorf("non-inject hooks must not flip the gate")
	}
}

// fixedPrependHook is an in-process stand-in for the memory subsystem's
// PrePrompt face (user-role <context> Prepend).
type fixedPrependHook struct{ content string }

func (h *fixedPrependHook) OnPrePrompt(ctx context.Context, in PrePromptIn) (PrePromptOut, error) {
	return PrePromptOut{Prepend: []deepseek.Message{{Role: deepseek.RoleUser, Content: h.content}}}, nil
}

func readAuditT(t *testing.T, path string) []hooksconfig.AuditEntry {
	t.Helper()
	entries, err := hooksconfig.ReadAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
