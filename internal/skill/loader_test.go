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

// writePackageSkill drops a directory-package skill at <parent>/<name>/<entry>
// where entry is typically "SKILL.md" or "skill.md". Extras lets callers add
// sibling files (e.g. "references/foo.md" -> "...") to mimic real Anthropic
// Agent Skills layouts.
func writePackageSkill(t *testing.T, parent, name, entry, desc, body string, extras map[string]string) {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, entry), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, body := range extras {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
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

	// plan-mode (PRD docs/prd/feature-plan-mode.md, P5) describes the
	// analyze → propose → execute loop the propose tool participates
	// in. Same load-bearing test as dual-model: presence + trigger
	// keywords in the description.
	pm := set.Get("plan-mode")
	if pm == nil {
		t.Fatalf("builtin plan-mode not present; loaded: %v", listNames(set))
	}
	if !strings.Contains(pm.Description, "plan-analyze") &&
		!strings.Contains(pm.Description, "plan-execute") &&
		!strings.Contains(pm.Description, "/plan") {
		t.Errorf("plan-mode description should mention the mode-reminder substates or the /plan command so the model loads it at the right time; got %q", pm.Description)
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
	// filepath.ToSlash normalises Windows path separators so the
	// substring check works on every supported OS.
	if !strings.Contains(filepath.ToSlash(got.Source), ".seek/skills") {
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

func TestLoad_DirectoryPackage_SKILLMd(t *testing.T) {
	// PRD v2 §4.1 — the canonical directory-package form: <dir>/SKILL.md
	// (uppercase) with frontmatter inline. This is the Anthropic Agent
	// Skills standard layout.
	proj := t.TempDir()
	userSkills := t.TempDir()
	writePackageSkill(t,
		filepath.Join(proj, ".seek", "skills"),
		"my-pkg-skill",
		"SKILL.md",
		"package form",
		"# Body\nstep 1",
		nil,
	)

	set, _, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	got := set.Get("my-pkg-skill")
	if got == nil {
		t.Fatalf("package skill not loaded; have: %v", listNames(set))
	}
	if got.Type != TypePackage {
		t.Errorf("Type = %v, want TypePackage", got.Type)
	}
	if !strings.HasSuffix(got.Source, filepath.Join("my-pkg-skill", "SKILL.md")) {
		t.Errorf("Source = %q, want suffix my-pkg-skill/SKILL.md", got.Source)
	}
	if !strings.HasPrefix(got.Body, "# Body") {
		t.Errorf("body wasn't preserved: %q", got.Body)
	}
}

func TestLoad_DirectoryPackage_LowercaseFallback(t *testing.T) {
	// Tolerate lowercase skill.md inside a directory — happens when a
	// long-time seek user converts an existing single-file skill into a
	// package by mkdir + mv without renaming.
	proj := t.TempDir()
	userSkills := t.TempDir()
	writePackageSkill(t,
		filepath.Join(proj, ".seek", "skills"),
		"lowercase-pkg",
		"skill.md",
		"lowercase form",
		"body",
		nil,
	)

	set, _, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	got := set.Get("lowercase-pkg")
	if got == nil {
		t.Fatalf("lowercase package not loaded; have: %v", listNames(set))
	}
	if got.Type != TypePackage {
		t.Errorf("Type = %v, want TypePackage", got.Type)
	}
}

func TestLoad_DirectoryPackage_PrefersUppercaseWhenBothPresent(t *testing.T) {
	// PRD v2 §4.1 — when both SKILL.md and skill.md exist, take the
	// canonical uppercase and emit a non-fatal warning. This catches
	// users who accidentally end up with both after a rename.
	proj := t.TempDir()
	userSkills := t.TempDir()
	dir := filepath.Join(proj, ".seek", "skills", "dual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// On case-insensitive filesystems (macOS APFS default, Windows
	// NTFS) the two filenames collide into one inode — the scenario
	// the loader has to handle simply can't be reproduced. Skip
	// rather than test a fictional state. Linux ext4/btrfs CI still
	// exercises it.
	if !caseSensitiveFS(t, dir) {
		t.Skip("filesystem is case-insensitive; SKILL.md + skill.md can't coexist as distinct files")
	}
	upper := "---\nname: dual\ndescription: from-upper\n---\nupper body\n"
	lower := "---\nname: dual\ndescription: from-lower\n---\nlower body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(upper), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill.md"), []byte(lower), 0o644); err != nil {
		t.Fatal(err)
	}

	set, stats, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	got := set.Get("dual")
	if got == nil {
		t.Fatalf("dual not loaded")
	}
	if got.Description != "from-upper" {
		t.Errorf("uppercase didn't win: desc=%q", got.Description)
	}
	// A warning should be reported but not fatal.
	foundWarning := false
	for _, e := range stats.Errors {
		if strings.Contains(e.Error(), "dual") &&
			(strings.Contains(e.Error(), "both") || strings.Contains(e.Error(), "skill.md")) {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about SKILL.md + skill.md coexistence; got errors=%v", stats.Errors)
	}
}

func TestLoad_DirectoryPackage_MissingEntryFile(t *testing.T) {
	// A directory under .seek/skills/ without any SKILL.md / skill.md is
	// reported as a warning but doesn't break the load. Common when a
	// user `mkdir`s a placeholder.
	proj := t.TempDir()
	userSkills := t.TempDir()
	empty := filepath.Join(proj, ".seek", "skills", "empty-pkg")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	// And a valid sibling so we know loading didn't bail.
	writeSkill(t, filepath.Join(proj, ".seek", "skills", "alpha.md"), "alpha", "ok", "")

	set, stats, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	if set.Get("alpha") == nil {
		t.Errorf("alpha not loaded despite sibling empty dir")
	}
	hasErr := false
	for _, e := range stats.Errors {
		if strings.Contains(e.Error(), "empty-pkg") {
			hasErr = true
			break
		}
	}
	if !hasErr {
		t.Errorf("expected warning about empty-pkg; got errors=%v", stats.Errors)
	}
}

func TestLoad_SingleFileAndPackageCoexist(t *testing.T) {
	// PRD v2 §3 — design objective #1: directory and single-file skills
	// must load side-by-side without interference.
	proj := t.TempDir()
	userSkills := t.TempDir()
	writeSkill(t, filepath.Join(proj, ".seek", "skills", "single.md"), "single", "f", "")
	writePackageSkill(t,
		filepath.Join(proj, ".seek", "skills"),
		"pkg",
		"SKILL.md",
		"p",
		"",
		nil,
	)

	set, _, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	if set.Get("single") == nil || set.Get("pkg") == nil {
		t.Errorf("missing one of single/pkg: have %v", listNames(set))
	}
	if set.Get("single").Type != TypeSingleFile {
		t.Errorf("single.Type = %v, want TypeSingleFile", set.Get("single").Type)
	}
	if set.Get("pkg").Type != TypePackage {
		t.Errorf("pkg.Type = %v, want TypePackage", set.Get("pkg").Type)
	}
}

func TestLoad_PackageWithReferencesSubdir(t *testing.T) {
	// Mimic real Anthropic Agent Skills layout (zero-skills, etc.) —
	// SKILL.md at top, references/ and examples/ subdirs containing
	// supplementary markdown. The loader must ignore subdirs and only
	// parse SKILL.md.
	proj := t.TempDir()
	userSkills := t.TempDir()
	extras := map[string]string{
		"references/api.md":   "# API reference",
		"references/usage.md": "# Usage",
		"examples/hello.md":   "# Hello example",
		"scripts/run.sh":      "#!/bin/sh\necho hi\n",
		"README.md":           "human-readable readme",
	}
	writePackageSkill(t,
		filepath.Join(proj, ".seek", "skills"),
		"big-skill",
		"SKILL.md",
		"big skill",
		"# Body\nuses [refs](references/api.md)",
		extras,
	)

	set, stats, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	got := set.Get("big-skill")
	if got == nil {
		t.Fatalf("big-skill not loaded; errors=%v", stats.Errors)
	}
	if !strings.Contains(got.Body, "uses [refs](references/api.md)") {
		t.Errorf("body should preserve links to extras: %q", got.Body)
	}
	// references/api.md should not have been parsed as a skill on its own.
	if set.Get("api") != nil {
		t.Errorf("subdir markdown was incorrectly parsed as a skill")
	}
}

func TestLoad_InstallSourceSidecar(t *testing.T) {
	// PRD v2 §4.2 — .install.json sidecar is read at load time and
	// populated into Skill.InstallSource. Manual `cp -r` skills (no
	// sidecar) leave InstallSource nil — that's the signal that
	// `seek skill update` is unavailable for them.
	proj := t.TempDir()
	userSkills := t.TempDir()
	dir := filepath.Join(proj, ".seek", "skills", "tracked")
	writePackageSkill(t,
		filepath.Join(proj, ".seek", "skills"),
		"tracked",
		"SKILL.md",
		"tracked skill",
		"body",
		nil,
	)
	sidecar := `{
  "schema_version": 1,
  "installed_at": "2026-05-22T13:15:00Z",
  "type": "git",
  "url": "https://github.com/foo/bar",
  "ref": "v1.0.0",
  "resolved_commit": "9f8a1c2"
}`
	if err := os.WriteFile(filepath.Join(dir, ".install.json"), []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}

	set, _, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	got := set.Get("tracked")
	if got == nil {
		t.Fatalf("tracked not loaded")
	}
	if got.InstallSource == nil {
		t.Fatalf("InstallSource not populated despite .install.json present")
	}
	if got.InstallSource.Type != "git" {
		t.Errorf("InstallSource.Type = %q", got.InstallSource.Type)
	}
	if got.InstallSource.URL != "https://github.com/foo/bar" {
		t.Errorf("InstallSource.URL = %q", got.InstallSource.URL)
	}
	if got.InstallSource.Ref != "v1.0.0" {
		t.Errorf("InstallSource.Ref = %q", got.InstallSource.Ref)
	}
	if got.InstallSource.ResolvedCommit != "9f8a1c2" {
		t.Errorf("InstallSource.ResolvedCommit = %q", got.InstallSource.ResolvedCommit)
	}
}

func TestLoad_TypeFieldPopulated(t *testing.T) {
	// Concrete cross-check that Type is set correctly for all three
	// origin tiers. Without this we could regress to "all packages" or
	// "all single-file" without noticing.
	proj := t.TempDir()
	userSkills := t.TempDir()
	writeSkill(t, filepath.Join(proj, ".seek", "skills", "single.md"), "single", "s", "")
	writePackageSkill(t,
		filepath.Join(proj, ".seek", "skills"),
		"pkg",
		"SKILL.md",
		"p",
		"",
		nil,
	)

	set, _, err := Load(LoadOptions{ProjectDir: proj, UserSkillsDir: userSkills})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Get("single").Type; got != TypeSingleFile {
		t.Errorf("single.Type = %v, want TypeSingleFile", got)
	}
	if got := set.Get("pkg").Type; got != TypePackage {
		t.Errorf("pkg.Type = %v, want TypePackage", got)
	}
	// One of the builtins (always there per TestLoad_BuiltinAlwaysAvailable).
	if got := set.Get("go-test-runner").Type; got != TypeBuiltin {
		t.Errorf("go-test-runner.Type = %v, want TypeBuiltin", got)
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

// caseSensitiveFS probes whether the filesystem at `dir` distinguishes
// "PROBE" from "probe". Used by the SKILL.md / skill.md coexistence
// test, which is only meaningful on case-sensitive filesystems.
func caseSensitiveFS(t *testing.T, dir string) bool {
	t.Helper()
	up := filepath.Join(dir, "FS_CASE_PROBE")
	lo := filepath.Join(dir, "fs_case_probe")
	if err := os.WriteFile(up, []byte("U"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lo, []byte("L"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(up)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(up)
	_ = os.Remove(lo)
	return string(data) == "U"
}

func listNames(s *Set) []string {
	out := make([]string, 0, s.Len())
	for _, sk := range s.List() {
		out = append(out, sk.Name)
	}
	return out
}
