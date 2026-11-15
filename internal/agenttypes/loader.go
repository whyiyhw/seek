package agenttypes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/paths"
)

// LoadStats is what cmd/seek prints at startup, same shape as
// skill.LoadStats: per-source counts plus non-fatal errors. A
// malformed file must never keep the agent from launching.
type LoadStats struct {
	BySource map[string]int // e.g. {"project .seek": 1, "user ~/.seek": 2, "builtin": 3}
	Errors   []error
}

// LoadOptions controls Load. ProjectDir defaults to "." (cwd).
// UserAgentsDir overrides paths.UserAgents() (~/.seek/agents/) — tests
// inject fakes through it.
type LoadOptions struct {
	ProjectDir    string
	UserAgentsDir string // empty = default ~/.seek/agents/
}

// Load discovers and parses agent-type definitions from the two
// on-disk scopes plus the compile-time built-ins:
//
//  1. <project>/.seek/agents/   — project-level, checked-in (wins)
//  2. ~/.seek/agents/           — user-level
//  3. Builtins()                — general-purpose / explore / plan
//
// First writer wins on name collision, so a project `explore.md`
// overrides a user `explore.md`, which overrides the built-in explore
// (D11's scope stack: 内置默认 < agents/*.md, project > user). Bad
// files are recorded in stats.Errors and skipped — they don't block
// startup, and they never shadow a working definition of the same name
// from a lower scope... except within one directory: two files in the
// SAME directory declaring one name resolve deterministically by
// filename sort order (first wins, loser reported in stats.Errors).
func Load(opts LoadOptions) (*Set, LoadStats, error) {
	stats := LoadStats{BySource: map[string]int{}}

	if opts.ProjectDir == "" {
		opts.ProjectDir = "."
	}
	userAgentsDir := opts.UserAgentsDir
	if userAgentsDir == "" {
		// Best-effort, same as the skill loader: an unresolvable home
		// dir skips the user slot; project + builtin still work.
		if p, err := paths.UserAgents(); err == nil {
			userAgentsDir = p
		}
	}

	set := NewSet()

	// sourceSlot mirrors skill's sourceDir: one slot in the priority
	// cascade, lower number wins on name collision.
	type sourceSlot struct {
		dir    string
		label  string
		origin Origin
	}
	onDisk := []sourceSlot{
		{filepath.Join(opts.ProjectDir, ".seek", "agents"), "project .seek", OriginProject},
	}
	if userAgentsDir != "" {
		onDisk = append(onDisk, sourceSlot{userAgentsDir, "user ~/.seek", OriginUser})
	}

	for _, d := range onDisk {
		count, errs := loadFromDir(set, d.dir, d.label, d.origin)
		stats.BySource[d.label] = count
		stats.Errors = append(stats.Errors, errs...)
	}

	// Built-ins fill the names no file provided.
	for _, def := range Builtins() {
		if set.Add(def) {
			stats.BySource["builtin"]++
		}
	}

	return set, stats, nil
}

// loadFromDir reads one scope directory. Missing directory is the
// common case (most projects define no custom types) and is not an
// error. Entries are visited in sorted filename order so same-dir name
// collisions resolve deterministically.
func loadFromDir(set *Set, dir, label string, origin Origin) (int, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, []error{fmt.Errorf("agents: read %s (%s): %w", dir, label, err)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		added int
		errs  []error
	)
	for _, e := range entries {
		name := e.Name()
		// Dot entries: .gitkeep, editor scratch, future sidecars.
		// Skipped silently, same rule as the skill loader.
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			// Agent types are single .md files (D10) — a subdirectory
			// is more likely a mistake than a placeholder, so warn
			// rather than skip silently.
			errs = append(errs, fmt.Errorf("agents: %s: subdirectory ignored (agent types are single .md files)", filepath.Join(dir, name)))
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("agents: %s: %w", path, err))
			continue
		}
		def, err := Parse(data, strings.TrimSuffix(name, ".md"), path)
		if err != nil {
			errs = append(errs, fmt.Errorf("agents: %s: %w", path, err))
			continue
		}
		def.Origin = origin
		if set.Add(def) {
			added++
		} else if winner := set.Get(def.Name); winner.Origin == origin {
			// Same-scope duplicate (two files in one directory
			// declaring one name) is a user mistake — report it.
			// Cross-scope shadowing (project explore.md over user
			// explore.md) is the designed override and stays silent;
			// it remains visible via Set.Shadowed.
			errs = append(errs, fmt.Errorf(
				"agents: %s: duplicate name %q — shadows %s (sorted-filename order decides within a scope)",
				path, def.Name, winner.Source))
		}
	}
	return added, errs
}

// Set is a name-indexed collection of Definitions. Construction is via
// Load() (or Builtins() feeding Add); the shape mirrors skill.Set —
// first-writer-wins, insertion-ordered List, shadowed provenance kept
// for reporting.
type Set struct {
	byName map[string]*Definition
	order  []string // insertion order, stable List output
	// shadowed records lower-priority definitions rejected because a
	// same-named higher-priority entry won. Powers the /agents
	// "overridden by" hint and load diagnostics.
	shadowed map[string][]string
}

// NewSet returns an empty Set.
func NewSet() *Set { return &Set{byName: map[string]*Definition{}} }

// Add inserts def if no definition with that name is present.
// Returns true if added, false if shadowed by an earlier
// (higher-priority) entry; shadowed Sources are remembered.
func (s *Set) Add(def *Definition) bool {
	if def == nil || def.Name == "" {
		return false
	}
	if _, exists := s.byName[def.Name]; exists {
		if s.shadowed == nil {
			s.shadowed = map[string][]string{}
		}
		s.shadowed[def.Name] = append(s.shadowed[def.Name], def.Source)
		return false
	}
	s.byName[def.Name] = def
	s.order = append(s.order, def.Name)
	return true
}

// Get returns the definition for name, or nil if not found.
func (s *Set) Get(name string) *Definition { return s.byName[name] }

// Shadowed returns the Sources of definitions sharing `name` that lost
// to the winner. Empty when no collision occurred.
func (s *Set) Shadowed(name string) []string { return s.shadowed[name] }

// List returns definitions in insertion order — built-ins last, so a
// fresh Load() lists project types, then user types, then the three
// defaults. Stable for tests and /agents rendering.
func (s *Set) List() []*Definition {
	out := make([]*Definition, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.byName[n])
	}
	return out
}

// Len returns the number of definitions in the set.
func (s *Set) Len() int { return len(s.order) }

// CustomCount returns how many definitions came from files (project or
// user scope) — everything that is not a built-in. /agents uses it to
// decide whether the default view grows its custom-types hint line.
func (s *Set) CustomCount() int {
	n := 0
	for _, def := range s.List() {
		if def.Origin != OriginBuiltin {
			n++
		}
	}
	return n
}
