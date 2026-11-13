package skillmgr

// Draft-store tests (feature-skill-distill M-SD1). The load-bearing
// one is TestDraftsInvisibleToLoader: the entire human-review-gate
// design rests on the loader skipping dot directories, and this test
// is the pin that fails if that wall ever cracks (PRD §8 #3).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
)

func validDraftMD(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: |\n  When X happens, follow this.\nversion: 0.1.0-draft\nlicense: MIT\nauthor: distilled\n---\n\n## When to use\nX\n## Steps\n1. do it\n## Verification\ngo test green\n")
}

func pinnedClock(t *testing.T) func() time.Time {
	t.Helper()
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

func TestWriteDraft_LaysDownSKILLAndSidecar(t *testing.T) {
	userDir := t.TempDir()
	store := &DraftStore{UserDir: userDir, Now: pinnedClock(t)}

	side := DraftSidecar{
		Status:         DraftStatusReviewable,
		SuggestedScope: "project",
		Sources:        []DraftSource{{SessionID: "s1", ProjectID: "p1", At: "2026-11-13T09:00:00Z"}},
		Evidence:       []string{"verbatim quote"},
		Generator:      &DraftGenerator{Model: "deepseek-v4-flash", PromptRev: "skill-distill-v1"},
	}
	dir, err := store.WriteDraft(validDraftMD("fix-tui-bug"), side)
	if err != nil {
		t.Fatalf("WriteDraft: %v", err)
	}
	wantDir := filepath.Join(userDir, DraftsDirName, "fix-tui-bug")
	if dir != wantDir {
		t.Errorf("dir = %q, want %q", dir, wantDir)
	}

	// SKILL.md is byte-identical to what was rendered.
	md, err := os.ReadFile(filepath.Join(wantDir, "SKILL.md"))
	if err != nil || string(md) != string(validDraftMD("fix-tui-bug")) {
		t.Fatalf("SKILL.md roundtrip failed: %v", err)
	}

	// Sidecar carries defaults + provenance.
	var got DraftSidecar
	data, err := os.ReadFile(filepath.Join(wantDir, SidecarName))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	if got.SchemaVersion != 1 || got.Status != DraftStatusReviewable || got.Recurrence != 1 {
		t.Errorf("sidecar defaults wrong: %+v", got)
	}
	if got.CreatedAt != "2026-11-13T09:00:00Z" {
		t.Errorf("CreatedAt = %q (Now injection ignored?)", got.CreatedAt)
	}
	if len(got.Sources) != 1 || got.Sources[0].SessionID != "s1" {
		t.Errorf("sources = %+v", got.Sources)
	}
}

func TestWriteDraft_RejectsInvalidSKILL(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir()}
	bad := []byte("---\nname: Not_Kebab\ndescription: d\n---\nbody")
	if _, err := store.WriteDraft(bad, DraftSidecar{}); err == nil {
		t.Fatal("a draft that skill.Parse rejects must not land on disk")
	}
	drafts, _ := store.ListDrafts()
	if len(drafts) != 0 {
		t.Errorf("rejected draft left residue: %+v", drafts)
	}
}

func TestWriteDraft_RefusesOverwrite(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir()}
	if _, err := store.WriteDraft(validDraftMD("same-name"), DraftSidecar{}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := store.WriteDraft(validDraftMD("same-name"), DraftSidecar{}); err == nil {
		t.Fatal("second write of the same name must refuse (never silently overwrite a draft under review)")
	}
}

func TestListDrafts_OldestFirstAndMissingDirOK(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir(), Now: pinnedClock(t)}

	// Empty inbox on a missing drafts dir.
	if drafts, err := store.ListDrafts(); err != nil || len(drafts) != 0 {
		t.Fatalf("empty inbox: drafts=%v err=%v", drafts, err)
	}

	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for i, name := range []string{"newer-draft", "older-draft"} {
		at := base.Add(time.Duration(-i) * 24 * time.Hour)
		side := DraftSidecar{Status: DraftStatusReviewable, CreatedAt: at.Format(time.RFC3339)}
		if _, err := store.WriteDraft(validDraftMD(name), side); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	drafts, err := store.ListDrafts()
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if len(drafts) != 2 {
		t.Fatalf("len = %d, want 2", len(drafts))
	}
	if drafts[0].Name != "older-draft" || drafts[1].Name != "newer-draft" {
		t.Errorf("order = [%s, %s], want oldest first", drafts[0].Name, drafts[1].Name)
	}
	if drafts[0].Recurrence != 1 || drafts[0].SuggestedScope != "project" {
		t.Errorf("row fields not surfaced: %+v", drafts[0])
	}
}

func TestDeleteDraft(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir()}
	if _, err := store.WriteDraft(validDraftMD("doomed-draft"), DraftSidecar{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.DeleteDraft("doomed-draft"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	drafts, _ := store.ListDrafts()
	if len(drafts) != 0 {
		t.Fatalf("draft survived deletion: %+v", drafts)
	}
	if _, err := store.DeleteDraft("doomed-draft"); err == nil {
		t.Error("deleting a missing draft must error")
	}
}

// The name goes through RemoveAll — the kebab gate must run FIRST,
// or "../escape" style names walk out of the drafts tree.
func TestDeleteDraft_PathEscapeRefused(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir()}
	for _, name := range []string{"../evil", "..", "/abs/path", "UPPER", "under_score"} {
		if _, err := store.DeleteDraft(name); err == nil {
			t.Errorf("DeleteDraft(%q) must refuse", name)
		}
	}
}

// PRD §8 #3 — THE load-bearing invisibility pin. With drafts in
// .drafts/, skill.Load over the same user tree must produce exactly
// the same set (names AND descriptions AND sources) as with .drafts/
// absent. If this test ever fails, the human-review gate is broken:
// drafts would leak into manifests of every future session.
func TestDraftsInvisibleToLoader(t *testing.T) {
	proj := t.TempDir()
	userDir := t.TempDir()

	// One real installed skill + one draft.
	real := filepath.Join(userDir, "real-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, validDraftMD("real-skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseline, _, err := skill.Load(skill.LoadOptions{ProjectDir: proj, UserSkillsDir: userDir})
	if err != nil {
		t.Fatalf("baseline Load: %v", err)
	}

	store := &DraftStore{UserDir: userDir}
	for _, name := range []string{"draft-one", "draft-two"} {
		if _, err := store.WriteDraft(validDraftMD(name), DraftSidecar{}); err != nil {
			t.Fatalf("write draft %s: %v", name, err)
		}
	}

	after, stats, err := skill.Load(skill.LoadOptions{ProjectDir: proj, UserSkillsDir: userDir})
	if err != nil {
		t.Fatalf("Load with drafts: %v", err)
	}

	if after.Len() != baseline.Len() {
		t.Fatalf("loader saw drafts: %d skills with drafts vs %d without", after.Len(), baseline.Len())
	}
	for _, sk := range baseline.List() {
		got := after.Get(sk.Name)
		if got == nil || got.Description != sk.Description || got.Source != sk.Source {
			t.Errorf("skill %q changed after drafts appeared: %+v vs %+v", sk.Name, got, sk)
		}
	}
	for _, name := range []string{"draft-one", "draft-two"} {
		if after.Get(name) != nil {
			t.Errorf("draft %q leaked into the loaded set — the review gate is broken", name)
		}
	}
	if got := after.Manifest(); strings.Contains(got, "draft-one") {
		t.Errorf("draft leaked into the manifest")
	}
	_ = stats
}
