package skillmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/skill"
)

// writePackage drops a minimal valid directory-package skill at
// <parent>/<dirname>/. The skill's frontmatter `name` is `frontName`,
// independent of the directory name so we can verify name-resolution
// rules (PRD v2 §5.1: --name > frontmatter.name > directory name).
func writePackage(t *testing.T, parent, dirname, frontName, desc string) string {
	t.Helper()
	dir := filepath.Join(parent, dirname)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + frontName + "\ndescription: " + desc + "\n---\n\n# Body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ---------- Source detection ----------

func TestDetectSource_LocalPaths(t *testing.T) {
	cases := []string{
		"./my-skill",
		"../sibling/skill",
		"/abs/path",
		"~/somewhere",       // tilde expansion is install-time, but detection treats as local
		"plain-name",        // no scheme → local; will fail later if path doesn't exist
		".\\windows\\style", // weird but still no scheme
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := detectSourceType(in); got != SourceLocal {
				t.Errorf("detectSourceType(%q) = %v, want SourceLocal", in, got)
			}
		})
	}
}

func TestDetectSource_HTTPSTarball(t *testing.T) {
	cases := []string{
		"https://example.com/foo.tar.gz",
		"https://example.com/foo.tgz",
		"https://example.com/foo.zip",
		"https://example.com/path/foo-1.0.0.tar.gz",
		"http://example.com/insecure.zip",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := detectSourceType(in); got != SourceHTTPS {
				t.Errorf("detectSourceType(%q) = %v, want SourceHTTPS", in, got)
			}
		})
	}
}

func TestDetectSource_Git(t *testing.T) {
	cases := []string{
		"https://github.com/foo/bar",
		"https://github.com/foo/bar.git",
		"https://gitlab.com/foo/bar",
		"git@github.com:foo/bar.git",
		"git://example.com/repo.git",
		"ssh://git@example.com/repo.git",
		"https://github.com/foo/bar#v1.0.0", // ref fragment doesn't change type
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := detectSourceType(in); got != SourceGit {
				t.Errorf("detectSourceType(%q) = %v, want SourceGit", in, got)
			}
		})
	}
}

// ---------- Install: local ----------

