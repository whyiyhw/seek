package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// initGitRepo creates a temporary git repo with an initial commit on main.
// The caller should use t.Cleanup to remove the dir (t.TempDir handles it).
// Returns the path to the repo root.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "test@test")
	run("git", "config", "user.name", "test")
	run("git", "checkout", "-b", "main")
	run("git", "commit", "--allow-empty", "-m", "initial")
	return dir
}

// requireGit skips the test if git is not available.
func requireGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

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
	t.Parallel()
	m := emptyModel()
	handled, _ := dispatchCommand(m, "hello world")
	if handled {
		t.Errorf("non-slash input treated as command")
	}
}

func TestDispatch_Unknown(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	handled, cmd := dispatchCommand(m, "/foobar")
	if !handled {
		t.Errorf("unknown should be handled (with a feedback line)")
	}
	if cmd == nil {
		t.Errorf("unknown should still produce a feedback cmd")
	}
}

// TestHelp_NoArgs_OpensPicker verifies that /help without arguments
// opens the help topic picker, mirroring /model's picker pattern.
func TestHelp_NoArgs_OpensPicker(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	_ = runHandler(t, m, "/help")
	if !m.modelPickerOpen {
		t.Fatal("/help should open the topic picker, got closed")
	}
	if m.pickerPurpose != "help-topic" {
		t.Fatalf("/help picker purpose should be 'help-topic', got %q", m.pickerPurpose)
	}
	if len(m.modelPickerFiltered) == 0 {
		t.Fatal("/help picker should have filtered candidates")
	}
	// Verify all expected help topics are present in the picker.
	topics := map[string]bool{"all": false, "commands": false, "keys": false, "about": false}
	for _, c := range m.modelPickerFiltered {
		topics[c.id] = true
	}
	for name, found := range topics {
		if !found {
			t.Errorf("/help picker missing topic %q", name)
		}
	}
}

// TestHelp_WithArg_ShowsTopic verifies that /help <topic> shows the
// help overlay with topic-specific content, mirroring /model <id>.
func TestHelp_WithArg_ShowsTopic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    string
		present []string
		absent  []string
	}{
		{"all", "all", []string{" Commands ", " Keys ", "/help"}, nil},
		{"commands", "commands", []string{" Commands ", "/help", "/model"}, []string{"↑ / ↓"}},
		{"keys", "keys", []string{" Keys ", "↑ / ↓", "Ctrl+J"}, []string{" Commands "}},
		{"about", "about", []string{"Version", "MIT"}, []string{" Commands "}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := emptyModel()
			_ = runHandler(t, m, "/help "+tc.args)
			if !m.helpOverlayOpen {
				t.Fatalf("/help %s should set helpOverlayOpen", tc.args)
			}
			if m.helpContent == "" {
				t.Fatalf("/help %s should set non-empty helpContent", tc.args)
			}
			for _, want := range tc.present {
				if !strings.Contains(m.helpContent, want) {
					t.Errorf("/help %s content missing %q", tc.args, want)
				}
			}
			for _, notWant := range tc.absent {
				if strings.Contains(m.helpContent, notWant) {
					t.Errorf("/help %s content should not contain %q (different topic)", tc.args, notWant)
				}
			}
		})
	}
}

// TestClearAliasesNew locks the unified-semantics decision: /clear
// and /new dispatch to the same handler, so their cmdResults are
// indistinguishable. Hooks fire identically; both request a screen
// clear; both emit the "previous session saved" notice. The
// "blank-screen only" use case lives on Ctrl+L (see
// TestHandleKey_CtrlL_RequestsClearScreen in update_test.go).
func TestClearAliasesNew(t *testing.T) {
	t.Parallel()

	build := func() *Model {
		m := emptyModel()
		m.opts.RebuildAgent = func() (*agent.Agent, error) { return nil, nil }
		return m
	}

	resClear := runHandler(t, build(), "/clear")
	resNew := runHandler(t, build(), "/new")

	if resClear.clear != resNew.clear {
		t.Errorf("clear=%v new=%v — both should request ClearScreen", resClear.clear, resNew.clear)
	}
	if !resClear.clear {
		t.Errorf("/clear must request ClearScreen (unified with /new)")
	}
	if resClear.text != resNew.text {
		t.Errorf("/clear and /new must produce identical notice text;\nclear: %q\nnew:   %q", resClear.text, resNew.text)
	}
	if !strings.Contains(resClear.text, "previous session saved") {
		t.Errorf("/clear should now emit the same notice as /new; got %q", resClear.text)
	}
}

