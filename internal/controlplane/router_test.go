package controlplane

// M2 router tests: the full chain — fake Frontend injects Inbound /
// records Send, the real Router routes, the real BindingStore persists
// to a temp file, the real Executor drives this test binary as the
// fake seek (reusing the executor_test.go stub harness). No SDK
// anywhere: frontend-agnosticism made executable (PRD §6). The §6
// routing-segment assertions run here; the executor's own self-heal /
// concurrency bars stay in executor_test.go.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestRouter builds the chain under test: feishu-named fake
// frontend + temp-file binding store + stub-seek executor.
func newTestRouter(t *testing.T, mutate func(*ExecutorConfig), rcfg RouterConfig) (*Router, *fakeFrontend, *BindingStore, string) {
	t.Helper()
	ex, stubDir := newStubExecutor(t, mutate)
	store := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	fe := &fakeFrontend{name: "feishu"}
	return NewRouter([]Frontend{fe}, store, ex, rcfg), fe, store, stubDir
}

// conv1 is the standard conversation key of these tests.
var conv1 = NewConvID("feishu", "c1")

// inbound builds a DirectAt message for conv1 with a fresh EventID —
// the default shape of "a message that should trigger".
func inbound(text, eventID string) Inbound {
	return Inbound{Conv: conv1, Text: text, EventID: eventID, DirectAt: true}
}

