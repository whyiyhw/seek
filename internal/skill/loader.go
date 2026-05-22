package skill

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// LoadStats is what cmd/seek prints at startup so the user knows their
// .seek/.claude/builtin directories were actually read. PRD §4.6.2
// specifies the format.
type LoadStats struct {
	BySource map[string]int // e.g. {"project .seek": 2, "user .claude": 1, "builtin": 4}
	Errors   []error        // non-fatal: a malformed file shouldn't keep the agent from launching
}

// LoadOptions controls Load — kept tiny on purpose. ProjectDir defaults
// to "." (cwd). HomeDir defaults to os.UserHomeDir(). Tests inject
// fakes via these fields.
type LoadOptions struct {
	ProjectDir string
	HomeDir    string
	XDGConfig  string // overrides $XDG_CONFIG_HOME; empty = real env / default
}

// sourceDir is one slot in the priority cascade. Lower priority number
// = wins on name collision.
type sourceDir struct {
	priority int
	label    string // human-readable, shown in LoadStats and /skills
	path     string // may be empty if HomeDir/ProjectDir weren't known
}

// Load discovers and parses skills from the documented locations,
// merging with PRD §4.6.2 priority semantics. The returned Set is
// safe to use even if some files failed to parse — bad files are
// recorded in stats.Errors and skipped.
func Load(opts LoadOptions) (*Set, LoadStats, error) {
	stats := LoadStats{BySource: map[string]int{}}

	if opts.ProjectDir == "" {
		opts.ProjectDir = "."
	}
	if opts.HomeDir == "" {
		// Best-effort — if we can't find the home dir, just skip the
		// user-level slots; project-level + builtin still work.
		opts.HomeDir, _ = os.UserHomeDir()
	}
	xdg := opts.XDGConfig
	if xdg == "" {
		xdg = os.Getenv("XDG_CONFIG_HOME")
	}
	if xdg == "" && opts.HomeDir != "" {
		xdg = filepath.Join(opts.HomeDir, ".config")
	}

	dirs := []sourceDir{
		{1, "project .seek", filepath.Join(opts.ProjectDir, ".seek", "skills")},
		{2, "project .claude", filepath.Join(opts.ProjectDir, ".claude", "skills")},
	}
	if xdg != "" {
		dirs = append(dirs, sourceDir{3, "user .config/seek", filepath.Join(xdg, "seek", "skills")})
	}
	if opts.HomeDir != "" {
		dirs = append(dirs, sourceDir{4, "user .claude", filepath.Join(opts.HomeDir, ".claude", "skills")})
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
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
		if set.Add(sk) {
			added++
		}
	}
	return added, errs
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