// TestClearWithoutRebuildHook_SurfacesError mirrors TestNew_NoHook's
// behaviour now that /clear shares the /new handler. emptyModel()
// doesn't wire RebuildAgent, so the handler must surface the
// unsupported-state notice rather than silently no-op.
func TestClearWithoutRebuildHook_SurfacesError(t *testing.T) {
	t.Parallel()
	m := emptyModel() // no RebuildAgent
	res := runHandler(t, m, "/clear")
	if !strings.Contains(res.text, "unsupported") {
		t.Errorf("/clear without RebuildAgent should surface error; got %q", res.text)
	}
	if res.clear {
		t.Errorf("/clear without RebuildAgent should NOT request ClearScreen; got clear=true")
	}
}

func TestExit_SetsQuit(t *testing.T) {
	t.Parallel()
	res := runHandler(t, emptyModel(), "/exit")
	if !res.quit {
		t.Errorf("expected quit=true")
	}
}

func TestModel_NoArg_OpensPicker(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestEffort_DescriptionHasPickerHint ensures the /effort description
// mentions the picker — same as /model and /lang. Without this the
// usage text reads as if only manual typing is supported (see
// picker-over-typed-input memory entry).
func TestEffort_DescriptionHasPickerHint(t *testing.T) {
	t.Parallel()
	for _, c := range allCommands() {
		for _, n := range c.names {
			if n == "/effort" {
				if !strings.Contains(c.description, "No args opens a picker") {
					t.Errorf("/effort description should mention the picker, got:\n  %s", c.description)
				}
				return
			}
		}
	}
	t.Fatal("/effort not found in allCommands()")
}

// TestEffort_PickerPreselectsCurrent pins the affordance that Enter
// without arrow-key motion is a safe no-op — the picker lands on the
// row matching the current setting. Without this a user opening
// /effort to inspect their state could accidentally flip to "off" by
// pressing Enter.
func TestEffort_PickerPreselectsCurrent(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	m := emptyModel()
	var seen []bool
	m.opts.SetYolo = func(b bool) { seen = append(seen, b) }
	runHandler(t, m, "/yolo")
	runHandler(t, m, "/yolo")
	if len(seen) != 2 || seen[0] != true || seen[1] != false {
		t.Errorf("toggle: %v", seen)
	}
}

func TestPlan_TogglesAndFiresHook(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	var seen []bool
	m.opts.SetPlan = func(b bool) { seen = append(seen, b) }
	runHandler(t, m, "/plan")
	runHandler(t, m, "/plan")
	if len(seen) != 2 || seen[0] != true || seen[1] != false {
		t.Errorf("toggle: %v", seen)
	}
}

// TestPlan_TogglesSetsAndClearsSubstate pins the v2 contract: /plan
// entry seeds substate to "analyze" (default state of the plan-mode
// state machine, PRD §2.1); /plan off clears it so a stale value
// doesn't leak into the next session's status bar or into a future
// re-entry that should default to analyze.
func TestPlan_TogglesSetsAndClearsSubstate(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.SetPlan = func(b bool) {}

	runHandler(t, m, "/plan") // on
	if !m.opts.Plan {
		t.Fatal("Plan should be on after /plan")
	}
	if m.opts.PlanSubstate != "analyze" {
		t.Errorf("entry substate = %q, want %q", m.opts.PlanSubstate, "analyze")
	}

	runHandler(t, m, "/plan") // off
	if m.opts.Plan {
		t.Fatal("Plan should be off after second /plan")
	}
	if m.opts.PlanSubstate != "" {
		t.Errorf("exit substate = %q, want \"\"", m.opts.PlanSubstate)
	}
}

func TestYolo_ToggleTurnsOffPlan(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Plan = true
	m.opts.SetYolo = func(b bool) {}
	m.opts.SetPlan = func(b bool) {}
	runHandler(t, m, "/yolo")
	if !m.opts.Yolo {
		t.Error("/yolo should set Yolo=true")
	}
	if m.opts.Plan {
		t.Error("/yolo should turn Plan off (mutual exclusion)")
	}
}

func TestPlan_ToggleTurnsOffYolo(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	m.opts.Yolo = true
	m.opts.SetYolo = func(b bool) {}
	m.opts.SetPlan = func(b bool) {}
	runHandler(t, m, "/plan")
	if !m.opts.Plan {
		t.Error("/plan should set Plan=true")
	}
	if m.opts.Yolo {
		t.Error("/plan should turn Yolo off (mutual exclusion)")
	}
}

func TestReview_UnknownArgsError(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	res := runHandler(t, m, "/review no-such-branch")
	if !strings.Contains(res.text, "no diff found") {
		t.Errorf("expected error about no diff found, got %q", res.text)
	}
}

func TestNew_UsesHook(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	old := session.New("deepseek-chat", "/tmp", "sys", false, false)
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
	t.Parallel()
	all := allCommands()
	for _, prefix := range []string{"", "/"} {
		if got := filterCommands(all, prefix); len(got) != len(all) {
			t.Errorf("prefix=%q: got %d, want %d (all)", prefix, len(got), len(all))
		}
	}
}

func TestFilterCommands_PrefixMatch(t *testing.T) {
	t.Parallel()
	all := allCommands()
	got := filterCommands(all, "/m")
	if len(got) != 2 || got[0].names[0] != "/model" || got[1].names[0] != "/memory" {
		t.Errorf("/m → %v, want [/model /memory]", names(got))
	}
}

func TestFilterCommands_MatchesAlias(t *testing.T) {
	t.Parallel()
	// /q is an alias of /exit — should be findable by alias prefix.
	all := allCommands()
	got := filterCommands(all, "/q")
	if len(got) != 1 || got[0].names[0] != "/exit" {
		t.Errorf("/q → %v, want /exit (matched via alias)", names(got))
	}
}

func TestFilterCommands_NoMatch(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	m := emptyModel()
	res := runHandler(t, m, "/new")
	if !strings.Contains(res.text, "unsupported") {
		t.Errorf("text = %q", res.text)
	}
}

func TestBranch_NoSessionReportsUnavailable(t *testing.T) {
	t.Parallel()
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
	parent := session.New("deepseek-v4-flash", "/tmp", "sys", false, false)
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
	t.Parallel()
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
	t.Parallel()
	res := runHandler(t, emptyModel(), "/skills")
	if !strings.Contains(res.text, "no skills loaded") {
		t.Errorf("text = %q", res.text)
	}
}

func TestSkills_ListsLoadedSkillsWithSource(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	snap := session.New("deepseek-chat", "/tmp", "sys", false, false)
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

// --- Git utility function tests -----------------------------------------

func TestCurrentGitBranch(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	got := currentGitBranch(context.Background(), dir)
	if got != "main" {
		t.Errorf("currentGitBranch = %q, want %q", got, "main")
	}
}

func TestCurrentGitBranch_EmptyCWD(t *testing.T) {
	t.Parallel()
	if got := currentGitBranch(context.Background(), ""); got != "" {
		t.Errorf("currentGitBranch('') = %q, want ''", got)
	}
}

func TestGatherChangedFiles_WithChanges(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Create an untracked file and a modified (then committed) file.
	git("commit", "--allow-empty", "-m", "base")
	os.WriteFile(dir+"/newfile.go", []byte("untracked\n"), 0o644)
	os.WriteFile(dir+"/modified.go", []byte("v1\n"), 0o644)
	git("add", "modified.go")
	git("commit", "-m", "add modified.go")

	// Modify modified.go after the commit (different content).
	os.WriteFile(dir+"/modified.go", []byte("v2\n"), 0o644)

	result, ok := gatherChangedFiles(dir)
	if !ok {
		t.Fatal("gatherChangedFiles returned false, expected true")
	}
	if !strings.Contains(result, "newfile.go") {
		t.Errorf("result should mention newfile.go, got:\n%s", result)
	}
}

func TestGatherChangedFiles_NoChanges(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	_, ok := gatherChangedFiles(dir)
	if ok {
		t.Error("gatherChangedFiles should return false for clean repo")
	}
}

func TestGatherChangedFiles_EmptyCWD(t *testing.T) {
	t.Parallel()
	_, ok := gatherChangedFiles("")
	if ok {
		t.Error("gatherChangedFiles('') should return false")
	}
}

func TestGatherBranchDiff_WithBranch(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Create a feature branch with a commit, then add a divergent commit
	// on main so target...HEAD is non-empty. Use actual file changes so
	// git diff has content to show (--allow-empty produces no diff).
	git("checkout", "-b", "feature")
	git("commit", "--allow-empty", "-m", "feature work")
	git("checkout", "main")
	os.WriteFile(dir+"/main-only.go", []byte("package main\n"), 0o644)
	git("add", "main-only.go")
	git("commit", "-m", "main work after branch")

	result, ok := gatherBranchDiff(dir, "feature")
	if !ok {
		t.Fatal("gatherBranchDiff returned false, expected true")
	}
	if !strings.Contains(result, "Diff against feature") {
		t.Errorf("result should mention target branch, got:\n%s", result)
	}
}

func TestGatherBranchDiff_UnknownBranch(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	_, ok := gatherBranchDiff(dir, "no-such-branch")
	if ok {
		t.Error("gatherBranchDiff should return false for unknown branch")
	}
}

func TestGatherBranchDiff_FlagInjectionGuard(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	_, ok := gatherBranchDiff(dir, "--all")
	if ok {
		t.Error("gatherBranchDiff should reject flag-like branch names")
	}
}

func TestGatherBranchDiff_EmptyCWD(t *testing.T) {
	t.Parallel()
	_, ok := gatherBranchDiff("", "main")
	if ok {
		t.Error("gatherBranchDiff('', 'main') should return false")
	}
}

func TestGatherBranchDiff_EmptyTarget(t *testing.T) {
	t.Parallel()
	_, ok := gatherBranchDiff("/tmp", "")
	if ok {
		t.Error("gatherBranchDiff('/tmp', '') should return false")
	}
}

func TestReviewChoices_WithChanges(t *testing.T) {
	t.Parallel()
	requireGit(t)

	dir := initGitRepo(t)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Create a branch so there's a non-working-tree option.
	run("checkout", "-b", "other")
	run("checkout", "main")

	choices := reviewChoices(dir)
	if len(choices) == 0 {
		t.Fatal("reviewChoices returned empty, expected at least 'Type a branch name...'")
	}
	// Should have "type-branch" as the last option.
	last := choices[len(choices)-1]
	if last.id != "type-branch" {
		t.Errorf("last choice id = %q, want %q", last.id, "type-branch")
	}
}

func TestReviewChoices_EmptyCWD(t *testing.T) {
	t.Parallel()
	choices := reviewChoices("")
	if choices != nil {
		t.Errorf("reviewChoices('') should return nil, got %v", choices)
	}
}

func TestReviewChoices_NonGitDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	choices := reviewChoices(dir)
	if choices != nil {
		t.Errorf("reviewChoices(non-git-dir) should return nil, got %v", choices)
	}
}

// --- review prompt tests ------------------------------------------------

func TestWorkingTreeReviewPrompt_IncludesChanges(t *testing.T) {
	t.Parallel()
	prompt := workingTreeReviewPrompt("M  foo.go\n?? bar.go")
	if !strings.Contains(prompt, "foo.go") {
		t.Error("prompt should contain file from changes")
	}
	if !strings.Contains(prompt, "Do NOT write or edit files") {
		t.Error("prompt should contain read-only instruction")
	}
}

func TestFallbackReviewPrompt(t *testing.T) {
	t.Parallel()
	prompt := fallbackReviewPrompt()
	if !strings.Contains(prompt, "Review the code") {
		t.Errorf("unexpected prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "Do NOT write or edit files") {
		t.Error("prompt should contain read-only instruction")
	}
}

func TestBranchDiffReviewPrompt_IncludesDiff(t *testing.T) {
	t.Parallel()
	prompt := branchDiffReviewPrompt("main", "diff --git a/x.go b/x.go")
	if !strings.Contains(prompt, "main") {
		t.Error("prompt should mention target branch")
	}
	if !strings.Contains(prompt, "diff --git") {
		t.Error("prompt should include diff content")
	}
}

// --- Helper behaviour (timeout sanity) ----------------------------------

func TestCurrentGitBranch_Timeout(t *testing.T) {
	// Not parallel: tests that a cancelled context returns "" immediately.
	ctx, cancel := context.WithTimeout(context.Background(), -1*time.Nanosecond)
	defer cancel()

	got := currentGitBranch(ctx, "/nonexistent")
	if got != "" {
		t.Errorf("expected empty for cancelled context, got %q", got)
	}
}

// --- /skills --used ------------------------------------------------------
//
// Coverage targets PRD v2 §4.3 (the .stats.jsonl session-id filter) and
// the small UX rules: pre-existing /skills (no args) must keep working,
// --used with a session that has no calls says so explicitly, and
// counts are sorted by count desc.

// writeStatsJSONL puts a synthetic .stats.jsonl under $SEEK_HOME for
// cmdSkillsUsed to read. Each line is one skillstats.Entry shape; the
// caller controls which session_id values appear so the filter rule
// can be exercised.
func writeStatsJSONL(t *testing.T, lines []string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(skillsDir, ".stats.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write stats: %v", err)
	}
}

func TestCmdSkills_NoArgsListsLoaded(t *testing.T) {
	m := emptyModel()
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "go-test-runner", Description: "runs go tests", Source: "builtin"})
	m.opts.Skills = set

	res := runHandler(t, m, "/skills")
	for _, want := range []string{"loaded 1 skill", "go-test-runner", "runs go tests"} {
		if !strings.Contains(res.text, want) {
			t.Errorf("expected %q in:\n%s", want, res.text)
		}
	}
}

