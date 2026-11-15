package subagent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// transcriptRunner is a stub Runner that mirrors the one behaviour of
// the production Runner (cmd/seek buildSubagentRunner) that
// continuation depends on: persisting transcript.jsonl into
// job.SessionDir after a successful run, with the conversation being
// the seeded PriorMessages (continuations) plus this run's user
// prompt and assistant reply. Fresh spawns write a two-message
// transcript; continuations write prior + two more — exactly the
// self-contained-growth property the production writer has.
func transcriptRunner(t *testing.T) Runner {
	t.Helper()
	return func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		msgs := make([]deepseek.Message, 0, len(job.PriorMessages)+2)
		msgs = append(msgs, job.PriorMessages...)
		msgs = append(msgs,
			deepseek.Message{Role: deepseek.RoleUser, Content: job.UserPrompt},
			deepseek.Message{Role: deepseek.RoleAssistant, Content: "summary for " + job.UserPrompt},
		)
		sess := session.New("test-model", job.Policy.Cwd(), job.SystemPrompt, true, false)
		sess.ID = job.SubSid
		sess.Messages = msgs
		if err := session.SaveTo(filepath.Join(job.SessionDir, "transcript.jsonl"), sess); err != nil {
			t.Errorf("stub runner: save transcript %s: %v", job.SubSid, err)
		}
		return RunnerResult{
			Summary: "summary for " + job.UserPrompt,
			Tokens:  Tokens{Prompt: 100, Completion: 10, CacheHit: 0},
			Turns:   1,
		}, nil
	}
}

// TestContinue_Lifecycle is the acceptance spine: spawn → complete →
// continue → second completion, with the continuation's context
// seeded from the first run's transcript.
func TestContinue_Lifecycle(t *testing.T) {
	mgr := newManager(t, transcriptRunner(t))

	// 1. Fresh spawn completes and leaves a transcript.
	res1 := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "research adapters",
		Prompt:      "survey the adapter layer",
		Type:        TypeExplore,
		ParentTurn:  1,
	})
	if res1.Status != StatusCompleted {
		t.Fatalf("spawn status = %s, want completed (summary: %s)", res1.Status, res1.Summary)
	}

	// 2. Continue it: new sub_sid, completed, lineage footer.
	res2 := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid:  res1.SubSid,
		Description: "edge cases follow-up",
		Prompt:      "now audit the edge cases",
		ParentTurn:  2,
	})
	if res2.Status != StatusCompleted {
		t.Fatalf("continue status = %s, want completed (summary: %s)", res2.Status, res2.Summary)
	}
	if res2.SubSid == res1.SubSid {
		t.Fatalf("continuation reused sub_sid %s — one sub_sid must be one lifecycle", res1.SubSid)
	}
	if !strings.HasPrefix(res2.Summary, "[agent: completed] ") {
		t.Errorf("continuation summary missing completed prefix:\n%s", res2.Summary)
	}
	if !strings.Contains(res2.Summary, "resumed-from: "+shortSid(res1.SubSid)) {
		t.Errorf("continuation summary missing resumed-from lineage footer:\n%s", res2.Summary)
	}
	if !strings.Contains(res2.Summary, "summary for now audit the edge cases") {
		t.Errorf("continuation summary missing body:\n%s", res2.Summary)
	}

	// 3. Context continuity: the continuation's transcript contains
	// the first run's conversation verbatim, then the follow-up.
	contSess := loadSubTranscript(t, mgr, res2.SubSid)
	if len(contSess.Messages) != 4 {
		t.Fatalf("continuation transcript has %d messages, want 4 (2 prior + follow-up + reply)", len(contSess.Messages))
	}
	if contSess.Messages[0].Content != "survey the adapter layer" {
		t.Errorf("seed[0] = %q, want first run's user prompt", contSess.Messages[0].Content)
	}
	if contSess.Messages[1].Content != "summary for survey the adapter layer" {
		t.Errorf("seed[1] = %q, want first run's assistant reply", contSess.Messages[1].Content)
	}
	if contSess.Messages[2].Content != "now audit the edge cases" {
		t.Errorf("seed[2] = %q, want follow-up instruction", contSess.Messages[2].Content)
	}

	// 4. Index fold: two records, lineage recorded, prior untouched.
	recs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("List len = %d, want 2", len(recs))
	}
	var contRec, firstRec *Subagent
	for i := range recs {
		switch recs[i].SubSid {
		case res1.SubSid:
			firstRec = &recs[i]
		case res2.SubSid:
			contRec = &recs[i]
		}
	}
	if firstRec == nil || contRec == nil {
		t.Fatalf("fold missing records: first=%v cont=%v", firstRec, contRec)
	}
	if contRec.ResumedFrom != res1.SubSid {
		t.Errorf("fold ResumedFrom = %q, want %q", contRec.ResumedFrom, res1.SubSid)
	}
	if contRec.Type != TypeExplore {
		t.Errorf("continuation type = %q, want inherited %q", contRec.Type, TypeExplore)
	}
	if contRec.Status != StatusCompleted || firstRec.Status != StatusCompleted {
		t.Errorf("statuses = %s / %s, want completed/completed", contRec.Status, firstRec.Status)
	}
	if firstRec.ResumedFrom != "" {
		t.Errorf("fresh spawn folded with ResumedFrom=%q, want empty", firstRec.ResumedFrom)
	}

	// 5. The chain continues: the NEW sub-sid is itself continuable
	// (二次完成后再续), seeds grow by exactly this run's two messages.
	res3 := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid:  res2.SubSid,
		Description: "third pass",
		Prompt:      "summarise for the parent",
	})
	if res3.Status != StatusCompleted {
		t.Fatalf("second continue status = %s (summary: %s)", res3.Status, res3.Summary)
	}
	third := loadSubTranscript(t, mgr, res3.SubSid)
	if len(third.Messages) != 6 {
		t.Fatalf("chained transcript has %d messages, want 6", len(third.Messages))
	}
}

