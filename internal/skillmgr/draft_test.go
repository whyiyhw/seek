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

func TestReadDraft(t *testing.T) {
	userDir := t.TempDir()
	store := &DraftStore{UserDir: userDir}

	side := DraftSidecar{
		Status:         DraftStatusReviewable,
		SuggestedScope: "user",
		Sources:        []DraftSource{{SessionID: "s-42", ProjectID: "p-1", At: "2026-11-13T09:00:00Z"}},
		FilterTrace:    []FilterTraceEntry{{Stage: "L2", Verdict: "GREY", Reason: "borderline"}},
	}
	if _, err := store.WriteDraft(validDraftMD("readable-draft"), side); err != nil {
		t.Fatalf("write: %v", err)
	}

	detail, err := store.ReadDraft("readable-draft")
	if err != nil {
		t.Fatalf("ReadDraft: %v", err)
	}
	if string(detail.MD) != string(validDraftMD("readable-draft")) {
		t.Error("MD must be the raw SKILL.md bytes")
	}
	if detail.Side.SuggestedScope != "user" || len(detail.Side.Sources) != 1 {
		t.Errorf("sidecar = %+v", detail.Side)
	}
	if detail.Info.Sources[0].SessionID != "s-42" || detail.Info.FilterTrace[0].Verdict != "GREY" {
		t.Errorf("info provenance not surfaced: %+v", detail.Info)
	}
}

func TestReadDraft_MissingAndEscapedNames(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir()}
	if _, err := store.ReadDraft("no-such-draft"); err == nil {
		t.Error("missing draft must error")
	}
	// Same path-escape guard DeleteDraft has — the name joins a path.
	for _, name := range []string{"../evil", "/abs", "UPPER"} {
		if _, err := store.ReadDraft(name); err == nil {
			t.Errorf("ReadDraft(%q) must refuse", name)
// ---------- M-SD2: weak-inbox filtering, StrengthenDraft, GC ----------

// The inbox hides weak drafts (single-session evidence hasn't earned a
// human's attention); ListAllDrafts still surfaces them for GC and L4
// merge-matching. PRD §4 L4.
func TestListDrafts_HidesWeak(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir(), Now: pinnedClock(t)}
	if _, err := store.WriteDraft(validDraftMD("weak-one"), DraftSidecar{Status: DraftStatusWeak}); err != nil {
		t.Fatalf("write weak: %v", err)
	}
	if _, err := store.WriteDraft(validDraftMD("ready-one"), DraftSidecar{Status: DraftStatusReviewable}); err != nil {
		t.Fatalf("write reviewable: %v", err)
	}

	inbox, err := store.ListDrafts()
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Name != "ready-one" {
		t.Fatalf("inbox = %+v, want only ready-one", inbox)
	}
	all, err := store.ListAllDrafts()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAllDrafts = %d, want 2 (weak included)", len(all))
	}
	for _, d := range all {
		if d.Description == "" {
			t.Errorf("draft %s: Description not populated from SKILL.md", d.Name)
		}
	}
}

