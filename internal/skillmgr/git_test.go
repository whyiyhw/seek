package skillmgr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ensureGit skips the test when git isn't on PATH. The git fetcher
// shells out via os/exec, so without a git binary the tests have
// nothing to drive.
func ensureGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
}

// runGit shells out from the given dir. Bails the test on failure so
// callers don't have to plumb errors.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Quiet noisy git config issues in CI: set author identity for
	// the throwaway repo so `git commit` works without a global
	// config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=skillmgr-test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=skillmgr-test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		// Avoid GPG signing requirements that some user setups
		// enforce globally.
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// initRepoWith creates a temporary git repo populated with the given
// files (relative path → contents) on the default branch (main).
// Returns the repo's absolute path — `file://<path>` can be passed
// as a Git source URL.
func initRepoWith(t *testing.T, files map[string]string, tag string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")
	if tag != "" {
		runGit(t, dir, "tag", tag)
	}
	return dir
}

// ---------- git happy paths ----------

func TestInstall_Git_HappyPath_DefaultBranch(t *testing.T) {
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"SKILL.md":          "---\nname: git-skill\ndescription: from git\n---\n\n# Body\n",
		"references/api.md": "# refs",
	}, "")
	userDir := t.TempDir()

	res, err := Install(InstallOptions{
		Source:  "file://" + repoDir,
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "git-skill" {
		t.Errorf("Name = %q, want git-skill", res.Name)
	}
	if res.Type != SourceGit {
		t.Errorf("Type = %v, want SourceGit", res.Type)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	// .git/ must be stripped — skills shouldn't ship version control metadata.
	if _, err := os.Stat(filepath.Join(res.Dir, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git was not stripped; stat err=%v", err)
	}
}

func TestInstall_Git_WithTag(t *testing.T) {
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"SKILL.md": "---\nname: git-skill\ndescription: tagged\n---\nbody\n",
	}, "v1.0.0")
	userDir := t.TempDir()

	_, err := Install(InstallOptions{
		Source:  "file://" + repoDir + "#v1.0.0",
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(userDir, "git-skill", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
}

func TestInstall_Git_WithSubpath(t *testing.T) {
	ensureGit(t)
	// Real-world community-skill repos often host multiple skills
	// under skills/<name>/ alongside other content. --subpath lets
	// the user pick one.
	repoDir := initRepoWith(t, map[string]string{
		"README.md":           "# repo readme",
		"skills/foo/SKILL.md": "---\nname: foo\ndescription: subpath\n---\nbody\n",
		"skills/foo/extra.md": "# extra",
	}, "")
	userDir := t.TempDir()

	res, err := Install(InstallOptions{
		Source:  "file://" + repoDir,
		Subpath: "skills/foo",
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "foo" {
		t.Errorf("Name = %q, want foo", res.Name)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing at subpath target: %v", err)
	}
	// Stuff outside the subpath must not be installed.
	if _, err := os.Stat(filepath.Join(res.Dir, "README.md")); !os.IsNotExist(err) {
		t.Errorf("subpath leaked sibling files; stat err=%v", err)
	}
}

// ---------- git error paths ----------

func TestInstall_Git_NoSKILLMd(t *testing.T) {
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"README.md": "# no skill here",
	}, "")
	userDir := t.TempDir()

	_, err := Install(InstallOptions{
		Source:  "file://" + repoDir,
		UserDir: userDir,
	})
	if err == nil {
		t.Fatal("expected error for repo without SKILL.md")
	}
	// And no on-disk artefacts at the install target.
	entries, _ := os.ReadDir(userDir)
	if len(entries) != 0 {
		t.Errorf("userDir should be empty after failed install, got: %v", entries)
	}
}

func TestInstall_Git_SubpathMissing(t *testing.T) {
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\ndescription: x\n---\nbody",
	}, "")
	userDir := t.TempDir()

	_, err := Install(InstallOptions{
		Source:  "file://" + repoDir,
		Subpath: "skills/does-not-exist",
		UserDir: userDir,
	})
	if err == nil {
		t.Fatal("expected error for non-existent subpath")
	}
	if !strings.Contains(err.Error(), "subpath") && !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("err = %v, want it to mention the missing subpath", err)
	}
}

func TestInstall_Git_BadRef(t *testing.T) {
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"SKILL.md": "---\nname: x\ndescription: x\n---\nbody",
	}, "")
	userDir := t.TempDir()

	_, err := Install(InstallOptions{
		Source:  "file://" + repoDir + "#nonexistent-ref",
		UserDir: userDir,
	})
	if err == nil {
		t.Fatal("expected error for non-existent ref")
	}
}

func TestInstall_Git_BadURL(t *testing.T) {
	ensureGit(t)
	_, err := Install(InstallOptions{
		Source:  "file:///definitely/not/a/real/git/repo/anywhere",
		UserDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for bogus git URL")
	}
}
