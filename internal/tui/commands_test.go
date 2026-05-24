package tui

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// emptyModel returns a Model with just enough wiring for command tests.
// No viewport (inline mode); no real agent.
func emptyModel() *Model {
	m := Model{
		opts: Options{
			Tracker: cache.New(),
			Model:   "deepseek-chat",
		},
		input: textarea.New(),
	}
	return &m
}

// runHandler invokes a command handler and returns its cmdResult. In
// inline mode the result is the testable surface — text + flags — and
// avoids any bubbletea machinery.
func runHandler(t *testing.T, m *Model, input string) cmdResult {
	t.Helper()
	parts := strings.SplitN(input, " ", 2)
	name := strings.TrimSpace(parts[0])
	var args string
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	for _, c := range allCommands() {
		for _, n := range c.names {
			if n == name {
				return c.handler(m, args)
			}
		}
	}
	t.Fatalf("no handler for %q", input)
	return cmdResult{}
}

func TestDispatch_RejectsNonCommands(t *testing.T) {
	m := emptyModel()
	handled, _ := dispatchCommand(m, "hello world")
	if handled {
		t.Errorf("non-slash input treated as command")
	}
}

func TestDispatch_Unknown(t *testing.T) {
	m := emptyModel()
	handled, cmd := dispatchCommand(m, "/foobar")
	if !handled {
		t.Errorf("unknown should be handled (with a feedback line)")
	}
	if cmd == nil {
		t.Errorf("unknown should still produce a feedback cmd")
	}
}

func TestHelp_SetsOverlayFlag(t *testing.T) {
	m := emptyModel()
	if m.helpOverlayOpen {
		t.Fatal("should start with overlay closed")
	}
	res := runHandler(t, m, "/help")
	if !m.helpOverlayOpen {
		t.Errorf("help should set helpOverlayOpen")
	}
	if res.text != "" {
		t.Errorf("help should produce no text, got %q", res.text)
	}
}

func TestExit_SetsQuit(t *testing.T) {
	res := runHandler(t, emptyModel(), "/exit")
	if !res.quit {
		t.Errorf("expected quit=true")
	}
}

func TestClear_SetsClear(t *testing.T) {
	res := runHandler(t, emptyModel(), "/clear")
	if !res.clear {
		t.Errorf("expected clear=true")
	}
}

func TestModel_NoArg_OpensPicker(t *testing.T) {
	// Behaviour change: `/model` with no args opens the model picker
	// for curated providers (emptyModel has ProviderName="" → DeepSeek
	// path → curated list). Full picker behaviour (preselect, accept,
	// Esc) is covered in update_test.go's TestCmdModel_NoArgsOpensPicker
	// and TestHandleKey_ModelPicker_*.
	m := emptyModel()
	res := runHandler(t, m, "/model")
	if !m.modelPickerOpen {
		t.Error("/model with no args should open the picker on a curated provider")
	}
	if res.text != "" {
		t.Errorf("opening picker should not emit text, got %q", res.text)
	}
}

func TestModel_WithArg_SwitchesAndFiresHook(t *testing.T) {
	m := emptyModel()
	var captured string
	m.opts.SetModel = func(s string) { captured = s }
	res := runHandler(t, m, "/model deepseek-reasoner")
	if captured != "deepseek-reasoner" {
		t.Errorf("SetModel got %q", captured)
	}
	if m.opts.Model != "deepseek-reasoner" {
		t.Errorf("Options.Model = %q", m.opts.Model)
	}
	if !strings.Contains(res.text, "deepseek-reasoner") {
		t.Errorf("feedback text = %q", res.text)
	}
}

func TestUpgrade_RefusesWhileStreaming(t *testing.T) {
	m := emptyModel()
	m.streaming = true
	res := runHandler(t, m, "/upgrade")
	if !strings.Contains(res.text, "wait for the current turn") {
		t.Errorf("expected refusal text, got %q", res.text)
	}
	if res.extra != nil {
		t.Error("must not start the upgrade goroutine while streaming")
	}
}

func TestUpgrade_RejectsUnknownFlag(t *testing.T) {
	m := emptyModel()
	res := runHandler(t, m, "/upgrade --garbage")
	if !strings.Contains(res.text, "unknown flag") {
		t.Errorf("expected unknown-flag hint, got %q", res.text)
	}
	if res.extra != nil {
		t.Error("must not start the upgrade goroutine after a flag error")
	}
}

