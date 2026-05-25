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

// StageResult is the output of Stage(). It carries everything Commit
// needs to finish the install without re-fetching, plus the metadata
// the model uses to decide whether to commit at all.
//
// StagingDir lives under os.TempDir() with a "seek-skill-staging-"
// prefix; the caller passes it back into Commit. The skill's body has
// already been parsed and validated; Description and BodyPreview are
// surfaced so the LLM has enough context to judge legitimacy without
// re-reading from disk (though it can: the staging files are real
// files at StagingDir).
type StageResult struct {
	Name        string     // resolved kebab-case name (frontmatter / override / dir)
	Description string     // from frontmatter (single line)
	Source      string     // verbatim Source from StageOptions, for sidecar + approval display
	Type        SourceType // local / git / https
	StagingDir  string     // absolute path under os.TempDir(); fed back to Commit
	Files       []string   // relative paths in the package, sorted; model uses for read/grep targeting
	BodyPreview string     // first ~500 chars of SKILL.md body; spec hint for the model

	// Internal: fields Commit needs that aren't useful to the caller.
	// Kept exported so Commit's contract is a value pass, not a hidden
	// state bag.
	Force      bool   // mirrors StageOptions.Force; surfaced for the approval prompt
	Project    bool   // mirrors StageOptions.Project
	ProjectDir string // mirrors StageOptions.ProjectDir
	UserDir    string // mirrors StageOptions.UserDir
	SHA256     string // mirrors StageOptions.SHA256 (for sidecar)
	Subpath    string // mirrors StageOptions.Subpath (for sidecar)
	NowFn      func() time.Time
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
//
// Implementation is the Stage + Commit pair — kept as a single entry
// point for the CLI path where the user has already opted in at the
// shell. The interactive (tool-driven) path goes Stage → human
// approval → Commit so the user sees the staged skill before it lands
// on disk.
func Install(opts InstallOptions) (*InstallResult, error) {
	stage, err := Stage(StageOptions{
		Source:     opts.Source,
		Type:       opts.Type,
		Name:       opts.Name,
		Force:      opts.Force,
		Project:    opts.Project,
		ProjectDir: opts.ProjectDir,
		UserDir:    opts.UserDir,
		Subpath:    opts.Subpath,
		SHA256:     opts.SHA256,
		HTTP:       opts.HTTP,
		Now:        opts.Now,
	})
	if err != nil {
		return nil, err
	}
	// Stage may have created the temp dir; if Commit refuses or
	// fails we still want it gone. On Commit success the staging
	// dir is consumed by the rename, so RemoveAll is a no-op.
	defer os.RemoveAll(filepath.Dir(stage.StagingDir))
	return Commit(stage)
}

// StageOptions mirrors InstallOptions but is the input to Stage —
// the half of the flow that fetches and validates. The two structs
// stay in sync deliberately: the only thing CallSite-the-tool needs
// that CallSite-the-CLI doesn't is the ability to pause between
// these two phases for human approval.
type StageOptions struct {
	Source     string
	Type       SourceType
	Name       string
	Force      bool
	Project    bool
	ProjectDir string
	UserDir    string
	Subpath    string
	SHA256     string
	HTTP       *http.Client
	Now        func() time.Time
}

// Stage fetches and validates a skill package without writing to the
// user's skills directory. Output is a *StageResult that can be:
//
//   - displayed to a human for approval (Source / Name / Description),
//   - inspected by an LLM via the staged filesystem path (StagingDir),
//   - handed back to Commit verbatim to finish the install.
//
// The staging directory lives under os.TempDir() with a
// "seek-skill-staging-" prefix; callers SHOULD treat the path as
// opaque and only re-feed it through Commit. The directory survives
// until Commit consumes it (via rename) or the OS cleans /tmp.
func Stage(opts StageOptions) (*StageResult, error) {
	if opts.Source == "" {
		return nil, errors.New("skillmgr: stage: Source is required")
	}
	typ := opts.Type
	if typ == SourceAuto {
		typ = detectSourceType(opts.Source)
	}

	// stagingParent gets a distinct prefix so Commit can validate
	// the path it receives is a real staging dir and not, say, /etc.
	stagingParent, err := os.MkdirTemp("", "seek-skill-staging-*")
	if err != nil {
		return nil, fmt.Errorf("skillmgr: create staging: %w", err)
	}
	staging := filepath.Join(stagingParent, "pkg")

	// On any error from here down, the staging tree is dead weight —
	// remove it before returning so /tmp doesn't accumulate detritus
	// from failed fetches. Success returns before this fires.
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(stagingParent)
		}
	}()

	switch typ {
	case SourceLocal:
		if err := stageLocal(opts.Source, staging); err != nil {
			return nil, err
		}
	case SourceHTTPS:
		if err := stageHTTPS(installOptsFromStage(opts), staging); err != nil {
			return nil, fmt.Errorf("skillmgr: %w", err)
		}
	case SourceGit:
		if err := stageGit(installOptsFromStage(opts), staging); err != nil {
			return nil, fmt.Errorf("skillmgr: %w", err)
		}
	default:
		return nil, fmt.Errorf("skillmgr: unsupported source type %v", typ)
	}

	sk, err := loadStagedSkill(staging)
	if err != nil {
		return nil, fmt.Errorf("skillmgr: source is not a valid skill package: %w", err)
	}
	name := pickName(opts.Name, sk.Name, filepath.Base(opts.Source))
	if name == "" {
		return nil, errors.New("skillmgr: could not resolve skill name (no --name, no frontmatter name, no usable directory name)")
	}

	files, err := listPackageFiles(staging)
	if err != nil {
		return nil, fmt.Errorf("skillmgr: list staged files: %w", err)
	}
	preview := bodyPreview(sk.Body, 500)

	success = true
	return &StageResult{
		Name:        name,
		Description: strings.ReplaceAll(sk.Description, "\n", " "),
		Source:      opts.Source,
		Type:        typ,
		StagingDir:  staging,
		Files:       files,
		BodyPreview: preview,
		Force:       opts.Force,
		Project:     opts.Project,
		ProjectDir:  opts.ProjectDir,
		UserDir:     opts.UserDir,
		SHA256:      opts.SHA256,
		Subpath:     opts.Subpath,
		NowFn:       opts.Now,
	}, nil
}

