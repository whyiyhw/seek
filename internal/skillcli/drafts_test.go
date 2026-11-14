package skillcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillmgr"
)

func seedDraft(t *testing.T, userDir, name, createdAt string) {
	t.Helper()
	store := &skillmgr.DraftStore{UserDir: userDir}
	md := []byte("---\nname: " + name + "\ndescription: |\n  When X, do Y.\nversion: 0.1.0-draft\nlicense: MIT\nauthor: distilled\n---\n\n## Steps\n1. do it\n")
	side := skillmgr.DraftSidecar{
		Status:     skillmgr.DraftStatusReviewable,
		Recurrence: 2,
		CreatedAt:  createdAt,
	}
	if _, err := store.WriteDraft(md, side); err != nil {
		t.Fatalf("seed draft %s: %v", name, err)
	}
}

func TestSkillDrafts_EmptyInbox(t *testing.T) {
	withSeekHome(t)
	var out, errBuf bytes.Buffer
	if err := Run([]string{"drafts"}, &out, &errBuf); err != nil {
		t.Fatalf("drafts: %v", err)
	}
	if !strings.Contains(out.String(), "no skill drafts") {
		t.Errorf("empty inbox message missing: %q", out.String())
	}
}

func TestSkillDrafts_ListsRowsWithInstallHint(t *testing.T) {
	userDir := withSeekHome(t)
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	seedDraft(t, userDir, "fix-tui-bug", base.Add(-48*time.Hour).Format(time.RFC3339))
	seedDraft(t, userDir, "repro-flaky-first", base.Add(-24*time.Hour).Format(time.RFC3339))

	var out, errBuf bytes.Buffer
	if err := Run([]string{"drafts"}, &out, &errBuf); err != nil {
		t.Fatalf("drafts: %v", err)
	}
	text := out.String()
	// Oldest first.
	if strings.Index(text, "fix-tui-bug") > strings.Index(text, "repro-flaky-first") {
		t.Errorf("drafts must list oldest first:\n%s", text)
	}
	// c_jvn3ky changed the row hint from the bare `seek skill install`
	// pointer to the inbox's own review flow — approve is now the
	// one-command human gate (still the same install chain underneath).
	if !strings.Contains(text, "seek skill drafts show") || !strings.Contains(text, "seek skill drafts approve") {
		t.Errorf("review-flow hint missing:\n%s", text)
	}
}

func TestSkillDrafts_JSON(t *testing.T) {
	userDir := withSeekHome(t)
	seedDraft(t, userDir, "fix-tui-bug", "2026-11-11T09:00:00Z")

	var out, errBuf bytes.Buffer
	if err := Run([]string{"drafts", "--json"}, &out, &errBuf); err != nil {
		t.Fatalf("drafts --json: %v", err)
	}
	var rows []draftsRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("json decode: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].Name != "fix-tui-bug" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Recurrence != 2 || rows[0].Status != skillmgr.DraftStatusReviewable {
		t.Errorf("row fields = %+v", rows[0])
	}
	if !strings.Contains(rows[0].Dir, ".drafts") {
		t.Errorf("dir must point into .drafts/: %q", rows[0].Dir)
	}
}