func TestCmdSkillsUsed_FiltersBySession(t *testing.T) {
	sess := session.New("deepseek-chat", "/tmp", "sys", false, false)
	other := "20260101-000000-aaaaaa" // a different session id

	writeStatsJSONL(t, []string{
		`{"ts":"2026-05-24T10:00:00Z","name":"dual-model","session_id":"` + sess.ID + `","model":"deepseek-reasoner","provider":"deepseek"}`,
		`{"ts":"2026-05-24T10:05:00Z","name":"dual-model","session_id":"` + sess.ID + `","model":"deepseek-reasoner","provider":"deepseek"}`,
		`{"ts":"2026-05-24T10:10:00Z","name":"go-test-runner","session_id":"` + sess.ID + `","model":"deepseek-chat","provider":"deepseek"}`,
		// Different session — must NOT be counted.
		`{"ts":"2026-05-24T11:00:00Z","name":"dual-model","session_id":"` + other + `","model":"deepseek-chat","provider":"deepseek"}`,
	})

	m := emptyModel()
	m.opts.Session = sess
	res := runHandler(t, m, "/skills --used")

	if !strings.Contains(res.text, "skills called this session (2)") {
		t.Errorf("expected 2 distinct skills called, got:\n%s", res.text)
	}
	// dual-model (2 calls) should rank above go-test-runner (1 call).
	dual := strings.Index(res.text, "dual-model")
	gtr := strings.Index(res.text, "go-test-runner")
	if dual < 0 || gtr < 0 {
		t.Fatalf("expected both skills in output:\n%s", res.text)
	}
	if dual > gtr {
		t.Errorf("dual-model (2 calls) should rank before go-test-runner (1 call):\n%s", res.text)
	}
	// The other-session entry must NOT bump dual-model's count.
	if !strings.Contains(res.text, "2 call(s)") {
		t.Errorf("dual-model count should be exactly 2 (other-session row must not leak):\n%s", res.text)
	}
}

