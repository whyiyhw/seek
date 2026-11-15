package subagent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/agenttypes"
	"github.com/whyiyhw/seek/internal/permission"
)

// custom.go is the P2-b D13 gate: it adapts a file-defined agent type
// (agenttypes.Definition, loaded read-only at session start) into the
// spawn-time Template, enforcing everything that can only be checked
// against LIVE spawn context (feature-orchestration.md §5 D13):
//
//   - tools ⊆ parent registry ∩ non-forbidden (`agent`/`ask_user`
//     never — the nesting/picker red lines hold for custom types too);
//   - workflow tighten-only: "plan-analyze" is the one legal value,
//     "plan-execute" or any loosening is a config error — the hard
//     door stays permission.Policy.Spawn's Restriction monotonicity;
//     the frontmatter is just one more source for it;
//   - provider/model validity via ResolveRoute (D8's provider filter
//     then applies downstream in the Runner over job.ToolNames, same
//     as for built-ins — think/fim_complete vanish on non-DeepSeek
//     lanes automatically).
//
// The gate re-validates shapes agenttypes.Parse already rejects for
// files on purpose: the Set can also be populated by hand (tests) or
// by future sources, and a safety gate must not trust its feeders.

// ValidateCustomType checks a custom Definition against the parent
// tool registry and the routing table, returning a clear error for
// every illegal shape (D13's full list — the table-driven suite in
// custom_test.go pins each message):
//
//	no name / workflow loosening (plan-execute…) / forbidden tools
//	(agent, ask_user) / unknown tool names / unknown provider or model
//
// The host calls it once at startup (after the parent registry is
// final) to print warnings; Manager.resolveType calls the spawn-side
// twin ResolveCustomType per spawn. Unknown tool NAMES are reported
// here as config errors — at spawn the type degrades to the
// general-purpose toolset instead (see ResolveCustomType).
func ValidateCustomType(def *agenttypes.Definition, parentToolNames []string) error {
	if err := customTypeShapeErr(def); err != nil {
		return err
	}
	if unknown := unknownTools(def.Tools, parentToolNames); len(unknown) > 0 {
		return fmt.Errorf(
			"tools: unknown to the parent registry: %s — config error; spawns of this type degrade to the general-purpose toolset (never silently narrowed)",
			strings.Join(unknown, ", "))
	}
	if def.Provider != "" || def.Model != "" {
		if _, err := ResolveRoute(def.Provider, def.Model); err != nil {
			return err
		}
	}
	return nil
}

// ResolveCustomType adapts def into the spawn-time Template. Illegal
// shapes other than unknown tool names are hard errors — the spawn
// fails with the gate's message (wire-format spawn_error) rather than
// running a type whose declaration grants something the system
// refuses.
//
// Unknown tool names (⊆ parent registry violated) do NOT error here:
// per D13 the whole whitelist is dropped and the type DEGRADES to the
// general-purpose toolset (ToolNames = nil → Template.Filter inherits
// the parent registry minus agent/ask_user). The loud half of that
// contract is the host's startup warning via ValidateCustomType — the
// degrade itself must never be silent narrowing either: a typo'd
// "grpe" silently disappearing from the whitelist is exactly the
// confusion D13 forbids.
func ResolveCustomType(def *agenttypes.Definition, parentToolNames []string) (Template, error) {
	if err := customTypeShapeErr(def); err != nil {
		return Template{}, err
	}
	// Provider validity is a property of the FILE, not the call: a
	// def declaring provider "claude" is a config error even when the
	// spawn's per-call params would shadow it. ResolveRoute owns the
	// table (no second source of truth); "" / "" inherits the parent.
	if def.Provider != "" || def.Model != "" {
		if _, err := ResolveRoute(def.Provider, def.Model); err != nil {
			return Template{}, err
		}
	}
	tmpl := defToTemplate(def)
	if unknown := unknownTools(def.Tools, parentToolNames); len(unknown) > 0 {
		tmpl.ToolNames = nil // degrade: general-purpose toolset
	}
	return tmpl, nil
}

// customTypeShapeErr validates the shape fields that don't depend on
// spawn context: name presence, workflow tighten-only, forbidden
// tools. Shared by ValidateCustomType and ResolveCustomType so the
// warning path and the spawn gate can never disagree.
func customTypeShapeErr(def *agenttypes.Definition) error {
	if def == nil || def.Name == "" {
		return fmt.Errorf("no name: definition has neither frontmatter name nor filename stem")
	}
	// agenttypes.Parse already rejects these for FILE-loaded defs
	// (P2-a); re-checked here because the Set may be hand-built.
	if def.Workflow != "" && def.Workflow != "plan-analyze" {
		return fmt.Errorf("workflow: %q — only \"plan-analyze\" is allowed (tighten-only; loosening is a config error)", def.Workflow)
	}
	for _, tool := range def.Tools {
		if tool == "agent" || tool == "ask_user" {
			return fmt.Errorf("tools: %q is never allowed in an agent type (universal exclusion)", tool)
		}
	}
	return nil
}

// unknownTools returns the sorted subset of tools that the parent
// registry does not have. Sorted so the error message is deterministic
// regardless of map or slice ordering upstream.
func unknownTools(tools, parentToolNames []string) []string {
	parent := make(map[string]bool, len(parentToolNames))
	for _, n := range parentToolNames {
		parent[n] = true
	}
	var unknown []string
	for _, t := range tools {
		if !parent[t] {
			unknown = append(unknown, t)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// defToTemplate adapts one Definition into the spawn-time Template.
// Shared with the built-in table (builtinsToTemplates loops it) so a
// custom type and a built-in of the same declaration adapt
// identically — they cannot drift apart.
func defToTemplate(def *agenttypes.Definition) Template {
	t := Template{
		Type:      Type(def.Name),
		ToolNames: def.Tools,
		Extra:     def.Body,
	}
	if def.Workflow == "plan-analyze" {
		t.Restriction = permission.Restriction{Workflow: &planAnalyzeWorkflow}
	}
	return t
}