func TestUpgrade_AcceptsKnownFlags(t *testing.T) {
	cases := []string{"/upgrade", "/upgrade --force", "/upgrade --dry-run", "/upgrade --force --dry-run"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			m := emptyModel()
			res := runHandler(t, m, in)
			if res.text == "" {
				t.Error("expected a status line in scrollback")
			}
			if res.extra == nil {
				t.Error("expected a background tea.Cmd for the upgrade work")
			}
		})
	}
}

// TestEffort_WithArg_SetsAndFiresHook covers the three accepted levels
// plus the "off"/"none" aliases. Each must (a) update opts.Effort to
// the wire value, (b) fire SetEffort with the same value, and (c) emit
// a transition Println so the user sees the change.
func TestEffort_WithArg_SetsAndFiresHook(t *testing.T) {
	cases := []struct {
		input string
		wire  string // expected on-wire value ("" for off/none)
	}{
		{"/effort high", "high"},
		{"/effort max", "max"},
		{"/effort off", ""},
		{"/effort none", ""},
		{"/effort HIGH", "high"}, // case-insensitive
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			m := emptyModel()
			var captured string
			var fired bool
			m.opts.SetEffort = func(e string) { captured = e; fired = true }

			res := runHandler(t, m, c.input)
			if !fired {
				t.Errorf("SetEffort not called")
			}
			if captured != c.wire {
				t.Errorf("SetEffort got %q, want %q", captured, c.wire)
			}
			if m.opts.Effort != c.wire {
				t.Errorf("opts.Effort = %q, want %q", m.opts.Effort, c.wire)
			}
			if res.text == "" {
				t.Errorf("expected a feedback line; got empty")
			}
		})
	}
}

func TestEffort_RejectsUnknownLevel(t *testing.T) {
	m := emptyModel()
	var fired bool
	m.opts.SetEffort = func(string) { fired = true }
	res := runHandler(t, m, "/effort bananas")
	if fired {
		t.Errorf("SetEffort fired for invalid level — should be rejected before hook")
	}
	if !strings.Contains(res.text, "unknown level") {
		t.Errorf("expected an unknown-level hint, got %q", res.text)
	}
}

func TestEffort_NoArg_OpensPicker(t *testing.T) {
	m := emptyModel()
	m.opts.SetEffort = func(string) {}
	res := runHandler(t, m, "/effort")
	if !m.modelPickerOpen {
		t.Errorf("/effort no-arg should open the picker")
	}
	if m.pickerPurpose != "effort" {
		t.Errorf("pickerPurpose = %q, want %q", m.pickerPurpose, "effort")
	}
	if res.text != "" {
		t.Errorf("opening picker should not emit text, got %q", res.text)
	}
}

// TestEffort_PickerPreselectsCurrent pins the affordance that Enter
// without arrow-key motion is a safe no-op — the picker lands on the
// row matching the current setting. Without this a user opening
// /effort to inspect their state could accidentally flip to "off" by
// pressing Enter.
func TestEffort_PickerPreselectsCurrent(t *testing.T) {
	cases := map[string]int{
		"":     0, // off
		"high": 1,
		"max":  2,
	}
	for cur, wantIdx := range cases {
		t.Run("current="+cur, func(t *testing.T) {
			m := emptyModel()
			m.opts.Effort = cur
			m.opts.SetEffort = func(string) {}
			runHandler(t, m, "/effort")
			if m.modelPickerSelected != wantIdx {
				t.Errorf("preselect index = %d, want %d", m.modelPickerSelected, wantIdx)
			}
		})
	}
}

func TestEffort_UnavailableWithoutHook(t *testing.T) {
	m := emptyModel()
	// SetEffort intentionally nil — TUI was launched in a build that
	// doesn't wire host-side state (shouldn't happen with cmd/seek but
	// the contract is documented and tested).
	res := runHandler(t, m, "/effort high")
	if !strings.Contains(res.text, "unavailable") {
		t.Errorf("expected unavailable message, got %q", res.text)
	}
}

func TestYolo_TogglesAndFiresHook(t *testing.T) {
	m := emptyModel()
	var seen []bool
	m.opts.SetYolo = func(b bool) { seen = append(seen, b) }
	runHandler(t, m, "/yolo")
	runHandler(t, m, "/yolo")
	if len(seen) != 2 || seen[0] != true || seen[1] != false {
		t.Errorf("toggle: %v", seen)
	}
}