// TestContinue_UnknownSid: a session id with no index record fails
// before any run with a clear unknown_session error naming the id.
func TestContinue_UnknownSid(t *testing.T) {
	mgr := newManager(t, transcriptRunner(t))
	res := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid:  "20990101-000000-zzzzzz",
		Description: "follow-up",
		Prompt:      "continue",
	})
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if !strings.HasPrefix(res.Summary, "[agent: failed reason=unknown_session]") {
		t.Errorf("summary missing unknown_session prefix:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "20990101-000000-zzzzzz") {
		t.Errorf("error does not name the dangling id:\n%s", res.Summary)
	}
}

// TestContinue_DanglingTranscript: the index says completed but the
// transcript file is gone (断链) — the error must name the status,
// the missing path, and why.
func TestContinue_DanglingTranscript(t *testing.T) {
	// Runner that completes WITHOUT writing a transcript (simulates
	// a pre-G1 run or a deleted file).
	mgr := newManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{Summary: "done", Turns: 1}, nil
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "no transcript run", Prompt: "work", Type: TypeGeneralPurpose,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("spawn status = %s", res.Status)
	}
	cont := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid: res.SubSid, Description: "follow-up", Prompt: "continue",
	})
	if cont.Status != StatusFailed {
		t.Fatalf("continue status = %s, want failed", cont.Status)
	}
	if !strings.HasPrefix(cont.Summary, "[agent: failed reason=unknown_session]") {
		t.Errorf("summary prefix wrong:\n%s", cont.Summary)
	}
	for _, want := range []string{"status=completed", "transcript", "no readable"} {
		if !strings.Contains(cont.Summary, want) {
			t.Errorf("hint missing %q:\n%s", want, cont.Summary)
		}
	}
}

