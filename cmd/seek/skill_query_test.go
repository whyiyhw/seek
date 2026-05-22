package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/skillstats"
)

// writeStats drops a .stats.jsonl with the given entries into the
// user's SEEK_HOME skills dir. Used to seed list/status/stats output.
func writeStats(t *testing.T, entries []skillstats.Entry) {
	t.Helper()
	path, err := paths.UserSkillStats()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	w := skillstats.New(path)
	for _, e := range entries {
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

// ---------- list ----------

func TestSkillCmd_List_EmptyHomeReportsNoSkills(t *testing.T) {
	withSeekHome(t)
	// We're not running from a real seek project, but loadAll uses
	// CWD as ProjectDir. Force CWD to a tempdir to avoid pulling in
	// anything from the dev tree.
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("list")
	if err != nil {
		t.Fatal(err)
	}
	// Builtins always load — we don't expect "no skills loaded"
	// here in practice. But the column header must be present.
	if !strings.Contains(stdout, "NAME") {
		t.Errorf("expected table header; got:\n%s", stdout)
	}
	// And we expect the always-present go-test-runner builtin.
	if !strings.Contains(stdout, "go-test-runner") {
		t.Errorf("builtin go-test-runner not listed:\n%s", stdout)
	}
}

func TestSkillCmd_List_ShowsInstalledUserSkill(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "listed-skill", "x")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "listed-skill") {
		t.Errorf("install didn't surface in list; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "user") {
		t.Errorf("list missing 'user' source label:\n%s", stdout)
	}
}

func TestSkillCmd_List_SourceFilter(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "user-only", "x")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("list", "--source", "user")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "user-only") {
		t.Errorf("filter swallowed user skill; got:\n%s", stdout)
	}
	if strings.Contains(stdout, "go-test-runner") {
		t.Errorf("--source=user must hide builtins; got:\n%s", stdout)
	}
}

func TestSkillCmd_List_JSON(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, stdout)
	}
	if len(rows) == 0 {
		t.Errorf("expected at least the builtins, got empty array")
	}
}

// ---------- status ----------

func TestSkillCmd_Status_UserInstall(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "diag-skill", "diagnostic")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("status", "diag-skill")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"diag-skill",
		"type",
		"source_tier   user",
		"install_source",
		"type",
		"local",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status missing %q:\n%s", want, stdout)
		}
	}
}

func TestSkillCmd_Status_WithStatsHistogram(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "tracked", "x")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{
		{TS: now, Name: "tracked", Model: "deepseek-chat", Provider: "deepseek"},
		{TS: now, Name: "tracked", Model: "deepseek-chat", Provider: "deepseek"},
		{TS: now, Name: "tracked", Model: "gpt-4", Provider: "openai"},
	})

	stdout, _, err := runSkill("status", "tracked")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "calls         3") {
		t.Errorf("status didn't aggregate calls; got:\n%s", stdout)
	}
	// Models / providers must appear with counts.
	for _, want := range []string{"deepseek-chat", "gpt-4", "deepseek", "openai"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status missing %q:\n%s", want, stdout)
		}
	}
}

func TestSkillCmd_Status_NotFound(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_, _, err := runSkill("status", "no-such-skill")
	if err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Errorf("err = %v, want it to say not loaded", err)
	}
}

func TestSkillCmd_Status_JSON(t *testing.T) {
	withSeekHome(t)
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "jsonable", "x")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("status", "--json", "jsonable")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, stdout)
	}
	if report["name"] != "jsonable" {
		t.Errorf("report.name = %v", report["name"])
	}
}

// ---------- stats ----------

func TestSkillCmd_Stats_EmptyHome(t *testing.T) {
	withSeekHome(t)
	stdout, _, err := runSkill("stats")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "no skill calls") {
		t.Errorf("expected empty-window message; got:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_TopNDescByCount(t *testing.T) {
	withSeekHome(t)
	now := time.Now().UTC().Format(time.RFC3339)
	var entries []skillstats.Entry
	// alpha: 5 calls; beta: 2; gamma: 1
	for i := 0; i < 5; i++ {
		entries = append(entries, skillstats.Entry{TS: now, Name: "alpha", Provider: "deepseek"})
	}
	for i := 0; i < 2; i++ {
		entries = append(entries, skillstats.Entry{TS: now, Name: "beta", Provider: "deepseek"})
	}
	entries = append(entries, skillstats.Entry{TS: now, Name: "gamma", Provider: "deepseek"})
	writeStats(t, entries)

	stdout, _, err := runSkill("stats", "--top", "2")
	if err != nil {
		t.Fatal(err)
	}
	// Top 2 should be alpha, beta — in that order. gamma must not appear.
	idxA := strings.Index(stdout, "alpha")
	idxB := strings.Index(stdout, "beta")
	if idxA == -1 || idxB == -1 {
		t.Fatalf("missing alpha/beta in stats:\n%s", stdout)
	}
	if idxA > idxB {
		t.Errorf("alpha (5 calls) should outrank beta (2 calls); got:\n%s", stdout)
	}
	if strings.Contains(stdout, "gamma") {
		t.Errorf("--top 2 leaked gamma; got:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_SinceFiltersOldEntries(t *testing.T) {
	withSeekHome(t)
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{
		{TS: old, Name: "ancient", Provider: "x"},
		{TS: fresh, Name: "recent", Provider: "x"},
	})

	// Go's time.ParseDuration tops out at "h" (no "d"/"w"), so the
	// flag accepts only hour-based windows. 720h = 30 days.
	stdout, _, err := runSkill("stats", "--since", "720h")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "ancient") {
		t.Errorf("--since=720h leaked 90-day-old entry:\n%s", stdout)
	}
	if !strings.Contains(stdout, "recent") {
		t.Errorf("fresh entry missing from filtered stats:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_JSON(t *testing.T) {
	withSeekHome(t)
	now := time.Now().UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{{TS: now, Name: "x", Provider: "y"}})

	stdout, _, err := runSkill("stats", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, stdout)
	}
	if len(rows) != 1 {
		t.Errorf("got %d rows, want 1", len(rows))
	}
}
