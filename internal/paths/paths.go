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

// Projects returns the parent directory holding per-project memory
// (~/.seek/projects/). Each project has its own subdirectory keyed by a
// 16-char SHA-256 prefix of its absolute path; layout details live in
// internal/memory.
func Projects() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "projects"), nil
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
