package tui

import (
	"math"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skilldistill"
)

// setupDistillModel builds a Model wired to a real (tempdir-backed)
// memory.Project so accept paths can verify Project.Add actually wrote
// to disk. Returns the Model and the Project so tests can assert on
// either side.
func setupDistillModel(t *testing.T) (Model, *memory.Project) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	cwd := t.TempDir()
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	m := New(Options{MemoryProject: p})
	return m, p
}

// openReview puts a model into review-mode with the given candidates,
// mirroring what handleDistillDone does without going through the
// async msg pipeline.
func openReview(m Model, candidates []memory.Candidate) Model {
	m.distillCandidates = candidates
	m.distillIdx = 0
	m.distillReviewOpen = true
	return m
}

func keyRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: string(r)}
}

func TestHandleDistillDone_EmptyClosesQuietly(t *testing.T) {
	m, _ := setupDistillModel(t)
	_ = m.handleDistillDone(distillDoneMsg{candidates: nil})
	if m.distillReviewOpen {
		t.Errorf("empty candidates should NOT open review modal")
	}
}

func TestHandleDistillDone_OpensModalForCandidates(t *testing.T) {
	m, _ := setupDistillModel(t)
	cands := []memory.Candidate{
		{Name: "a", Tagline: "first", Content: "x"},
		{Name: "b", Tagline: "second", Content: "y"},
	}
	_ = m.handleDistillDone(distillDoneMsg{candidates: cands})
	if !m.distillReviewOpen {
		t.Errorf("non-empty candidates should open the modal")
	}
	if m.distillIdx != 0 || len(m.distillCandidates) != 2 {
		t.Errorf("unexpected initial state: idx=%d len=%d", m.distillIdx, len(m.distillCandidates))
	}
}

func TestDistill_YSavesAndAdvances(t *testing.T) {
	m, p := setupDistillModel(t)
	cands := []memory.Candidate{
		{Name: "first", Tagline: "1", Content: "c1"},
		{Name: "second", Tagline: "2", Content: "c2"},
	}
	m = openReview(m, cands)

	out, _ := m.handleDistillKey(keyRune('y'))
	mm := out.(Model)
	if mm.distillSaved != 1 {
		t.Errorf("Saved = %d, want 1", mm.distillSaved)
	}
	if mm.distillIdx != 1 {
		t.Errorf("Idx = %d, want 1 (advanced)", mm.distillIdx)
	}
	if !mm.distillReviewOpen {
		t.Errorf("modal should still be open with one candidate left")
	}
	if _, ok := p.Get("first"); !ok {
		t.Errorf("first candidate should have been written to M")
	}
}

func TestDistill_NDropsAndAdvances(t *testing.T) {
	m, p := setupDistillModel(t)
	cands := []memory.Candidate{
		{Name: "first", Tagline: "1", Content: "c1"},
		{Name: "second", Tagline: "2", Content: "c2"},
	}
	m = openReview(m, cands)

	out, _ := m.handleDistillKey(keyRune('n'))
	mm := out.(Model)
	if mm.distillDropped != 1 || mm.distillSaved != 0 {
		t.Errorf("counts wrong: saved=%d dropped=%d", mm.distillSaved, mm.distillDropped)
	}
	if mm.distillIdx != 1 {
		t.Errorf("Idx = %d, want 1", mm.distillIdx)
	}
	if _, ok := p.Get("first"); ok {
		t.Errorf("dropped candidate should NOT have been written to M")
	}
}

func TestDistill_QAbortsRemaining(t *testing.T) {
	m, p := setupDistillModel(t)
	cands := []memory.Candidate{
		{Name: "first", Tagline: "1", Content: "c1"},
		{Name: "second", Tagline: "2", Content: "c2"},
		{Name: "third", Tagline: "3", Content: "c3"},
	}
	m = openReview(m, cands)

	out, _ := m.handleDistillKey(keyRune('q'))
	mm := out.(Model)
	if mm.distillReviewOpen {
		t.Errorf("'q' should close the modal")
	}
	for _, n := range []string{"first", "second", "third"} {
		if _, ok := p.Get(n); ok {
			t.Errorf("nothing should have been written to M after 'q', but %s was", n)
		}
	}
}

func TestDistill_EscBehavesLikeQ(t *testing.T) {
	m, _ := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "x", Tagline: "x", Content: "x"}})

	out, _ := m.handleDistillKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := out.(Model)
	if mm.distillReviewOpen {
		t.Errorf("Esc should close the modal (treated as abort)")
	}
}

