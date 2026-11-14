package skilldistill

// auto_test.go — M-SD2 acceptance: a fake session history drives the
// whole automatic path (funnel → drafts on disk → loader-invisible →
// no duplicate output), plus the timing and concurrency pins the card
// asks for: SessionStart never blocks on the batch, and concurrent
// SessionStarts fire exactly one batch.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillmgr"
	"github.com/whyiyhw/seek/internal/skillstats"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---------- fakes ----------

// fakeAutoSessions is an in-memory SessionReader.
type fakeAutoSessions struct {
	mu        sync.Mutex
	sessions  map[string][]deepseek.Message
	updatedAt map[string]time.Time
}

func newFakeAutoSessions() *fakeAutoSessions {
	return &fakeAutoSessions{
		sessions:  map[string][]deepseek.Message{},
		updatedAt: map[string]time.Time{},
	}
}

func (f *fakeAutoSessions) add(id string, msgs []deepseek.Message, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id] = msgs
	f.updatedAt[id] = at
}

func (f *fakeAutoSessions) ProjectSessions(string) ([]SessionRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var refs []SessionRef
	for id, at := range f.updatedAt {
		refs = append(refs, SessionRef{ID: id, UpdatedAt: at})
	}
	// oldest first, matching session.Store.ListProject's contract
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && refs[j].UpdatedAt.Before(refs[j-1].UpdatedAt); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
	return refs, nil
}

func (f *fakeAutoSessions) Messages(id string) ([]deepseek.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, ok := f.sessions[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return msgs, nil
}

// gatedAutoClient is a ChatClient that distinguishes the extraction
// call from the L3 filter call by system prompt, returns canned
// payloads, counts calls, and can be held at a gate so tests can
// observe the batch being in flight.
type gatedAutoClient struct {
	extractJSON string // raw reasoner payload for extraction calls
	filterJSON  string // raw reasoner payload for L3 calls
	err         error

	extractCalls atomic.Int32
	filterCalls  atomic.Int32

	gate chan struct{} // non-nil → every call blocks until closed
}

func newGatedAutoClient(extract, filter string) *gatedAutoClient {
	return &gatedAutoClient{extractJSON: extract, filterJSON: filter}
}

func (f *gatedAutoClient) withGate() *gatedAutoClient {
	f.gate = make(chan struct{})
	return f
}

func (f *gatedAutoClient) openGate() { close(f.gate) }

func (f *gatedAutoClient) Chat(_ context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	if f.gate != nil {
		<-f.gate
	}
	if f.err != nil {
		return nil, f.err
	}
	content := f.filterJSON
	if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "skill-distillation engine") {
		content = f.extractJSON
		f.extractCalls.Add(1)
	} else {
		f.filterCalls.Add(1)
	}
	return &deepseek.ChatResponse{Choices: []deepseek.Choice{{
		Message: deepseek.Message{Content: content},
	}}}, nil
}

// proceduralHistory builds a session transcript that passes L0 (≥20
// messages, ≥3 tool calls — calls ride on ASSISTANT messages, which is
// what MechanicalGate counts) and contains the evidence quote
// verbatim.
func proceduralHistory(evidence string) []deepseek.Message {
	var h []deepseek.Message
	for i := 0; i < 6; i++ {
		h = append(h, deepseek.Message{Role: deepseek.RoleUser, Content: "turn " + strings.Repeat("x", i+1)})
		h = append(h, deepseek.Message{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{{
			ID:       "c",
			Function: deepseek.ToolCallFunc{Name: "bash", Arguments: "{}"},
		}}})
		h = append(h, deepseek.Message{Role: deepseek.RoleTool, ToolCallID: "c", Content: "tool output " + strings.Repeat("y", i+1)})
		h = append(h, deepseek.Message{Role: deepseek.RoleAssistant, Content: "step " + strings.Repeat("z", i+1) + " verified"})
	}
	h = append(h, deepseek.Message{Role: deepseek.RoleAssistant, Content: "The procedure is: " + evidence})
	return h
}

const autoEvidence = "REPRO: open modal, press esc, observe swallowed key, fix handler deregistration"

