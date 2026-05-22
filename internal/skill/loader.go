package skill

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/paths"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// LoadStats is what cmd/seek prints at startup so the user knows their
// skill directories were actually read.
type LoadStats struct {
	BySource map[string]int // e.g. {"project .seek": 2, "user ~/.seek": 1, "builtin": 4}
	Errors   []error        // non-fatal: a malformed file shouldn't keep the agent from launching
}

// LoadOptions controls Load — kept tiny on purpose. ProjectDir defaults
// to "." (cwd). UserSkillsDir defaults to paths.UserSkills() (~/.seek/skills/).
// Tests inject fakes via these fields.
type LoadOptions struct {
	ProjectDir    string
	UserSkillsDir string // overrides the default ~/.seek/skills/ path; empty = default
}

// sourceDir is one slot in the priority cascade. Lower priority number
// = wins on name collision.
type sourceDir struct {
	priority int
	label    string // human-readable, shown in LoadStats and /skills
	path     string // may be empty if a directory couldn't be resolved
}

// Load discovers and parses skills from two on-disk locations plus the
// embedded built-ins:
//
//  1. <project>/.seek/skills/   — project-level, checked-in
//  2. ~/.seek/skills/           — user-level (or $SEEK_HOME/skills)
//  3. embedded builtin/*.md     — shipped with the binary
//
// First writer wins on name collision, so project skills override
// user skills, which override built-ins. Bad files are recorded in
// stats.Errors and skipped — they don't block startup.
func Load(opts LoadOptions) (*Set, LoadStats, error) {
	stats := LoadStats{BySource: map[string]int{}}

	if opts.ProjectDir == "" {
		opts.ProjectDir = "."
	}
	userSkillsDir := opts.UserSkillsDir
	if userSkillsDir == "" {
		// Best-effort: if we can't resolve the home dir, skip the user
		// slot entirely — project-level + builtin still work.
		if p, err := paths.UserSkills(); err == nil {
			userSkillsDir = p
		}
	}

	dirs := []sourceDir{
		{1, "project .seek", filepath.Join(opts.ProjectDir, ".seek", "skills")},
	}
	if userSkillsDir != "" {
		dirs = append(dirs, sourceDir{2, "user ~/.seek", userSkillsDir})
	}

	set := NewSet()

	// Walk the on-disk slots in priority order, first writer wins.
	for _, d := range dirs {
		if d.path == "" {
			continue
		}
		count, errs := loadFromDir(set, d.path, d.label)
		stats.BySource[d.label] = count
		stats.Errors = append(stats.Errors, errs...)
	}

	// Built-ins fill in any names not already provided by the user.
	count, errs := loadEmbedded(set)
	stats.BySource["builtin"] = count
	stats.Errors = append(stats.Errors, errs...)

	return set, stats, nil
}

func loadFromDir(set *Set, dir, label string) (int, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil // missing directory is fine — that's the common case
		}
		return 0, []error{fmt.Errorf("skills: read %s (%s): %w", dir, label, err)}
	}

	// Sort for deterministic iteration so tests don't flake when two
	// files in the same dir happen to share a name (shouldn't happen,
	// but a stable winner is friendlier when it does).
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		added int
		errs  []error
	)
	for _, e := range entries {
		name := e.Name()
		// Skip dotfiles/dotdirs (.install.json, .gitkeep, editor
		// scratch, etc). Skill names can't start with `.` anyway —
		// the kebab regex rejects it.
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			sk, warnings := loadPackage(filepath.Join(dir, name))
			errs = append(errs, warnings...)
			if sk == nil {
				continue
			}
			if set.Add(sk) {
				added++
			}
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("skills: %s: %w", path, err))
			continue
		}
		sk, err := Parse(data, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sk.Type = TypeSingleFile
		if set.Add(sk) {
			added++
		}
	}
	return added, errs
}

// loadPackage reads a directory-package skill (PRD v2 §4.1). Returns
// (nil, warnings) when the directory doesn't contain a SKILL.md /
// skill.md — that's not fatal, the caller skips and surfaces the
// warning via LoadStats.
//
// Resolution rules:
//   - SKILL.md (uppercase) is the canonical Anthropic Agent Skills
//     entry point — preferred when present.
//   - skill.md (lowercase) is a tolerated fallback so users migrating
//     a v0 single-file skill into a directory don't have to rename.
//   - Both present → take SKILL.md and warn so the user can clean up.
//   - Neither present → warn and skip (an empty subdir is more often
//     a mistake than an intentional placeholder).
func loadPackage(dir string) (*Skill, []error) {
	upper := filepath.Join(dir, "SKILL.md")
	lower := filepath.Join(dir, "skill.md")
	hasUpper := fileExists(upper)
	hasLower := fileExists(lower)

	var warnings []error
	var entry string
	switch {
	case hasUpper && hasLower:
		entry = upper
		warnings = append(warnings, fmt.Errorf(
			"skills: %s: both SKILL.md and skill.md present; using SKILL.md (Anthropic Agent Skills canonical form)", dir))
	case hasUpper:
		entry = upper
	case hasLower:
		entry = lower
	default:
		return nil, []error{fmt.Errorf(
			"skills: %s: directory has no SKILL.md or skill.md; skipped", dir)}
	}

	data, err := os.ReadFile(entry)
	if err != nil {
		warnings = append(warnings, fmt.Errorf("skills: %s: %w", entry, err))
		return nil, warnings
	}
	sk, err := Parse(data, entry)
	if err != nil {
		warnings = append(warnings, err)
		return nil, warnings
	}
	sk.Type = TypePackage

	// Sibling .install.json (PRD v2 §4.2) — absence is the common case
	// for manual `cp` installs. A malformed file is surfaced as a
	// warning so `seek skill status` can show why update doesn't work.
	if src, err := readInstallSource(filepath.Join(dir, ".install.json")); err != nil {
		warnings = append(warnings, fmt.Errorf("skills: %s: %w", filepath.Join(dir, ".install.json"), err))
	} else {
		sk.InstallSource = src // nil when the file isn't there — fine
	}

	return sk, warnings
}

// fileExists is a tiny helper kept inline rather than promoted to
// internal/paths so the loader doesn't grow a dependency for a single
// stat call.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// readInstallSource returns the parsed sidecar or nil if the file
// doesn't exist. A malformed file is an error — silent omission would
// mask why update isn't working.
func readInstallSource(path string) (*InstallSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var src InstallSource
	if err := json.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("parse .install.json: %w", err)
	}
	return &src, nil
}

func loadEmbedded(set *Set) (int, []error) {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		// A missing embed directory means we built without any
		// built-ins. Not fatal — agent still runs.
		return 0, nil
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		added int
		errs  []error
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := "builtin/" + e.Name()
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("skills: embedded %s: %w", path, err))
			continue
		}
		sk, err := Parse(data, "builtin:"+strings.TrimSuffix(e.Name(), ".md"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sk.Type = TypeBuiltin
		if set.Add(sk) {
			added++
		}
	}
	return added, errs
}

// FormatLoadSummary renders LoadStats as a single line suitable for
// the startup banner. Empty when nothing was loaded.
func (s LoadStats) FormatLoadSummary() string {
	total := 0
	for _, n := range s.BySource {
		total += n
	}
	if total == 0 {
		return ""
	}

	// Stable order so the summary doesn't shuffle between runs.
	keys := make([]string, 0, len(s.BySource))
	for k := range s.BySource {
		if s.BySource[k] > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d from %s", s.BySource[k], k))
	}
	return fmt.Sprintf("Loaded %d skills (%s)", total, strings.Join(parts, ", "))
}
