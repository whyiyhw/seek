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
// inject fakes through it. ConfigAgents is the config `agents` 段
// (P2-c): per-built-in routing overrides applied between the file
// scopes and the built-ins (D14's "插在 builtin 与文件层之间").
type LoadOptions struct {
	ProjectDir    string
	UserAgentsDir string // empty = default ~/.seek/agents/
	ConfigAgents  map[string]RouteOverride
}

// RouteOverride is one config `agents`-section entry as data — the
// routing knobs a config layer may set on a built-in type (D6's
// minimal-override shape, realised in P2-c). Tools/body/workflow are
// deliberately absent: the override clones a built-in and may only
// re-route it, never re-permission or re-prompt it (D13's tighten-only
// axis has no config surface; those fields belong to agents/*.md).
type RouteOverride struct {
	Provider string
	Model    string
	Effort   string // "" | low | medium | high — legalEfforts, validated in Load
	MaxTurns int    // > 0 or ignored with a warning
}

// Load discovers and parses agent-type definitions from the two
// on-disk scopes, the config override layer, and the compile-time
// built-ins:
//
//  1. <project>/.seek/agents/   — project-level, checked-in (wins)
//  2. ~/.seek/agents/           — user-level
//  3. ConfigAgents              — config `agents` 段 routing overrides
//  4. Builtins()                — general-purpose / explore / plan
//
// First writer wins on name collision, so a project `explore.md`
// overrides a user `explore.md`, which overrides a config entry, which
// overrides the built-in explore (the scope stack 内置默认 < config 段
// < agents/*.md, project > user — D11 + D14 as landed in P2-c). Bad
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

	// Config layer (P2-c): between the file scopes and the built-ins.
	stats.BySource["config agents"] = applyConfigOverrides(set, opts.ConfigAgents, &stats)

	// Built-ins fill the names nothing above provided.
	for _, def := range Builtins() {
		if set.Add(def) {
			stats.BySource["builtin"]++
		}
	}

	return set, stats, nil
}

// applyConfigOverrides merges the config `agents` 段 into the set as
// OriginConfig clones of the built-ins. Keys are visited in sorted
// order so the Set's insertion order (and everything derived from it —
// /agents types, the schema enum) is deterministic for a given
// config: the D12 byte-discipline depends on it.
//
// Semantics per key:
//
//   - names a built-in (and no file claimed it) → clone the built-in,
//     override the routing knobs, Origin=OriginConfig;
//   - names a type a FILE already defined → the file wins (config <
//     files); Add records the loss in Set.Shadowed, silent by design —
//     same convention as project-over-user;
//   - names nothing known (no built-in, no file type) → warning into
//     stats.Errors and ignored: forward-compat, a config written for a
//     newer seek (or a type since deleted) must not break startup.
//
// Field-level shape errors (bad effort, non-positive max_turns) warn
// and drop just that field, not the whole entry.
func applyConfigOverrides(set *Set, overrides map[string]RouteOverride, stats *LoadStats) int {
	if len(overrides) == 0 {
		return 0
	}
	builtinByName := map[string]*Definition{}
	for _, b := range Builtins() {
		builtinByName[b.Name] = b
	}

	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)

	applied := 0
	for _, name := range names {
		ov := overrides[name]
		base := builtinByName[name]
		if base == nil {
			if set.Get(name) == nil {
				stats.Errors = append(stats.Errors, fmt.Errorf(
					"agents: config key %q matches no built-in or file type — ignored", name))
			} else {
				// A file type owns the name; config < files. Record
				// the loss through Add's shadow bookkeeping (a
				// name-only stub — Add stores nothing on a lost race)
				// so /agents types shows the override provenance.
				set.Add(&Definition{Name: name, Source: "config:agents:" + name})
			}
			continue
		}
		clone := *base // shallow: Tools/Body shared read-only, never mutated
		if ov.Provider != "" {
			clone.Provider = ov.Provider
		}
		if ov.Model != "" {
			clone.Model = ov.Model
		}
		if ov.Effort != "" {
			if !legalEfforts[ov.Effort] {
				stats.Errors = append(stats.Errors, fmt.Errorf(
					"agents: config %q effort %q — valid: low, medium, high; field ignored", name, ov.Effort))
			} else {
				clone.Effort = ov.Effort
			}
		}
		if ov.MaxTurns != 0 {
			if ov.MaxTurns < 0 {
				stats.Errors = append(stats.Errors, fmt.Errorf(
					"agents: config %q max_turns %d — must be positive; field ignored", name, ov.MaxTurns))
			} else {
				clone.MaxTurns = ov.MaxTurns
			}
		}
		clone.Origin = OriginConfig
		clone.Source = "config:agents:" + name
		if set.Add(&clone) {
			applied++
		}
	}
	return applied
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