func TestCmdSkillsUsed_NoCallsInThisSession(t *testing.T) {
	sess := session.New("deepseek-chat", "/tmp", "sys", false, false)
	// Stats file exists but contains only entries from a different session.
	writeStatsJSONL(t, []string{
		`{"ts":"2026-05-24T10:00:00Z","name":"dual-model","session_id":"some-other-session","model":"deepseek-chat","provider":"deepseek"}`,
	})

	m := emptyModel()
	m.opts.Session = sess
	res := runHandler(t, m, "/skills --used")

	if !strings.Contains(res.text, "no skills called in this session yet") {
		t.Errorf("expected 'no skills called' message, got:\n%s", res.text)
	}
}

func TestCmdSkillsUsed_NoSessionSaysSo(t *testing.T) {
	// --no-save / inline mode: opts.Session is nil. /skills --used must
	// give a clear reason rather than panic or pretend to query.
	m := emptyModel()
	m.opts.Session = nil
	res := runHandler(t, m, "/skills --used")

	if !strings.Contains(res.text, "needs a session") {
		t.Errorf("expected helpful 'needs a session' message, got:\n%s", res.text)
	}
}

// --- /skill use --------------------------------------------------------
//
// Arm-vs-fire semantics: bare name arms (sets pendingSkill); name +
// extra fires immediately; "clear" disarms. Slash dispatch is gated
// before consumeArm runs in the live KeyEnter path, so we assert the
// state changes directly on Model and check that consumeArm wraps
// then clears.