func TestSkillDrafts_Rm(t *testing.T) {
	userDir := withSeekHome(t)
	seedDraft(t, userDir, "unwanted-draft", "2026-11-11T09:00:00Z")

	var out, errBuf bytes.Buffer
	if err := Run([]string{"drafts", "rm", "unwanted-draft"}, &out, &errBuf); err != nil {
		t.Fatalf("drafts rm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userDir, ".drafts", "unwanted-draft")); !os.IsNotExist(err) {
		t.Errorf("draft dir still exists after rm")
	}
	// And the inbox reflects it.
	out.Reset()
	if err := Run([]string{"drafts"}, &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no skill drafts") {
		t.Errorf("inbox not empty after rm: %q", out.String())
	}
}

func TestSkillDrafts_RmRequiresName(t *testing.T) {
	withSeekHome(t)
	var out, errBuf bytes.Buffer
	if err := Run([]string{"drafts", "rm"}, &out, &errBuf); err == nil {
		t.Fatal("rm without a name must error")
	}
}

// ---------- review flow: show / approve / reject (card c_jvn3ky) ----------

// richDraftMD is a draft whose description near-duplicates an
// installed skill — the shape `drafts show`'s nearest-skill diff
// exists for.
func richDraftMD(name, desc string) []byte {
	return []byte("---\nname: " + name + "\ndescription: |\n  " + desc + "\nversion: 0.1.0-draft\nlicense: MIT\nauthor: distilled\n---\n\n## When to use\nTUI state-machine bugs.\n\n## Steps\n1. write a failing test that reproduces the swallowed key\n2. fix the handler\n\n## Verification\ngo test green\n")
}

// seedRichDraft lands a draft with the full provenance sidecar: source
// session, funnel trace, scope suggestion.
func seedRichDraft(t *testing.T, userDir, name, desc string, side skillmgr.DraftSidecar) {
	t.Helper()
	store := &skillmgr.DraftStore{UserDir: userDir}
	if _, err := store.WriteDraft(richDraftMD(name, desc), side); err != nil {
		t.Fatalf("seed rich draft %s: %v", name, err)
	}
}

const reviewFlowDesc = "When fixing TUI state machine bugs where a modal key handler swallows keys after exit, follow this reproduction-first sequence of steps."

// TestSkillDrafts_FullReviewFlow is the card's acceptance test: fake
// drafts drive the whole inbox — list (name / source session /
// produced-at / funnel verdict), show (full text + nearest-skill
// diff), approve (existing skillmgr install chain + draft removal) —
// with the load-bearing wall asserted on BOTH sides: the draft is
// invisible to skill.Load before approve, and visible exactly after.
func TestSkillDrafts_FullReviewFlow(t *testing.T) {
	userDir := withSeekHome(t)

	// An installed skill the draft nearly duplicates — the nearest
	// panel must find it via the L2 trigram metric.
	writeFixtureSkill(t, userDir, "fix-tui-state-bug", "fix-tui-state-bug", reviewFlowDesc)

	side := skillmgr.DraftSidecar{
		Status:         skillmgr.DraftStatusReviewable,
		Recurrence:     2,
		SuggestedScope: "user",
		CreatedAt:      "2026-11-12T09:00:00Z",
		Sources:        []skillmgr.DraftSource{{SessionID: "20261112-090001-a3b9", ProjectID: "011c0e58aa79deb2", At: "2026-11-12T09:00:00Z"}},
		FilterTrace:    []skillmgr.FilterTraceEntry{{Stage: "L2", Verdict: "GREY", Reason: "borderline overlap"}},
	}
	seedRichDraft(t, userDir, "fix-tui-modal-keys", reviewFlowDesc, side)

	// WALL, pre-approve: the loader must not see the draft. This is
	// skillmgr's TestDraftsInvisibleToLoader continued through the
	// CLI's own tree — the review flow being mid-air changes nothing.
	pre, _, err := skill.Load(skill.LoadOptions{UserSkillsDir: userDir})
	if err != nil {
		t.Fatalf("pre Load: %v", err)
	}
	if pre.Get("fix-tui-modal-keys") != nil {
		t.Fatal("draft leaked into the loaded set before approve — the review gate is broken")
	}
	if strings.Contains(pre.Manifest(), "fix-tui-modal-keys") {
		t.Fatal("draft leaked into the manifest before approve")
	}

	// List: name / source session / produced-at / funnel verdict.
	out, _, err := runSkill("drafts")
	if err != nil {
		t.Fatalf("drafts: %v", err)
	}
	for _, want := range []string{
		"fix-tui-modal-keys", "session 20261112-090001-a3b9", "2026-11-12", "L2:GREY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list row missing %q:\n%s", want, out)
		}
	}

	// Show: full SKILL.md text + diff against the nearest skill.
	out, _, err = runSkill("drafts", "show", "fix-tui-modal-keys")
	if err != nil {
		t.Fatalf("drafts show: %v", err)
	}
	for _, want := range []string{
		"## Steps", // full draft body
		"nearest installed skill: fix-tui-state-bug",
		"--- a/", "+++ b/", // unified diff headers
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}

	// Approve: sidecar suggests user scope → installs into the user
	// tree through the existing install chain, then removes the draft.
	out, _, err = runSkill("drafts", "approve", "fix-tui-modal-keys")
	if err != nil {
		t.Fatalf("drafts approve: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: user, sidecar suggestion") {
		t.Errorf("approve output should state the scope and its origin:\n%s", out)
	}
	installed := filepath.Join(userDir, "fix-tui-modal-keys", "SKILL.md")
	if got, err := os.ReadFile(installed); err != nil || string(got) != string(richDraftMD("fix-tui-modal-keys", reviewFlowDesc)) {
		t.Errorf("installed SKILL.md wrong (%v)", err)
	}
	// User-scope installs carry the .install.json record (v2 semantics).
	if _, err := os.Stat(filepath.Join(userDir, "fix-tui-modal-keys", ".install.json")); err != nil {
		t.Errorf("user-scope install must write .install.json: %v", err)
	}
	// The draft left the inbox.
	drafts, err := (&skillmgr.DraftStore{UserDir: userDir}).ListDrafts()
	if err != nil || len(drafts) != 0 {
		t.Errorf("approved draft must leave the inbox: %v %+v", err, drafts)
	}

	// WALL, post-approve: approve was the only door across it.
	post, _, err := skill.Load(skill.LoadOptions{UserSkillsDir: userDir})
	if err != nil {
		t.Fatalf("post Load: %v", err)
	}
	if post.Get("fix-tui-modal-keys") == nil {
		t.Fatal("approved skill not visible to the loader after install")
	}
}

