package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/memory"
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