var autoExtractOK = `[{"name":"fix-tui-state-bug","description":"When a TUI modal swallows keys after exit, follow this reproduction-first sequence to fix the state machine.","body":"## When to use\nA modal's key handler swallows keys after exit.\n\n## Steps\n1. reproduce\n2. fix deregistration\n\n## Verification\ngo test green","evidence":["` + autoEvidence + `"]}]`

const autoFilterAccept = `{"decision":"ACCEPT","reason":"concrete and repeatable"}`

// autoFixture wires a fully-working AutoHook against fakes.
type autoFixture struct {
	hook     *AutoHook
	client   *gatedAutoClient
	sessions *fakeAutoSessions
	drafts   *skillmgr.DraftStore
	userDir  string
	projDir  string
	statsDir string
	now      time.Time
}

func newAutoFixture(t *testing.T, extract, filter string) *autoFixture {
	t.Helper()
	t.Setenv(EnvAutoSkillDistill, "1")
	f := &autoFixture{
		client:   newGatedAutoClient(extract, filter),
		sessions: newFakeAutoSessions(),
		userDir:  t.TempDir(),
		projDir:  t.TempDir(),
		statsDir: t.TempDir(),
		now:      time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
	}
	f.drafts = &skillmgr.DraftStore{UserDir: f.userDir, Now: func() time.Time { return f.now }}
	f.hook = &AutoHook{
		Distiller:  &Distiller{Client: f.client},
		Drafts:     f.drafts,
		Stats:      skillstats.New(filepath.Join(f.statsDir, "stats.jsonl")),
		Sessions:   f.sessions,
		ProjectID:  "proj-test",
		ProjectDir: f.projDir,
		Now:        func() time.Time { return f.now },
	}
	return f
}

// armDue plants an about-to-fire state (keeping the existing
// watermark — re-arming is what production does cadence cycles later)
// and returns the done channel.
func (f *autoFixture) armDue(t *testing.T) chan struct{} {
	t.Helper()
	state, err := loadAutoState(f.projDir)
	if err != nil {
		t.Fatal(err)
	}
	state.SessionsSinceDistill = autoEverySessions
	if err := saveAutoState(f.projDir, state); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	f.hook.autoDone = done
	return done
}

func (f *autoFixture) waitDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("auto batch did not complete within 2s")
	}
}

// readSidecar loads a draft's .distill.json.
func readSidecar(t *testing.T, drafts *skillmgr.DraftStore, name string) skillmgr.DraftSidecar {
	t.Helper()
	dir, err := drafts.DraftsDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, name, skillmgr.SidecarName))
	if err != nil {
		t.Fatalf("read sidecar %s: %v", name, err)
	}
	var side skillmgr.DraftSidecar
	if err := json.Unmarshal(data, &side); err != nil {
		t.Fatalf("parse sidecar %s: %v", name, err)
	}
	return side
}