func TestNew_UsesHook(t *testing.T) {
	m := emptyModel()
	called := false
	m.opts.RebuildAgent = func() (*agent.Agent, error) {
		called = true
		return nil, nil
	}
	res := runHandler(t, m, "/new")
	if !called {
		t.Error("RebuildAgent hook not invoked")
	}
	if !res.clear {
		t.Errorf("/new should also clear the screen")
	}
}

func TestNew_ReportsHookErrors(t *testing.T) {
	m := emptyModel()
	m.opts.RebuildAgent = func() (*agent.Agent, error) {
		return nil, errors.New("network down")
	}
	res := runHandler(t, m, "/new")
	if !strings.Contains(res.text, "/new failed") || !strings.Contains(res.text, "network down") {
		t.Errorf("text = %q", res.text)
	}
}

func TestNew_EphemeralNoSession_StaysEphemeral(t *testing.T) {
	// --no-save mode: Session == nil, Store == nil.
	// /new must NOT create a session (would convert ephemeral to persisted).
	m := emptyModel()
	m.opts.RebuildAgent = func() (*agent.Agent, error) { return nil, nil }
	res := runHandler(t, m, "/new")
	if m.opts.Session != nil {
		t.Error("/new created a Session in ephemeral mode")
	}
	if !strings.Contains(res.text, "previous session saved") {
		t.Errorf("text = %q", res.text)
	}
}

func TestNew_SessionCreatedAndSaved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", dir)
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	old := session.New("deepseek-chat", "/tmp", "sys", false)
	// /new needs a real agent so persistSession can read Messages().
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}

	m := emptyModel()
	m.opts.RebuildAgent = func() (*agent.Agent, error) { return nil, nil }
	m.opts.Agent = ag
	m.opts.Session = old
	m.opts.Store = store
	m.turns = 2
	m.toolCalls = 1

	res := runHandler(t, m, "/new")

	// Old session should be on disk.
	if _, err := store.Load(old.ID); err != nil {
		t.Errorf("old session not saved after /new: %v", err)
	}
	// New session should be set on the model.
	if m.opts.Session == nil {
		t.Fatal("Session is nil after /new")
	}
	if m.opts.Session.ID == old.ID {
		t.Errorf("Session ID unchanged: %s", m.opts.Session.ID)
	}
	if m.opts.Session.ParentID != "" {
		t.Errorf("new session should not have a parent link, got %q", m.opts.Session.ParentID)
	}
	// New session should be on disk too.
	if _, err := store.Load(m.opts.Session.ID); err != nil {
		t.Errorf("new session not saved: %v", err)
	}
	// Counters should be reset.
	if m.turns != 0 || m.toolCalls != 0 {
		t.Errorf("counters not reset after /new: turns=%d tools=%d", m.turns, m.toolCalls)
	}
	if !res.clear {
		t.Errorf("/new should clear the screen")
	}
}

func TestFilterCommands_EmptyOrSlashReturnsAll(t *testing.T) {
	all := allCommands()
	for _, prefix := range []string{"", "/"} {
		if got := filterCommands(all, prefix); len(got) != len(all) {
			t.Errorf("prefix=%q: got %d, want %d (all)", prefix, len(got), len(all))
		}
	}
}

func TestFilterCommands_PrefixMatch(t *testing.T) {
	all := allCommands()
	got := filterCommands(all, "/m")
	if len(got) != 1 || got[0].names[0] != "/model" {
		t.Errorf("/m → %v, want just /model", names(got))
	}
}

func TestFilterCommands_MatchesAlias(t *testing.T) {
	// /q is an alias of /exit — should be findable by alias prefix.
	all := allCommands()
	got := filterCommands(all, "/q")
	if len(got) != 1 || got[0].names[0] != "/exit" {
		t.Errorf("/q → %v, want /exit (matched via alias)", names(got))
	}
}

func TestFilterCommands_NoMatch(t *testing.T) {
	got := filterCommands(allCommands(), "/zz")
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", names(got))
	}
}

func names(cs []command) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.names[0]
	}
	return out
}

func TestNew_NoHook(t *testing.T) {
	m := emptyModel()
	res := runHandler(t, m, "/new")
	if !strings.Contains(res.text, "unsupported") {
		t.Errorf("text = %q", res.text)
	}
}

func TestBranch_NoSessionReportsUnavailable(t *testing.T) {
	res := runHandler(t, emptyModel(), "/branch")
	if !strings.Contains(res.text, "unavailable") || !strings.Contains(res.text, "--no-save") {
		t.Errorf("text = %q", res.text)
	}
}

