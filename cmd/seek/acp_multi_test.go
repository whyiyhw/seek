package main

// ACP·P1-2 (card c_5flg7r, backlog ③) — one seek process serving N ACP
// sessions. In-process tests against the REAL acpBackend (agent + SSE
// upstream + session store): the four per-session axes (cwd anchor,
// permission workflow/grants, history stack, model lane), turn-gate
// semantics, and the resource surface. The real-binary wire shape
// (two sessions crossing on one stdio connection) is
// acp_multi_e2e_test.go; this file owns the state machine.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// multiSSE is the canned upstream: every request answers one "ack" chunk
// and stops. No tool calls — these tests exercise the session state
// machine, not the tool surface (the e2e file covers tools).
func multiSSE(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"ack"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// multiBackend wires an acpBackend like loadTestBackend, but with a live
// (canned) upstream so Prompt completes real turns, plus the switch-test
// menu/laneFor so the lane axis is exercisable.
func multiBackend(t *testing.T, srvURL string) *acpBackend {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", filepath.Join(dir, "sessions"))
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	pol, err := permission.New(old, permission.PrefAsk)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srvURL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &acpBackend{
		ag:     ag,
		policy: pol,
		menu:   acpTestMenu(),
		sess:   session.New(deepseek.ModelV41Flash, old, "sys", false, false),
		store:  store,
		laneFor: func(provider string) (llm.Provider, *deepseek.Client, string, error) {
			switch provider {
			case "deepseek":
				// Pointed at the canned upstream — a lane swap must not
				// silently reach the real API from a test.
				return nil, deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srvURL)), deepseek.ModelV41Flash, nil
			case "glm":
				return stubLaneProvider{"GLM"}, nil, "glm-5.3", nil
			}
			return nil, nil, "", fmt.Errorf("unknown provider %s", provider)
		},
	}
}

// multiDir is a symlink-resolved temp dir (activation chdirs into it;
// os.Getwd resolves symlinks, so the assertions compare resolved paths).
func multiDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// runPrompt drives one turn synchronously and requires end_turn.
func runPrompt(t *testing.T, b *acpBackend, sid, text string) {
	t.Helper()
	res, err := b.Prompt(context.Background(), acp.PromptParams{
		SessionID: sid,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: text}},
	}, func(acp.SessionUpdate) {})
	if err != nil {
		t.Fatalf("prompt %q: %v", text, err)
	}
	if res.StopReason != acp.StopEndTurn {
		t.Fatalf("prompt %q stopReason = %q, want end_turn", text, res.StopReason)
	}
}

// userTexts lists the user-role contents recorded in a session entry's
// history (the per-session history stack, post-turn). The agent appends
// its per-message workflow reminders to user text, so assertions match
// on Contains, not equality.
func userTexts(s *session.Session) []string {
	var out []string
	for _, m := range s.Messages {
		if m.Role == deepseek.RoleUser {
			out = append(out, m.Content)
		}
	}
	return out
}

// hasUser reports whether the history stack carries a user message
// STARTING with text (before any appended reminder).
func hasUser(s *session.Session, text string) bool {
	for _, u := range userTexts(s) {
		if strings.HasPrefix(u, text) {
			return true
		}
	}
	return false
}

var durableIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{16}$`)

// ① basics: the SECOND session/new mints a NEW durable session instead
// of re-anchoring the live one — distinct ids, both table entries, the
// first session keeps the startup transcript id (pre-P1-2 behaviour).
func TestACPBackend_SecondNewMintsNewSession(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dirA, dirB := multiDir(t), multiDir(t)

	resA, err := b.NewSession(acp.NewSessionParams{Cwd: dirA})
	if err != nil {
		t.Fatal(err)
	}
	if resA.SessionID != b.sess.ID {
		t.Fatalf("first session id = %q, want the startup transcript id %q", resA.SessionID, b.sess.ID)
	}
	resB, err := b.NewSession(acp.NewSessionParams{Cwd: dirB})
	if err != nil {
		t.Fatal(err)
	}
	if resB.SessionID == resA.SessionID {
		t.Fatalf("second session/new returned the SAME id %q — must mint a new session", resB.SessionID)
	}
	if !durableIDRe.MatchString(resB.SessionID) {
		t.Fatalf("second session id %q is not a durable transcript id", resB.SessionID)
	}
	if len(b.sessions) != 2 {
		t.Fatalf("session table has %d entries, want 2", len(b.sessions))
	}
	// The first session stays the installed one until a turn switches.
	b.mu.Lock()
	installed := b.installed
	b.mu.Unlock()
	if installed == nil || installed.id != resA.SessionID {
		t.Fatalf("installed session = %+v, want the first session", installed)
	}
	// Second new is record-only: the live cwd/policy still serve A.
	if got := b.policy.Cwd(); got != dirA {
		t.Fatalf("policy cwd after second new = %q, want A's %q", got, dirA)
	}
	// Bad cwd on a later session still fails loud (validated, just not
	// anchored).
	if _, err := b.NewSession(acp.NewSessionParams{Cwd: filepath.Join(dirB, "no-such-dir")}); err == nil {
		t.Fatal("session/new with a bad cwd must fail loud even as a later session")
	}
}

// ① the four axes, one story: interleave turns across two sessions and
// watch each axis follow ITS session.
func TestACPBackend_MultiSession_FourAxesIndependent(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dirA, dirB := multiDir(t), multiDir(t)

	resA, _ := b.NewSession(acp.NewSessionParams{Cwd: dirA})
	resB, _ := b.NewSession(acp.NewSessionParams{Cwd: dirB})
	sA, sB := resA.SessionID, resB.SessionID

	// --- cwd axis ---------------------------------------------------
	runPrompt(t, b, sA, "alpha-one")
	if got := b.policy.Cwd(); got != dirA {
		t.Fatalf("policy cwd after A's turn = %q, want %q", got, dirA)
	}
	runPrompt(t, b, sB, "beta-one")
	if got := b.policy.Cwd(); got != dirB {
		t.Fatalf("policy cwd after B's turn = %q, want %q (activation must re-anchor)", got, dirB)
	}
	if got, _ := os.Getwd(); got != dirB {
		t.Fatalf("process cwd after B's turn = %q, want %q", got, dirB)
	}
	runPrompt(t, b, sA, "alpha-two")
	if got, _ := os.Getwd(); got != dirA {
		t.Fatalf("process cwd after A's turn = %q, want %q (switch back)", got, dirA)
	}

	// --- history axis ------------------------------------------------
	ta := userTexts(b.sessions[sA].sess)
	if len(ta) != 2 || !strings.HasPrefix(ta[0], "alpha-one") || !strings.HasPrefix(ta[1], "alpha-two") {
		t.Fatalf("A's history = %q, want [alpha-one alpha-two]", ta)
	}
	tb := userTexts(b.sessions[sB].sess)
	if len(tb) != 1 || !strings.HasPrefix(tb[0], "beta-one") {
		t.Fatalf("B's history = %q, want [beta-one] — B must not see A's turns", tb)
	}

	// --- permission axis (grants + workflow) --------------------------
	// A grant given while A is the live conversation rides A's entry at
	// its next turn end (the approval lane grants mid-turn; between
	// turns the register belongs to the installed session).
	b.policy.SetAlwaysAllow(permission.KindEdit)
	runPrompt(t, b, sA, "alpha-three")
	if got := b.sessions[sA].grants; len(got) != 1 || got[0] != permission.KindEdit {
		t.Fatalf("A's grants = %v, want [edit] (pull-back at turn end)", got)
	}
	runPrompt(t, b, sB, "beta-two")
	if b.policy.AlwaysAllowed(permission.KindEdit) {
		t.Fatal("B's turn must not inherit A's always-allow grant")
	}
	runPrompt(t, b, sA, "alpha-four")
	if !b.policy.AlwaysAllowed(permission.KindEdit) {
		t.Fatal("A's grant must restore on re-activation")
	}
	// Workflow: set_mode on a backgrounded session records, does not
	// flip the live policy.
	if err := b.SetMode(acp.SetModeParams{SessionID: sB, ModeID: acp.ModePlan}); err != nil {
		t.Fatal(err)
	}
	if b.policy.Workflow() != permission.WorkflowNone {
		t.Fatal("set_mode on a backgrounded session must not flip the live policy")
	}
	if b.sessions[sB].workflow != permission.WorkflowPlanAnalyze {
		t.Fatal("set_mode on a backgrounded session must record its workflow")
	}
	runPrompt(t, b, sB, "beta-three")
	if b.policy.Workflow() != permission.WorkflowPlanAnalyze {
		t.Fatal("B's activation must install B's plan workflow")
	}
	runPrompt(t, b, sA, "alpha-five")
	if b.policy.Workflow() != permission.WorkflowNone {
		t.Fatal("A's activation must restore A's default workflow")
	}

	// --- lane axis ----------------------------------------------------
	// B (backgrounded — A served the last turn) picks glm: record-only,
	// the live agent serving A must NOT swap.
	res, err := b.SetConfigOption(acp.SetConfigOptionParams{
		SessionID: sB, ConfigID: "model", Value: "glm:glm-5.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ConfigOptions) != 1 || res.ConfigOptions[0].CurrentValue != "glm:glm-5.3" {
		t.Fatalf("set_config response = %+v, want B's pick echoed", res.ConfigOptions)
	}
	if got := b.ag.Model(); got != deepseek.ModelV41Flash {
		t.Fatalf("agent model after a backgrounded session's pick = %q, want A's lane untouched", got)
	}
	if b.menu[0].CurrentValue != "deepseek:deepseek-flash" {
		t.Fatalf("shared menu mutated in place: %q — sessions must serve copies", b.menu[0].CurrentValue)
	}
	// A (installed) picks glm and back: the live swap still works,
	// exactly the single-session behaviour.
	for _, v := range []string{"glm:glm-5.3", "deepseek:deepseek-flash"} {
		if _, err := b.SetConfigOption(acp.SetConfigOptionParams{
			SessionID: sA, ConfigID: "model", Value: v,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.ag.Model(); got != deepseek.ModelV41Flash {
		t.Fatalf("agent model after A's pick-and-revert = %q, want flash", got)
	}
	// A's turn keeps its lane; B's recorded pick survives A's turns.
	runPrompt(t, b, sA, "alpha-six")
	if got := b.ag.Model(); got != deepseek.ModelV41Flash {
		t.Fatalf("agent model after A's turn = %q, want A's deepseek-flash", got)
	}
	if b.sessions[sB].sess.Model != "glm-5.3" {
		t.Fatalf("B's recorded model = %q, want glm-5.3 (pick must survive A's turns)", b.sessions[sB].sess.Model)
	}
}

// ② two prompts CONCURRENTLY in flight (both registered, turns gated):
// both complete, each lands in its own history. The -race detector in
// CI is the second half of this test (the gate + table are the shared
// state it watches).
func TestACPBackend_MultiSession_ConcurrentPromptsNoCrossTalk(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dirA, dirB := multiDir(t), multiDir(t)
	resA, _ := b.NewSession(acp.NewSessionParams{Cwd: dirA})
	resB, _ := b.NewSession(acp.NewSessionParams{Cwd: dirB})

	var wg sync.WaitGroup
	results := make([]acp.PromptResult, 2)
	errs := make([]error, 2)
	for i, sid := range []string{resA.SessionID, resB.SessionID} {
		wg.Add(1)
		go func(i int, sid, text string) {
			defer wg.Done()
			results[i], errs[i] = b.Prompt(context.Background(), acp.PromptParams{
				SessionID: sid,
				Prompt:    []acp.ContentBlock{{Type: "text", Text: text}},
			}, func(acp.SessionUpdate) {})
		}(i, sid, fmt.Sprintf("concurrent-%d", i))
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent prompt %d: %v", i, err)
		}
		if results[i].StopReason != acp.StopEndTurn {
			t.Fatalf("concurrent prompt %d stopReason = %q", i, results[i].StopReason)
		}
	}
	ta := userTexts(b.sessions[resA.SessionID].sess)
	tb := userTexts(b.sessions[resB.SessionID].sess)
	if len(ta) != 1 || !strings.HasPrefix(ta[0], "concurrent-0") {
		t.Fatalf("A's history after concurrent turns = %q", ta)
	}
	if len(tb) != 1 || !strings.HasPrefix(tb[0], "concurrent-1") {
		t.Fatalf("B's history after concurrent turns = %q", tb)
	}
}

// ② the queued half: a prompt waiting on the turn gate must leave
// CLEANLY (cancelled + exactly one termination frame) when its ctx dies
// — a Mutex could not do this, which is why the gate is a channel.
func TestACPBackend_MultiSession_QueuedPromptCancelsCleanly(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dirA, dirB := multiDir(t), multiDir(t)
	_, _ = b.NewSession(acp.NewSessionParams{Cwd: dirA})
	resB, _ := b.NewSession(acp.NewSessionParams{Cwd: dirB})

	// Hold the gate: simulate A's long turn.
	gate := b.turnGate()
	gate <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	var (
		frames []acp.SessionUpdate
		mu     sync.Mutex
		wg     sync.WaitGroup
		res    acp.PromptResult
		err    error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err = b.Prompt(ctx, acp.PromptParams{
			SessionID: resB.SessionID,
			Prompt:    []acp.ContentBlock{{Type: "text", Text: "queued-then-cancelled"}},
		}, func(u acp.SessionUpdate) {
			mu.Lock()
			frames = append(frames, u)
			mu.Unlock()
		})
	}()
	time.Sleep(100 * time.Millisecond) // let it queue on the gate
	cancel()
	wg.Wait()
	if err != nil {
		t.Fatalf("queued-cancel prompt errored: %v", err)
	}
	if res.StopReason != acp.StopCancelled {
		t.Fatalf("stopReason = %q, want cancelled", res.StopReason)
	}
	mu.Lock()
	defer mu.Unlock()
	stops := 0
	for _, u := range frames {
		if m, ok := u.Update.(acp.TurnStopUpdate); ok {
			stops++
			if m.StopReason != acp.FrameStopCancelled {
				t.Fatalf("frame stopReason = %q, want cancelled", m.StopReason)
			}
		}
	}
	if stops != 1 {
		t.Fatalf("termination frames = %d, want exactly 1", stops)
	}
	<-gate // release for the cleanup chdir-back
}

// ② the data-race half the round-1 suite missed: session/new is served
// INLINE in the read loop by design (a client opening a second panel
// while another conversation thinks must not queue behind that turn),
// so NewSession reads the shared session slot with neither the turn
// gate nor — before the mu fix — any lock, while the turn goroutine
// writes the same slot (activate's whole-struct write on switch,
// saveTurn's field writes per TurnEnd, deactivate's pull-back read).
// That is a Go memory-model data race — a torn string header on Model
// is real possible damage — and it was invisible to the round-1 tests
// because every one of them called session/new while idle, so the
// accesses were ordered by plain program order. -race's vector clocks
// don't need wall-clock overlap: with no happens-before edge between
// the two goroutines' slot accesses, the pair is reported however far
// apart in time it executes. The turn goroutine here cycles
// activate/saveTurn/deactivate under the gate while the test goroutine
// mints sessions as fast as it can.
func TestACPBackend_MultiSession_NewDuringTurnFreshBranchNoRace(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dirA, dirB, dirC := multiDir(t), multiDir(t), multiDir(t)
	resA, _ := b.NewSession(acp.NewSessionParams{Cwd: dirA})
	resB, _ := b.NewSession(acp.NewSessionParams{Cwd: dirB})
	stB := b.sessions[resB.SessionID] // captured before concurrency starts

	done := make(chan struct{})
	go func() {
		// The turn goroutine: holds the gate (a real turn in flight),
		// then cycles the three slot-touching phases of a switching
		// turn. activate on an already-installed entry re-runs the
		// anchor/lane machinery — harmless, and it keeps the slot write
		// in the loop.
		defer close(done)
		gate := b.turnGate()
		gate <- struct{}{}
		defer func() { <-gate }()
		for i := 0; i < 200; i++ {
			if err := b.activate(stB); err != nil {
				t.Errorf("activate: %v", err)
				return
			}
			b.saveTurn()
			b.deactivate(stB)
		}
	}()
	defer func() { <-done }() // join on every exit path (done is closed)
	// The read loop's session/new: mints fresh sessions (the template
	// reads in the fresh branch) until the turn goroutine finishes. The
	// cap only bounds a slow-disk run: -race needs no wall-clock
	// overlap, one unordered pair per side suffices.
	minted := 0
	for minted < 5000 {
		select {
		case <-done:
		default:
			if _, err := b.NewSession(acp.NewSessionParams{Cwd: dirC}); err != nil {
				t.Fatal(err)
			}
			minted++
			continue
		}
		break
	}
	if minted == 0 {
		t.Fatal("session/new never ran alongside the turn's slot writes")
	}
	// Sanity: the crossing left both original sessions intact.
	if resA.SessionID == "" || b.sessions[resB.SessionID] != stB {
		t.Fatal("the original session entries were disturbed by the crossing")
	}
}

// The first-branch sibling of the race above: a turn running with NO
// session table yet (the prompt-falls-back-to-startup shape — the
// acp-smoke-test "smoke" probe prompts without a session/new) while the
// FIRST session/new arrives. The first branch WRITES the live slot (the
// CWD anchor) and copies the whole struct — against the turn's saveTurn
// writes. Same mu discipline, same -race verdict.
func TestACPBackend_MultiSession_NewDuringTurnFirstBranchNoRace(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dir := multiDir(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		gate := b.turnGate()
		gate <- struct{}{}
		defer func() { <-gate }()
		for i := 0; i < 200; i++ {
			b.saveTurn() // the turn goroutine's slot writes
		}
	}()
	defer func() { <-done }() // join on every exit path (done is closed)
	res, err := b.NewSession(acp.NewSessionParams{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	<-done // ordered join: the assertions below read post-turn state
	// First-branch semantics survive the fix: the startup transcript's
	// id is the session id and the entry is installed.
	if res.SessionID != b.sess.ID {
		t.Fatalf("first session id = %q, want the startup transcript id %q", res.SessionID, b.sess.ID)
	}
	b.mu.Lock()
	installed := b.installed
	b.mu.Unlock()
	if installed == nil || installed.id != res.SessionID {
		t.Fatalf("installed = %+v, want the first session", installed)
	}
}

// ④ the resource surface: sessions live for the process lifetime BY
// DESIGN (ACP v1 has no session/delete; seek does not advertise one),
// so "session end" is the process ending — what must NOT leak is
// per-TURN state: N sessions × M interleaved turns leave the goroutine
// count back at baseline, the table at exactly N entries, and exactly
// one installed session.
func TestACPBackend_MultiSession_TurnLeaktest(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)

	const n = 4
	dirs := make([]string, n)
	sids := make([]string, n)
	for i := range dirs {
		dirs[i] = multiDir(t)
		res, err := b.NewSession(acp.NewSessionParams{Cwd: dirs[i]})
		if err != nil {
			t.Fatal(err)
		}
		sids[i] = res.SessionID
	}
	// Warm one turn so lazily-created machinery (upstream conns, the
	// turn gate) exists before the baseline is taken.
	runPrompt(t, b, sids[0], "warm")

	baseline := runtime.NumGoroutine()
	for round := 0; round < 3; round++ {
		for i, sid := range sids {
			runPrompt(t, b, sid, fmt.Sprintf("round-%d-session-%d", round, i))
		}
	}
	// Give in-flight I/O goroutines a moment to unwind.
	time.Sleep(250 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > baseline+2 {
		t.Fatalf("goroutines %d → %d after %d turns — per-turn state is leaking", baseline, after, 3*n)
	}
	if len(b.sessions) != n {
		t.Fatalf("session table = %d entries after turns, want exactly %d (turns must not grow it)", len(b.sessions), n)
	}
	b.mu.Lock()
	installed := b.installed
	b.mu.Unlock()
	if installed == nil {
		t.Fatal("exactly one session must remain installed")
	}
	// And every session's history is intact after the interleaving.
	for i, sid := range sids {
		want := fmt.Sprintf("round-2-session-%d", i)
		if !strings.Contains(strings.Join(userTexts(b.sessions[sid].sess), "\n"), want) {
			t.Fatalf("session %d lost its last turn", i)
		}
	}
}

// session/load in the multi-session world: loading installs the loaded
// conversation (same semantics as before) AND records it in the table —
// a subsequent prompt continues it, and the session it displaced keeps
// its own state.
func TestACPBackend_MultiSession_LoadInstallsAndRecords(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	dirA, dirB := multiDir(t), multiDir(t)
	resA, _ := b.NewSession(acp.NewSessionParams{Cwd: dirA})
	runPrompt(t, b, resA.SessionID, "alpha-before-load")
	sidA := resA.SessionID

	// A second session exists; load targets a STORED transcript.
	stored := session.New(deepseek.ModelV41Flash, dirB, "sys", false, false)
	stored.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "stored question"},
		{Role: deepseek.RoleAssistant, Content: "stored answer"},
	}
	if err := b.store.Save(stored); err != nil {
		t.Fatal(err)
	}
	var replayed []acp.SessionUpdate
	if _, err := b.LoadSession(acp.LoadSessionParams{SessionID: stored.ID, Cwd: dirB}, func(u acp.SessionUpdate) {
		replayed = append(replayed, u)
	}); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replay = %d updates, want 2", len(replayed))
	}
	// Loaded conversation is installed and recorded.
	b.mu.Lock()
	inst := b.installed
	b.mu.Unlock()
	if inst == nil || inst.id != stored.ID {
		t.Fatalf("installed = %+v, want the loaded %q", inst, stored.ID)
	}
	// A continues where it left off — its own history, its own cwd.
	runPrompt(t, b, sidA, "alpha-after-load")
	if !hasUser(b.sessions[sidA].sess, "alpha-after-load") {
		t.Fatalf("A's history after load+continue = %q", userTexts(b.sessions[sidA].sess))
	}
	if got := b.policy.Cwd(); got != dirA {
		t.Fatalf("policy cwd after A's turn = %q, want %q", got, dirA)
	}
	// The loaded session still resumes with its stored history.
	runPrompt(t, b, stored.ID, "stored-continue")
	if !strings.Contains(strings.Join(userTexts(b.sessions[stored.ID].sess), "\n"), "stored question") {
		t.Fatal("loaded session lost its stored history")
	}
}

// Unknown session ids ride the installed session (pre-P1-2 semantics —
// acp-smoke-test.sh's "smoke" probe depends on it), and set_config /
// set_mode still reject ids that are neither live nor installed.
func TestACPBackend_MultiSession_UnknownIDFallsBackToInstalled(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	resA, _ := b.NewSession(acp.NewSessionParams{Cwd: multiDir(t)})

	runPrompt(t, b, "bogus-id", "ghost turn")
	if !hasUser(b.sessions[resA.SessionID].sess, "ghost turn") {
		t.Fatalf("ghost turn must land on the installed session, got %q", userTexts(b.sessions[resA.SessionID].sess))
	}
	if _, err := b.SetConfigOption(acp.SetConfigOptionParams{SessionID: "bogus-id", ConfigID: "model", Value: "glm:glm-5.3"}); err == nil {
		t.Fatal("set_config_option for an unknown id must be rejected")
	}
	if err := b.SetMode(acp.SetModeParams{SessionID: "bogus-id", ModeID: acp.ModePlan}); err == nil {
		t.Fatal("set_mode for an unknown id must be rejected")
	}
}

// saveTurn per session: each turn writes ITS session's transcript —
// both files exist afterwards with their own first user message.
func TestACPBackend_MultiSession_PerSessionTranscripts(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	resA, _ := b.NewSession(acp.NewSessionParams{Cwd: multiDir(t)})
	resB, _ := b.NewSession(acp.NewSessionParams{Cwd: multiDir(t)})
	runPrompt(t, b, resA.SessionID, "transcript-alpha")
	runPrompt(t, b, resB.SessionID, "transcript-beta")

	for sid, want := range map[string]string{
		resA.SessionID: "transcript-alpha",
		resB.SessionID: "transcript-beta",
	} {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("SEEK_SESSIONS_DIR"), sid+".jsonl"))
		if err != nil {
			t.Fatalf("transcript for %s: %v", sid, err)
		}
		if !strings.Contains(string(raw), want) {
			t.Fatalf("transcript %s lacks %q:\n%s", sid, want, raw)
		}
		var hdr struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw[:strings.IndexByte(string(raw), '\n')], &hdr); err != nil || hdr.ID != sid {
			t.Fatalf("transcript %s header id mismatch: %v %+v", sid, err, hdr)
		}
	}
}

// --no-save cross-session switch: a nil session object means "fresh
// history", not "whatever the previous session left in the agent" — the
// lane is CLI-unreachable (flags don't parse past "acp") but the backend
// half must still be sound.
func TestACPBackend_MultiSession_NoSaveSwitchResetsHistory(t *testing.T) {
	b := multiBackend(t, multiSSE(t).URL)
	b.sess = nil
	b.store = nil
	dirA, dirB := multiDir(t), multiDir(t)
	resA, err := b.NewSession(acp.NewSessionParams{Cwd: dirA})
	if err != nil {
		t.Fatal(err)
	}
	resB, err := b.NewSession(acp.NewSessionParams{Cwd: dirB})
	if err != nil {
		t.Fatal(err)
	}
	if resA.SessionID == resB.SessionID {
		t.Fatalf("--no-save ids collapsed: %q", resA.SessionID)
	}
	runPrompt(t, b, resA.SessionID, "nosave-alpha")
	runPrompt(t, b, resB.SessionID, "nosave-beta")
	var saw string
	for _, m := range b.ag.Messages() {
		if m.Role == deepseek.RoleUser {
			saw += m.Content + "\n"
		}
	}
	if strings.Contains(saw, "nosave-alpha") {
		t.Fatalf("B's turn saw A's history after a --no-save switch:\n%s", saw)
	}
	if !strings.Contains(saw, "nosave-beta") {
		t.Fatalf("B's own turn missing from the agent:\n%s", saw)
	}
}
