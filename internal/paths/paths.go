// Package paths is seek's single source of truth for user-data
// directories. Everything user-scoped (sessions, MCP config, user-level
// skills) lives under one root so users have exactly one place to back
// up, sync, or symlink.
//
// Resolution order for the root:
//
//  1. $SEEK_HOME — explicit override for users who want to move the
//     directory (network drive, dotfiles repo, multi-config setups).
//  2. ~/.seek/  — the default. Cross-platform: on Unix it's
//     /home/<user>/.seek/, on macOS /Users/<user>/.seek/, on
//     Windows C:\Users\<user>\.seek\.
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// envHome is the environment variable users set to override the
// default ~/.seek/ root. Exported as a name (not the value) so
// docs and error messages can reference it consistently.
const envHome = "SEEK_HOME"

// Home returns the seek user-data root directory. Empty string and a
// non-nil error mean "couldn't determine a home directory" — the
// caller's behaviour at that point should be the same as if the
// directory didn't exist (no sessions, no user skills, no mcp config).
func Home() (string, error) {
	if v := os.Getenv(envHome); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: home dir: %w", err)
	}
	return filepath.Join(home, ".seek"), nil
}

// Sessions returns the directory holding persisted conversation files
// (one JSONL per session). The caller is responsible for creating the
// directory if it doesn't yet exist; this function only returns the
// resolved path.
func Sessions() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sessions"), nil
}

// MCPConfig returns the path to the MCP server configuration file
// (mcp.json). May not exist — having no MCP servers configured is a
// normal state and the caller should treat ENOENT as "no servers".
func MCPConfig() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "mcp.json"), nil
}

// UserSkills returns the user-level skills directory (~/.seek/skills/).
// Project-level skills live at <project>/.seek/skills/ which is NOT
// computed here — it's relative to whatever working directory the
// caller has resolved.
func UserSkills() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "skills"), nil
}

// UserSkillStats returns the path to the global call-statistics
// JSONL file (~/.seek/skills/.stats.jsonl). Per PRD v2 §4.3 the
// stats file lives inside UserSkills so it ships alongside the
// skills it describes. Filename starts with `.` so the skill
// loader skips it during directory scans (single source of truth
// for that filter rule).
func UserSkillStats() (string, error) {
	dir, err := UserSkills()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".stats.jsonl"), nil
}

// Projects returns the parent directory holding per-project state
// (~/.seek/projects/). Each project has its own subdirectory keyed by a
// 16-char SHA-256 prefix of its absolute path — see ProjectID.
func Projects() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "projects"), nil
}

// ProjectID returns the 16-char hex prefix of sha256(absPath) — the
// canonical on-disk identifier used to namespace project state under
// ~/.seek/projects/. 16 hex chars = 64 bits of namespace; collision
// probability is negligible at personal-scale project counts.
//
// Stable across machines for the same absolute path. Independent of
// git remote / branch state — moving the project on disk (e.g. via
// rename) produces a different ID. That's a deliberate trade-off:
// content-based identity would couple to file contents and break on
// every edit; path-based identity is stable for as long as the path
// is.
func ProjectID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:16]
}

// ProjectDir returns ~/.seek/projects/<id>/ for the given absolute
// project path. Does NOT create the directory — callers requiring the
// directory to exist should MkdirAll separately so the failure mode is
// theirs to handle.
func ProjectDir(absPath string) (string, error) {
	root, err := Projects()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ProjectID(absPath)), nil
}

// ProjectPlans returns ~/.seek/projects/<id>/plans/ for the given
// absolute project path. Used by the plan artifact writer
// (internal/tools/plan/artifact.go) — see PRD docs/prd/feature-plan-mode.md
// §八. Does NOT create the directory.
func ProjectPlans(absPath string) (string, error) {
	dir, err := ProjectDir(absPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plans"), nil
}

// Soul returns the path to the L-layer (long-term / cross-project user
// traits) markdown file at ~/.seek/soul.md. The file may not exist —
// that's the steady state until `seek -dream` produces L candidates.
func Soul() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "soul.md"), nil
}

// ProjectSessionDir returns ~/.seek/projects/<id>/sessions/<sid>/ for the
// given absolute project path and session id. Used by the v3 checkpoint
// subsystem as the per-session scratch space — feature PRD docs/prd/
// feature-checkpoint.md §3.2. Does NOT create the directory.
//
// Note: this is NOT where session JSONL files live (those stay flat
// under ~/.seek/sessions/<id>.jsonl for backward-compat). This is the
// per-project, per-session sidecar dir that holds checkpoint blobs +
// indexes, scoped so cleanup on SessionEnd is a simple RemoveAll.
func ProjectSessionDir(absPath, sid string) (string, error) {
	if sid == "" {
		return "", fmt.Errorf("paths: ProjectSessionDir: empty session id")
	}
	dir, err := ProjectDir(absPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions", sid), nil
}

// SessionCheckpointDir returns the per-session checkpoint root:
// ~/.seek/projects/<id>/sessions/<sid>/. The checkpoint subsystem
// (internal/checkpoint) hangs its `checkpoints.jsonl` (git index) and
// `checkpoints/` subdir (file CAS blob store + index) under this root.
// Does NOT create the directory — callers MkdirAll on first use.
//
// Why per-project + per-session rather than per-session-only: the
// project ID is stable across machines for the same path, so a future
// "list all checkpoints for this project" UI has a natural namespace,
// and cleanup is a single RemoveAll without grepping for the session
// across multiple roots.
func SessionCheckpointDir(absPath, sid string) (string, error) {
	return ProjectSessionDir(absPath, sid)
}