// A sidecar-less draft (manual meddling / half-written dir) still
// reads: SKILL.md is the review target, provenance degrades to defaults.
func TestReadDraft_SidecarOptional(t *testing.T) {
	userDir := t.TempDir()
	store := &DraftStore{UserDir: userDir}
	if _, err := store.WriteDraft(validDraftMD("bare-draft"), DraftSidecar{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Remove(filepath.Join(userDir, DraftsDirName, "bare-draft", SidecarName)); err != nil {
		t.Fatal(err)
	}
	detail, err := store.ReadDraft("bare-draft")
	if err != nil {
		t.Fatalf("ReadDraft without sidecar: %v", err)
	}
	if detail.Info.Status != "(no sidecar)" || detail.Info.SuggestedScope != "project" {
		t.Errorf("degraded info = %+v", detail.Info)
	}
}

func TestListDrafts_SurfacesProvenance(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir()}
	side := DraftSidecar{
		Sources:     []DraftSource{{SessionID: "s-1"}, {SessionID: "s-2"}},
		FilterTrace: []FilterTraceEntry{{Stage: "L2", Verdict: "PASS"}},
	}
	if _, err := store.WriteDraft(validDraftMD("provenanced"), side); err != nil {
		t.Fatalf("write: %v", err)
	}
	drafts, err := store.ListDrafts()
	if err != nil || len(drafts) != 1 {
		t.Fatalf("ListDrafts: %v %+v", err, drafts)
	}
	if len(drafts[0].Sources) != 2 || drafts[0].FilterTrace[0].Stage != "L2" {
		t.Errorf("provenance missing from inbox row: %+v", drafts[0])
func TestStrengthenDraft(t *testing.T) {
	store := &DraftStore{UserDir: t.TempDir(), Now: pinnedClock(t)}
	if _, err := store.WriteDraft(validDraftMD("grow-me"), DraftSidecar{
		Status: DraftStatusWeak,
		Sources: []DraftSource{
			{SessionID: "s1", ProjectID: "p1", At: "2026-11-12T09:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// First strengthen: same project, second session → recurrence 2 →
	// reviewable, scope stays project.
	side, err := store.StrengthenDraft("grow-me",
		DraftSource{SessionID: "s2", ProjectID: "p1", At: "2026-11-13T09:00:00Z"},
		[]string{"more evidence"},
		[]FilterTraceEntry{{Stage: "L3", Verdict: "ACCEPT"}},
	)
	if err != nil {
		t.Fatalf("strengthen: %v", err)
	}
	if side.Recurrence != 2 || side.Status != DraftStatusReviewable {
		t.Errorf("after 2nd source: recurrence=%d status=%s, want 2/reviewable", side.Recurrence, side.Status)
	}
	if side.SuggestedScope != "project" {
		t.Errorf("scope = %s, want project (single project so far)", side.SuggestedScope)
	}
	if len(side.Sources) != 2 || len(side.Evidence) != 1 || len(side.FilterTrace) != 1 {
		t.Errorf("provenance not appended: %+v", side)
	}

	// Cross-project strengthen → scope upgrades to user (PRD §5.4).
	side, err = store.StrengthenDraft("grow-me",
		DraftSource{SessionID: "s3", ProjectID: "p2", At: "2026-11-13T10:00:00Z"}, nil, nil)
	if err != nil {
		t.Fatalf("strengthen 2: %v", err)
	}
	if side.Recurrence != 3 || side.SuggestedScope != "user" {
		t.Errorf("after 3rd source: recurrence=%d scope=%s, want 3/user", side.Recurrence, side.SuggestedScope)
	}

	// SKILL.md untouched — first write wins for content.
	draftsDir, err := store.DraftsDir()
	if err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(draftsDir, "grow-me", "SKILL.md"))
	if err != nil || string(md) != string(validDraftMD("grow-me")) {
		t.Errorf("SKILL.md must not change on strengthen")
	}

	// Strengthening a missing draft errors; a bad name is refused.
	if _, err := store.StrengthenDraft("no-such-draft", DraftSource{}, nil, nil); err == nil {
		t.Error("missing draft must error")
	}
	if _, err := store.StrengthenDraft("../escape", DraftSource{}, nil, nil); err == nil {
		t.Error("path-escape name must be refused")
	}
}

func TestGCDrafts_AgeAndCap(t *testing.T) {
	now := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC) // GC moment: 15d after pinnedClock base
	clock := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	store := &DraftStore{UserDir: t.TempDir(), Now: func() time.Time { return clock }}

	// stale-draft: untouched since creation (15d old) → aged out.
	if _, err := store.WriteDraft(validDraftMD("stale-draft"), DraftSidecar{}); err != nil {
		t.Fatal(err)
	}
	// fresh-draft: created old but strengthened recently → survives
	// age GC (age runs on UpdatedAt; activity keeps a draft alive).
	if _, err := store.WriteDraft(validDraftMD("fresh-draft"), DraftSidecar{
		CreatedAt: "2026-11-01T09:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	yesterday := now.Add(-24 * time.Hour) // strengthen happened yesterday
	clock = yesterday
	if _, err := store.StrengthenDraft("fresh-draft", DraftSource{SessionID: "s", ProjectID: "p"}, nil, nil); err != nil {
		t.Fatal(err)
	}

	removed, err := store.GCDrafts(now, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "stale-draft" {
		t.Fatalf("age GC removed %v, want [stale-draft]", removed)
	}
	all, _ := store.ListAllDrafts()
	if len(all) != 1 || all[0].Name != "fresh-draft" {
		t.Fatalf("survivors = %+v", all)
	}

	// Cap: write 12 drafts, cap at 10 → 2 oldest-by-CreatedAt evicted.
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		name := "cap-draft-" + string(rune('a'+i))
		createdAt := base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		if _, err := store.WriteDraft(validDraftMD(name), DraftSidecar{CreatedAt: createdAt}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err = store.GCDrafts(now, time.Duration(100*24)*time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	all, _ = store.ListAllDrafts()
	if len(all) != 10 {
		t.Fatalf("after cap GC: %d drafts, want 10 (removed %v)", len(all), removed)
	}
	for _, d := range all {
		if d.Name == "cap-draft-a" || d.Name == "cap-draft-b" {
			t.Errorf("oldest draft %s survived cap eviction", d.Name)
		}
	}
}
