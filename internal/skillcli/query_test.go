package skillcli

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
		{TS: now, Name: "tracked", Model: "deepseek-v4-flash", Provider: "deepseek"},
		{TS: now, Name: "tracked", Model: "deepseek-v4-flash", Provider: "deepseek"},
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
	for _, want := range []string{"deepseek-v4-flash", "gpt-4", "deepseek", "openai"} {
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
	for range 5 {
		entries = append(entries, skillstats.Entry{TS: now, Name: "alpha", Provider: "deepseek"})
	}
	for range 2 {
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

// Distillation provenance rows (provider=distill-auto, M-SD2) share
// .stats.jsonl but must not count as skill calls — a much-distilled
// draft would otherwise top `seek skill stats`.
func TestAggregate_SkipsDistillAutoRows(t *testing.T) {
	entries := []skillstats.Entry{
		{TS: "2026-11-13T09:00:00Z", Name: "real-skill", Model: "m"},
		{TS: "2026-11-13T09:01:00Z", Name: "draft-name", Provider: skillstats.ProviderDistillAuto},
		{TS: "2026-11-13T09:02:00Z", Name: "draft-name", Provider: skillstats.ProviderDistillAuto},
	}
	agg := aggregate(entries, 0, time.Now())
	if len(agg) != 1 {
		t.Fatalf("aggregated %d names, want 1 (distill rows excluded): %+v", len(agg), agg)
	}
	if _, ok := agg["real-skill"]; !ok {
		t.Errorf("real-skill missing from aggregation: %+v", agg)
	}
	if _, ok := agg["draft-name"]; ok {
		t.Errorf("distill-auto row leaked into call counts")
	}
}

// ---------- stats --distill (funnel report, 三期 c_jqwd72) ----------

// normalizeTableSpaces collapses runs of tabs/spaces into single
// spaces, so tabwriter-padded table rows can be matched with plain
// Contains checks.
func normalizeTableSpaces(s string) string {
	var sb strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !space {
				sb.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		sb.WriteRune(r)
	}
	return sb.String()
}

// seedFunnelStats writes a realistic mixed file: one real call row,
// staged funnel rows across the layers, and a pre-三期 produce row
// (provider set, stage empty) that must still surface rather than
// vanish from the report.
func seedFunnelStats(t *testing.T) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{
		{TS: now, Name: "go-test-runner", Provider: "deepseek"},
		{TS: now, Name: "", SessionID: "s1", Provider: skillstats.ProviderDistillAuto, Stage: "L0", Verdict: "PASS"},
		{TS: now, Name: "", SessionID: "s2", Provider: skillstats.ProviderDistillAuto, Stage: "L0", Verdict: "PASS"},
		{TS: now, Name: "", SessionID: "s3", Provider: skillstats.ProviderDistillAuto, Stage: "L0", Verdict: "DROP"},
		{TS: now, Name: "bad-name", SessionID: "s1", Provider: skillstats.ProviderDistillAuto, Stage: "shape", Verdict: "REJECT"},
		{TS: now, Name: "collide", SessionID: "s2", Provider: skillstats.ProviderDistillAuto, Stage: "L2", Verdict: "REJECT"},
		{TS: now, Name: "good-one", SessionID: "s1", Provider: skillstats.ProviderDistillAuto, Stage: "L3", Verdict: "ACCEPT"},
		{TS: now, Name: "good-one", SessionID: "s2", Provider: skillstats.ProviderDistillAuto, Stage: "L3", Verdict: "REJECT"},
		{TS: now, Name: "good-one", SessionID: "s1", Provider: skillstats.ProviderDistillAuto, Stage: "L4", Verdict: "DRAFT"},
		{TS: now, Name: "good-one", SessionID: "s2", Provider: skillstats.ProviderDistillAuto, Stage: "L4", Verdict: "STRENGTHEN"},
		{TS: now, Name: "legacy-draft", SessionID: "s0", Provider: skillstats.ProviderDistillAuto}, // pre-三期
	})
}

func TestSkillCmd_Stats_DistillFunnelReport(t *testing.T) {
	withSeekHome(t)
	seedFunnelStats(t)

	stdout, _, err := runSkill("stats", "--distill")
	if err != nil {
		t.Fatal(err)
	}
	// tabwriter pads cells; compare on whitespace-normalised lines.
	norm := normalizeTableSpaces(stdout)
	// Call rows are NOT funnel events.
	if strings.Contains(norm, "go-test-runner") {
		t.Errorf("call row leaked into the funnel report:\n%s", stdout)
	}
	// Header + the layer counts.
	for _, want := range []string{
		"STAGE VERDICT COUNT",
		"L0 PASS 2", "L0 DROP 1",
		"shape REJECT 1",
		"L2 REJECT 1",
		"L3 ACCEPT 1", "L3 REJECT 1",
		"L4 DRAFT 1", "L4 STRENGTHEN 1",
	} {
		if !strings.Contains(norm, want) {
			t.Errorf("funnel report missing %q:\n%s", want, stdout)
		}
	}
	// Stages appear in funnel order: the rank of every known stage
	// never decreases down the table (equal ranks are the same stage's
	// multiple verdicts).
	last := -1
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		r := funnelStageRank(fields[0])
		if r >= len(funnelStageOrder) {
			continue // unknown/legacy stage — sorted after, not ranked
		}
		if r < last {
			t.Errorf("stage %s out of funnel order in:\n%s", fields[0], stdout)
		}
		last = r
	}
}

func TestSkillCmd_Stats_DistillJSON(t *testing.T) {
	withSeekHome(t)
	seedFunnelStats(t)

	stdout, _, err := runSkill("stats", "--distill", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var funnel map[string]map[string]int
	if err := json.Unmarshal([]byte(stdout), &funnel); err != nil {
		t.Fatalf("--distill --json is not a stage→verdict→count map: %v\n%s", err, stdout)
	}
	if funnel["L0"]["PASS"] != 2 || funnel["L3"]["ACCEPT"] != 1 || funnel["L4"]["STRENGTHEN"] != 1 {
		t.Errorf("funnel JSON counts wrong: %+v", funnel)
	}
	// The pre-三期 row (no stage) still counts — under the empty key.
	if funnel[""]["DRAFT"] != 0 || funnel[""][""] != 1 {
		t.Errorf("legacy unstaged row must surface under the empty stage: %+v", funnel)
	}
}

func TestSkillCmd_Stats_DistillEmpty(t *testing.T) {
	withSeekHome(t)
	stdout, _, err := runSkill("stats", "--distill")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "no distillation telemetry") {
		t.Errorf("empty funnel report should explain itself:\n%s", stdout)
	}
}

func TestAggregateFunnel_SinceFiltersOldRows(t *testing.T) {
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)
	entries := []skillstats.Entry{
		{TS: old, Provider: skillstats.ProviderDistillAuto, Stage: "L0", Verdict: "DROP"},
		{TS: fresh, Provider: skillstats.ProviderDistillAuto, Stage: "L0", Verdict: "PASS"},
		// Not a distill row at all — ignored whatever its age.
		{TS: fresh, Provider: "deepseek", Stage: "L0", Verdict: "PASS"},
	}
	funnel := aggregateFunnel(entries, 30*24*time.Hour, time.Now())
	if funnel["L0"]["DROP"] != 0 {
		t.Errorf("90-day-old row leaked past --since: %+v", funnel)
	}
	if funnel["L0"]["PASS"] != 1 {
		t.Errorf("fresh row missing: %+v", funnel)
	}
}
