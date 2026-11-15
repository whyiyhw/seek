package tui

// Skill-segment tests for /distill (feature-skill-distill M-SD1). The
// modal mechanics mirror distill_test.go; what's specific here:
//   - the interleave (skill results queue behind an open M review and
//     chain when it closes),
//   - 'y' writes a DRAFT via DraftStore — and nothing else (the
//     pipeline never installs: no new dir outside .drafts/),
//   - the L0-gated cmdDistill wiring fires both passes in parallel.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skilldistill"
	"github.com/whyiyhw/seek/internal/skillmgr"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func setupSkillModel(t *testing.T) (Model, string) {
	t.Helper()
	m, _ := setupDistillModel(t)
	draftsUserDir := t.TempDir()
	m.opts.DraftsUserDir = draftsUserDir
	m.opts.Skills = skill.NewSet() // empty registry: nothing collides
	return m, draftsUserDir
}

func sampleSkillCandidate(name string) skilldistill.SkillCandidate {
	return skilldistill.SkillCandidate{
		Name:        name,
		Description: "When a TUI test flakes only under -race, reproduce single-threaded first",
		Body:        "## When to use\nflaky TUI tests\n## Steps\n1. run without -race\n## Verification\ngo test green",
		Evidence:    []string{"--- FAIL: TestFlaky"},
	}
}

func TestHandleSkillDistillDone_ErrorPrintsLine(t *testing.T) {
	m, _ := setupSkillModel(t)
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{err: errors.New("boom")})
	if m.skillReviewOpen || m.skillDistilling {
		t.Errorf("error must not open the modal")
	}
}

func TestHandleSkillDistillDone_EmptyClosesQuietly(t *testing.T) {
	m, _ := setupSkillModel(t)
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{})
	if m.skillReviewOpen {
		t.Errorf("empty skill result must NOT open the review modal")
	}
	if m.skillDistilling {
		t.Errorf("spinner must clear")
	}
}

func TestHandleSkillDistillDone_RejectionsReportedNotFatal(t *testing.T) {
	m, _ := setupSkillModel(t)
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		rejected: []skilldistill.Rejection{{Name: "bad-name", Reason: "name \"Bad_Name\" is not kebab-case"}},
	})
	if m.skillReviewOpen {
		t.Errorf("rejections alone must not open the modal")
	}
}

func TestHandleSkillDistillDone_OpensModalWhenIdle(t *testing.T) {
	m, _ := setupSkillModel(t)
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{sampleSkillCandidate("repro-flaky-first")},
	})
	if !m.skillReviewOpen || m.skillIdx != 0 {
		t.Fatalf("modal should be open at candidate 0")
	}
}

func TestHandleSkillDistillDone_QueuesBehindOpenMReview(t *testing.T) {
	m, _ := setupSkillModel(t)
	// M review open (mirrors the normal ordering: M lands first).
	m.distillReviewOpen = true

	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{sampleSkillCandidate("queued-skill")},
	})
	if m.skillReviewOpen {
		t.Fatalf("skill modal must NOT steal the keyboard from an open M review")
	}
	if len(m.pendingSkill) != 1 {
		t.Fatalf("candidate must be queued, pending=%d", len(m.pendingSkill))
	}

	// M review finishes → skill review chains open.
	m2, _ := m.exitDistillReview(false)
	m = m2.(Model)
	if !m.skillReviewOpen {
		t.Fatalf("skill review must chain after the M review closes")
	}
	if len(m.skillCandidates) != 1 || m.skillCandidates[0].Name != "queued-skill" {
		t.Errorf("chained candidates = %+v", m.skillCandidates)
	}
	if len(m.pendingSkill) != 0 {
		t.Errorf("pending queue must drain: %d", len(m.pendingSkill))
	}
}