// waitFor polls cond until it holds or the deadline; Fatal on timeout.
// Deadline-bounded, never a bare sleep — the suite must not hang on a
// routing bug, only fail.
func waitFor(t *testing.T, desc string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func sendsContaining(fe *fakeFrontend, substr string) func() bool {
	return func() bool {
		for _, s := range fe.sends() {
			if strings.Contains(s.Text, substr) {
				return true
			}
		}
		return false
	}
}

// ---- §6 routing segment: 未绑 / bind / switch / new ----

// Unbound conversation + ordinary message → the /bind hint, no exec.
func TestRouter_UnboundPrompt(t *testing.T) {
	r, fe, _, stubDir := newTestRouter(t, nil, RouterConfig{})

	r.Handle(context.Background(), inbound("hello?", "e1"))

	sends := fe.sends()
	if len(sends) != 1 {
		t.Fatalf("sends = %d, want 1 (the bind hint)", len(sends))
	}
	if !strings.Contains(sends[0].Text, "/bind") {
		t.Errorf("hint should mention /bind, got: %q", sends[0].Text)
	}
	if _, err := os.Stat(filepath.Join(stubDir, "invocations.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unbound message must not exec seek (invocations file exists)")
	}
}

// /bind then an ordinary message: the run lands in the bound project,
// the reply flows back, the session id is written into the binding,
// and the NEXT turn carries -resume <sid> (D-C3 through the router).
func TestRouter_BindThenPromptRunsInProject(t *testing.T) {
	r, fe, store, stubDir := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()

	r.Handle(context.Background(), inbound("/bind "+project, "e1"))
	if sends := fe.sends(); len(sends) != 1 || !strings.Contains(sends[0].Text, "已绑定") {
		t.Fatalf("bind ack missing: %+v", sends)
	}

	r.Handle(context.Background(), inbound("hello world", "e2"))
	invs := readInvocations(t, stubDir)
	if len(invs) != 1 {
		t.Fatalf("invocations = %d, want 1", len(invs))
	}
	if invs[0].CWD != project {
		t.Errorf("run cwd = %q, want bound project %q", invs[0].CWD, project)
	}
	if !argvHas(invs[0], "-p", "hello world") {
		t.Errorf("message not passed to seek: %q", invs[0].Argv)
	}
	if argvHas(invs[0], "-resume", "") {
		t.Errorf("first turn must not carry -resume: %q", invs[0].Argv)
	}

	sends := fe.sends()
	if len(sends) != 2 || sends[1].Text != "Hello hello world" {
		t.Fatalf("run reply = %+v, want 'Hello hello world'", sends)
	}

	b, err := store.Get(conv1)
	if err != nil {
		t.Fatalf("binding after run: %v", err)
	}
	if b.SessionID == "" {
		t.Fatal("session id not written back into the binding")
	}

	// Second turn: same conversation, -resume <stored id>.
	r.Handle(context.Background(), inbound("again", "e3"))
	invs = readInvocations(t, stubDir)
	if len(invs) != 2 {
		t.Fatalf("invocations = %d, want 2", len(invs))
	}
	if !argvHas(invs[1], "-resume", b.SessionID) {
		t.Errorf("second turn missing -resume %s: %q", b.SessionID, invs[1].Argv)
	}
}

// /bind with no path → usage; with a path that does not exist, or a
// plain file → a failure reply and NOTHING stored.
func TestRouter_BindPathValidation(t *testing.T) {
	r, fe, store, _ := newTestRouter(t, nil, RouterConfig{})

	r.Handle(context.Background(), inbound("/bind", "e1"))
	if s := fe.sends(); len(s) != 1 || !strings.Contains(s[0].Text, "用法") {
		t.Fatalf("missing-arg reply = %+v", s)
	}

	r.Handle(context.Background(), inbound("/bind /nonexistent/definitely/not/here", "e2"))
	if s := fe.sends(); len(s) != 2 || !strings.Contains(s[1].Text, "不存在") {
		t.Fatalf("missing-dir reply = %+v", s)
	}

	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Handle(context.Background(), inbound("/bind "+file, "e3"))
	if s := fe.sends(); len(s) != 3 || !strings.Contains(s[2].Text, "不是目录") {
		t.Fatalf("not-a-dir reply = %+v", s)
	}

	if _, err := store.Get(conv1); !errors.Is(err, ErrBindingNotFound) {
		t.Errorf("rejected binds must not store anything, got err %v", err)
	}
}

// Repeated /bind — same path or new path — always resets the session
// (D-C2: /bind is "start over here", never a no-op), and preserves a
// hand-set yolo flag (the only Yolo source in v1).
func TestRouter_RebindResetsSession(t *testing.T) {
	r, _, store, _ := newTestRouter(t, nil, RouterConfig{})
	a, b := t.TempDir(), t.TempDir()
	if err := store.Put(Binding{ConvID: conv1, ProjectPath: a, Yolo: true}); err != nil {
		t.Fatal(err)
	}

	r.Handle(context.Background(), inbound("hi", "e1")) // run → session id stored
	if got, err := store.Get(conv1); err != nil || got.SessionID == "" {
		t.Fatalf("setup: run should store a session id, got %+v %v", got, err)
	}

	r.Handle(context.Background(), inbound("/bind "+a, "e2")) // rebind SAME path
	got, err := store.Get(conv1)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "" {
		t.Errorf("rebinding same path must clear the session, got %q", got.SessionID)
	}
	if !got.Yolo {
		t.Error("rebind must preserve the binding's yolo flag")
	}

	r.Handle(context.Background(), inbound("/bind "+b, "e3")) // rebind OTHER path
	got, err = store.Get(conv1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectPath != b || got.SessionID != "" {
		t.Errorf("rebind to new path = %+v, want project %q + empty session", got, b)
	}
	if !got.Yolo {
		t.Error("rebind to a new path must preserve the yolo flag too")
	}
}

// "~" in /bind expands to the home dir (a phone user cannot reasonably
// type the absolute path). Skipped on Windows: os.UserHomeDir ignores
// $HOME there.
func TestRouter_BindTildeExpands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME-based tilde expansion is a unix convention")
	}
	r, _, store, _ := newTestRouter(t, nil, RouterConfig{})
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	r.Handle(context.Background(), inbound("/bind ~/proj", "e1"))

	got, err := store.Get(conv1)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.ProjectPath != proj {
		t.Errorf("tilde bind = %q, want %q", got.ProjectPath, proj)
	}
}