func TestDistill_LastCandidateAcceptClosesModal(t *testing.T) {
	m, _ := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "only", Tagline: "t", Content: "c"}})

	out, _ := m.handleDistillKey(keyRune('y'))
	mm := out.(Model)
	if mm.distillReviewOpen {
		t.Errorf("modal should close after the last candidate")
	}
	if mm.distillSaved != 1 {
		t.Errorf("Saved = %d, want 1", mm.distillSaved)
	}
}

func TestDistill_EnterEnterEditMode(t *testing.T) {
	m, _ := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "x", Tagline: "t", Content: "original content"}})

	out, _ := m.handleDistillKey(keyRune('e'))
	mm := out.(Model)
	if !mm.distillEditing {
		t.Errorf("'e' should enter edit mode")
	}
	if mm.input.Value() != "original content" {
		t.Errorf("input should be prefilled with candidate's content, got %q", mm.input.Value())
	}
}

func TestDistill_EditEnterSavesEditedContent(t *testing.T) {
	m, p := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "x", Tagline: "t", Content: "original"}})

	out, _ := m.handleDistillKey(keyRune('e'))
	m = out.(Model)
	// Simulate user replacing content. SetValue mirrors what arrow-key /
	// typing edits would produce; we skip the per-key forwarding for
	// brevity since that's textarea library code, not our state machine.
	m.input.SetValue("edited rationale")

	out, _ = m.handleDistillKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := out.(Model)
	if mm.distillEditing {
		t.Errorf("Enter in edit mode should exit edit mode")
	}
	if mm.distillSaved != 1 {
		t.Errorf("edit-commit should count as save, got Saved=%d", mm.distillSaved)
	}
	stored, ok := p.Get("x")
	if !ok {
		t.Fatalf("entry not stored after edit-commit")
	}
	if stored.Content != "edited rationale" {
		t.Errorf("stored content = %q, want edited", stored.Content)
	}
}

func TestDistill_EditEscCancelsWithoutSaving(t *testing.T) {
	m, p := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "x", Tagline: "t", Content: "original"}})

	out, _ := m.handleDistillKey(keyRune('e'))
	m = out.(Model)
	m.input.SetValue("trash that should not be saved")

	out, _ = m.handleDistillKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := out.(Model)
	if mm.distillEditing {
		t.Errorf("Esc should exit edit mode")
	}
	if mm.distillSaved != 0 {
		t.Errorf("Esc-cancelled edit should NOT save (Saved=%d)", mm.distillSaved)
	}
	if !mm.distillReviewOpen {
		t.Errorf("Esc from edit mode should return to review, not close it")
	}
	if _, ok := p.Get("x"); ok {
		t.Errorf("nothing should have been written after Esc-cancelled edit")
	}
}

func TestDistill_EditCommitWithEmptyContentDrops(t *testing.T) {
	// Committing an empty edit shouldn't write an empty entry to M
	// (that's useless and Add() would warn). Treat it as a drop.
	m, p := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "x", Tagline: "t", Content: "x"}})

	out, _ := m.handleDistillKey(keyRune('e'))
	m = out.(Model)
	m.input.SetValue("   \n\n  ")

	out, _ = m.handleDistillKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := out.(Model)
	if mm.distillSaved != 0 || mm.distillDropped != 1 {
		t.Errorf("empty edit should drop, got saved=%d dropped=%d", mm.distillSaved, mm.distillDropped)
	}
	if _, ok := p.Get("x"); ok {
		t.Errorf("empty-edit drop should not write to M")
	}
}

func TestDistill_UnknownKeyIgnored(t *testing.T) {
	m, _ := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{{Name: "x", Tagline: "t", Content: "c"}})

	for _, r := range []rune{'a', 'z', '1', ' '} {
		out, _ := m.handleDistillKey(keyRune(r))
		mm := out.(Model)
		if mm.distillIdx != 0 || mm.distillSaved != 0 || mm.distillDropped != 0 {
			t.Errorf("unknown rune %q advanced state: idx=%d saved=%d dropped=%d",
				r, mm.distillIdx, mm.distillSaved, mm.distillDropped)
		}
	}
}

func TestRenderDistillReview_ShowsAllFields(t *testing.T) {
	m, _ := setupDistillModel(t)
	m = openReview(m, []memory.Candidate{
		{
			Name:    "naming-convention",
			Tagline: "kebab-case all the things",
			Content: "explanation here",
			Tags:    []string{"style", "lint"},
		},
	})

	got := m.renderDistillReview()
	for _, want := range []string{
		"naming-convention",
		"kebab-case all the things",
		"explanation here",
		"style", "lint",
		"[y] save", "[n] drop", "[e] edit", "[q] quit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q\n got: %s", want, got)
		}
	}
}