// Commit moves a staged skill into its final location and writes the
// .install.json sidecar. Idempotent against the staging dir: if the
// rename fails we leave the staging intact for retry.
//
// The staging dir is REQUIRED to live under os.TempDir() with the
// "seek-skill-staging-" prefix produced by Stage; arbitrary paths are
// refused. This is the defence in depth that keeps a misbehaving tool
// caller from asking Commit to move /etc/passwd into ~/.seek/skills/.
func Commit(stage *StageResult) (*InstallResult, error) {
	if stage == nil {
		return nil, errors.New("skillmgr: commit: nil stage")
	}
	if err := validateStagingPath(stage.StagingDir); err != nil {
		return nil, err
	}

	targetParent, err := resolveTargetParent(InstallOptions{
		Project:    stage.Project,
		ProjectDir: stage.ProjectDir,
		UserDir:    stage.UserDir,
	})
	if err != nil {
		return nil, err
	}
	target := filepath.Join(targetParent, stage.Name)

	if _, err := os.Stat(target); err == nil {
		if !stage.Force {
			return nil, fmt.Errorf("skillmgr: %q already exists at %s (use force=true to replace)", stage.Name, target)
		}
		if err := os.RemoveAll(target); err != nil {
			return nil, fmt.Errorf("skillmgr: force replace: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("skillmgr: stat target: %w", err)
	}

	if err := os.MkdirAll(targetParent, 0o755); err != nil {
		return nil, fmt.Errorf("skillmgr: mkdir target parent: %w", err)
	}

	// Atomic-ish rename across same-fs; copy-fallback for cross-fs
	// (e.g. /tmp on a different mount than $HOME).
	if err := os.Rename(stage.StagingDir, target); err != nil {
		if err := copyTree(stage.StagingDir, target); err != nil {
			return nil, fmt.Errorf("skillmgr: install copy fallback: %w", err)
		}
	}

	if !stage.Project {
		// Reconstruct the install options needed by the sidecar
		// writer from the StageResult — we don't carry the full
		// InstallOptions through, only what's relevant.
		sideOpts := &InstallOptions{
			Source:  stage.Source,
			Subpath: stage.Subpath,
			SHA256:  stage.SHA256,
			Now:     stage.NowFn,
		}
		if err := writeInstallSidecar(target, sideOpts, stage.Type); err != nil {
			return &InstallResult{Name: stage.Name, Dir: target, Type: stage.Type},
				fmt.Errorf("skillmgr: write .install.json (skill installed, but update tracking unavailable): %w", err)
		}
	}

	return &InstallResult{Name: stage.Name, Dir: target, Type: stage.Type}, nil
}

// installOptsFromStage converts a StageOptions back into the shape
// the source-specific stagers (stageHTTPS / stageGit) expect. Keeping
// the legacy InstallOptions input on those helpers avoids touching
// the four files they live in for a refactor that doesn't change
// behaviour.
func installOptsFromStage(s StageOptions) InstallOptions {
	return InstallOptions{
		Source:     s.Source,
		Type:       s.Type,
		Name:       s.Name,
		Force:      s.Force,
		Project:    s.Project,
		ProjectDir: s.ProjectDir,
		UserDir:    s.UserDir,
		Subpath:    s.Subpath,
		SHA256:     s.SHA256,
		HTTP:       s.HTTP,
		Now:        s.Now,
	}
}

// validateStagingPath refuses anything that isn't a "seek-skill-
// staging-<random>/pkg" path under the system temp dir. This is the
// hardening that lets us trust StagingDir as a tool argument without
// auditing every caller.
func validateStagingPath(p string) error {
	if p == "" {
		return errors.New("skillmgr: commit: StagingDir is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return fmt.Errorf("skillmgr: commit: resolve staging path: %w", err)
	}
	tmpDir, err := filepath.Abs(os.TempDir())
	if err != nil {
		return fmt.Errorf("skillmgr: commit: resolve tempdir: %w", err)
	}
	// The staging dir is `<TempDir>/seek-skill-staging-XYZ/pkg`.
	rel, err := filepath.Rel(tmpDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("skillmgr: commit: staging path %q is not under temp dir %q", abs, tmpDir)
	}
	// Walk up one level: must be "seek-skill-staging-*".
	parent := filepath.Base(filepath.Dir(abs))
	if !strings.HasPrefix(parent, "seek-skill-staging-") {
		return fmt.Errorf("skillmgr: commit: staging path %q does not have the expected seek-skill-staging-* prefix", abs)
	}
	if filepath.Base(abs) != "pkg" {
		return fmt.Errorf("skillmgr: commit: staging path %q does not end in /pkg", abs)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("skillmgr: commit: staging dir not found: %w", err)
	}
	return nil
}

// listPackageFiles walks the staging dir and returns relative paths
// (forward-slash) in sorted order. Capped at 200 entries — anything
// more is suspicious for a skill package and would push the tool
// result past useful sizes anyway.
func listPackageFiles(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) > 200 {
			return errors.New("too many files (>200) — refusing to stage; this looks like a non-skill repo")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// filepath.Walk returns lexicographic order on most platforms,
	// but make it explicit so the tool result is byte-stable across
	// OSes (prefix-cache friendliness).
	sortStrings(out)
	return out, nil
}

// bodyPreview returns the first n runes (NOT bytes) of body, trimmed
// so the preview doesn't end in the middle of a multi-byte character.
// Appends an ellipsis hint when truncated.
func bodyPreview(body string, n int) string {
	body = strings.TrimSpace(body)
	if len([]rune(body)) <= n {
		return body
	}
	runes := []rune(body)
	return string(runes[:n]) + "\n… [truncated]"
}

func sortStrings(s []string) {
	// stdlib sort would be one import for one call site — inline
	// insertion sort is fine for the ≤200 entries cap above.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
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
// inside the target directory. The recorded fields are exactly what
// Update needs to re-fetch — store too little here and `seek skill
// update` can't tell what to do; store too much and the format
// becomes a private migration burden.
func writeInstallSidecar(target string, opts *InstallOptions, typ SourceType) error {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	src := &skill.InstallSource{
		SchemaVersion: 1,
		InstalledAt:   now().UTC().Format(time.RFC3339),
		Type:          typ.String(),
		Subpath:       opts.Subpath,
	}
	switch typ {
	case SourceGit:
		// Split the user-facing URL into the bare repo URL and ref.
		// Re-fetch in Update needs both as separate fields so a
		// tag/branch can be replayed accurately.
		url, ref := splitRefFragment(opts.Source)
		src.URL = url
		src.Ref = ref
	case SourceLocal:
		// Resolve the path to absolute so a future `update` from a
		// different cwd still locates the source. Best-effort — if
		// resolution fails, fall back to the raw input.
		if abs, err := filepath.Abs(opts.Source); err == nil {
			src.URL = abs
		} else {
			src.URL = opts.Source
		}
	case SourceHTTPS:
		src.URL = opts.Source
		src.ChecksumSHA256 = opts.SHA256
	default:
		src.URL = opts.Source
	}
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
