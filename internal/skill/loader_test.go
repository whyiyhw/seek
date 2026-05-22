package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill drops a minimal valid skill file at the given path so the
// loader has something to find. Wraps mkdir + write so tests stay terse.
func writeSkill(t *testing.T, path, name, desc, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_BuiltinAlwaysAvailable(t *testing.T) {
	// Use an empty temp dir for both slots so on-disk discovery
	// finds nothing. The embedded skill should still load.
	tmp := t.TempDir()
	set, stats, err := Load(LoadOptions{
		ProjectDir:    tmp,
		UserSkillsDir: filepath.Join(tmp, "user-skills"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.BySource["builtin"] == 0 {
		t.Fatalf("no embedded skills loaded; stats=%+v", stats.BySource)
	}
	// go-test-runner is the seed builtin; if it's gone, the loader
	// path likely broke.
	if set.Get("go-test-runner") == nil {
		t.Errorf("builtin go-test-runner not present; loaded: %v", listNames(set))
	}
	// dual-model is PRD §4.8.2 Level 2 — its presence is an
	// acceptance criterion for v1.0, so a deletion or rename must
	// trip a test, not just a code review.
	dm := set.Get("dual-model")
	if dm == nil {
		t.Fatalf("builtin dual-model not present; loaded: %v", listNames(set))
	}
	// Description copy is load-bearing: the model only sees this
	// line in the system prompt manifest, so it has to mention
	// triggering conditions (when to invoke). If someone shortens
	// the description to a generic "use for planning", the skill
	// will fire on every trivial task — exactly the PRD §7 risk we
	// flagged.
	if !strings.Contains(dm.Description, "non-trivial") &&
		!strings.Contains(dm.Description, "multi-step") {
		t.Errorf("dual-model description should signal trigger conditions; got %q", dm.Description)
	}
}

func TestLoad_PriorityCascade(t *testing.T) {
	// Project .seek wins over user ~/.seek wins over builtin. Verify by
	// overriding the SAME name in each layer and checking Source.
	proj := t.TempDir()
	userSkills := t.TempDir()

	// Pick a name that does NOT collide with the embedded built-ins so
	// the builtin tier never enters the race for this name.
	const n = "my-skill"

	writeSkill(t, filepath.Join(userSkills, n+".md"), n, "user-seek", "u1")
	writeSkill(t, filepath.Join(proj, ".seek", "skills", n+".md"), n, "project-seek", "p2")

	set, _, err := Load(LoadOptions{
		ProjectDir:    proj,
		UserSkillsDir: userSkills,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := set.Get(n)
	if got == nil {
		t.Fatalf("skill %q not loaded", n)
	}
	if !strings.Contains(got.Source, ".seek/skills") {
		t.Errorf("project .seek didn't win: source=%s", got.Source)
	}
	if got.Description != "project-seek" {
		t.Errorf("description = %q, want project-seek", got.Description)
	}
}

func TestLoad_LowerPriorityFillsIn(t *testing.T) {
	proj := t.TempDir()
	userSkills := t.TempDir()

	writeSkill(t, filepath.Join(proj, ".seek", "skills", "alpha.md"), "alpha", "from project", "")
	writeSkill(t, filepath.Join(userSkills, "beta.md"), "beta", "from user", "")

	set, _, err := Load(LoadOptions{
		ProjectDir:    proj,
		UserSkillsDir: userSkills,
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Get("alpha") == nil || set.Get("beta") == nil {
		t.Errorf("expected both alpha + beta loaded: %v", listNames(set))
	}
}

func TestLoad_MalformedFileGoesIntoErrorsNotFatal(t *testing.T) {
	proj := t.TempDir()
	userSkills := t.TempDir()

	// File without frontmatter — should be reported but not stop the load.
	bad := filepath.Join(proj, ".seek", "skills", "broken.md")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("not a skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// And one good one alongside it so we can confirm partial load.
	writeSkill(t, filepath.Join(proj, ".seek", "skills", "good.md"), "good", "ok", "")

	set, stats, err := Load(LoadOptions{
		ProjectDir:    proj,
		UserSkillsDir: userSkills,
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Get("good") == nil {
		t.Errorf("good skill not loaded despite sibling error")
	}
	if len(stats.Errors) == 0 {
		t.Errorf("expected error for broken.md, got none")
	}
}

func TestFormatLoadSummary_StableAndEmpty(t *testing.T) {
	if got := (LoadStats{BySource: map[string]int{}}).FormatLoadSummary(); got != "" {
		t.Errorf("empty stats should produce empty summary, got %q", got)
	}
	stats := LoadStats{BySource: map[string]int{
		"project .seek": 2,
		"builtin":       3,
	}}
	out := stats.FormatLoadSummary()
	if !strings.Contains(out, "Loaded 5 skills") {
		t.Errorf("summary missing total: %q", out)
	}
	if !strings.Contains(out, "2 from project .seek") || !strings.Contains(out, "3 from builtin") {
		t.Errorf("summary missing per-source counts: %q", out)
	}
}

func listNames(s *Set) []string {
	out := make([]string, 0, s.Len())
	for _, sk := range s.List() {
		out = append(out, sk.Name)
	}
	return out
}
