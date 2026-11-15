package subagent

// Background-continuation tests (P1-b × P1-a composition, card
// c_u4ugnu): ContinueBackground's handle/notice contract, background
// runs being continuable (by a FRESH Manager — the resume rebuild),
// the shared capacity gate, and the orphan answer for runs that died
// with the process. New file by design: manager_continue_test.go
// (sync continuation) and background_test.go (background spawn) stay
// byte-untouched.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// managerOverProject builds a Manager WITH SessionCtx over an
// existing project dir, WITHOUT touching SEEK_HOME again (the caller
// sets the home once — a second Setenv would point at a different
// index and break the cross-Manager rebuild tests). This is the
// "process restart" constructor: same home + project on disk, zero
// memory carry-over.
func managerOverProject(t *testing.T, runner Runner, projectDir string) (*Manager, context.CancelFunc) {
	t.Helper()
	policy, err := permission.New(projectDir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	mgr, err := NewManager(ManagerOpts{
		ProjectAbsPath:    projectDir,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return nil },
		MaxConcurrent:     3,
		Runner:            runner,
		SessionCtx:        sessionCtx,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); mgr.Shutdown() })
	return mgr, cancel
}

// gatedTranscriptRunner gates CONTINUATIONS only (jobs seeded with
// PriorMessages): they block on release until signalled, while fresh
// spawns complete immediately. This lets one test's sync seed spawn
// finish while its background continuation is observably in flight.
// release == nil never blocks.
func gatedTranscriptRunner(release <-chan struct{}) Runner {
	return func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		if release != nil && len(job.PriorMessages) > 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return RunnerResult{}, ctx.Err()
			}
		}
		return writeTranscriptAndSummarise(ctx, job)
	}
}

// writeTranscriptAndSummarise is the error-returning core of
// manager_continue_test.go's transcriptRunner: persist prior + this
// run's exchange as transcript.jsonl, return the summary. It mirrors
// the one production-Runner behaviour continuation depends on
// (transcript persisted on success, self-contained growth).
func writeTranscriptAndSummarise(_ context.Context, job RunnerJob) (RunnerResult, error) {
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
		return RunnerResult{}, fmt.Errorf("stub runner: save transcript %s: %w", job.SubSid, err)
	}
	return RunnerResult{
		Summary: "summary for " + job.UserPrompt,
		Tokens:  Tokens{Prompt: 100, Completion: 10},
		Turns:   1,
	}, nil
}

