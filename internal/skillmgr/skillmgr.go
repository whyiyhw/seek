// Package skillmgr owns the lifecycle of on-disk skill packages —
// install, uninstall, update, and the queries needed to back the
// `seek skill ...` CLI subcommands (PRD v2 §5).
//
// The loader in internal/skill is intentionally read-only; this
// package is the only one that mutates the filesystem under
// ~/.seek/skills/ (or <project>/.seek/skills/ for --project). Splitting
// the two keeps the loader's cost model predictable — it's called once
// at startup and never wakes up later — while letting management
// commands be heavier without complicating the agent path.
//
// What this package does NOT own:
//   - Parsing skill files — that's internal/skill.Parse.
//   - Recording call statistics — that's internal/skillstats (M8.3).
//   - CLI wiring — that's cmd/seek/skill.go (M8.4). This package
//     exposes pure functions that take options and return results;
//     the CLI translates flags into those.
package skillmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/skill"
)

// SourceType identifies how a skill should be fetched. The zero value
// SourceAuto signals "infer from the Source string"; explicit values
// let callers override the inference (useful for ambiguous URLs).
type SourceType int

const (
	SourceAuto  SourceType = iota
	SourceLocal            // filesystem path
	SourceGit              // git clone (M8.1c)
	SourceHTTPS            // https tarball / zip (M8.1b)
)

func (s SourceType) String() string {
	switch s {
	case SourceLocal:
		return "local"
	case SourceGit:
		return "git"
	case SourceHTTPS:
		return "https"
	case SourceAuto:
		return "auto"
	default:
		return "unknown"
	}
}

// InstallOptions controls Install. Most fields are optional; minimum
// is Source. UserDir / ProjectDir / HTTP / Now exist mainly so tests
// can inject fakes — production code uses zero values.
type InstallOptions struct {
	Source string     // path, URL, or git URL
	Type   SourceType // SourceAuto = infer from Source
	Name   string     // override; "" = use SKILL.md frontmatter name → fallback to dir name
	Force  bool       // overwrite an existing skill of the same name

	// Project=true installs into <ProjectDir>/.seek/skills/<name>/
	// instead of <UserDir>/<name>/. ProjectDir defaults to cwd.
	// Project installs deliberately skip writing .install.json
	// (PRD v2 §4.2) — they are "team-shared via git" and shouldn't
	// carry local install state. Trade-off: no `seek skill update`
	// support for project skills.
	Project    bool
	ProjectDir string

	// UserDir overrides paths.UserSkills() — tests inject a tempdir.
	UserDir string

	// Subpath: for git sources, the directory inside the cloned repo
	// that holds the skill (e.g. "skills/foo"). Ignored for other
	// source types.
	Subpath string

	// SHA256: for https tarballs, the expected hex-encoded sha256
	// digest of the downloaded archive. Empty = no verification
	// (allowed but warned in CLI; this struct just records the value).
	SHA256 string

	// HTTP is the http.Client used for tarball fetches. Tests inject
	// httptest server clients. nil = http.DefaultClient.
	HTTP *http.Client

	// Now lets tests pin the installed_at timestamp written into
	// .install.json. nil = time.Now.
	Now func() time.Time
}

// InstallResult describes what was placed where.
type InstallResult struct {
	Name string // the kebab-case skill name as registered
	Dir  string // absolute path of the installed skill directory
	Type SourceType
}

// UninstallOptions controls Uninstall.
type UninstallOptions struct {
	Name     string
	UserDir  string     // overrides paths.UserSkills() — tests inject
	Force    bool       // not used yet; reserved for "uninstall even if metadata is corrupt"
	Builtins *skill.Set // optional; when provided, uninstall refuses names present in this Set
	// KeepStats / Purge — reserved for M8.3 once .stats.jsonl exists.
}

// UninstallResult records what was removed.
type UninstallResult struct {
	Name string
	Path string // absolute path of what was deleted
}

