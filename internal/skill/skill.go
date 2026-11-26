// Package skill is seek's runtime registry for Markdown-with-frontmatter
// skill files. Skills are read-only instructions injected into the
// agent's context on-demand via the `Skill` tool (PRD v0 §4.6, v2 §4).
//
// What this package owns:
//   - Parsing: pull recognised frontmatter fields out of a SKILL.md /
//     skill.md header, keep the rest as Body.
//   - Loading: scan the on-disk skill directories plus embedded
//     built-ins, recognise both single .md files (v0) and directory
//     packages (v2 — <dir>/SKILL.md), merge with priority (project >
//     user > builtin).
//   - A Set type with the lookups the agent loop needs.
//
// What it explicitly does NOT own:
//   - Deciding when to invoke a skill — that's the model's job. We only
//     surface the manifest in the system prompt.
//   - Anything network/MCP-shaped — Skill ≠ MCP (PRD §4.6 preface).
//   - Installation / uninstallation / update — those live in
//     internal/skillmgr (v2). The loader only reads on-disk state.
package skill

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/whyiyhw/seek/internal/frontmatter"
)

// Type tags how a Skill was loaded so `seek skill status` can show
// "single-file" / "package" / "builtin" without each call site having
// to re-derive it from Source.
type Type uint8

const (
	TypeSingleFile Type = iota // <dir>/foo.md — pre-v2 layout, still supported
	TypePackage                // <dir>/foo/SKILL.md — v2 directory-package layout (PRD v2 §4.1)
	TypeBuiltin                // go:embed'd, ships with the binary
)

// String returns the lowercase tag used in CLI output and tests.
func (t Type) String() string {
	switch t {
	case TypeSingleFile:
		return "single-file"
	case TypePackage:
		return "package"
	case TypeBuiltin:
		return "builtin"
	default:
		return "unknown"
	}
}

// Skill is one parsed skill — either a single .md file or a directory
// package's SKILL.md.
//
// v0 shipped with just Name/Description/Body/Source. v2 (PRD §4.1)
// appends optional metadata fields recognised from the SKILL.md
// frontmatter plus an InstallSource sidecar — all append-only so
// pre-v2 call-sites that only touch the first four fields keep
// compiling.
type Skill struct {
	Name        string // kebab-case unique identifier
	Description string // "when to use this" — goes into the system-prompt manifest (may be multi-line)
	Body        string // Markdown instructions — only revealed when the model calls the Skill tool
	Source      string // file path, or "builtin:<name>" for go:embed'd skills

	// v2 metadata (all optional; absent fields stay zero-valued).
	Type         Type              // how this skill was discovered — see Type constants
	Version      string            // semver from frontmatter `version:`; empty means "unversioned"
	License      string            // SPDX-ish string; v2 records but does not validate
	Author       string            // freeform author/contact
	Keywords     []string          // frontmatter `keywords:` list; reserved for v3 search
	AllowedTools []string          // frontmatter `allowed-tools:` list; v2 records but does not enforce
	Extra        map[string]string // any frontmatter key we didn't recognise — forward-compat with future spec additions

	// InstallSource is populated when a sibling .install.json is read
	// at load time (PRD v2 §4.2). nil for manual `cp` / unmanaged skills,
	// which is the signal that `seek skill update` is unavailable.
	InstallSource *InstallSource
}

// InstallSource records how a skill landed on disk so `seek skill update`
// knows where to fetch from. Written by `seek skill install`, read by
// the loader, never edited by skill authors.
//
// The JSON layout is flat (PRD v2 §4.2): seek-private metadata, no
// reason for nesting.
type InstallSource struct {
	SchemaVersion  int    `json:"schema_version"`
	InstalledAt    string `json:"installed_at"`              // RFC3339
	Type           string `json:"type"`                      // "local" | "git" | "https"
	URL            string `json:"url,omitempty"`             // original source URL (git remote or HTTPS)
	Ref            string `json:"ref,omitempty"`             // git only — tag / branch / sha at install time
	Subpath        string `json:"subpath,omitempty"`         // git only — package's path inside the repo
	ResolvedCommit string `json:"resolved_commit,omitempty"` // git only — recorded so update can detect drift
	ChecksumSHA256 string `json:"checksum_sha256,omitempty"` // https only — fixed checksum of the tarball
}

