// Package agenttypes is seek's read-only registry for typed subagent
// definitions — Markdown files with YAML frontmatter that declare a
// spawnable agent type (feature-orchestration.md §5, P2 / D10–D13).
//
// Layout mirrors the skill precedent exactly (single .md + frontmatter
// + body, project > user scope cascade, first-writer-wins on name
// collision):
//
//	<project>/.seek/agents/<name>.md   # project-level, checked-in
//	~/.seek/agents/<name>.md           # user-level
//
// What this package owns:
//   - Definition: the parsed, validated shape of one agent type.
//   - Parse: frontmatter (via internal/frontmatter — the same dialect
//     as SKILL.md) + static validation of every field.
//   - Load: the project > user > builtin scope cascade into a Set.
//   - Builtins: the three compile-time defaults (general-purpose /
//     explore / plan), which are also the single source of truth for
//     internal/subagent's spawn-time templates (templates.go adapts
//     them — the hardcoded map was retired there).
//
// What it explicitly does NOT own:
//   - Writes to ~/.seek/agents/ — those belong to internal/agentsmgr
//     (same red line as skillmgr for skills; landed in M-O2.3).
//     This package has NO write API by design; grep-audit acceptance
//     #4 in the PRD depends on that staying true.
//   - Spawn-time semantics (registry intersection, policy
//     restriction, routing). The Definition is data; since P2-b
//     internal/subagent's D13 gate (custom.go) adapts it into the
//     spawn-time Template via ManagerOpts.AgentTypes, and since P2-c
//     every origin routes through that one path. The `agent` tool
//     schema enum is generated from the registry at startup by
//     internal/tools/agent (D12).
//   - Hot reload — loaded once at session start. A later turn's file
//     edits are invisible until the next seek launch (deliberate:
//     schema bytes are frozen per-process for the prefix cache).
package agenttypes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/whyiyhw/seek/internal/frontmatter"
)

// Origin tags where a Definition came from so /agents can render
// "builtin" / "project" / "user" without re-deriving it from Source.
type Origin uint8

const (
	OriginBuiltin Origin = iota
	OriginProject
	OriginUser
	// OriginConfig tags a built-in clone carrying the config `agents`
	// section's routing overrides (P2-c, D6/D14): provider/model/
	// effort/max_turns only — tools, body and workflow are copied
	// verbatim from the built-in, because config routes brains, never
	// permissions.
	OriginConfig
)

// String returns the lowercase tag used in /agents and tests.
func (o Origin) String() string {
	switch o {
	case OriginBuiltin:
		return "builtin"
	case OriginProject:
		return "project"
	case OriginUser:
		return "user"
	case OriginConfig:
		return "config"
	default:
		return "unknown"
	}
}

// Definition is one typed agent definition — the D11 frontmatter
// contract as data. Zero-valued optional fields mean "inherit the
// parent session" (same semantics as omitting the key).
type Definition struct {
	// Name is the unique kebab-case identifier — the `subagent_type`
	// wire value. Comes from frontmatter `name`, falling back to the
	// filename stem (CC-style files omit the field).
	Name string

	// Description is the one-line "when to spawn this type" summary.
	// Required — it is what the model and /agents read to pick a type
	// (D12 injects it into the schema enum once M-O2.2 lands).
	Description string

	// Body is the markdown after the closing `---`: the role prompt
	// that replaces the built-in template's Extra clause. Required —
	// a type without a role prompt is a config error (PRD P2
	// acceptance #6), not a silent fallback to generic wording.
	Body string

	// Tools is the explicit tool whitelist. nil (or an empty list) =
	// inherit the parent registry minus the universal exclusions —
	// the general-purpose semantics.
	Tools []string

	// Provider / Model / Effort / MaxTurns are optional routing knobs
	// (D11). Provider and Model are carried verbatim — lane validity
	// (deepseek/glm, model∈lane) is resolved at spawn time by
	// subagent.ResolveRoute, which owns the routing table; duplicating
	// it here would give it two sources of truth.
	Provider string
	Model    string
	Effort   string // "" | low | medium | high
	MaxTurns int    // 0 = inherit

	// Workflow may only tighten. The single legal value is
	// "plan-analyze"; "plan-execute" or any loosening is a config
	// error (D13 — Policy.Spawn's monotonic restriction is a hard
	// gate, frontmatter is just one more source for it).
	Workflow string

	// Origin / Source record provenance for /agents and shadow reports.
	Origin Origin
	Source string // file path, or "builtin:<name>"
}