// Install fetches and lays down a skill. Side effects are gated on
// success: a failed fetch or validation leaves the target untouched.
func Install(opts InstallOptions) (*InstallResult, error) {
	if opts.Source == "" {
		return nil, errors.New("skillmgr: install: Source is required")
	}
	typ := opts.Type
	if typ == SourceAuto {
		typ = detectSourceType(opts.Source)
	}

	stagingParent, err := os.MkdirTemp("", "seek-install-*")
	if err != nil {
		return nil, fmt.Errorf("skillmgr: create staging: %w", err)
	}
	// Best-effort cleanup. On success the directory will be empty
	// (its contents were moved into the final target).
	defer os.RemoveAll(stagingParent)

	// staging is the directory the source-specific fetcher populates.
	// Every fetcher MUST produce a layout that looks like a valid
	// skill package — SKILL.md / skill.md at the top, optional
	// supporting files alongside.
	staging := filepath.Join(stagingParent, "pkg")

	switch typ {
	case SourceLocal:
		if err := stageLocal(opts.Source, staging); err != nil {
			return nil, err
		}
	case SourceHTTPS:
		if err := stageHTTPS(opts, staging); err != nil {
			return nil, fmt.Errorf("skillmgr: %w", err)
		}
	case SourceGit:
		if err := stageGit(opts, staging); err != nil {
			return nil, fmt.Errorf("skillmgr: %w", err)
		}
	default:
		return nil, fmt.Errorf("skillmgr: unsupported source type %v", typ)
	}

	// Parse the staged skill to extract name + validate it's a real
	// skill package (PRD v2 §4.1). This is the only place we read
	// SKILL.md during install — we trust internal/skill for the
	// frontmatter contract.
	sk, err := loadStagedSkill(staging)
	if err != nil {
		return nil, fmt.Errorf("skillmgr: source is not a valid skill package: %w", err)
	}
	name := pickName(opts.Name, sk.Name, filepath.Base(opts.Source))
	if name == "" {
		return nil, errors.New("skillmgr: could not resolve skill name (no --name, no frontmatter name, no usable directory name)")
	}

	// Resolve the target. Project installs land in
	// <ProjectDir>/.seek/skills/<name>/ ; user installs in <UserDir>/<name>/.
	targetParent, err := resolveTargetParent(opts)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(targetParent, name)

	// Conflict check.
	if _, err := os.Stat(target); err == nil {
		if !opts.Force {
			return nil, fmt.Errorf("skillmgr: %q already exists at %s (use --force to replace)", name, target)
		}
		if err := os.RemoveAll(target); err != nil {
			return nil, fmt.Errorf("skillmgr: --force replace: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("skillmgr: stat target: %w", err)
	}

	// Make sure the parent exists. MkdirAll is idempotent — safe to
	// call even when the dir is already there.
	if err := os.MkdirAll(targetParent, 0o755); err != nil {
		return nil, fmt.Errorf("skillmgr: mkdir target parent: %w", err)
	}

	// Atomic-ish rename. Same filesystem (both under TempDir or
	// inside the user's home tree on most setups) → atomic. Cross-
	// filesystem → fallback copy.
	if err := os.Rename(staging, target); err != nil {
		if err := copyTree(staging, target); err != nil {
			return nil, fmt.Errorf("skillmgr: install copy fallback: %w", err)
		}
	}

	// Write .install.json sidecar, unless this is a project install.
	if !opts.Project {
		if err := writeInstallSidecar(target, &opts, typ); err != nil {
			// Sidecar failure is non-fatal — the skill is installed.
			// We warn via the returned error wrapper but leave it
			// up to the CLI to decide whether to log / continue.
			return &InstallResult{Name: name, Dir: target, Type: typ},
				fmt.Errorf("skillmgr: write .install.json (skill installed, but update tracking unavailable): %w", err)
		}
	}

	return &InstallResult{Name: name, Dir: target, Type: typ}, nil
}

// Uninstall removes an installed skill. Builtins (when the optional
// Builtins set is provided) and project-level skills are refused —
// the user is expected to remove project skills via git, not via
// this command (PRD v2 §5.1).
func Uninstall(opts UninstallOptions) (*UninstallResult, error) {
	if opts.Name == "" {
		return nil, errors.New("skillmgr: uninstall: Name is required")
	}
	if opts.Builtins != nil {
		if sk := opts.Builtins.Get(opts.Name); sk != nil && sk.Type == skill.TypeBuiltin {
			return nil, fmt.Errorf("skillmgr: %q is a builtin skill and cannot be uninstalled", opts.Name)
		}
	}
	userDir, err := resolveUserDir(opts.UserDir)
	if err != nil {
		return nil, err
	}

	// A user-installed skill is either a directory <userDir>/<name>/
	// or a single file <userDir>/<name>.md. Check both.
	candidates := []string{
		filepath.Join(userDir, opts.Name),
		filepath.Join(userDir, opts.Name+".md"),
	}
	for _, p := range candidates {
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("skillmgr: stat %s: %w", p, err)
		}
		if info.IsDir() {
			if err := os.RemoveAll(p); err != nil {
				return nil, fmt.Errorf("skillmgr: remove dir %s: %w", p, err)
			}
		} else {
			if err := os.Remove(p); err != nil {
				return nil, fmt.Errorf("skillmgr: remove file %s: %w", p, err)
			}
		}
		return &UninstallResult{Name: opts.Name, Path: p}, nil
	}
	return nil, fmt.Errorf("skillmgr: %q not found in %s", opts.Name, userDir)
}

// detectSourceType infers a SourceType from the source string. The
// rules are deliberately conservative:
//
//   - Has a scheme that's clearly a fetch protocol (http/https/git/
//     ssh) AND the URL path ends with a known archive extension
//     (.tar.gz / .tgz / .tar / .zip) → SourceHTTPS.
//   - Otherwise has one of those schemes (or git@host: style) →
//     SourceGit.
//   - Anything else → SourceLocal.
//
// This means "plain-name" without a path qualifier is treated as a
// local path — Install will then fail with a clear "no such file"
// error rather than guessing the user meant a non-existent registry
// lookup. Explicit --type at the CLI layer can override (M8.4).
func detectSourceType(src string) SourceType {
	s := src
	// scp-like syntax: user@host:path
	if strings.HasPrefix(s, "git@") && strings.Contains(s, ":") {
		return SourceGit
	}
	switch {
	case strings.HasPrefix(s, "git://"),
		strings.HasPrefix(s, "ssh://"),
		strings.HasPrefix(s, "file://"):
		// file:// is a git transport too — git clone file:///path works.
		// Most useful for tests and local mirrors; production users
		// will overwhelmingly use https://github.com/... here.
		return SourceGit
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		// Strip query/fragment for extension matching, since URLs
		// like example.com/x.tar.gz?token=1 are common.
		path := s
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i]
		}
		lower := strings.ToLower(path)
		for _, ext := range []string{".tar.gz", ".tgz", ".tar", ".zip"} {
			if strings.HasSuffix(lower, ext) {
				return SourceHTTPS
			}
		}
		return SourceGit
	}
	return SourceLocal
}

