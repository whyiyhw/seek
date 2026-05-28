package subagent

import (
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/sysprompt"
)

// Template is the static configuration for one subagent Type. The
// three v5 built-in templates (general-purpose / explore / plan)
// each declare a tool subset, a permission Restriction, and an
// Extra clause that goes into the child system prompt after the
// standard subagent role intro.
//
// See docs/prd/feature-subagent.md §3.6 for the source-of-truth
// table; this code is the canonical Go reflection of it. Changes
// here must update the PRD table in lock-step.
type Template struct {
	// Type identifies which built-in this is.
	Type Type

	// ToolNames is the explicit whitelist of tool names the child
	// Registry exposes to the LLM. Empty slice means "inherit
	// everything from parent registry minus `agent` and `ask_user`
	// (those are excluded for all templates — see method-level
	// docs on Filter)".
	//
	// `agent` is excluded so the child cannot nest-spawn (v5 hard
	// cap of depth = 1; see PRD §2 anti-goals + Tracker.AdoptChild
	// nesting panic as defense in depth). `ask_user` is excluded
	// because subagent picker disambiguation UX isn't in v5 —
	// subagents pass decisions back via summary, not by talking to
	// the user directly.
	ToolNames []string

	// Restriction is fed to permission.Policy.Spawn when the
	// Manager constructs the child policy. Nil-pointer fields mean
	// "inherit from parent"; explicit pointers tighten the
	// corresponding axis. Loosening is rejected at Spawn time —
	// these values are vetted to be either equal-or-stricter.
	Restriction permission.Restriction

	// Extra is the type-specific clause appended to the standard
	// subagent role intro in the child system prompt. Goes into
	// sysprompt.SubagentRole.Extra; can be empty for templates
	// that don't need additional context beyond the role intro
	// (general-purpose).
	Extra string
}

// planAnalyzePtr is a package-level pointer so the three template
// declarations can take its address. Using a literal &workflow each
// time would lose the "monotonic-only intent" expressed by sharing
// one canonical pointer to the read-only workflow.
var planAnalyzeWorkflow = permission.WorkflowPlanAnalyze

// templates holds the three v5 built-ins, indexed by Type. New
// templates added to this map must also extend the LLM-side
// `agent` tool schema's `subagent_type` enum (PRD §3.1).
var templates = map[Type]Template{
	TypeGeneralPurpose: {
		Type: TypeGeneralPurpose,
		// nil ToolNames + Filter's universal `agent`/`ask_user`
		// exclusion = "parent全集 减 agent / ask_user".
		ToolNames:   nil,
		Restriction: permission.Restriction{}, // inherit both axes
		Extra:       "",                        // no extra clause; role intro alone
	},
	TypeExplore: {
		Type: TypeExplore,
		ToolNames: []string{
			"read", "grep", "list_dir", "git", "webfetch", "think",
		},
		// Force PlanAnalyze even when parent is Yolo. This is the
		// load-bearing safety property of `explore`: the user picks
		// "research-only" by Type and trusts the template, not the
		// parent pref. PRD §8 risk table "PrefYolo + explore" calls
		// this out explicitly.
		Restriction: permission.Restriction{Workflow: &planAnalyzeWorkflow},
		Extra:       "You are in research-only mode. You cannot write, edit, or run mutating commands. Return findings as bulleted summary.",
	},
	TypePlan: {
		Type:      TypePlan,
		ToolNames: nil, // inherit (minus agent/ask_user)
		// Same PlanAnalyze force as explore, but with full tool
		// access — `plan` subagents propose changes via the
		// propose tool without executing.
		Restriction: permission.Restriction{Workflow: &planAnalyzeWorkflow},
		Extra:       "You are in plan-analyze mode. Propose changes via the `propose` tool; do not execute.",
	},
}

// TemplateFor returns the Template for the given Type, or ok=false
// if Type is not a built-in. Used by Manager.Spawn to look up the
// template before constructing the child policy / tracker / system
// prompt.
//
// The returned Template is a shallow copy; callers can read its
// fields freely without affecting other Spawns.
func TemplateFor(t Type) (Template, bool) {
	tmpl, ok := templates[t]
	return tmpl, ok
}

// Role builds the sysprompt.SubagentRole that gets fed to
// sysprompt.ComposeSubagent. Description is the parent-supplied
// 1-line task summary from the `agent` tool's `description` field;
// the Extra clause comes from this template.
//
// Living here (not in sysprompt) keeps sysprompt unaware of subagent
// types — sysprompt's public API takes a generic SubagentRole with
// pre-built strings; the per-type wording lives next to the rest of
// the template config.
func (t Template) Role(description string) sysprompt.SubagentRole {
	return sysprompt.SubagentRole{
		Description: description,
		Extra:       t.Extra,
	}
}

// Filter returns the tool name allow-list for this template against
// the parent's registered tool names. Inputs are the parent
// Registry's full Names() slice; output is the subset the child
// Registry should expose.
//
// Universal exclusions (apply to ALL templates):
//
//   - "agent": v5 limits spawn depth = 1.
//   - "ask_user": disambiguation UX for subagent pickers isn't in
//     v5 (PRD §3.6 + §9 backlog).
//
// Template-specific:
//
//   - ToolNames != nil → strict whitelist intersected with parent
//     names (parent might not have every name in our hardcoded
//     list; e.g. fim_complete isn't in every registry).
//   - ToolNames == nil → inherit all parent names except the
//     universal exclusions.
//
// The intersection-with-parent-names behaviour is deliberate: it
// keeps templates declarative ("explore needs grep+read+...") while
// gracefully degrading when a tool isn't available (e.g. test
// scaffolding with a partial registry, future deployments where
// some tools are gated by build tags).
func (t Template) Filter(parentNames []string) []string {
	exclude := map[string]bool{
		"agent":    true,
		"ask_user": true,
	}
	if t.ToolNames == nil {
		// Inherit-mode: parent minus universal exclusions.
		out := make([]string, 0, len(parentNames))
		for _, n := range parentNames {
			if !exclude[n] {
				out = append(out, n)
			}
		}
		return out
	}
	// Whitelist-mode: intersect template list with parent names,
	// also subtract universal exclusions (defense in depth — a
	// template author would have to actively add "agent" to their
	// ToolNames to get past this, but better safe).
	parentSet := make(map[string]bool, len(parentNames))
	for _, n := range parentNames {
		parentSet[n] = true
	}
	out := make([]string, 0, len(t.ToolNames))
	for _, n := range t.ToolNames {
		if parentSet[n] && !exclude[n] {
			out = append(out, n)
		}
	}
	return out
}