func TestRenderDistillReview_HiddenWhenModalClosed(t *testing.T) {
	m, _ := setupDistillModel(t)
	if got := m.renderDistillReview(); got != "" {
		t.Errorf("expected empty render when modal closed, got %q", got)
	}
}

// ---------- ⚡ dual-store overlap warning (bridge §8.4 / M-B4) ----------

// newDistillModelWithSkills mirrors setupDistillModel but wires a
// loaded skill Set — the overlap warning reads it via m.opts.Skills.
func newDistillModelWithSkills(t *testing.T, set *skill.Set) Model {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	p, err := memory.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return New(Options{MemoryProject: p, Skills: set})
}

// The warning fires exactly at the L2 reject threshold (inclusive) and
// NOT inside the grey band — the gate is the reject line, not grey.
// Fixtures are built for exact Jaccard values: with Name/Tagline
// empty, the compared text normalizes to Content verbatim
// (whitespace-run collapsing drops the joiner), so hand-counted
// trigram sets hold: "abcdef" vs "zabcde" → 3/5 = 0.60 exactly;
// "abcdefgh" vs "zabcdefz" → 4/8 = 0.50 (grey zone).
func TestDistillOverlapWarning_ThresholdBoundary(t *testing.T) {
	exact := skill.NewSet()
	exact.Add(&skill.Skill{Name: "exact-boundary", Description: "zabcde"})
	grey := skill.NewSet()
	grey.Add(&skill.Skill{Name: "grey-zone", Description: "zabcdefz"})

	// Fixture sanity first: the sims really are what we counted.
	if _, sim := skilldistill.NearestSkill(" ", "abcdef", exact); math.Abs(sim-0.60) > 1e-12 {
		t.Fatalf("exact fixture sim = %v, want 0.60", sim)
	}
	if _, sim := skilldistill.NearestSkill(" ", "abcdefgh", grey); math.Abs(sim-0.50) > 1e-12 {
		t.Fatalf("grey fixture sim = %v, want 0.50", sim)
	}

	atThreshold := distillOverlapWarning(memory.Candidate{Content: "abcdef"}, exact)
	if atThreshold == "" {
		t.Error("sim = 0.60 (>= SimilarityReject) must warn")
	} else if !strings.Contains(atThreshold, `"exact-boundary"`) {
		t.Errorf("warning must name the overlapping skill: %q", atThreshold)
	}

	if w := distillOverlapWarning(memory.Candidate{Content: "abcdefgh"}, grey); w != "" {
		t.Errorf("grey-zone sim 0.50 must NOT warn (only ≥0.60 does): %q", w)
	}

	// Degenerate inputs: no skills loaded → nothing to overlap.
	if w := distillOverlapWarning(memory.Candidate{Content: "abcdef"}, nil); w != "" {
		t.Errorf("nil skill set must not warn: %q", w)
	}
	if w := distillOverlapWarning(memory.Candidate{Content: "abcdef"}, skill.NewSet()); w != "" {
		t.Errorf("empty skill set must not warn: %q", w)
	}
}

func TestRenderDistillReview_OverlapWarningLine(t *testing.T) {
	desc := "sessions are JSONL because prefix-cache bytes must stay identical"
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "jsonl-session-format", Description: desc})
	m := newDistillModelWithSkills(t, set)
	m = openReview(m, []memory.Candidate{{
		Name:    "keep-sessions-jsonl",
		Tagline: desc,
		Content: "always keep " + desc,
	}})

	got := m.renderDistillReview()
	for _, want := range []string{"⚠", "overlaps installed skill", `"jsonl-session-format"`, "double-store"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q\n got: %s", want, got)
		}
	}
}

// Disjoint texts must render NO warning — pure display also means
// silence when it doesn't apply.
func TestRenderDistillReview_NoWarningWhenDisjoint(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "git-rebase-care", Description: "When a git rebase drops commits mid-stack, verify the reflog before force-pushing anything"})
	m := newDistillModelWithSkills(t, set)
	m = openReview(m, []memory.Candidate{{
		Name:    "release-checklist",
		Tagline: "cut releases from a written checklist",
		Content: "1. run the full test suite\n2. tag\n3. publish",
	}})

	if got := m.renderDistillReview(); strings.Contains(got, "⚠") {
		t.Errorf("disjoint candidate must not warn:\n%s", got)
	}
}