// stageLocal copies the contents of src (a directory) into the
// staging directory.
func stageLocal(src, staging string) error {
	abs, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", src, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if !info.IsDir() {
		// Single-file install isn't a v2 target — packages are the
		// scaffold. If the user really wants `cp foo.md ~/.seek/skills/`
		// they can do it manually.
		return errors.New("source must be a directory (single-file .md install: copy manually into ~/.seek/skills/)")
	}
	return copyTree(abs, staging)
}

// loadStagedSkill is the only place install validates that staging
// actually contains a skill. Reuses internal/skill's loader rules:
// SKILL.md preferred, skill.md fallback.
func loadStagedSkill(staging string) (*skill.Skill, error) {
	for _, name := range []string{"SKILL.md", "skill.md"} {
		path := filepath.Join(staging, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		return skill.Parse(data, path)
	}
	return nil, errors.New("staging directory has no SKILL.md or skill.md")
}

// pickName implements the PRD v2 §5.1 resolution order:
// --name override > SKILL.md frontmatter name > directory basename.
func pickName(override, fromFrontmatter, fromDir string) string {
	if override != "" {
		return override
	}
	if fromFrontmatter != "" {
		return fromFrontmatter
	}
	return fromDir
}

// resolveTargetParent picks the dir under which <name>/ will be
// created. For project installs that's <ProjectDir>/.seek/skills/;
// for user installs it's UserDir or paths.UserSkills().
func resolveTargetParent(opts InstallOptions) (string, error) {
	if opts.Project {
		base := opts.ProjectDir
		if base == "" {
			base = "."
		}
		abs, err := filepath.Abs(base)
		if err != nil {
			return "", fmt.Errorf("resolve project dir: %w", err)
		}
		return filepath.Join(abs, ".seek", "skills"), nil
	}
	return resolveUserDir(opts.UserDir)
}

// resolveUserDir returns the user-level skills directory. Falls back
// to paths.UserSkills() when the caller didn't override.
func resolveUserDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	dir, err := paths.UserSkills()
	if err != nil {
		return "", fmt.Errorf("resolve user skills dir: %w", err)
	}
	return dir, nil
}

// writeInstallSidecar serialises the install state to .install.json
// inside the target directory.
func writeInstallSidecar(target string, opts *InstallOptions, typ SourceType) error {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	src := &skill.InstallSource{
		SchemaVersion:  1,
		InstalledAt:    now().UTC().Format(time.RFC3339),
		Type:           typ.String(),
		URL:            opts.Source,
		Ref:            "", // set by git fetcher when M8.1c lands
		Subpath:        opts.Subpath,
		ChecksumSHA256: opts.SHA256,
	}
	// For local sources, the "URL" stored in the sidecar is the
	// original path — useful for `update` (re-copy from the same
	// path) once that command lands.
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, ".install.json"), append(data, '\n'), 0o644)
}

// copyTree recursively copies src into dst. Skips .git directories at
// any depth — they'd be enormous and don't belong in a skill package
// (the git fetcher already strips its own .git after clone, but a
// user might have a `.git/` inside a local source by accident).
//
// Permissions on regular files are preserved; directories get 0755.
// Symlinks are followed (the symlink target is copied, not the link
// itself) — skill packages aren't expected to ship symlinks, and
// chasing them avoids dangling-link surprises after install.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		// Skip the entire .git tree — large and irrelevant.
		if info.IsDir() && rel != "." && info.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		// Regular files: open both, io.Copy, close. Preserve mode.
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