func TestBranch_ForksAndSwitchesSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", dir)
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	parent := session.New("deepseek-v4-flash", "/tmp", "sys", false)
	parent.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "hi"},
		{Role: deepseek.RoleAssistant, Content: "hello"},
	}
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}

	// /branch reads the agent's Messages() in persistSession before
	// forking — we need a real Agent for that path. No LLM call is
	// made by /branch itself, so the client baseURL can be a stub.
	ag, err := agent.New(agent.Config{
		Client:          deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		SystemPrompt:    "sys",
		InitialMessages: parent.Messages,
	})
	if err != nil {
		t.Fatal(err)
	}

	m := emptyModel()
	m.opts.Agent = ag
	m.opts.Session = parent
	m.opts.Store = store
	m.turns = 2
	m.toolCalls = 1

	res := runHandler(t, m, "/branch")
	if !strings.Contains(res.text, "branched") {
		t.Errorf("expected branched feedback, got %q", res.text)
	}

	if m.opts.Session == parent {
		t.Fatalf("session pointer did not switch")
	}
	if m.opts.Session.ParentID != parent.ID {
		t.Errorf("child ParentID = %q, want %q", m.opts.Session.ParentID, parent.ID)
	}
	if m.turns != 0 || m.toolCalls != 0 {
		t.Errorf("counters not reset: turns=%d tools=%d", m.turns, m.toolCalls)
	}
	// Both parent and child should exist on disk after the fork.
	if _, err := store.Load(parent.ID); err != nil {
		t.Errorf("parent missing after /branch: %v", err)
	}
	if _, err := store.Load(m.opts.Session.ID); err != nil {
		t.Errorf("child missing after /branch: %v", err)
	}
}

func TestCompact_ShortHistoryIsNoOp(t *testing.T) {
	ag, _ := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		SystemPrompt: "sys",
	})
	m := emptyModel()
	m.opts.Agent = ag
	res := runHandler(t, m, "/compact")
	if !strings.Contains(res.text, "already short") {
		t.Errorf("text = %q", res.text)
	}
	if res.extra != nil {
		t.Errorf("expected no async cmd for short history")
	}
}

func TestSkills_EmptyReportsNoneLoaded(t *testing.T) {
	res := runHandler(t, emptyModel(), "/skills")
	if !strings.Contains(res.text, "no skills loaded") {
		t.Errorf("text = %q", res.text)
	}
}

func TestSkills_ListsLoadedSkillsWithSource(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{
		Name: "alpha", Description: "use for A", Source: "project .seek/alpha.md",
	})
	set.Add(&skill.Skill{
		Name: "beta", Description: "use for B", Source: "builtin:beta",
	})

	m := emptyModel()
	m.opts.Skills = set

	res := runHandler(t, m, "/skills")
	for _, want := range []string{
		"loaded 2 skill",
		"alpha",
		"project .seek/alpha.md",
		"use for A",
		"beta",
		"builtin:beta",
	} {
		if !strings.Contains(res.text, want) {
			t.Errorf("missing %q in output:\n%s", want, res.text)
		}
	}
}

func TestSkillCLI_HelpRendersDispatcherText(t *testing.T) {
	// `/skill` with no args should hit skillcli's help printer. The
	// TUI rendering shouldn't lose the command list.
	m := emptyModel()
	res := runHandler(t, m, "/skill")
	for _, want := range []string{"install", "uninstall", "update", "list", "status"} {
		if !strings.Contains(res.text, want) {
			t.Errorf("/skill help missing %q:\n%s", want, res.text)
		}
	}
}

func TestSkillCLI_UnknownVerbSurfaces(t *testing.T) {
	// skillcli.Run returns an error for unknown verbs. The TUI
	// command should fold that error into the rendered text rather
	// than swallowing it — silent failure is the worst UX here.
	m := emptyModel()
	res := runHandler(t, m, "/skill frobnicate")
	if !strings.Contains(res.text, "unknown") {
		t.Errorf("expected error message in output, got:\n%s", res.text)
	}
}