// TestContinue_StillActive: continuing a sub_sid that is currently
// running returns session_active — and does not disturb the run
// (the concurrent Spawn finishes normally afterwards). Exercised
// under -race.
func TestContinue_StillActive(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	mgr := newManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		close(entered)
		<-release
		return RunnerResult{Summary: "slow done", Turns: 1}, nil
	})

	var (
		wg       sync.WaitGroup
		spawnRes Result
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		spawnRes = mgr.Spawn(context.Background(), SpawnArgs{
			Description: "slow", Prompt: "take your time", Type: TypeGeneralPurpose,
		})
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner never entered")
	}

	// The active sub folds as status=active in the index; Manager
	// must reject the continuation on that live state.
	var sid string
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs, err := mgr.List(ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 1 && recs[0].Status == StatusActive {
			sid = recs[0].SubSid
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("started event never became visible in fold")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cont := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid: sid, Description: "follow-up", Prompt: "continue",
	})
	if cont.Status != StatusFailed {
		t.Fatalf("continue status = %s, want failed", cont.Status)
	}
	if !strings.HasPrefix(cont.Summary, "[agent: failed reason=session_active]") {
		t.Errorf("summary prefix wrong:\n%s", cont.Summary)
	}

	close(release)
	wg.Wait()
	if spawnRes.Status != StatusCompleted {
		t.Errorf("spawn disturbed by rejected continue: %s", spawnRes.Status)
	}
}

// TestContinue_Promoted: a promoted sub-sid must not be continuable
// (its transcript now backs a top-level session; continuing would
// fork the lineage silently).
func TestContinue_Promoted(t *testing.T) {
	mgr := newManager(t, transcriptRunner(t))
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "to be promoted", Prompt: "work", Type: TypeGeneralPurpose,
	})
	indexPath, err := paths.SubagentsIndex(mgr.opts.ProjectAbsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(indexPath, event{Kind: "promoted", SubSid: res.SubSid, TS: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	cont := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid: res.SubSid, Description: "follow-up", Prompt: "continue",
	})
	if cont.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", cont.Status)
	}
	if !strings.HasPrefix(cont.Summary, "[agent: failed reason=unknown_session]") {
		t.Errorf("summary prefix wrong:\n%s", cont.Summary)
	}
	if !strings.Contains(cont.Summary, "promoted") {
		t.Errorf("hint does not explain the promoted status:\n%s", cont.Summary)
	}
}

// TestContinue_RebuildsFromDisk is the event-sourcing acceptance: a
// Manager constructed FRESH over the same project (zero in-memory
// carry-over — the parent seek process was "resumed") continues a
// prior run purely from the on-disk index + transcript.
func TestContinue_RebuildsFromDisk(t *testing.T) {
	withHome(t)
	parentCwd := t.TempDir()
	policy, err := permission.New(parentCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	opts := func(runner Runner) ManagerOpts {
		return ManagerOpts{
			ProjectAbsPath:    parentCwd,
			ParentSidFn:       func() string { return "20260601-100000-parent" },
			ParentTracker:     cache.New(),
			ParentPolicy:      policy,
			ProjectSectionFn:  func() string { return "" },
			SkillManifestFn:   func() string { return "" },
			ParentToolNamesFn: func() []string { return nil },
			MaxConcurrent:     3,
			Runner:            runner,
		}
	}

	mgrA, err := NewManager(opts(transcriptRunner(t)))
	if err != nil {
		t.Fatal(err)
	}
	res1 := mgrA.Spawn(context.Background(), SpawnArgs{
		Description: "first process run", Prompt: "survey", Type: TypeExplore,
	})
	if res1.Status != StatusCompleted {
		t.Fatalf("spawn status = %s", res1.Status)
	}

	// "Process restart": brand-new Manager, same SEEK_HOME + project
	// dir. Everything the continuation needs must come from disk.
	mgrB, err := NewManager(opts(transcriptRunner(t)))
	if err != nil {
		t.Fatal(err)
	}
	res2 := mgrB.Continue(context.Background(), ContinueArgs{
		FromSubSid:  res1.SubSid,
		Description: "second process continuation",
		Prompt:      "go deeper",
	})
	if res2.Status != StatusCompleted {
		t.Fatalf("cross-manager continue status = %s (summary: %s)", res2.Status, res2.Summary)
	}
	sess := loadSubTranscript(t, mgrB, res2.SubSid)
	if len(sess.Messages) != 4 || sess.Messages[0].Content != "survey" {
		t.Fatalf("cross-manager continuation lost context: %d messages, [0]=%q", len(sess.Messages), sess.Messages[0].Content)
	}
}

// TestContinue_FailedRunHasNoTranscript: a run that ended failed
// (killed / orphaned behave the same: the production writer only
// persists on success) yields a continuation error that NAMES the
// status, so the model understands why the chain is broken.
func TestContinue_FailedRunHasNoTranscript(t *testing.T) {
	mgr := newManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{}, context.DeadlineExceeded
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "doomed", Prompt: "work", Type: TypeGeneralPurpose,
	})
	if res.Status != StatusFailed {
		t.Fatalf("spawn status = %s, want failed", res.Status)
	}
	cont := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid: res.SubSid, Description: "follow-up", Prompt: "continue",
	})
	if !strings.HasPrefix(cont.Summary, "[agent: failed reason=unknown_session]") {
		t.Errorf("summary prefix wrong:\n%s", cont.Summary)
	}
	if !strings.Contains(cont.Summary, "status=failed") {
		t.Errorf("hint does not name the failed status:\n%s", cont.Summary)
	}
}

