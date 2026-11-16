package agenttypes

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// readonly_test.go pins the loader's red line (PRD P2 acceptance #4 /
// AGENTS.md): internal/agenttypes has NO write API — the write path
// under ~/.seek/agents/ belongs exclusively to internal/agentsmgr,
// exactly as ~/.seek/skills/ belongs to skillmgr/skillstats. The
// sandbox-testable form of "no write API" is behavioural: Load must
// leave the filesystem byte-identical and must not even materialise
// the directories it reads.

// fsSnapshot walks root and records every regular file's path, size,
// and mtime. Directories are recorded by their mere existence in the
// walked paths.
func fsSnapshot(t *testing.T, root string) map[string][2]int64 {
	t.Helper()
	snap := map[string][2]int64{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			snap[rel+"/"] = [2]int64{}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snap[rel] = [2]int64{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestLoad_ReadOnly_NeverWrites: with well-populated project + user
// directories (including a file that FAILS parse, so every code path
// including the error one runs), Load must not create, delete, resize
// or touch-mtime a single file — no caches, no indexes, no locks.
func TestLoad_ReadOnly_NeverWrites(t *testing.T) {
	project := t.TempDir()
	user := t.TempDir()
	projAgents := filepath.Join(project, ".seek", "agents")
	write(t, filepath.Join(projAgents, "verifier.md"), verifierMD)
	write(t, filepath.Join(projAgents, "broken.md"),
		"---\nname: broken\ntools: [read, agent]\ndescription: bad\n---\nBody.")
	write(t, filepath.Join(user, "librarian.md"),
		"---\nname: librarian\ndescription: finds docs\n---\nIndex things.")

	before := map[string]map[string][2]int64{
		"project": fsSnapshot(t, project),
		"user":    fsSnapshot(t, user),
	}

	set, stats, err := Load(LoadOptions{ProjectDir: project, UserAgentsDir: user})
	if err != nil {
		t.Fatal(err)
	}
	// 2 custom (verifier, librarian) + 3 builtins; broken.md is the
	// one parse error and must not register.
	if set.Len() != 5 {
		t.Errorf("set.Len() = %d, want 5", set.Len())
	}
	if len(stats.Errors) != 1 {
		t.Errorf("stats.Errors = %v, want exactly the broken.md parse error", stats.Errors)
	}

	for name, root := range map[string]string{"project": project, "user": user} {
		if after := fsSnapshot(t, root); !reflect.DeepEqual(before[name], after) {
			t.Errorf("%s tree mutated by Load:\nbefore=%v\nafter=%v", name, before[name], after)
		}
	}
}

// TestLoad_ReadOnly_DoesNotCreateMissingDirs: a missing user agents
// directory is the common case; Load must skip it without creating it
// (creating ~/.seek/agents/ on read would already be a write-path
// violation — that right belongs to agentsmgr).
func TestLoad_ReadOnly_DoesNotCreateMissingDirs(t *testing.T) {
	project := t.TempDir()
	user := filepath.Join(t.TempDir(), "absent-agents")
	missingProjAgents := filepath.Join(project, ".seek", "agents")

	if _, _, err := Load(LoadOptions{ProjectDir: project, UserAgentsDir: user}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{user, missingProjAgents} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s exists after Load (err=%v) — the loader must not create directories", dir, err)
		}
	}
}

// TestLoad_ReadOnly_MtimesUnchanged guards the subtle variant: a file
// whose content parses fine gets READ — on some filesystems a read
// can update atime, but mtime must never move. An mtime change would
// mean something wrote.
func TestLoad_ReadOnly_MtimesUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verifier.md")
	write(t, path, verifierMD)
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(LoadOptions{ProjectDir: t.TempDir(), UserAgentsDir: dir}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Errorf("mtime moved: %v → %v — Load must not write", stamp, info.ModTime())
	}
}