// nameRE enforces the PRD §4.6.1 contract for `name`: lowercase letters,
// digits, hyphens; must start with a letter. Strict on purpose — we use
// the name as a tool argument and want a single canonical form.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// recognisedFields is the set of frontmatter keys that get their own
// typed Skill field. Anything outside this set lands in Skill.Extra so
// future Anthropic Agent Skills spec additions don't break older seek
// builds (PRD v2 §3 design objective #2 "zero format invention").
var recognisedFields = map[string]struct{}{
	"name":          {},
	"description":   {},
	"version":       {},
	"license":       {},
	"author":        {},
	"keywords":      {},
	"allowed-tools": {},
}

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
// Frontmatter is parsed by internal/frontmatter — the shared
// Markdown+frontmatter dialect extracted verbatim from this package —
// so SKILL.md and agents/<name>.md (feature-orchestration.md D10)
// accept the exact same value shapes. Error strings keep the
// "skill <source>: ..." prefix that has always wrapped them.
func Parse(data []byte, source string) (*Skill, error) {
	fields, lists, body, err := frontmatter.Split(data)
	if err != nil {
		return nil, fmt.Errorf("skill %s: %w", source, err)
	}

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

	sk := &Skill{
		Name:         name,
		Description:  desc,
		Body:         body,
		Source:       source,
		Version:      fields["version"],
		License:      fields["license"],
		Author:       fields["author"],
		Keywords:     lists["keywords"],
		AllowedTools: lists["allowed-tools"],
	}

	// Stash anything we didn't recognise. Scalars in fields[] and lists
	// in lists[] — for Extra we only keep scalars (joining list shapes
	// would lose information; the v3 `status` command can show counts).
	for k, v := range fields {
		if _, ok := recognisedFields[k]; ok {
			continue
		}
		if sk.Extra == nil {
			sk.Extra = map[string]string{}
		}
		sk.Extra[k] = v
	}
	return sk, nil
}

// Set is a name-indexed collection of skills. Construction is via
// Load() in loader.go; the in-place mutator Add lets the loader merge
// from multiple sources with priority semantics (first writer wins).
type Set struct {
	byName map[string]*Skill
	order  []string // insertion order, used for stable List output
	// shadowed tracks lower-priority skills rejected because a
	// same-named higher-priority entry won. Powers
	// `seek skill status <name>` (PRD v2 §7 acceptance #5).
	shadowed map[string][]string
}

// NewSet returns an empty Set.
func NewSet() *Set { return &Set{byName: map[string]*Skill{}} }

// Add inserts s into the set if no skill with that name is already
// present. Returns true if the skill was added, false if it was shadowed
// by an earlier (higher-priority) entry. Shadowed entries' Source
// paths are remembered so `status` can show them.
func (s *Set) Add(sk *Skill) bool {
	if sk == nil || sk.Name == "" {
		return false
	}
	if _, exists := s.byName[sk.Name]; exists {
		if s.shadowed == nil {
			s.shadowed = map[string][]string{}
		}
		s.shadowed[sk.Name] = append(s.shadowed[sk.Name], sk.Source)
		return false
	}
	s.byName[sk.Name] = sk
	s.order = append(s.order, sk.Name)
	return true
}

// Get returns the skill for name, or nil if not found.
func (s *Set) Get(name string) *Skill { return s.byName[name] }

// Shadowed returns the Source paths of skills sharing `name` that
// were rejected because the priority cascade landed them below the
// winner. Empty when no collision occurred.
func (s *Set) Shadowed(name string) []string { return s.shadowed[name] }

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
	if s == nil || s.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Available skills\n\n")
	b.WriteString("Each skill describes when it should be applied. To use one, call the `Skill` tool with the skill's `name`; the tool returns the skill's instructions, which you should then follow.\n\n")
	for _, sk := range s.List() {
		// Manifest stays single-line per skill — block-scalar
		// descriptions are joined back into one line so we don't
		// poison the prefix-cache with multi-line variability.
		desc := strings.ReplaceAll(sk.Description, "\n", " ")
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, desc)
	}
	return b.String()
}

// ErrNotFound is returned by the Skill tool when the requested name
// isn't in the loaded Set.
var ErrNotFound = errors.New("skill: not found")