func TestSkillReviewKeys_SaveDropQuit(t *testing.T) {
	m, draftsDir := setupSkillModel(t)
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{
			sampleSkillCandidate("keep-me"),
			sampleSkillCandidate("drop-me"),
			sampleSkillCandidate("skip-me"),
		},
	})

	// y on the first: a draft lands under .drafts/, nothing else.
	m2, _ := m.handleSkillDistillKey(keyRune('y'))
	m = m2.(Model)
	if m.skillSaved != 1 || m.skillIdx != 1 {
		t.Fatalf("y: saved=%d idx=%d, want 1/1", m.skillSaved, m.skillIdx)
	}
	draftMD := filepath.Join(draftsDir, ".drafts", "keep-me", "SKILL.md")
	if _, err := os.Stat(draftMD); err != nil {
		t.Fatalf("draft SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(draftsDir, ".drafts", "keep-me", ".distill.json")); err != nil {
		t.Fatalf("draft sidecar missing: %v", err)
	}
	// The human gate: y must NOT install — nothing outside .drafts/.
	entries, _ := os.ReadDir(draftsDir)
	for _, e := range entries {
		if e.Name() != ".drafts" {
			t.Errorf("distill wrote outside .drafts/: %s", e.Name())
		}
	}

	// n on the second: dropped, no draft.
	m2, _ = m.handleSkillDistillKey(keyRune('n'))
	m = m2.(Model)
	if m.skillDropped != 1 || m.skillIdx != 2 {
		t.Fatalf("n: dropped=%d idx=%d, want 1/2", m.skillDropped, m.skillIdx)
	}
	if _, err := os.Stat(filepath.Join(draftsDir, ".drafts", "drop-me")); !os.IsNotExist(err) {
		t.Error("dropped candidate must not leave a draft")
	}

	// q on the third: abort, modal closed, one skipped.
	m2, _ = m.handleSkillDistillKey(keyRune('q'))
	m = m2.(Model)
	if m.skillReviewOpen {
		t.Error("q must close the modal")
	}
	if m.skillSaved != 1 || m.skillDropped != 1 {
		t.Errorf("counters after q: saved=%d dropped=%d", m.skillSaved, m.skillDropped)
	}
}

func TestSkillReview_WritesParseableDraftWithProvenance(t *testing.T) {
	m, draftsDir := setupSkillModel(t)
	cand := sampleSkillCandidate("provenance-check")
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{candidates: []skilldistill.SkillCandidate{cand}})
	m2, _ := m.handleSkillDistillKey(keyRune('y'))
	m = m2.(Model)
	_ = m

	sidecarPath := filepath.Join(draftsDir, ".drafts", "provenance-check", ".distill.json")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var side skillmgr.DraftSidecar
	if err := json.Unmarshal(data, &side); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	// Manual path contract: reviewable immediately, L2 trace present.
	if side.Status != skillmgr.DraftStatusReviewable {
		t.Errorf("status = %q, want reviewable (manual path: the user is the recurrence evidence)", side.Status)
	}
	if len(side.FilterTrace) != 1 || side.FilterTrace[0].Stage != "L2" {
		t.Errorf("filter trace = %+v", side.FilterTrace)
	}
	if side.Generator == nil || side.Generator.PromptRev != skilldistill.PromptRev {
		t.Errorf("generator = %+v", side.Generator)
	}
}

func TestSkillReview_GreyWarningSurvivesIntoSidecar(t *testing.T) {
	m, draftsDir := setupSkillModel(t)
	m.opts.Skills.Add(&skill.Skill{
		Name:        "flaky-test-routine",
		Description: "When a TUI test flakes under the race detector, reproduce single-threaded before touching the state machine",
	})
	// Same desc+body pair the skilldistill tests measured into the grey
	// band (≈0.48 vs the installed skill below) — a shorter body drops
	// it to PASS, so this fixture must stay verbatim.
	cand := skilldistill.SkillCandidate{
		Name:        "repro-flaky-first",
		Description: "When a TUI test flakes only under the race detector, reproduce it single-threaded before touching the state machine",
		Body:        "## When to use\nA TUI test passes alone but fails under -race\n## Steps\n1. run go test ./internal/tui/ -run Flaky without -race\n2. only edit the state machine once it reproduces",
		Evidence:    []string{"--- FAIL: TestFlaky"},
	}
	// Run the real funnel so L2Note is set the way production sets it.
	l2 := skilldistill.CheckNovelty(cand, m.opts.Skills, nil)
	if l2.Verdict != skilldistill.L2Grey {
		t.Fatalf("fixture should land GREY, got %v", l2.Verdict)
	}
	cand.L2Note = l2.Reason

	_ = m.handleSkillDistillDone(skillDistillDoneMsg{candidates: []skilldistill.SkillCandidate{cand}})
	m2, _ := m.handleSkillDistillKey(keyRune('y'))
	m = m2.(Model)
	_ = m

	data, err := os.ReadFile(filepath.Join(draftsDir, ".drafts", "repro-flaky-first", ".distill.json"))
	if err != nil {
		t.Fatal(err)
	}
	var side skillmgr.DraftSidecar
	if err := json.Unmarshal(data, &side); err != nil {
		t.Fatal(err)
	}
	if side.FilterTrace[0].Verdict != "GREY" || side.FilterTrace[0].Reason == "" {
		t.Errorf("grey warning must survive into the sidecar: %+v", side.FilterTrace)
	}
}