// TestContinue_ArgsValidation: empty prompt / description / id fail
// fast with the same taxonomy Spawn uses.
func TestContinue_ArgsValidation(t *testing.T) {
	mgr := newManager(t, transcriptRunner(t))
	for name, args := range map[string]ContinueArgs{
		"no id":          {Description: "d", Prompt: "p"},
		"no prompt":      {FromSubSid: "x", Description: "d"},
		"no description": {FromSubSid: "x", Prompt: "p"},
	} {
		res := mgr.Continue(context.Background(), args)
		if res.Status != StatusFailed {
			t.Errorf("%s: status = %s, want failed", name, res.Status)
		}
	}
}

// TestContinue_ShortSidResolution: quoting just the footer's short
// suffix (what the model naturally reads back from a result) uniquely
// resolves the prior run; an ambiguous suffix errors with the full
// candidates.
func TestContinue_ShortSidResolution(t *testing.T) {
	mgr := newManager(t, transcriptRunner(t))
	res1 := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "short id source", Prompt: "survey", Type: TypeGeneralPurpose,
	})
	if res1.Status != StatusCompleted {
		t.Fatalf("spawn status = %s", res1.Status)
	}
	short := shortSid(res1.SubSid)
	cont := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid: short, Description: "via short id", Prompt: "deeper",
	})
	if cont.Status != StatusCompleted {
		t.Fatalf("short-id continue status = %s (summary: %s)", cont.Status, cont.Summary)
	}
	if !strings.Contains(cont.Summary, "resumed-from: "+short) {
		t.Errorf("lineage footer missing short prior id:\n%s", cont.Summary)
	}

	// Ambiguity: script a second record sharing the same suffix.
	indexPath, err := paths.SubagentsIndex(mgr.opts.ProjectAbsPath)
	if err != nil {
		t.Fatal(err)
	}
	twin := "20250101-000000-" + short
	if err := appendEvent(indexPath, event{
		Kind: "started", SubSid: twin, ParentSid: "20260601-100000-parent",
		TS: time.Now().UTC(), Type: TypeGeneralPurpose, Description: "twin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(indexPath, event{Kind: "completed", SubSid: twin, TS: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	amb := mgr.Continue(context.Background(), ContinueArgs{
		FromSubSid: short, Description: "ambiguous", Prompt: "deeper",
	})
	if !strings.HasPrefix(amb.Summary, "[agent: failed reason=unknown_session]") {
		t.Errorf("ambiguous suffix must fail unknown_session:\n%s", amb.Summary)
	}
	if !strings.Contains(amb.Summary, twin) || !strings.Contains(amb.Summary, res1.SubSid) {
		t.Errorf("ambiguity error must list both full candidates:\n%s", amb.Summary)
	}
}

// --- helpers ------------------------------------------------------------

// loadSubTranscript reads <sessionDir>/transcript.jsonl for subSid.
func loadSubTranscript(t *testing.T, m *Manager, subSid string) *session.Session {
	t.Helper()
	dir, err := paths.SubagentSessionDir(m.opts.ProjectAbsPath, m.opts.ParentSidFn(), subSid)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.LoadFrom(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		t.Fatalf("load transcript %s: %v", subSid, err)
	}
	return sess
}