// skillsTreeEntries lists the user skills tree root — the "nothing
// installed" assertion (PRD §8 #10): after an auto batch the tree may
// contain .drafts and nothing else.
func skillsTreeEntries(t *testing.T, userDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(userDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// ---------- cadence + env ----------

func TestAutoEnabled_DefaultOff(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv(EnvAutoSkillDistill, v)
		if AutoEnabled() {
			t.Errorf("%q must NOT enable the auto path (default off)", v)
		}
	}
	t.Setenv(EnvAutoSkillDistill, "1")
	if !AutoEnabled() {
		t.Error(`"1" must enable`)
	}
}

func TestAutoDue(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	if autoDue(&AutoState{SessionsSinceDistill: 7}, now) {
		t.Error("7 sessions: not due yet (cap 8)")
	}
	if !autoDue(&AutoState{SessionsSinceDistill: 8}, now) {
		t.Error("8 sessions: due")
	}
	if autoDue(&AutoState{LastDistillAt: now.Add(-6 * 24 * time.Hour)}, now) {
		t.Error("6 days: not due yet (cap 7)")
	}
	if !autoDue(&AutoState{LastDistillAt: now.Add(-7 * 24 * time.Hour)}, now) {
		t.Error("7 days: due")
	}
	if autoDue(&AutoState{}, now) {
		t.Error("fresh install (no watermark, 0 sessions): must NOT fire on day 1")
	}
}

// ---------- the acceptance path ----------

// TestAutoPath_EndToEnd is the card's headline acceptance: fake
// session history → full funnel → weak draft on disk → invisible to
// the loader → nothing installed.
func TestAutoPath_EndToEnd(t *testing.T) {
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept)
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-2*time.Hour))

	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	f.waitDone(t, done)

	// Weak draft landed: recurrence 1, single source, weak status.
	side := readSidecar(t, f.drafts, "fix-tui-state-bug")
	if side.Status != skillmgr.DraftStatusWeak || side.Recurrence != 1 {
		t.Fatalf("first batch: status=%s recurrence=%d, want weak/1", side.Status, side.Recurrence)
	}
	if len(side.Sources) != 1 || side.Sources[0].SessionID != "sess-1" {
		t.Errorf("sources = %+v, want sess-1 only", side.Sources)
	}
	if side.Sources[0].ProjectID != "proj-test" {
		t.Errorf("source project = %q, want proj-test", side.Sources[0].ProjectID)
	}
	if side.Generator == nil || side.Generator.PromptRev != PromptRev {
		t.Errorf("generator provenance missing: %+v", side.Generator)
	}
	// L3 accept is recorded in the trace.
	foundL3 := false
	for _, tr := range side.FilterTrace {
		if tr.Stage == "L3" && tr.Verdict == "ACCEPT" {
			foundL3 = true
		}
	}
	if !foundL3 {
		t.Errorf("L3 ACCEPT missing from filter_trace: %+v", side.FilterTrace)
	}

	// The SKILL.md is a legal package.
	dir, _ := f.drafts.DraftsDir()
	md, err := os.ReadFile(filepath.Join(dir, "fix-tui-state-bug", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Parse(md, "check"); err != nil {
		t.Fatalf("draft SKILL.md must parse: %v", err)
	}

	// Loader-invisible (dot-dir wall) + inbox still empty (weak).
	assertLoaderBlind(t, f.userDir)
	if inbox, _ := f.drafts.ListDrafts(); len(inbox) != 0 {
		t.Fatalf("weak draft must NOT surface in the inbox: %+v", inbox)
	}

	// Telemetry: one provenance row, provider-marked.
	entries, err := skillstats.Read(filepath.Join(f.statsDir, "stats.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("stats rows = %d, want 1", len(entries))
	}
	if e := entries[0]; e.Provider != skillstats.ProviderDistillAuto ||
		e.Name != "fix-tui-state-bug" || e.SessionID != "sess-1" || e.ProjectID != "proj-test" {
		t.Errorf("bad stats row: %+v", e)
	}

	// IRON RULE (PRD §8 #10): the auto path never installs. The skills
	// tree gained .drafts and nothing else.
	names := skillsTreeEntries(t, f.userDir)
	if len(names) != 1 || names[0] != skillmgr.DraftsDirName {
		t.Fatalf("skills tree after auto batch = %v, want only [.drafts] — the auto path must never install", names)
	}

	// State advanced: counter reset, watermark set.
	state, err := loadAutoState(f.projDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionsSinceDistill != 0 || state.LastDistillAt.IsZero() {
		t.Errorf("state after batch = %+v, want counter 0 + watermark set", state)
	}
}

// TestAutoPath_InBatchMerge: two independent sessions inside ONE
// batch window — the second extraction strengthens the first draft
// within the same batch (recurrence 2 → reviewable, both sources).
func TestAutoPath_InBatchMerge(t *testing.T) {
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept)
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-2*time.Hour))
	f.sessions.add("sess-2", proceduralHistory(autoEvidence), f.now.Add(-1*time.Hour))

	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	f.waitDone(t, done)

	side := readSidecar(t, f.drafts, "fix-tui-state-bug")
	if side.Recurrence != 2 || side.Status != skillmgr.DraftStatusReviewable {
		t.Fatalf("same-batch merge: recurrence=%d status=%s, want 2/reviewable", side.Recurrence, side.Status)
	}
	if len(side.Sources) != 2 {
		t.Errorf("sources = %+v, want both sessions", side.Sources)
	}
	if inbox, _ := f.drafts.ListDrafts(); len(inbox) != 1 || inbox[0].Name != "fix-tui-state-bug" {
		t.Fatalf("reviewable draft must surface in the inbox: %+v", inbox)
	}
	assertLoaderBlind(t, f.userDir)
}