// TestContinueBackground_HandleAndNotice is the composition spine:
// a completed run is continued IN BACKGROUND — the tool gets the
// background-started handle at once, the terminal notice carries the
// lineage footer, and the wake channel fires (TUI feedback path).
func TestContinueBackground_HandleAndNotice(t *testing.T) {
	release := make(chan struct{})
	mgr, _ := newBackgroundManager(t, gatedTranscriptRunner(release))

	res1 := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "first pass",
		Prompt:      "survey the wire formats",
		Type:        TypeExplore,
	})
	if res1.Status != StatusCompleted {
		t.Fatalf("spawn status = %s (summary: %s)", res1.Status, res1.Summary)
	}

	start := time.Now()
	res2 := mgr.ContinueBackground(ContinueArgs{
		FromSubSid:  res1.SubSid,
		Description: "edge cases follow-up",
		Prompt:      "now audit the edge cases",
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ContinueBackground blocked %v behind the runner — background contract broken", elapsed)
	}
	if !strings.HasPrefix(res2.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("handle missing background-started prefix:\n%s", res2.Summary)
	}
	if res2.SubSid == res1.SubSid {
		t.Fatalf("continuation reused sub_sid %s — one sub_sid must be one lifecycle", res1.SubSid)
	}
	if res2.Status != StatusActive {
		t.Fatalf("handle status = %s, want active", res2.Status)
	}

	// While the continuation is gated in flight, the index shows the
	// NEW sub_sid as the active record (the old one stays completed).
	recs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range recs {
		switch recs[i].SubSid {
		case res2.SubSid:
			if recs[i].Status != StatusActive {
				t.Fatalf("in-flight continuation folds as %s, want active", recs[i].Status)
			}
			if recs[i].ResumedFrom != res1.SubSid {
				t.Fatalf("in-flight fold ResumedFrom = %q, want %q", recs[i].ResumedFrom, res1.SubSid)
			}
		case res1.SubSid:
			if recs[i].Status != StatusCompleted {
				t.Fatalf("prior run folded as %s mid-continuation, want completed", recs[i].Status)
			}
		}
	}

	// Release the gated continuation and wait for its terminal state.
	close(release)
	waitFor(t, 5*time.Second, func() bool {
		recs, err := mgr.List(ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range recs {
			if recs[i].SubSid == res2.SubSid {
				return recs[i].Status == StatusCompleted
			}
		}
		return false
	})

	// The notice carries the background-done prefix + lineage footer.
	notices := mgr.DrainNotices()
	if len(notices) != 1 {
		t.Fatalf("DrainNotices len = %d, want 1", len(notices))
	}
	nt := notices[0]
	if nt.SubSid != res2.SubSid {
		t.Errorf("notice SubSid = %s, want the continuation's %s", nt.SubSid, res2.SubSid)
	}
	if nt.Status != StatusCompleted {
		t.Errorf("notice Status = %s, want completed", nt.Status)
	}
	if !strings.HasPrefix(nt.Summary, "[agent: background done sub-sid="+res2.SubSid+"]") {
		t.Errorf("notice missing background-done prefix:\n%s", nt.Summary)
	}
	if !strings.Contains(nt.Summary, "resumed-from: "+shortSid(res1.SubSid)) {
		t.Errorf("notice missing resumed-from lineage footer:\n%s", nt.Summary)
	}
	if !strings.Contains(nt.Summary, "summary for now audit the edge cases") {
		t.Errorf("notice missing summary body:\n%s", nt.Summary)
	}

	// TUI wake fired for the completion (capacity-1 channel — the
	// non-blocking poke from enqueueNotice).
	select {
	case <-mgr.WakeCh():
	default:
		t.Error("WakeCh never fired for the background continuation's completion")
	}

	// Index fold after completion: lineage recorded, prior untouched.
	recs, err = mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var contRec, firstRec *Subagent
	for i := range recs {
		switch recs[i].SubSid {
		case res2.SubSid:
			contRec = &recs[i]
		case res1.SubSid:
			firstRec = &recs[i]
		}
	}
	if contRec == nil || firstRec == nil {
		t.Fatalf("fold missing records: cont=%v first=%v", contRec, firstRec)
	}
	if contRec.ResumedFrom != res1.SubSid {
		t.Errorf("fold ResumedFrom = %q, want %q", contRec.ResumedFrom, res1.SubSid)
	}
	if contRec.Status != StatusCompleted || firstRec.Status != StatusCompleted {
		t.Errorf("statuses = %s / %s, want completed/completed", contRec.Status, firstRec.Status)
	}

	// Context continuity held in the background too: the continuation's
	// transcript seeds from the first run.
	contSess := loadSubTranscript(t, mgr, res2.SubSid)
	if len(contSess.Messages) != 4 || contSess.Messages[0].Content != "survey the wire formats" {
		t.Fatalf("continuation transcript lost context: %d messages, [0]=%q",
			len(contSess.Messages), contSess.Messages[0].Content)
	}
}

// TestContinueBackground_FreshManagerContinuesBackgroundRun: a run
// that executed via SpawnBackground is continuable by a brand-new
// Manager over the same home + project — the process-death/resume
// rebuild. Background and sync runs write the same event kinds, so
// the continuation entry point cannot and does not distinguish them.
func TestContinueBackground_FreshManagerContinuesBackgroundRun(t *testing.T) {
	release := make(chan struct{})
	close(release) // complete immediately
	mgr, _ := newBackgroundManager(t, gatedTranscriptRunner(release))

	res1 := mgr.SpawnBackground(SpawnArgs{
		Description: "bg research",
		Prompt:      "survey the routing table",
		Type:        TypeExplore,
	})
	waitFor(t, 5*time.Second, func() bool {
		recs, err := mgr.List(ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range recs {
			if recs[i].SubSid == res1.SubSid {
				return recs[i].Status == StatusCompleted
			}
		}
		return false
	})
	mgr.DrainNotices() // don't leak the notice into later assertions

	// "Process restart": brand-new Manager, same SEEK_HOME + project
	// dir. Everything the continuation needs must come from disk.
	mgr2, _ := managerOverProject(t, gatedTranscriptRunner(nil), mgr.opts.ProjectAbsPath)
	res2 := mgr2.Continue(context.Background(), ContinueArgs{
		FromSubSid:  res1.SubSid,
		Description: "follow-up after resume",
		Prompt:      "what did you find about routing",
	})
	if res2.Status != StatusCompleted {
		t.Fatalf("cross-Manager continue status = %s (summary: %s)", res2.Status, res2.Summary)
	}
	if !strings.Contains(res2.Summary, "resumed-from: "+shortSid(res1.SubSid)) {
		t.Errorf("cross-Manager continue missing lineage footer:\n%s", res2.Summary)
	}
	contSess := loadSubTranscript(t, mgr2, res2.SubSid)
	if len(contSess.Messages) != 4 {
		t.Fatalf("cross-Manager continuation transcript has %d messages, want 4", len(contSess.Messages))
	}
	if contSess.Messages[0].Content != "survey the routing table" {
		t.Errorf("seed[0] = %q, want the background run's user prompt", contSess.Messages[0].Content)
	}
}

// TestContinueBackground_OrphanedRunIsNotResurrectable: a background
// run whose process died mid-flight leaves exactly this on disk — a
// `started` event with no terminal. The next process's OrphanRecover
// folds it to orphaned, and continuing it fails with unknown_session
// NAMING the status: the resume rebuild reports dead children
// honestly instead of resurrecting them.
func TestContinueBackground_OrphanedRunIsNotResurrectable(t *testing.T) {
	mgr, _ := newBackgroundManager(t, gatedTranscriptRunner(nil))
	deadSid := "20260101-000000-deadbeefdeadbeef"
	indexPath, err := paths.SubagentsIndex(mgr.opts.ProjectAbsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(indexPath, event{
		Kind:        "started",
		SubSid:      deadSid,
		ParentSid:   "20260601-100000-parent",
		ParentTurn:  1,
		TS:          time.Now(),
		Type:        TypeExplore,
		Description: "died with the process",
	}); err != nil {
		t.Fatal(err)
	}

	mgr2, _ := managerOverProject(t, gatedTranscriptRunner(nil), mgr.opts.ProjectAbsPath)
	idx2, err := paths.SubagentsIndex(mgr2.opts.ProjectAbsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OrphanRecover(idx2); err != nil {
		t.Fatal(err)
	}
	res := mgr2.Continue(context.Background(), ContinueArgs{
		FromSubSid:  deadSid,
		Description: "any follow-up",
		Prompt:      "continue what you were doing",
	})
	if res.Status != StatusFailed {
		t.Fatalf("continue of orphaned run status = %s, want failed", res.Status)
	}
	if !strings.Contains(res.Summary, "unknown_session") {
		t.Errorf("continue of orphaned run missing unknown_session reason:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, "orphaned") {
		t.Errorf("continue of orphaned run missing status hint:\n%s", res.Summary)
	}
}

// TestContinueBackground_SessionCtxRequired: a sync-only Manager
// (SessionCtx nil) refuses background continuation with the same
// wire-format failure SpawnBackground uses.
func TestContinueBackground_SessionCtxRequired(t *testing.T) {
	mgr := newManager(t, gatedTranscriptRunner(nil))
	res := mgr.ContinueBackground(ContinueArgs{
		FromSubSid:  "20990101-000000-zzzzzzzzzzzzzzzz",
		Description: "d",
		Prompt:      "p",
	})
	if !strings.Contains(res.Summary, "background continuation unavailable") {
		t.Fatalf("expected SessionCtx-missing failure, got:\n%s", res.Summary)
	}
}

// TestContinueBackground_SharedCapacityGate: a background
// continuation occupies the same MaxConcurrent pool as every other
// run — over-capacity continuations fail with too_many_subagents.
func TestContinueBackground_SharedCapacityGate(t *testing.T) {
	release := make(chan struct{})
	// Holders use the always-blocking runner (background_test.go's
	// blockingRunner): fresh spawns that stay in flight until released,
	// keeping the pool occupied while the gated runner's continuations
	// would run.
	mgr, _ := newBackgroundManager(t, blockingRunner(release))
	// Fill the pool (default 3) with in-flight background spawns…
	for i := 0; i < 3; i++ {
		res := mgr.SpawnBackground(SpawnArgs{
			Description: "holder",
			Prompt:      "hold",
			Type:        TypeExplore,
		})
		if res.Status != StatusActive {
			t.Fatalf("holder %d status = %s (summary: %s)", i, res.Status, res.Summary)
		}
	}
	// …and seed a completed run through a sibling Manager over the
	// same project (its gate pool is separate; the shared thing is
	// the on-disk index).
	seedMgr, _ := managerOverProject(t, gatedTranscriptRunner(nil), mgr.opts.ProjectAbsPath)
	seed := seedMgr.Spawn(context.Background(), SpawnArgs{
		Description: "seed",
		Prompt:      "seed prompt",
		Type:        TypeExplore,
	})
	if seed.Status != StatusCompleted {
		t.Fatalf("seed status = %s", seed.Status)
	}

	res := mgr.ContinueBackground(ContinueArgs{
		FromSubSid:  seed.SubSid,
		Description: "refused follow-up",
		Prompt:      "should not start",
	})
	if !strings.Contains(res.Summary, "too_many_subagents") {
		t.Fatalf("expected too_many_subagents refusal, got:\n%s", res.Summary)
	}
	close(release)
}