func TestSkillReview_KeysIgnoredWhenClosed(t *testing.T) {
	m, _ := setupSkillModel(t)
	m2, cmd := m.handleSkillDistillKey(keyRune('y'))
	if cmd != nil {
		t.Errorf("no cmd expected when modal closed")
	}
	if m2.(Model).skillSaved != 0 {
		t.Errorf("y outside the modal must do nothing")
	}
}

func TestSkillReview_RenderIncludesEvidenceAndGate(t *testing.T) {
	m, _ := setupSkillModel(t)
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{sampleSkillCandidate("render-me")},
	})
	view := m.renderSkillReview()
	for _, want := range []string{"skill candidate 1/1", "render-me", "evidence", "--- FAIL: TestFlaky", "seek skill install"} {
		if !strings.Contains(view, want) {
			t.Errorf("modal render missing %q:\n%s", want, view)
		}
	}
}

// The M review must also queue behind an OPEN skill review (reverse
// ordering) — both modals can never be open at once.
func TestMDistillDone_QueuesBehindOpenSkillReview(t *testing.T) {
	m, _ := setupSkillModel(t)
	m.skillReviewOpen = true
	m.skillCandidates = []skilldistill.SkillCandidate{sampleSkillCandidate("cur")}

	_ = m.handleDistillDone(distillDoneMsg{
		candidates: memoryCandidates{{Name: "mem", Tagline: "t", Content: "c"}},
	})
	if m.distillReviewOpen {
		t.Fatal("M modal must not steal the keyboard from an open skill review")
	}
	if len(m.pendingMCandidates) != 1 {
		t.Fatalf("M candidates must queue, got %d", len(m.pendingMCandidates))
	}

	m2, _ := m.exitSkillReview(false)
	m = m2.(Model)
	if !m.distillReviewOpen {
		t.Fatal("M review must chain after the skill review closes")
	}
}

// ---------- cmdDistill L0-gate wiring ----------

// gateAgent builds an Agent seeded via Reset with nMessages non-system
// messages and nCalls tool calls spread over assistant turns — the
// same shape MechanicalGate counts.
func gateAgent(t *testing.T, nMessages, nCalls int) *agent.Agent {
	t.Helper()
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	var hist []deepseek.Message
	calls := 0
	for i := range nMessages {
		m := deepseek.Message{Role: deepseek.RoleUser, Content: "please fix the flaky test"}
		if calls < nCalls && i%2 == 1 {
			m = deepseek.Message{
				Role:      deepseek.RoleAssistant,
				Content:   "running it",
				ToolCalls: []deepseek.ToolCall{{ID: "c", Function: deepseek.ToolCallFunc{Name: "bash"}}},
			}
			calls++
		}
		hist = append(hist, m)
	}
	ag.Reset(hist)
	return ag
}

func gateModel(t *testing.T, msgs, calls int) *Model {
	m0, _ := setupSkillModel(t)
	m := &m0
	m.opts.Agent = gateAgent(t, msgs, calls)
	m.opts.Distiller = &memory.Distiller{}
	m.opts.SkillDistiller = &skilldistill.Distiller{}
	return m
}

func TestCmdDistill_L0GateFails_SkillPassSkipped(t *testing.T) {
	m := gateModel(t, 10, 0) // pure Q&A shape
	res := runHandler(t, m, "/distill")
	if !strings.Contains(res.text, "skill drafts skipped") {
		t.Errorf("gate failure must say what was skipped and why: %q", res.text)
	}
	if res.extra == nil {
		t.Error("M pass must still run when the skill gate fails")
	}
	if m.skillDistilling {
		t.Error("skill spinner must not start when the gate fails")
	}
}

func TestCmdDistill_L0GatePasses_SkillPassFires(t *testing.T) {
	m := gateModel(t, 24, 4) // comfortably procedural
	res := runHandler(t, m, "/distill")
	if res.text != "" {
		t.Errorf("no gate note expected: %q", res.text)
	}
	if res.extra == nil {
		t.Fatal("both passes must fire in parallel")
	}
	if !m.skillDistilling {
		t.Error("skill spinner must be running")
	}
	if !strings.Contains(m.skillDistillGate, "24 messages") {
		t.Errorf("gate detail = %q", m.skillDistillGate)
	}
}

func TestCmdDistill_NoSkillDistiller_MOnlyAsBefore(t *testing.T) {
	m := gateModel(t, 24, 4)
	m.opts.SkillDistiller = nil
	runHandler(t, m, "/distill")
	if !m.distilling {
		t.Error("M pass must run")
	}
	if m.skillDistilling {
		t.Error("skill pass must stay dormant without a SkillDistiller")
	}
}