// TestAutoPath_SecondBatchRecurrence: recurrence across BATCHES — the
// L4 shape the PRD draws (two cadence cycles; the watermark must keep
// sess-1 out of batch 2).
func TestAutoPath_SecondBatchRecurrence(t *testing.T) {
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept)
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-2*time.Hour))

	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	f.waitDone(t, done)
	if side := readSidecar(t, f.drafts, "fix-tui-state-bug"); side.Recurrence != 1 {
		t.Fatalf("batch 1: recurrence = %d, want 1", side.Recurrence)
	}

	// Time passes; a NEW session lands after batch 1's watermark; the
	// next cadence cycle distills it alone.
	f.now = f.now.Add(2 * time.Hour)
	f.sessions.add("sess-2", proceduralHistory(autoEvidence), f.now.Add(-30*time.Minute))
	done = f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	f.waitDone(t, done)

	side := readSidecar(t, f.drafts, "fix-tui-state-bug")
	if side.Recurrence != 2 || side.Status != skillmgr.DraftStatusReviewable {
		t.Fatalf("batch 2: recurrence=%d status=%s, want 2/reviewable", side.Recurrence, side.Status)
	}
	if len(side.Sources) != 2 || side.Sources[1].SessionID != "sess-2" {
		t.Errorf("sources after batch 2 = %+v", side.Sources)
	}
	// sess-1 must NOT be re-extracted: exactly one extraction per batch.
	if got := f.client.extractCalls.Load(); got != 2 {
		t.Errorf("extract calls = %d, want 2 (one per batch)", got)
	}
	assertLoaderBlind(t, f.userDir)
}

// TestAutoPath_SessionStartDoesNotBlock: the user's first Prompt is
// moments away — the batch must run behind a gated reasoner while
// OnSessionStart has already returned (PRD §8 #9, autoDreamDone
// method).
func TestAutoPath_SessionStartDoesNotBlock(t *testing.T) {
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept)
	f.client.withGate()
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-time.Hour))

	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})

	// OnSessionStart returned while the batch is still parked at the
	// gate — proof by ordering: done cannot be closed until we open.
	select {
	case <-done:
		t.Fatal("batch finished before the gate opened — OnSessionStart blocked on the reasoner")
	default:
	}
	f.client.openGate()
	f.waitDone(t, done)
}

// TestAutoPath_ConcurrentStartsSingleBatch: multiple SessionStarts
// firing together produce exactly ONE batch (card acceptance: 并发安
// 全，不重复产出).
func TestAutoPath_ConcurrentStartsSingleBatch(t *testing.T) {
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept)
	f.client.withGate()
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-time.Hour))

	done := f.armDue(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
		}()
	}
	wg.Wait()
	f.client.openGate()
	f.waitDone(t, done)
	// Give any (buggy) second batch a chance to surface before asserting.
	time.Sleep(50 * time.Millisecond)

	if got := f.client.extractCalls.Load(); got != 1 {
		t.Errorf("extract calls = %d, want exactly 1 (single-flight)", got)
	}
	side := readSidecar(t, f.drafts, "fix-tui-state-bug")
	if side.Recurrence != 1 || len(side.Sources) != 1 {
		t.Fatalf("duplicate production: recurrence=%d sources=%d, want 1/1", side.Recurrence, len(side.Sources))
	}
	entries, _ := skillstats.Read(filepath.Join(f.statsDir, "stats.jsonl"))
	if len(entries) != 1 {
		t.Errorf("stats rows = %d, want 1", len(entries))
	}
}

// TestAutoPath_FilterRejectProducesNothing: L3 is mandatory on the
// auto path — a REJECT (or an unreachable filter) yields no draft and
// no crash.
func TestAutoPath_FilterRejectProducesNothing(t *testing.T) {
	for name, filter := range map[string]string{
		"reject verdict": `{"decision":"REJECT","reason":"generic knowledge"}`,
		"broken filter":  `not json at all`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newAutoFixture(t, autoExtractOK, filter)
			f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-time.Hour))
			done := f.armDue(t)
			f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
			f.waitDone(t, done)
			if all, _ := f.drafts.ListAllDrafts(); len(all) != 0 {
				t.Fatalf("no draft may land when L3 doesn't accept: %+v", all)
			}
		})
	}
}

