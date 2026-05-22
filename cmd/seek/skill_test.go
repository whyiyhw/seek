package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSeekHome points $SEEK_HOME at a temp dir for the duration of
// the test so install / uninstall / update don't reach for the real
// user's ~/.seek/. Returns the resolved skills directory.
func withSeekHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := os.Getenv("SEEK_HOME")
	t.Cleanup(func() { os.Setenv("SEEK_HOME", prev) })
	if err := os.Setenv("SEEK_HOME", home); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "skills")
}

func writeFixtureSkill(t *testing.T, parent, dirname, frontName, desc string) string {
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

// runSkill is a one-shot helper that runs the dispatcher and returns
// captured stdout/stderr + error. Used by every CLI test below.
// (Named to avoid colliding with main's runSkill().)
func runSkill(args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := runSkillCmd(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestSkillCmd_Help(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"-h"}} {
		out, _, err := runSkill(args...)
		if err != nil {
			t.Errorf("`seek skill %v` returned %v", args, err)
		}
		for _, want := range []string{"install", "uninstall", "update"} {
			if !strings.Contains(out, want) {
				t.Errorf("help missing %q for args=%v:\n%s", want, args, out)
			}
		}
	}
}

func TestSkillCmd_UnknownSubcommand(t *testing.T) {
	_, _, err := runSkill("frobnicate")
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("err = %v, want it to say unknown", err)
	}
}

// ---------- install ----------

func TestSkillCmd_Install_HappyPath(t *testing.T) {
	skillsDir := withSeekHome(t)
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "cli-skill", "test")

	stdout, _, err := runSkill("install", src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "installed cli-skill") {
		t.Errorf("output didn't confirm install: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "cli-skill", "SKILL.md")); err != nil {
		t.Errorf("skill not landed on disk: %v", err)
	}
}

func TestSkillCmd_Install_RequiresSource(t *testing.T) {
	_, _, err := runSkill("install")
	if err == nil {
		t.Fatal("expected error for missing source")
	}
	if !strings.Contains(err.Error(), "<source>") && !strings.Contains(err.Error(), "required") {
		t.Errorf("err = %v, want it to mention required source", err)
	}
}

func TestSkillCmd_Install_NameOverride(t *testing.T) {
	skillsDir := withSeekHome(t)
	src := writeFixtureSkill(t, t.TempDir(), "raw", "frontmatter", "x")

	if _, _, err := runSkill("install", "--name", "renamed", src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "renamed", "SKILL.md")); err != nil {
		t.Errorf("override target not present: %v", err)
	}
}

func TestSkillCmd_Install_BadType(t *testing.T) {
	withSeekHome(t)
	_, _, err := runSkill("install", "--type", "ftp", "./whatever")
	if err == nil || !strings.Contains(err.Error(), "--type") {
		t.Errorf("err = %v, want it to reject unknown --type", err)
	}
}

// ---------- uninstall ----------

func TestSkillCmd_Uninstall_HappyPath(t *testing.T) {
	skillsDir := withSeekHome(t)
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "to-remove", "x")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}
	out, _, err := runSkill("uninstall", "to-remove")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "uninstalled to-remove") {
		t.Errorf("output didn't confirm uninstall: %s", out)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "to-remove")); !os.IsNotExist(err) {
		t.Errorf("skill dir not removed; stat err=%v", err)
	}
}

func TestSkillCmd_Uninstall_NotFound(t *testing.T) {
	withSeekHome(t)
	_, _, err := runSkill("uninstall", "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want it to mention not found", err)
	}
}

func TestSkillCmd_Uninstall_Aliases(t *testing.T) {
	// `remove` and `rm` should be the same as `uninstall` so muscle
	// memory from npm / brew / apt / cargo all works.
	for _, alias := range []string{"remove", "rm"} {
		withSeekHome(t)
		src := writeFixtureSkill(t, t.TempDir(), "pkg", "alias-skill", "x")
		if _, _, err := runSkill("install", src); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runSkill(alias, "alias-skill"); err != nil {
			t.Errorf("`seek skill %s alias-skill` errored: %v", alias, err)
		}
	}
}

// ---------- update ----------

func TestSkillCmd_Update_NoArgs(t *testing.T) {
	_, _, err := runSkill("update")
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("err = %v, want required-name error", err)
	}
}

func TestSkillCmd_Update_AllAndNameConflict(t *testing.T) {
	_, _, err := runSkill("update", "--all", "some-name")
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("err = %v, want it to flag --all + positional conflict", err)
	}
}

func TestSkillCmd_Update_LocalHappyPath(t *testing.T) {
	withSeekHome(t)
	src := writeFixtureSkill(t, t.TempDir(), "pkg", "update-target", "v1")
	if _, _, err := runSkill("install", src); err != nil {
		t.Fatal(err)
	}
	// Modify source.
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"),
		[]byte("---\nname: update-target\ndescription: v2\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSkill("update", "update-target"); err != nil {
		t.Fatal(err)
	}
	// Best-effort: read back, expect v2.
	skillsDir, _ := os.UserHomeDir()
	_ = skillsDir
	// Use SEEK_HOME-derived path instead.
	data, _ := os.ReadFile(filepath.Join(os.Getenv("SEEK_HOME"), "skills", "update-target", "SKILL.md"))
	if !strings.Contains(string(data), "description: v2") {
		t.Errorf("update didn't refresh; got:\n%s", data)
	}
}

func TestSkillCmd_Update_AllEmpty(t *testing.T) {
	withSeekHome(t)
	out, _, err := runSkill("update", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no managed skills") {
		t.Errorf("expected 'no managed skills' message; got %q", out)
	}
}