// withSkills attaches a Set containing the named skills (with minimal
// metadata) so cmdSkillUse can validate them.
func withSkills(m *Model, names ...string) {
	set := skill.NewSet()
	for _, n := range names {
		set.Add(&skill.Skill{Name: n, Description: "desc for " + n, Source: "builtin"})
	}
	m.opts.Skills = set
}

func TestCmdSkillUse_BareNameArms(t *testing.T) {
	m := emptyModel()
	withSkills(m, "dual-model")

	res := runHandler(t, m, "/skill use dual-model")

	if m.pendingSkill != "dual-model" {
		t.Errorf("expected pendingSkill=dual-model, got %q", m.pendingSkill)
	}
	if !strings.Contains(res.text, "armed") || !strings.Contains(res.text, "dual-model") {
		t.Errorf("expected confirmation mentioning armed + skill name, got: %q", res.text)
	}
}

func TestCmdSkillUse_NameWithExtraFiresImmediately(t *testing.T) {
	m := emptyModel()
	withSkills(m, "dual-model")

	// submitOrSteer in non-streaming mode tries to call m.submit which
	// needs m.opts.Agent / opts.Ctx. We don't have those in emptyModel,
	// so we'd panic — fix by stubbing m.streaming = true: that path
	// re-routes through steerStream which DOES require Agent for the
	// cancel side... so instead we verify state preconditions and
	// re-enter the dispatch with a minimal mock. Simpler: drop down to
	// cmdSkillUse directly and only assert on the non-submit branches.
	//
	// Approach: assert that handing extra args takes us out of the arm
	// branch (pendingSkill stays empty) and produces a cmdResult whose
	// payload is the submit pipeline (extra cmd, not text).
	m.streaming = false
	// We can't easily run m.submit without an agent. Inspect cmdSkillUse
	// directly by routing through cmdSkillCLI on the "use" verb so
	// we exercise the production parser. To avoid Agent panic, replace
	// the model's submit path by using a streaming sentinel and
	// catching the steerStream call's lack of agent.
	m.streaming = true         // route through steerStream branch
	m.cancelStream = func() {} // steerStream calls cancel()
	res := runHandler(t, m, "/skill use dual-model 帮我重构 foo.go")

	if m.pendingSkill != "" {
		t.Errorf("immediate fire should NOT arm; got pendingSkill=%q", m.pendingSkill)
	}
	// steerStream stashes the wrapped text in pendingSteerText. Verify
	// the wrapper is exactly what consumeArm would produce — single
	// source of truth check.
	if !strings.Contains(m.pendingSteerText, "dual-model") {
		t.Errorf("steer text should reference the skill name, got: %q", m.pendingSteerText)
	}
	if !strings.Contains(m.pendingSteerText, "帮我重构 foo.go") {
		t.Errorf("steer text should preserve the user's task, got: %q", m.pendingSteerText)
	}
	_ = res
}