// /switch: changes the project AND clears the session; requires an
// existing binding (points unbound conversations at /bind); a bad path
// fails without touching the current binding.
func TestRouter_SwitchChangesProjectAndClearsSession(t *testing.T) {
	r, fe, store, stubDir := newTestRouter(t, nil, RouterConfig{})
	a, b := t.TempDir(), t.TempDir()

	r.Handle(context.Background(), inbound("/switch "+b, "e1")) // unbound
	if s := fe.sends(); len(s) != 1 || !strings.Contains(s[0].Text, "/bind") {
		t.Fatalf("unbound /switch should hint /bind: %+v", s)
	}

	r.Handle(context.Background(), inbound("/bind "+a, "e2"))
	r.Handle(context.Background(), inbound("hi", "e3")) // session lands in the binding

	r.Handle(context.Background(), inbound("/switch "+b, "e4"))
	got, err := store.Get(conv1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectPath != b {
		t.Errorf("switch left project = %q, want %q", got.ProjectPath, b)
	}
	if got.SessionID != "" {
		t.Errorf("switch must clear the session, got %q", got.SessionID)
	}

	// The next run goes to the NEW project, as a first turn.
	r.Handle(context.Background(), inbound("after switch", "e5"))
	invs := readInvocations(t, stubDir)
	if len(invs) != 2 {
		t.Fatalf("invocations = %d, want 2", len(invs))
	}
	if invs[1].CWD != b || argvHas(invs[1], "-resume", "") {
		t.Errorf("post-switch run wrong: cwd=%q argv=%q", invs[1].CWD, invs[1].Argv)
	}

	// Bad path: binding untouched.
	r.Handle(context.Background(), inbound("/switch /nonexistent", "e6"))
	got, err = store.Get(conv1)
	if err != nil || got.ProjectPath != b {
		t.Errorf("failed switch must not change the binding: %+v %v", got, err)
	}
}

// /new: keeps the project, drops the session; unbound → hint.
func TestRouter_NewKeepsProjectClearsSession(t *testing.T) {
	r, fe, store, stubDir := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()

	r.Handle(context.Background(), inbound("/new", "e1")) // unbound
	if s := fe.sends(); len(s) != 1 || !strings.Contains(s[0].Text, "/bind") {
		t.Fatalf("unbound /new should hint /bind: %+v", s)
	}

	r.Handle(context.Background(), inbound("/bind "+project, "e2"))
	r.Handle(context.Background(), inbound("hi", "e3"))

	r.Handle(context.Background(), inbound("/new", "e4"))
	got, err := store.Get(conv1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectPath != project || got.SessionID != "" {
		t.Fatalf("/new = project %q session %q, want project kept + session cleared", got.ProjectPath, got.SessionID)
	}

	r.Handle(context.Background(), inbound("fresh", "e5"))
	invs := readInvocations(t, stubDir)
	if len(invs) != 2 || argvHas(invs[1], "-resume", "") {
		t.Errorf("post-/new run must be a first turn: %+v", invs)
	}
}

// /projects renders the configured candidates, marks the current one,
// and stays honest when empty / unbound.
func TestRouter_ProjectsRendering(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	r, fe, store, _ := newTestRouter(t, nil, RouterConfig{Projects: []string{a, b}})

	r.Handle(context.Background(), inbound("/projects", "e1")) // unbound
	s := fe.sends()
	if len(s) != 1 {
		t.Fatalf("sends = %d, want 1", len(s))
	}
	for _, want := range []string{a, b, "未绑定"} {
		if !strings.Contains(s[0].Text, want) {
			t.Errorf("unbound /projects missing %q: %q", want, s[0].Text)
		}
	}
	if strings.Contains(s[0].Text, "← 当前") {
		t.Errorf("unbound /projects must not mark a current project: %q", s[0].Text)
	}

	if err := store.Put(Binding{ConvID: conv1, ProjectPath: b}); err != nil {
		t.Fatal(err)
	}
	r.Handle(context.Background(), inbound("/projects", "e2"))
	s = fe.sends()
	if !strings.Contains(s[1].Text, b+"  ← 当前") {
		t.Errorf("bound /projects must mark the current candidate: %q", s[1].Text)
	}
	if !strings.Contains(s[1].Text, "当前绑定："+b) {
		t.Errorf("bound /projects must state the binding: %q", s[1].Text)
	}

	// No candidates configured: the hint, not an empty list.
	r2, fe2, _, _ := newTestRouter(t, nil, RouterConfig{})
	r2.Handle(context.Background(), inbound("/projects", "e3"))
	if s := fe2.sends(); !strings.Contains(s[0].Text, "未配置") {
		t.Errorf("no-candidates /projects missing the hint: %q", s[0].Text)
	}
}

// ---- /cancel ----

// /cancel mid-run: the ack fires, the run dies with the terminal
// "已中断" reply, no session id is written back. The kill-tree itself
// is executor_test.go's bar; this proves the router plumbing.
func TestRouter_CancelActiveRun(t *testing.T) {
	r, fe, store, stubDir := newTestRouter(t, func(c *ExecutorConfig) { c.Timeout = 30 * time.Second }, RouterConfig{})
	t.Setenv("CP_STUB_MODE", "hang_tree")
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e1"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Handle(context.Background(), inbound("long running", "e2"))
	}()

	// The stub records its pids once the run is truly in flight.
	pidsPath := filepath.Join(stubDir, "pids")
	waitFor(t, "stub run to start", 10*time.Second, func() bool {
		_, err := os.Stat(pidsPath)
		return err == nil
	})

	r.Handle(context.Background(), inbound("/cancel", "e3"))
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not finish after /cancel")
	}

	waitFor(t, "terminal reply", 5*time.Second, sendsContaining(fe, "run 已中断"))
	ackSeen, termSeen := false, false
	for _, s := range fe.sends() {
		// The ack says WHAT was interrupted; the terminal reply is the
		// dead run's own last word ("run 已中断"). Distinct strings so
		// each assertion pins its own message.
		if strings.Contains(s.Text, "已中断 当前") || strings.Contains(s.Text, "个排队") {
			ackSeen = true
		}
		if strings.Contains(s.Text, "run 已中断") {
			termSeen = true
		}
	}
	if !ackSeen {
		t.Errorf("/cancel ack missing: %+v", fe.sends())
	}
	if !termSeen {
		t.Errorf("terminal 已中断 reply missing: %+v", fe.sends())
	}
	// A killed run must not write a session id (there is no agent_end).
	if b, err := store.Get(conv1); err == nil && b.SessionID != "" {
		t.Errorf("cancelled run wrote session %q", b.SessionID)
	}
}