func TestInstall_Local_HappyPath(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	writePackage(t, srcParent, "foo-pkg", "foo-skill", "test skill")

	res, err := Install(InstallOptions{
		Source:  filepath.Join(srcParent, "foo-pkg"),
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "foo-skill" {
		t.Errorf("Name = %q, want foo-skill (from frontmatter)", res.Name)
	}
	// Expected target: <userDir>/foo-skill/
	want := filepath.Join(userDir, "foo-skill")
	if res.Dir != want {
		t.Errorf("Dir = %q, want %q", res.Dir, want)
	}
	// Skill landed
	if _, err := os.Stat(filepath.Join(want, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing at target: %v", err)
	}
	// .install.json sidecar written for user-level install
	if _, err := os.Stat(filepath.Join(want, ".install.json")); err != nil {
		t.Errorf(".install.json missing: %v", err)
	}
}

func TestInstall_Local_NameOverride(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	writePackage(t, srcParent, "raw-dir", "frontmatter-name", "x")

	res, err := Install(InstallOptions{
		Source:  filepath.Join(srcParent, "raw-dir"),
		Name:    "renamed",
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "renamed" {
		t.Errorf("Name = %q, want renamed (override beats frontmatter)", res.Name)
	}
	if _, err := os.Stat(filepath.Join(userDir, "renamed", "SKILL.md")); err != nil {
		t.Errorf("override target not created: %v", err)
	}
}

func TestInstall_Local_NameConflict_RefusesWithoutForce(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	writePackage(t, srcParent, "foo", "foo", "x")
	if _, err := Install(InstallOptions{
		Source: filepath.Join(srcParent, "foo"), UserDir: userDir,
	}); err != nil {
		t.Fatal(err)
	}

	// Second install of a different source under the same name —
	// should refuse without --force, and the old install must remain
	// untouched.
	srcParent2 := t.TempDir()
	writePackage(t, srcParent2, "foo-v2", "foo", "v2")
	_, err := Install(InstallOptions{
		Source: filepath.Join(srcParent2, "foo-v2"), UserDir: userDir,
	})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "already") && !strings.Contains(err.Error(), "exists") {
		t.Errorf("err = %v, want one mentioning already/exists", err)
	}
	// Description should still be the original — the second install was refused.
	data, _ := os.ReadFile(filepath.Join(userDir, "foo", "SKILL.md"))
	if !strings.Contains(string(data), "description: x") {
		t.Errorf("original was clobbered despite no --force; got:\n%s", data)
	}
}

func TestInstall_Local_NameConflict_ForceReplaces(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	writePackage(t, srcParent, "foo", "foo", "x")
	if _, err := Install(InstallOptions{
		Source: filepath.Join(srcParent, "foo"), UserDir: userDir,
	}); err != nil {
		t.Fatal(err)
	}

	srcParent2 := t.TempDir()
	writePackage(t, srcParent2, "foo-v2", "foo", "v2")
	if _, err := Install(InstallOptions{
		Source: filepath.Join(srcParent2, "foo-v2"), UserDir: userDir, Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(userDir, "foo", "SKILL.md"))
	if !strings.Contains(string(data), "description: v2") {
		t.Errorf("--force did not replace; got:\n%s", data)
	}
}

func TestInstall_Local_RefusesMissingSKILLMd(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	bare := filepath.Join(srcParent, "no-skill")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "README.md"), []byte("# nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Install(InstallOptions{Source: bare, UserDir: userDir})
	if err == nil {
		t.Fatal("expected error for source without SKILL.md, got nil")
	}
	// And the target directory must not be partially created.
	if _, err := os.Stat(filepath.Join(userDir, "no-skill")); !os.IsNotExist(err) {
		t.Errorf("target was created despite invalid source; stat err=%v", err)
	}
}

func TestInstall_Local_RefusesNonExistentSource(t *testing.T) {
	userDir := t.TempDir()
	_, err := Install(InstallOptions{
		Source:  filepath.Join(t.TempDir(), "does-not-exist"),
		UserDir: userDir,
	})
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestInstall_Project_SkipsSidecar(t *testing.T) {
	srcParent := t.TempDir()
	projectDir := t.TempDir()
	writePackage(t, srcParent, "foo-pkg", "team-skill", "x")

	res, err := Install(InstallOptions{
		Source:     filepath.Join(srcParent, "foo-pkg"),
		Project:    true,
		ProjectDir: projectDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Lands in <projectDir>/.seek/skills/team-skill/, not user dir.
	wantDir := filepath.Join(projectDir, ".seek", "skills", "team-skill")
	if res.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", res.Dir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	// Architectural decision (PRD v2 §4.2): project-level install does
	// NOT write .install.json — that file is local state and would
	// pollute git. update isn't available for project skills.
	if _, err := os.Stat(filepath.Join(wantDir, ".install.json")); err == nil {
		t.Errorf(".install.json was written for project install; should be skipped")
	}
}

// ---------- Install: git stub (M8.1c) ----------

func TestInstall_Git_NotImplementedYet(t *testing.T) {
	// HTTPS-tarball was lifted out of this stub in M8.1b. Git is the
	// last remaining "not implemented" — exists so users don't think
	// install silently no-op'd if they hand it a git URL today.
	_, err := Install(InstallOptions{
		Source:  "https://github.com/foo/bar",
		UserDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected not-implemented error")
	}
}

// ---------- Uninstall ----------

func TestUninstall_HappyPath_Package(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	writePackage(t, srcParent, "foo-pkg", "foo", "x")
	if _, err := Install(InstallOptions{
		Source: filepath.Join(srcParent, "foo-pkg"), UserDir: userDir,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Uninstall(UninstallOptions{Name: "foo", UserDir: userDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Path, "/foo") {
		t.Errorf("Path = %q, want suffix /foo", res.Path)
	}
	if _, err := os.Stat(filepath.Join(userDir, "foo")); !os.IsNotExist(err) {
		t.Errorf("directory still present; stat err=%v", err)
	}
}

func TestUninstall_HappyPath_SingleFile(t *testing.T) {
	userDir := t.TempDir()
	// Single-file skill: <userDir>/lone.md
	content := "---\nname: lone\ndescription: x\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(userDir, "lone.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Uninstall(UninstallOptions{Name: "lone", UserDir: userDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Path, "/lone.md") {
		t.Errorf("Path = %q, want suffix /lone.md", res.Path)
	}
	if _, err := os.Stat(filepath.Join(userDir, "lone.md")); !os.IsNotExist(err) {
		t.Errorf("file still present; stat err=%v", err)
	}
}

func TestUninstall_NotFound(t *testing.T) {
	_, err := Uninstall(UninstallOptions{
		Name: "no-such-skill", UserDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want it to mention not found", err)
	}
}

func TestUninstall_RefusesBuiltin(t *testing.T) {
	// Builtins ship with the binary; uninstalling them via filesystem
	// makes no sense — they live in the embed.FS. Refuse so the user
	// gets a clear error instead of a silent no-op.
	set, _, err := skill.Load(skill.LoadOptions{
		ProjectDir:    t.TempDir(),
		UserSkillsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Get("go-test-runner") == nil {
		t.Skip("go-test-runner builtin not present; skipping")
	}
	_, err = Uninstall(UninstallOptions{
		Name:     "go-test-runner",
		UserDir:  t.TempDir(),
		Builtins: set,
	})
	if err == nil {
		t.Fatal("expected refusal for builtin name")
	}
	if !strings.Contains(err.Error(), "builtin") {
		t.Errorf("err = %v, want it to say builtin", err)
	}
}