func TestCmdSkillUse_Clear(t *testing.T) {
	m := emptyModel()
	withSkills(m, "dual-model")
	m.pendingSkill = "dual-model"

	res := runHandler(t, m, "/skill use clear")

	if m.pendingSkill != "" {
		t.Errorf("clear should disarm; got pendingSkill=%q", m.pendingSkill)
	}
	if !strings.Contains(res.text, "disarmed") {
		t.Errorf("expected 'disarmed' confirmation, got: %q", res.text)
	}
}

func TestCmdSkillUse_ClearWhenNothingArmed(t *testing.T) {
	m := emptyModel()
	withSkills(m, "dual-model")

	res := runHandler(t, m, "/skill use clear")
	// Idempotent — clearing nothing is fine. The message just tells the
	// user nothing was armed so they can tell apart "I cleared it" from
	// "there was nothing to clear" without checking state themselves.
	if !strings.Contains(res.text, "nothing armed") {
		t.Errorf("expected 'nothing armed' notice, got: %q", res.text)
	}
}

func TestCmdSkillUse_UnknownNameErrorsAndListsAvailable(t *testing.T) {
	m := emptyModel()
	withSkills(m, "dual-model", "go-test-runner")

	res := runHandler(t, m, "/skill use typo")

	if m.pendingSkill != "" {
		t.Errorf("unknown skill must not arm; got %q", m.pendingSkill)
	}
	// Error must surface the available names so the user can fix the
	// typo without another round-trip to /skills.
	for _, want := range []string{"typo", "not found", "dual-model", "go-test-runner"} {
		if !strings.Contains(res.text, want) {
			t.Errorf("error should contain %q, got: %q", want, res.text)
		}
	}
}

