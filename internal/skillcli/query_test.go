package skillcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/skill"
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

// ---------- stats --idle (feature-memory-skill-bridge §6 / M-B4) ----------

// chdirTemp moves CWD to a tempdir for the duration of one test so
// loadAll()'s ProjectDir probe doesn't pick up the dev tree.
func chdirTemp(t *testing.T) {
	t.Helper()
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// installSkill installs a one-line user-level fixture skill so the
// loaded Set (not just builtins) has join partners.
func installSkill(t *testing.T, name string) {
	t.Helper()
	src := writeFixtureSkill(t, t.TempDir(), "pkg-"+name, name, "fixture for "+name)
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}
}

func TestSkillCmd_Stats_Idle_JoinCompleteness(t *testing.T) {
	withSeekHome(t)
	chdirTemp(t)
	installSkill(t, "alpha-active")
	installSkill(t, "beta-old")
	installSkill(t, "gamma-never")

	fresh := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{
		{TS: fresh, Name: "alpha-active", Provider: "deepseek"}, // in-window → excluded
		{TS: old, Name: "beta-old", Provider: "deepseek"},       // pre-window only → idle, last_used = old
		{TS: fresh, Name: "delta-ghost", Provider: "deepseek"},  // stats row, not loaded → ignored
		// distill provenance rows not counting as calls is covered at
		// unit level in TestIdleRows_JoinAndOrder below.
	})

	stdout, _, err := runSkill("stats", "--idle", "720h")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"beta-old", "gamma-never", "never", old, "user"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("idle report missing %q:\n%s", want, stdout)
		}
	}
	for _, absent := range []string{"alpha-active", "delta-ghost"} {
		if strings.Contains(stdout, absent) {
			t.Errorf("idle report leaked %q (join must be Set-driven):\n%s", absent, stdout)
		}
	}
	if !strings.Contains(stdout, "LAST_USED") || !strings.Contains(stdout, "SOURCE") {
		t.Errorf("idle report missing column header:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_Idle_OrdersNeverFirstThenAscending(t *testing.T) {
	withSeekHome(t)
	chdirTemp(t)
	installSkill(t, "recent-old")
	installSkill(t, "ancient-old")
	installSkill(t, "never-one")

	older := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	newer := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{
		{TS: newer, Name: "recent-old", Provider: "deepseek"},
		{TS: older, Name: "ancient-old", Provider: "deepseek"},
	})

	stdout, _, err := runSkill("stats", "--idle", "720h")
	if err != nil {
		t.Fatal(err)
	}
	iNever := strings.Index(stdout, "never-one")
	iAncient := strings.Index(stdout, "ancient-old")
	iRecent := strings.Index(stdout, "recent-old")
	if iNever == -1 || iAncient == -1 || iRecent == -1 {
		t.Fatalf("missing rows:\n%s", stdout)
	}
	if !(iNever < iAncient && iAncient < iRecent) {
		t.Errorf("want never < ancient-old < recent-old (last_used ascending); got:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_Idle_EmptyStatsFile(t *testing.T) {
	withSeekHome(t)
	chdirTemp(t)
	installSkill(t, "zeta")
	// No writeStats at all — no .stats.jsonl exists. Every loaded
	// skill (installed + builtins) must come back as never-used, not
	// as an error or an empty report.
	stdout, _, err := runSkill("stats", "--idle", "720h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "zeta") || !strings.Contains(stdout, "never") {
		t.Errorf("empty stats should list every skill as never:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_Idle_ToleratesGarbageLines(t *testing.T) {
	withSeekHome(t)
	chdirTemp(t)
	installSkill(t, "beta-old")
	installSkill(t, "alpha-active")

	fresh := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	// Hand-write the file: valid rows interleaved with garbage and a
	// blank line (truncated-tail shape). Read() must skip the junk,
	// so the join still sees the valid rows.
	path, err := paths.UserSkillStats()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "this line is not json\n" +
		"{\"ts\":\"" + fresh + "\",\"name\":\"alpha-active\",\"provider\":\"deepseek\"}\n" +
		"\n" +
		"{broken json\n" +
		"{\"ts\":\"" + old + "\",\"name\":\"beta-old\",\"provider\":\"deepseek\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSkill("stats", "--idle", "720h")
	if err != nil {
		t.Fatalf("garbage lines must not fail the report: %v", err)
	}
	if strings.Contains(stdout, "alpha-active") {
		t.Errorf("valid fresh row should still exclude alpha-active:\n%s", stdout)
	}
	if !strings.Contains(stdout, "beta-old") || !strings.Contains(stdout, old) {
		t.Errorf("valid old row should survive the garbage:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_Idle_JSON(t *testing.T) {
	withSeekHome(t)
	chdirTemp(t)
	installSkill(t, "beta-old")
	installSkill(t, "gamma-never")

	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	writeStats(t, []skillstats.Entry{{TS: old, Name: "beta-old", Provider: "deepseek"}})

	stdout, _, err := runSkill("stats", "--idle", "720h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, stdout)
	}
	byName := map[string]map[string]any{}
	for _, r := range rows {
		byName[r["name"].(string)] = r
	}
	beta, ok := byName["beta-old"]
	if !ok {
		t.Fatalf("beta-old missing from idle JSON:\n%s", stdout)
	}
	if beta["last_used"] != old {
		t.Errorf("beta-old last_used = %v, want %q", beta["last_used"], old)
	}
	if beta["source"] != "user" {
		t.Errorf("beta-old source = %v, want user", beta["source"])
	}
	gamma, ok := byName["gamma-never"]
	if !ok {
		t.Fatalf("gamma-never missing from idle JSON:\n%s", stdout)
	}
	if _, has := gamma["last_used"]; has {
		t.Errorf("never-used row must omit last_used:\n%s", stdout)
	}
}

func TestSkillCmd_Stats_Idle_FlagConflicts(t *testing.T) {
	withSeekHome(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--idle", "720h", "--distill"}, "cannot be combined"},
		{[]string{"--idle", "720h", "--since", "24h"}, "cannot be combined"},
		{[]string{"--idle", "720h", "--top", "5"}, "cannot be combined"},
		{[]string{"--idle", "0"}, "positive duration"},
		{[]string{"--idle", "-1h"}, "positive duration"},
	}
	for _, c := range cases {
		_, _, err := runSkill(append([]string{"stats"}, c.args...)...)
		if err == nil {
			t.Errorf("args=%v should error", c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("args=%v err = %v, want it to mention %q", c.args, err, c.want)
		}
	}
}

// The unit-level join, free of builtin noise: order, ghost rows,
// distill provenance rows, and the never/timestamp distinction.
func TestIdleRows_JoinAndOrder(t *testing.T) {
	now := time.Now()
	fresh := now.UTC().Format(time.RFC3339)
	old1 := now.Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	old0 := now.Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	entries := []skillstats.Entry{
		{TS: fresh, Name: "active"},
		{TS: old1, Name: "old-recent"},
		{TS: old0, Name: "old-ancient"},
		{TS: fresh, Name: "ghost"}, // stats row, no loaded skill → ignored
		// Provenance rows are not calls: a loaded skill with only
		// distill-auto rows is idle AND never-used.
		{TS: fresh, Name: "only-distilled", Provider: skillstats.ProviderDistillAuto, Stage: "L4", Verdict: "DRAFT"},
	}
	set := skill.NewSet()
	for _, n := range []string{"active", "old-recent", "old-ancient", "never-a", "never-b", "only-distilled"} {
		set.Add(&skill.Skill{Name: n})
	}

	rows := idleRows(set, "", entries, 720*time.Hour, now)
	var got []string
	for _, r := range rows {
		got = append(got, r.Name)
	}
	// only-distilled lands in the never group: its only stats row is
	// provenance telemetry, not a call, so it has no last_used at all.
	want := []string{"never-a", "never-b", "only-distilled", "old-ancient", "old-recent"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v (never-first, ties by name, then last_used ascending)", got, want)
		}
	}
	for _, r := range rows {
		if r.Name == "only-distilled" && r.LastUsed != "" {
			t.Errorf("distill-auto rows must not count as calls: %+v", r)
		}
		if r.Name == "old-ancient" && r.LastUsed != old0 {
			t.Errorf("old-ancient LastUsed = %q, want the pre-window call time %q", r.LastUsed, old0)
		}
	}
}

// nil Set must yield an empty (not null) slice so --json stays [].
func TestIdleRows_NilSetIsEmptyJSONArray(t *testing.T) {
	rows := idleRows(nil, "", nil, 720*time.Hour, time.Now())
	if rows == nil || len(rows) != 0 {
		t.Errorf("nil set should give empty non-nil slice, got %#v", rows)
	}
}
