// Package skill is seek's runtime registry for Markdown-with-frontmatter
// skill files. Skills are read-only instructions injected into the
// agent's context on-demand via the `Skill` tool (PRD §4.6).
//
// What this package owns:
//   - Parsing: pull `name` + `description` out of a YAML-ish header,
//     keep the rest as Body.
//   - Loading: scan the four documented directories plus embedded
//     built-ins, merge with priority (project > user > builtin;
//     within a tier .seek beats .claude).
//   - A Set type with the lookups the agent loop needs.
//
// What it explicitly does NOT own:
//   - Deciding when to invoke a skill — that's the model's job. We only
//     surface the manifest in the system prompt.
//   - Anything network/MCP-shaped — Skill ≠ MCP (PRD §4.6 preface).
package skill

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Skill is one parsed .md file.
type Skill struct {
	Name        string // kebab-case unique identifier
	Description string // single-line "when to use this" — goes into the system-prompt manifest
	Body        string // Markdown instructions — only revealed when the model calls the Skill tool
	Source      string // file path, or "builtin:<name>" for go:embed'd skills
}

// nameRE enforces the PRD §4.6.1 contract for `name`: lowercase letters,
// digits, hyphens; must start with a letter. Strict on purpose — we use
// the name as a tool argument and want a single canonical form.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Parse reads a single skill file's bytes and returns the Skill. The
// expected layout is:
//
//	---
//	name: kebab-case-name
//	description: single-line summary of when to use this skill
//	---
//
//	<markdown body>
//
// Frontmatter is parsed as a deliberate subset of YAML — only top-level
// `key: value` pairs, no nested maps, no multi-line block scalars. The
// PRD spec only uses two fields and Claude Code skills follow the same
// convention, so the full YAML grammar is dead weight here.
func Parse(data []byte, source string) (*Skill, error) {
	// Strip a UTF-8 BOM if present so the `---` check below doesn't
	// silently miss the opener on files saved from Windows editors.
	text := strings.TrimPrefix(string(data), "\ufeff")
	// Be tolerant of leading blank lines — `---` MUST be the first
	// non-blank content though, otherwise it's not frontmatter.
	trimmed := strings.TrimLeft(text, "\n\r ")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, fmt.Errorf("skill %s: missing frontmatter (file must start with `---`)", source)
	}

	// Walk lines so we can locate the closing `---` precisely.
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("skill %s: empty after frontmatter opener", source)
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("skill %s: unterminated frontmatter (no closing `---`)", source)
	}

	fields := parseFrontmatter(lines[1:closeIdx])
	body := strings.TrimLeft(strings.Join(lines[closeIdx+1:], "\n"), "\n")

	name := fields["name"]
	if name == "" {
		return nil, fmt.Errorf("skill %s: frontmatter missing `name`", source)
	}
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("skill %s: name %q must be kebab-case ([a-z][a-z0-9-]*)", source, name)
	}
	desc := fields["description"]
	if desc == "" {
		return nil, fmt.Errorf("skill %s: frontmatter missing `description`", source)
	}

	return &Skill{
		Name:        name,
		Description: desc,
		Body:        body,
		Source:      source,
	}, nil
}

// parseFrontmatter pulls `key: value` pairs out of the supplied lines.
// Unknown keys are kept (so we don't break on future additions) but
// only `name` and `description` are validated downstream. Quoted values
// have their wrapping quotes stripped — matches what humans write when
// a description happens to contain a colon.
func parseFrontmatter(lines []string) map[string]string {
	out := map[string]string{}
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		// Blank lines and comments are fine in frontmatter.
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out
}

// Set is a name-indexed collection of skills. Construction is via
// Load() in loader.go; the in-place mutator Add lets the loader merge
// from multiple sources with priority semantics (first writer wins).
type Set struct {
	byName map[string]*Skill
	order  []string // insertion order, used for stable List output
}

// NewSet returns an empty Set.
func NewSet() *Set { return &Set{byName: map[string]*Skill{}} }

// Add inserts s into the set if no skill with that name is already
// present. Returns true if the skill was added, false if it was shadowed
// by an earlier (higher-priority) entry.
func (s *Set) Add(sk *Skill) bool {
	if sk == nil || sk.Name == "" {
		return false
	}
	if _, exists := s.byName[sk.Name]; exists {
		return false
	}
	s.byName[sk.Name] = sk
	s.order = append(s.order, sk.Name)
	return true
}

// Get returns the skill for name, or nil if not found.
func (s *Set) Get(name string) *Skill { return s.byName[name] }

// List returns skills in insertion order — stable for tests and for
// the /skills command's rendering.
func (s *Set) List() []*Skill {
	out := make([]*Skill, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.byName[n])
	}
	return out
}

// Len returns the number of skills in the set.
func (s *Set) Len() int { return len(s.order) }

// Manifest renders the system-prompt section that tells the model
// which skills exist. PRD §4.6.3 is explicit: only name + description,
// never body — bodies are pulled on-demand via the Skill tool.
//
// Returns "" when the set is empty so cmd/seek can decide whether to
// append the section at all.
func (s *Set) Manifest() string {
	if s.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Available skills\n\n")
	b.WriteString("Each skill describes when it should be applied. To use one, call the `Skill` tool with the skill's `name`; the tool returns the skill's instructions, which you should then follow.\n\n")
	for _, sk := range s.List() {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String()
}

// ErrNotFound is returned by the Skill tool when the requested name
// isn't in the loaded Set.
var ErrNotFound = errors.New("skill: not found")