func TestSkillCLI_WhitespaceSplitsArgs(t *testing.T) {
	// Verifies the args parser does the right thing for the
	// vanilla case (subcommand + flag + value). We trip a known
	// error path (uninstall a non-existent skill) and confirm the
	// name we passed shows up in the message — proves the tokens
	// reached skillcli intact.
	m := emptyModel()
	res := runHandler(t, m, "/skill uninstall does-not-exist")
	if !strings.Contains(res.text, "does-not-exist") {
		t.Errorf("token didn't reach skillcli; output was:\n%s", res.text)
	}
}

func TestCompact_KicksOffAsyncSummariseAndPostsDoneMsg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Summarise the conversation") {
			t.Errorf("missing summariser prompt in body: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id":"x","model":"deepseek-chat",
			"choices":[{"index":0,"message":{"role":"assistant","content":"BRIEFING"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}
		}`)
	}))
	defer srv.Close()

	ag, _ := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
		InitialMessages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "u1"},
			{Role: deepseek.RoleAssistant, Content: "a1"},
			{Role: deepseek.RoleUser, Content: "u2"},
			{Role: deepseek.RoleAssistant, Content: "a2"},
		},
	})
	m := emptyModel()
	m.opts.Agent = ag

	res := runHandler(t, m, "/compact")
	if res.extra == nil {
		t.Fatalf("expected async cmd, got nil")
	}
	msg := res.extra()
	done, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("unexpected msg type: %T", msg)
	}
	if done.err != nil {
		t.Fatalf("compact err: %v", done.err)
	}
	if !strings.Contains(done.summary, "BRIEFING") {
		t.Errorf("summary = %q", done.summary)
	}

	// Feed it back through the handler to verify history swap.
	cmds := m.handleCompactDone(done)
	if len(cmds) == 0 {
		t.Errorf("expected confirmation cmd")
	}
	hist := ag.Messages()
	// system + user(summary) + assistant(ack) = 3.
	if len(hist) != 3 {
		t.Fatalf("post-compact history len = %d, want 3: %+v", len(hist), hist)
	}
	if !strings.Contains(hist[1].Content, "BRIEFING") {
		t.Errorf("summary not folded into history: %q", hist[1].Content)
	}
}

// TestCompact_ForkPreservesFullHistory verifies that handleCompactDone
// writes the full history to a snapshot session and creates a child
// session (ParentID → snapshot) containing only the summary pair.
func TestCompact_ForkPreservesFullHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id":"x","model":"deepseek-chat",
			"choices":[{"index":0,"message":{"role":"assistant","content":"SUMMARY"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`)
	}))
	defer srv.Close()

	ag, _ := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
		InitialMessages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "msg1"},
			{Role: deepseek.RoleAssistant, Content: "reply1"},
			{Role: deepseek.RoleUser, Content: "msg2"},
			{Role: deepseek.RoleAssistant, Content: "reply2"},
		},
	})

	t.Setenv("SEEK_SESSIONS_DIR", t.TempDir())
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	snap := session.New("deepseek-chat", "/tmp", "sys", false)
	snap.Messages = ag.Messages()
	if err := store.Save(snap); err != nil {
		t.Fatalf("save initial session: %v", err)
	}

	m := emptyModel()
	m.opts.Agent = ag
	m.opts.Session = snap
	m.opts.Store = store

	// Run compact.
	res := runHandler(t, m, "/compact")
	done := res.extra().(compactDoneMsg)
	if done.err != nil {
		t.Fatalf("compact err: %v", done.err)
	}

	cmds := m.handleCompactDone(done)
	if len(cmds) == 0 {
		t.Error("expected at least one cmd from handleCompactDone")
	}

	// Child session should now be active with ParentID → snapshot.
	child := m.opts.Session
	if child.ID == snap.ID {
		t.Fatal("session ID should have changed after compact fork")
	}
	if child.ParentID != snap.ID {
		t.Errorf("child.ParentID = %q, want %q", child.ParentID, snap.ID)
	}

	// Agent history should be exactly the summary pair (+ system).
	hist := ag.Messages()
	if len(hist) != 3 {
		t.Fatalf("post-compact history len = %d, want 3", len(hist))
	}

	// Snapshot session on disk must still hold the original full history.
	loaded, err := store.Load(snap.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	// system + 4 conversation messages = 5
	if len(loaded.Messages) != 5 {
		t.Errorf("snapshot messages = %d, want 5", len(loaded.Messages))
	}

	// Counters reset to 0 after fork.
	if m.turns != 0 || m.toolCalls != 0 {
		t.Errorf("turns/toolCalls should reset after fork, got %d/%d", m.turns, m.toolCalls)
	}
}