// TestAutoPath_MalformedExtractSilentSkip: reasoner garbage (PRD §8
// #12) — the auto path logs-and-skips, never crashes, never writes.
func TestAutoPath_MalformedExtractSilentSkip(t *testing.T) {
	f := newAutoFixture(t, "I found nothing worth keeping.", autoFilterAccept)
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-time.Hour))
	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{}) // must not panic
	f.waitDone(t, done)
	if all, _ := f.drafts.ListAllDrafts(); len(all) != 0 {
		t.Fatalf("malformed extraction must not write drafts: %+v", all)
	}
}

// TestAutoPath_DisabledByDefault: with the env unset nothing happens —
// no state file, no reasoner call.
func TestAutoPath_DisabledByDefault(t *testing.T) {
	t.Setenv(EnvAutoSkillDistill, "")
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept) // sets env back on…
	t.Setenv(EnvAutoSkillDistill, "")                       // …so re-unset here
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-time.Hour))
	f.hook.autoDone = make(chan struct{})
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	select {
	case <-f.hook.autoDone:
		t.Fatal("no batch may run when the switch is off")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(f.projDir, "skill-distill-state.json")); !os.IsNotExist(err) {
		t.Error("state file must not be created while disabled")
	}
}

// TestAutoPath_GCAgedDraftsRemoved: the batch's deferred GC clears
// over-aged drafts even when the funnel yields nothing new.
func TestAutoPath_GCAgedDraftsRemoved(t *testing.T) {
	f := newAutoFixture(t, "[]", autoFilterAccept) // extraction: empty
	// Plant an ancient weak draft: WriteDraft stamps CreatedAt/UpdatedAt
	// from the store's Now, so wind the fixture clock back 15 days for
	// the write, then forward again for the batch.
	f.now = f.now.Add(-15 * 24 * time.Hour)
	old := skillmgr.DraftSidecar{Status: skillmgr.DraftStatusWeak, Recurrence: 1}
	if _, err := f.drafts.WriteDraft([]byte("---\nname: ancient-draft\ndescription: |\n  old one\n---\nbody"), old); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(15 * 24 * time.Hour)
	f.sessions.add("sess-1", proceduralHistory(autoEvidence), f.now.Add(-time.Hour))
	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	f.waitDone(t, done)
	if all, _ := f.drafts.ListAllDrafts(); len(all) != 0 {
		t.Fatalf("15-day-old draft must be GC'd by the batch: %+v", all)
	}
}

// TestAutoPath_L0SkipsQASessions: a pure-Q&A session never reaches
// the reasoner (zero API for non-procedural history).
func TestAutoPath_L0SkipsQASessions(t *testing.T) {
	f := newAutoFixture(t, autoExtractOK, autoFilterAccept)
	var qa []deepseek.Message
	for i := 0; i < 30; i++ {
		qa = append(qa, deepseek.Message{Role: deepseek.RoleUser, Content: "q"})
		qa = append(qa, deepseek.Message{Role: deepseek.RoleAssistant, Content: "a"})
	}
	f.sessions.add("sess-qa", qa, f.now.Add(-time.Hour))
	done := f.armDue(t)
	f.hook.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	f.waitDone(t, done)
	if f.client.extractCalls.Load() != 0 {
		t.Error("L0 must gate pure-Q&A sessions before any API call")
	}
	if all, _ := f.drafts.ListAllDrafts(); len(all) != 0 {
		t.Errorf("Q&A session must yield no drafts: %+v", all)
	}
}

// assertLoaderBlind: with drafts present, skill.Load over the tree
// must produce exactly the same (empty here) set as without them —
// the dot-dir wall (draft_test.go pins the non-empty case too).
func assertLoaderBlind(t *testing.T, userDir string) {
	t.Helper()
	set, _, err := skill.Load(skill.LoadOptions{UserSkillsDir: userDir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if set.Get("fix-tui-state-bug") != nil {
		t.Fatal("draft leaked into the loaded skill set — the review gate is broken")
	}
	if strings.Contains(set.Manifest(), "fix-tui-state-bug") {
		t.Fatal("draft leaked into the manifest")
	}
}