// /cancel with nothing live says so instead of erroring.
func TestRouter_CancelNothingActive(t *testing.T) {
	r, fe, _, _ := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e1"))

	r.Handle(context.Background(), inbound("/cancel", "e2"))
	if s := fe.sends(); len(s) != 2 || !strings.Contains(s[1].Text, "没有") {
		t.Fatalf("idle /cancel reply = %+v", s)
	}
}

// ---- dedup + DirectAt + malformed (§6) ----

// Same EventID redelivered → one exec, one reply.
func TestRouter_EventIDDedup(t *testing.T) {
	r, fe, _, stubDir := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e0"))

	r.Handle(context.Background(), inbound("once", "dup-1"))
	r.Handle(context.Background(), inbound("once", "dup-1")) // redelivery

	if len(readInvocations(t, stubDir)) != 1 {
		t.Errorf("duplicate EventID must not re-exec")
	}
	if s := fe.sends(); len(s) != 2 { // bind ack + one run reply
		t.Errorf("sends = %+v, want bind ack + exactly one run reply", s)
	}
}

// Empty EventID (a frontend without native ids) is never deduped.
func TestRouter_EmptyEventIDNotDeduped(t *testing.T) {
	r, _, _, stubDir := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e0"))

	r.Handle(context.Background(), inbound("a", ""))
	r.Handle(context.Background(), inbound("b", ""))

	if len(readInvocations(t, stubDir)) != 2 {
		t.Errorf("empty-EventID messages must each run: invocations = %d", len(readInvocations(t, stubDir)))
	}
}

// Group chatter (DirectAt=false) triggers nothing — not prompts, not
// commands; the same conversation then answers when directly addressed.
func TestRouter_DirectAtFilter(t *testing.T) {
	r, fe, _, stubDir := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()

	r.Handle(context.Background(), Inbound{Conv: conv1, Text: "hello?", EventID: "e1", DirectAt: false})
	r.Handle(context.Background(), Inbound{Conv: conv1, Text: "/projects", EventID: "e2", DirectAt: false})
	if got := fe.sends(); len(got) != 0 {
		t.Errorf("DirectAt=false must stay silent, sent %+v", got)
	}

	r.Handle(context.Background(), inbound("/bind "+project, "e3"))
	r.Handle(context.Background(), inbound("real question", "e4"))
	if len(readInvocations(t, stubDir)) != 1 {
		t.Errorf("only the DirectAt=true prompt may exec, invocations = %d", len(readInvocations(t, stubDir)))
	}
	if len(fe.sends()) != 2 {
		t.Errorf("bind ack + run reply expected, got %+v", fe.sends())
	}
}