// A failed approve (name collision, no --force) must leave the draft
// in the inbox — deleting it would turn a collision into data loss.
func TestSkillDrafts_ApproveKeepsDraftOnCollision(t *testing.T) {
	userDir := withSeekHome(t)
	writeFixtureSkill(t, userDir, "clash-name", "clash-name", "an installed description")
	seedRichDraft(t, userDir, "clash-name", "an installed description with extra steps", skillmgr.DraftSidecar{SuggestedScope: "user"})

	out, _, err := runSkill("drafts", "approve", "clash-name")
	if err == nil {
		t.Fatalf("approve onto an existing name must fail:\n%s", out)
	}
	if !strings.Contains(err.Error(), "draft kept") {
		t.Errorf("error should say the draft survived: %v", err)
	}
	drafts, _ := (&skillmgr.DraftStore{UserDir: userDir}).ListDrafts()
	if len(drafts) != 1 || drafts[0].Name != "clash-name" {
		t.Errorf("draft must survive the failed approve: %+v", drafts)
	}
}

func TestSkillDrafts_ApproveForceReplaces(t *testing.T) {
	userDir := withSeekHome(t)
	writeFixtureSkill(t, userDir, "clash-name", "clash-name", "an installed description")
	seedRichDraft(t, userDir, "clash-name", "an installed description with extra steps", skillmgr.DraftSidecar{SuggestedScope: "user"})

	if _, _, err := runSkill("drafts", "approve", "--force", "clash-name"); err != nil {
		t.Fatalf("approve --force: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(userDir, "clash-name", "SKILL.md"))
	if err != nil || !strings.Contains(string(got), "extra steps") {
		t.Errorf("--force must replace the installed copy with the draft (%v)", err)
	}
	if drafts, _ := (&skillmgr.DraftStore{UserDir: userDir}).ListDrafts(); len(drafts) != 0 {
		t.Errorf("draft must be gone after forced approve: %+v", drafts)
	}
}

func TestSkillDrafts_ApproveProjectScope(t *testing.T) {
	userDir := withSeekHome(t)
	proj := t.TempDir()
	t.Chdir(proj)
	seedRichDraft(t, userDir, "proj-skill", "a project-shaped procedure", skillmgr.DraftSidecar{SuggestedScope: "project"})

	out, _, err := runSkill("drafts", "approve", "proj-skill")
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: project, sidecar suggestion") {
		t.Errorf("scope line wrong:\n%s", out)
	}
	// Project installs land in <cwd>/.seek/skills/ and deliberately
	// skip .install.json (v2 project semantics — team-shared via git).
	if _, err := os.Stat(filepath.Join(proj, ".seek", "skills", "proj-skill", "SKILL.md")); err != nil {
		t.Errorf("project install missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".seek", "skills", "proj-skill", ".install.json")); !os.IsNotExist(err) {
		t.Errorf("project install must not carry .install.json")
	}
	if _, err := os.Stat(filepath.Join(userDir, "proj-skill")); !os.IsNotExist(err) {
		t.Errorf("project approve must not touch the user tree")
	}
}

// --project overrides a user suggestion and vice versa; both at once
// is a contradiction.
func TestSkillDrafts_ApproveScopeFlags(t *testing.T) {
	userDir := withSeekHome(t)
	proj := t.TempDir()
	t.Chdir(proj)
	seedRichDraft(t, userDir, "scope-flag-skill", "some procedure", skillmgr.DraftSidecar{SuggestedScope: "user"})

	out, _, err := runSkill("drafts", "approve", "--project", "scope-flag-skill")
	if err != nil {
		t.Fatalf("approve --project: %v\n%s", err, out)
	}
	if !strings.Contains(out, "scope: project, --project") {
		t.Errorf("scope line wrong:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(proj, ".seek", "skills", "scope-flag-skill", "SKILL.md")); err != nil {
		t.Errorf("--project install missing: %v", err)
	}
}

func TestSkillDrafts_ApproveFlagConflict(t *testing.T) {
	withSeekHome(t)
	if _, _, err := runSkill("drafts", "approve", "x", "--project", "--user"); err == nil {
		t.Fatal("--project + --user must error")
	}
}

func TestSkillDrafts_Reject(t *testing.T) {
	userDir := withSeekHome(t)
	seedDraft(t, userDir, "bad-draft", "2026-11-11T09:00:00Z")

	out, _, err := runSkill("drafts", "reject", "bad-draft")
	if err != nil {
		t.Fatalf("drafts reject: %v", err)
	}
	if !strings.Contains(out, "rejected draft bad-draft") {
		t.Errorf("reject output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(userDir, ".drafts", "bad-draft")); !os.IsNotExist(err) {
		t.Error("reject must delete the draft")
	}
}

// Unknown names and path-escape attempts are refused across the board.
func TestSkillDrafts_UnknownAndEscapedNames(t *testing.T) {
	withSeekHome(t)
	for _, args := range [][]string{
		{"drafts", "show", "ghost"},
		{"drafts", "approve", "ghost"},
		{"drafts", "reject", "ghost"},
		{"drafts", "show", "../evil"},
		{"drafts", "approve", "../evil"},
	} {
		if _, _, err := runSkill(args...); err == nil {
			t.Errorf("%v must error", args)
		}
	}
}

// A draft that shares no trigrams with anything installed (Chinese
// fixture vs English builtins) prints the honest "none" panel instead
// of a noise diff.
func TestSkillDrafts_ShowNoNearestSkill(t *testing.T) {
	userDir := withSeekHome(t)
	seedRichDraft(t, userDir, "wenzhong-caozuo", "当仓库的依赖图出现环时，按拓扑顺序逐个解除。", skillmgr.DraftSidecar{})

	out, _, err := runSkill("drafts", "show", "wenzhong-caozuo")
	if err != nil {
		t.Fatalf("drafts show: %v", err)
	}
	if !strings.Contains(out, "nearest installed skill: none") {
		t.Errorf("expected the none panel:\n%s", out)
	}
}

func TestSkillDrafts_JSONCarriesProvenance(t *testing.T) {
	userDir := withSeekHome(t)
	side := skillmgr.DraftSidecar{
		Sources:     []skillmgr.DraftSource{{SessionID: "s-json", ProjectID: "p-json", At: "2026-11-12T09:00:00Z"}},
		FilterTrace: []skillmgr.FilterTraceEntry{{Stage: "L2", Verdict: "GREY"}},
	}
	seedRichDraft(t, userDir, "json-draft", "a description", side)

	out, _, err := runSkill("drafts", "--json")
	if err != nil {
		t.Fatalf("drafts --json: %v", err)
	}
	var rows []draftsRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json decode: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0].Name != "json-draft" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(rows[0].Sources) != 1 || rows[0].Sources[0].SessionID != "s-json" {
		t.Errorf("sources not in JSON: %+v", rows[0])
	}
	if len(rows[0].FilterTrace) != 1 || rows[0].FilterTrace[0].Verdict != "GREY" {
		t.Errorf("filter_trace not in JSON: %+v", rows[0])
	}
}