// nameRE is the same kebab-case contract as skill names — the name is
// a tool argument (`subagent_type`) and wants a single canonical form.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// forbiddenTools are never allowed in a Tools whitelist, regardless of
// what the parent registry has (D13):
//
//   - "agent": spawn depth is capped at 1 (v5 anti-goal); a custom
//     type that could nest-spawn would break the recursion guard.
//   - "ask_user": subagent picker disambiguation UX doesn't exist;
//     subagents pass decisions back via their summary.
//
// Listing them in Tools is a hard parse error (not a silent drop) —
// the author meant to grant something the system refuses to grant,
// and pretending otherwise would only surface later as a confusing
// spawn failure.
var forbiddenTools = map[string]bool{
	"agent":    true,
	"ask_user": true,
}

// legalEfforts mirrors the /effort command's three tiers.
var legalEfforts = map[string]bool{"low": true, "medium": true, "high": true}

// Parse reads one agent-definition file's bytes and returns the
// validated Definition. fileStem is the filename without `.md`
// (fallback identity when the frontmatter has no `name`); source is
// the path used in error messages and Definition.Source.
//
// Every failure is an error — the caller (Load) records it in
// LoadStats.Errors and skips the file; nothing half-parsed ever enters
// the Set. The error strings carry no "agent type" prefix; Load adds
// the file path via source so the message reads
// "agents: /path/to/x.md: <reason>".
//
// Unknown frontmatter keys are tolerated and dropped — same
// forward-compat trade-off skills make: a newer spec key must not
// break an older seek, at the cost of a typo'd key silently doing
// nothing.
func Parse(data []byte, fileStem, source string) (*Definition, error) {
	fields, lists, body, err := frontmatter.Split(data)
	if err != nil {
		return nil, err
	}

	def := &Definition{
		Name:        fields["name"],
		Description: fields["description"],
		Body:        body,
		Tools:       lists["tools"],
		Provider:    fields["provider"],
		Model:       fields["model"],
		Effort:      fields["effort"],
		Workflow:    fields["workflow"],
		Source:      source,
	}
	if def.Name == "" {
		def.Name = fileStem
	}
	if def.Name == "" {
		return nil, fmt.Errorf("no name: frontmatter `name` missing and filename stem empty")
	}
	if !nameRE.MatchString(def.Name) {
		return nil, fmt.Errorf("name %q must be kebab-case ([a-z][a-z0-9-]*)", def.Name)
	}
	if def.Description == "" {
		return nil, fmt.Errorf("frontmatter missing `description`")
	}
	if strings.TrimSpace(def.Body) == "" {
		return nil, fmt.Errorf("empty body — the role prompt is a required field")
	}

	for _, tool := range def.Tools {
		if forbiddenTools[tool] {
			return nil, fmt.Errorf("tools: %q is never allowed in an agent type (universal exclusion)", tool)
		}
	}
	if len(def.Tools) == 0 {
		def.Tools = nil // normalise: empty list == omitted == inherit
	}

	if def.Workflow != "" && def.Workflow != "plan-analyze" {
		return nil, fmt.Errorf("workflow: %q — only \"plan-analyze\" is allowed (tighten-only; loosening is a config error)", def.Workflow)
	}
	if def.Effort != "" && !legalEfforts[def.Effort] {
		return nil, fmt.Errorf("effort: %q — valid: low, medium, high", def.Effort)
	}
	if raw := fields["max_turns"]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("max_turns: %q — must be a positive integer", raw)
		}
		def.MaxTurns = n
	}
	return def, nil
}