// Malformed events — no Conv, malformed ConvID, empty text — WARN and
// skip: no panic, no reply, no exec.
func TestRouter_MalformedSkipped(t *testing.T) {
	r, fe, _, stubDir := newTestRouter(t, nil, RouterConfig{})
	cases := []Inbound{
		{Text: "no conv", EventID: "e1", DirectAt: true},
		{Conv: "oc_noprefix", Text: "bad conv", EventID: "e2", DirectAt: true},
		{Conv: conv1, Text: "", EventID: "e3", DirectAt: true},
		{Conv: conv1, Text: "   ", EventID: "e4", DirectAt: true},
	}
	for i, m := range cases {
		r.Handle(context.Background(), m)
		if got := fe.sends(); len(got) != 0 {
			t.Fatalf("case %d: malformed input must not reply, got %+v", i, got)
		}
	}
	if _, err := os.Stat(filepath.Join(stubDir, "invocations.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Error("malformed input must not exec")
	}
}

// Unknown "/word" claims the command namespace: the command list comes
// back instead of a baffling seek run.
func TestRouter_UnknownCommand(t *testing.T) {
	r, fe, _, stubDir := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e0"))

	r.Handle(context.Background(), inbound("/frobnicate", "e1"))
	s := fe.sends()
	if len(s) != 2 || !strings.Contains(s[1].Text, "未知指令") || !strings.Contains(s[1].Text, "/switch") {
		t.Fatalf("unknown-command reply = %+v", s)
	}
	// No run: the invocations file itself must not exist.
	if _, err := os.Stat(filepath.Join(stubDir, "invocations.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Error("unknown command must not exec")
	}
}

// ---- reply plumbing ----

// The reply leaves through the frontend the conversation came from —
// Send-by-prefix (D-C1): no crosstalk onto a sibling frontend.
func TestRouter_ReplyGoesToOwningFrontend(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	store := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	feishu := &fakeFrontend{name: "feishu"}
	telegram := &fakeFrontend{name: "telegram"}
	r := NewRouter([]Frontend{feishu, telegram}, store, ex, RouterConfig{})

	r.Handle(context.Background(), Inbound{
		Conv: NewConvID("telegram", "t9"), Text: "hi", EventID: "e1", DirectAt: true,
	})

	if got := telegram.sends(); len(got) != 1 || !strings.Contains(got[0].Text, "/bind") {
		t.Errorf("telegram frontend must carry the reply: %+v", got)
	}
	if got := feishu.sends(); len(got) != 0 {
		t.Errorf("feishu frontend must stay silent: %+v", got)
	}
}

// A run failure surfaces as a chat reply naming the cause.
func TestRouter_RunFailureSurfaced(t *testing.T) {
	r, fe, _, _ := newTestRouter(t, func(c *ExecutorConfig) { c.SeekPath = "/nonexistent/seek-router-test" }, RouterConfig{})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e1"))

	r.Handle(context.Background(), inbound("doomed", "e2"))
	s := fe.sends()
	if len(s) != 2 {
		t.Fatalf("sends = %+v, want bind ack + failure reply", s)
	}
	if !strings.Contains(s[1].Text, "run 失败") || !strings.Contains(s[1].Text, "/nonexistent/seek-router-test") {
		t.Errorf("failure reply should name the cause: %q", s[1].Text)
	}
}

// A completed run with NO text and NO session id (bare agent_end): the
// placeholder reply goes out and the previously stored session id
// survives — nothing better is known, so nothing is overwritten.
func TestRouter_BareEndKeepsStoredSession(t *testing.T) {
	r, fe, store, _ := newTestRouter(t, nil, RouterConfig{})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e1"))
	r.Handle(context.Background(), inbound("first", "e2")) // happy → S1 stored

	t.Setenv("CP_STUB_MODE", "bare_end")
	r.Handle(context.Background(), inbound("second", "e3"))

	s := fe.sends()
	if len(s) != 3 || !strings.Contains(s[2].Text, "没有文本输出") {
		t.Fatalf("bare-end reply should be the placeholder: %+v", s)
	}
	b, err := store.Get(conv1)
	if err != nil || b.SessionID != "S1" {
		t.Errorf("empty session id must not clobber the stored one: %+v %v", b, err)
	}
}

// Global serve.yolo rides through the router onto the child argv
// (D-C5; per-binding yolo is covered by TestRouter_RebindResetsSession).
func TestRouter_GlobalYoloPassedThrough(t *testing.T) {
	r, _, _, stubDir := newTestRouter(t, nil, RouterConfig{Yolo: true})
	project := t.TempDir()
	r.Handle(context.Background(), inbound("/bind "+project, "e1"))
	r.Handle(context.Background(), inbound("go", "e2"))
	if invs := readInvocations(t, stubDir); !argvHas(invs[len(invs)-1], "--yolo", "") {
		t.Errorf("global yolo must reach the child: %q", invs[len(invs)-1].Argv)
	}
}

// ---- the Serve composition ----

// Router.Handle as the Serve handler: inbounds dispatched by the real
// lifecycle loop come back as Sends through the frontend. The unbound
// hint path keeps this deterministic (no exec, fixed reply count).
func TestRouter_ServeIntegration(t *testing.T) {
	r, fe, _, _ := newTestRouter(t, nil, RouterConfig{})
	fe.inbounds = []Inbound{
		{Conv: NewConvID("feishu", "s1"), Text: "hi", EventID: "e1", DirectAt: true},
		{Conv: NewConvID("feishu", "s2"), Text: "hello there", EventID: "e2", DirectAt: true},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, []ServeSource[Inbound]{fe}, r.Handle, ServeConfig{})
	}()

	waitFor(t, "both hint replies", 10*time.Second, func() bool { return len(fe.sends()) >= 2 })
	for _, s := range fe.sends() {
		if !strings.Contains(s.Text, "/bind") {
			t.Errorf("unexpected reply in Serve integration: %q", s.Text)
		}
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve with router must drain cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// Failure plumbing: a reply with no owning frontend is logged and
// dropped, never a panic; unreadable/unwritable binding stores surface
// as chat replies. The store path points INTO a plain file, so both
// Get (readfile) and Put (mkdirall) fail deterministically on every
// platform.
func TestRouter_StoreFailuresSurfaceAsReplies(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	// bindings.json as a DIRECTORY: ReadFile on a dir errors → the
	// read-failure branch of handlePrompt.
	dirStore := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.Mkdir(dirStore, 0o755); err != nil {
		t.Fatal(err)
	}
	fe := &fakeFrontend{name: "feishu"}
	r := NewRouter([]Frontend{fe}, OpenBindingStoreAt(dirStore), ex, RouterConfig{})

	r.Handle(context.Background(), inbound("hi", "e1"))
	if s := fe.sends(); len(s) != 1 || !strings.Contains(s[0].Text, "读取绑定失败") {
		t.Fatalf("read failure should reply: %+v", s)
	}

	// Store whose parent dir is a FILE: Put's MkdirAll fails → the
	// write-failure branch of /bind.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fe2 := &fakeFrontend{name: "feishu"}
	r2 := NewRouter([]Frontend{fe2}, OpenBindingStoreAt(filepath.Join(blocker, "bindings.json")), ex, RouterConfig{})
	project := t.TempDir()

	r2.Handle(context.Background(), inbound("/bind "+project, "e1"))
	if s := fe2.sends(); len(s) != 1 || !strings.Contains(s[0].Text, "写入绑定失败") {
		t.Fatalf("write failure should reply: %+v", s)
	}
}

// A Router built without the conversation's frontend (mis-wiring, not
// a runtime state): the reply is dropped with a log, never a panic.
func TestRouter_NoFrontendForConvDropsReply(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	store := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	other := &fakeFrontend{name: "telegram"}
	r := NewRouter([]Frontend{other}, store, ex, RouterConfig{})

	r.Handle(context.Background(), inbound("hi", "e1")) // feishu conv, telegram frontend
	if s := other.sends(); len(s) != 0 {
		t.Errorf("mis-routed reply leaked to the wrong frontend: %+v", s)
	}
}

// ---- eventDedup unit ----

// True LRU semantics: a repeat sighting refreshes recency, eviction
// removes the least-recent id, and an evicted id is novel again.
func TestEventDedup_LRUEvictionAndTouch(t *testing.T) {
	d := newEventDedup(2)

	if !d.firstSighting("A") || !d.firstSighting("B") {
		t.Fatal("A and B must be novel")
	}
	if d.firstSighting("A") {
		t.Fatal("A repeat must be deduped")
	}
	if !d.firstSighting("C") {
		t.Fatal("C must be novel")
	}
	// A was touched after B, so B (not A) was evicted by C. Check A's
	// remembered-ness BEFORE re-adding B — the re-add would evict A.
	if d.firstSighting("A") {
		t.Error("A must still be remembered (was touched)")
	}
	if !d.firstSighting("B") {
		t.Error("B was least-recent — must have been evicted")
	}
}
