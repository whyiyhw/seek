package skillcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(text, "seek skill install") {
		t.Errorf("install hint (the human gate) missing:\n%s", text)
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