func TestCmdSkillUse_NoSkillsLoaded(t *testing.T) {
	m := emptyModel() // m.opts.Skills nil
	res := runHandler(t, m, "/skill use dual-model")

	if m.pendingSkill != "" {
		t.Errorf("must not arm when no skills loaded; got %q", m.pendingSkill)
	}
	if !strings.Contains(res.text, "no skills loaded") {
		t.Errorf("expected explicit 'no skills loaded', got: %q", res.text)
	}
}

func TestCmdSkillUse_ReplacesExistingArm(t *testing.T) {
	m := emptyModel()
	withSkills(m, "dual-model", "go-test-runner")
	m.pendingSkill = "dual-model"

	res := runHandler(t, m, "/skill use go-test-runner")

	if m.pendingSkill != "go-test-runner" {
		t.Errorf("re-arm should replace existing arm; got %q", m.pendingSkill)
	}
	if !strings.Contains(res.text, "go-test-runner") {
		t.Errorf("confirmation should name the new skill, got: %q", res.text)
	}
}

func TestConsumeArm_WrapsAndClears(t *testing.T) {
	m := emptyModel()
	m.pendingSkill = "dual-model"

	wrapped := m.consumeArm("帮我重构这段代码")

	if m.pendingSkill != "" {
		t.Errorf("consumeArm must clear pendingSkill after use; got %q", m.pendingSkill)
	}
	for _, want := range []string{"dual-model", "帮我重构这段代码", "Please use"} {
		if !strings.Contains(wrapped, want) {
			t.Errorf("wrapped text missing %q: %q", want, wrapped)
		}
	}
}

func TestConsumeArm_NoArmIsNoOp(t *testing.T) {
	m := emptyModel()
	// pendingSkill empty by default

	got := m.consumeArm("hello")
	if got != "hello" {
		t.Errorf("consumeArm with no arm should be identity, got %q", got)
	}
}

func TestCmdNew_ClearsArm(t *testing.T) {
	// /new starts a fresh conversation — any armed skill from the prior
	// session is conceptually obsolete and should not silently apply to
	// the next message in the new session.
	m := emptyModel()
	m.pendingSkill = "dual-model"
	// /new needs RebuildAgent to do its real work; stub it.
	m.opts.RebuildAgent = func() (*agent.Agent, error) { return nil, nil }

	runHandler(t, m, "/new")

	if m.pendingSkill != "" {
		t.Errorf("/new should clear pendingSkill; got %q", m.pendingSkill)
	}
}
