package subagent

import (
	"slices"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

func TestTemplateFor_KnownTypes(t *testing.T) {
	for _, ty := range []Type{TypeGeneralPurpose, TypeExplore, TypePlan} {
		tmpl, ok := TemplateFor(ty)
		if !ok {
			t.Errorf("TemplateFor(%s) ok=false", ty)
		}
		if tmpl.Type != ty {
			t.Errorf("TemplateFor(%s).Type = %s", ty, tmpl.Type)
		}
	}
}

func TestTemplateFor_UnknownType(t *testing.T) {
	if _, ok := TemplateFor("bogus"); ok {
		t.Error("TemplateFor(bogus) ok=true, want false")
	}
}

// TestTemplate_ExploreForcesPlanAnalyze: load-bearing for the
// "PrefYolo + explore" safety property — even if a future bug lets
// parent Yolo leak into the Restriction, the explore template's
// hardcoded Workflow=PlanAnalyze stops mutating ops at the
// permission gate.
func TestTemplate_ExploreForcesPlanAnalyze(t *testing.T) {
	tmpl, _ := TemplateFor(TypeExplore)
	if tmpl.Restriction.Workflow == nil {
		t.Fatal("explore.Restriction.Workflow must be non-nil (forced PlanAnalyze)")
	}
	if *tmpl.Restriction.Workflow != permission.WorkflowPlanAnalyze {
		t.Errorf("explore.Restriction.Workflow = %s, want plan-analyze", *tmpl.Restriction.Workflow)
	}
}

func TestTemplate_PlanForcesPlanAnalyze(t *testing.T) {
	tmpl, _ := TemplateFor(TypePlan)
	if tmpl.Restriction.Workflow == nil || *tmpl.Restriction.Workflow != permission.WorkflowPlanAnalyze {
		t.Errorf("plan.Restriction.Workflow not pinned to plan-analyze: %+v", tmpl.Restriction.Workflow)
	}
}

func TestTemplate_GeneralPurposeInherits(t *testing.T) {
	tmpl, _ := TemplateFor(TypeGeneralPurpose)
	if tmpl.Restriction.Pref != nil || tmpl.Restriction.Workflow != nil {
		t.Errorf("general-purpose must inherit both axes, got %+v", tmpl.Restriction)
	}
}

// TestTemplate_FilterExcludesAgentAndAskUser is the universal
// exclusion check — ALL three templates must drop `agent` (no
// nested spawn) and `ask_user` (no subagent picker disambiguation
// in v5). Tested per-template so a future template that forgets the
// universal exclusion gets caught.
func TestTemplate_FilterExcludesAgentAndAskUser(t *testing.T) {
	parent := []string{
		"read", "grep", "write", "edit", "bash", "git",
		"agent", "ask_user", "think", "webfetch", "list_dir",
	}
	for _, ty := range []Type{TypeGeneralPurpose, TypeExplore, TypePlan} {
		t.Run(string(ty), func(t *testing.T) {
			tmpl, _ := TemplateFor(ty)
			got := tmpl.Filter(parent)
			if slices.Contains(got, "agent") {
				t.Errorf("Filter for %s includes `agent` — universal exclusion violated", ty)
			}
			if slices.Contains(got, "ask_user") {
				t.Errorf("Filter for %s includes `ask_user` — universal exclusion violated", ty)
			}
		})
	}
}

// TestTemplate_FilterInheritsForGeneralPurpose: nil ToolNames →
// parent全集 minus universal exclusions.
func TestTemplate_FilterInheritsForGeneralPurpose(t *testing.T) {
	tmpl, _ := TemplateFor(TypeGeneralPurpose)
	parent := []string{"read", "grep", "write", "agent", "ask_user", "bash"}
	got := tmpl.Filter(parent)
	want := []string{"read", "grep", "write", "bash"} // exclusions stripped, order preserved
	if !slices.Equal(got, want) {
		t.Errorf("Filter inherit-mode = %v, want %v", got, want)
	}
}

// TestTemplate_FilterIntersectsWhitelistForExplore: explicit
// whitelist mode intersects with parent (degrades gracefully when
// parent doesn't have all named tools).
func TestTemplate_FilterIntersectsWhitelistForExplore(t *testing.T) {
	tmpl, _ := TemplateFor(TypeExplore)
	parent := []string{"read", "grep", "list_dir", "think"} // no webfetch, no git
	got := tmpl.Filter(parent)
	// Template lists read/grep/list_dir/git/webfetch/think;
	// parent has read/grep/list_dir/think; intersection is those 4.
	want := []string{"read", "grep", "list_dir", "think"}
	if !slices.Equal(got, want) {
		t.Errorf("Filter explore = %v, want %v", got, want)
	}
}

// TestTemplate_FilterPlanIsReadOnly: lock in the C.2 redesign —
// plan must NOT include write/edit/bash even when the parent has
// them. If anyone "fixes" plan back to inheriting parent全集 (per
// the original PRD v1 wording) this test fails, prompting them to
// re-read the templates.go rationale before merging.
//
// Without this gate, the PRD §2.3 monotonic-收紧 promise quietly
// degrades to soft-enforcement-via-system-prompt for plan
// subagents.
func TestTemplate_FilterPlanIsReadOnly(t *testing.T) {
	tmpl, _ := TemplateFor(TypePlan)
	parent := []string{
		"read", "grep", "list_dir", "git", "webfetch", "think",
		"write", "edit", "bash", // mutating — MUST be filtered out
		"propose", "plan", // parent-session-bound — MUST be filtered out
		"agent", "ask_user", // universally excluded
	}
	got := tmpl.Filter(parent)
	mutating := []string{"write", "edit", "bash"}
	for _, name := range mutating {
		if slices.Contains(got, name) {
			t.Errorf("plan template leaked mutating tool %q — C.2 hard-safety violated", name)
		}
	}
	for _, name := range []string{"propose", "plan"} {
		if slices.Contains(got, name) {
			t.Errorf("plan template includes parent-bound tool %q — would pollute parent's plan panel", name)
		}
	}
	// Positive: the standard read-only six must be present.
	want := []string{"read", "grep", "list_dir", "git", "webfetch", "think"}
	if !slices.Equal(got, want) {
		t.Errorf("Filter plan = %v, want %v", got, want)
	}
}

// TestTemplate_PlanAndExploreHaveSameToolSubset: plan and explore
// share the same tool set by design (C.2). The framing difference
// lives in the Extra clause, not the registry. If a future change
// adds a tool to one but not the other (intentional divergence),
// this test fails and the author must justify in the PRD.
func TestTemplate_PlanAndExploreHaveSameToolSubset(t *testing.T) {
	plan, _ := TemplateFor(TypePlan)
	explore, _ := TemplateFor(TypeExplore)
	if !slices.Equal(plan.ToolNames, explore.ToolNames) {
		t.Errorf("plan tools %v != explore tools %v — divergence requires PRD §3.6 update",
			plan.ToolNames, explore.ToolNames)
	}
	// Extras MUST differ — that's the whole point of keeping
	// both templates after C.2. If they collapse, drop one.
	if plan.Extra == explore.Extra {
		t.Error("plan.Extra == explore.Extra — templates collapsed; drop one or differentiate")
	}
}

// TestTemplate_RoleBuildsSubagentRole: the Role() method packages
// the description + Extra clause into a sysprompt.SubagentRole
// ready for ComposeSubagent. Verify the round-trip carries content.
func TestTemplate_RoleBuildsSubagentRole(t *testing.T) {
	tmpl, _ := TemplateFor(TypeExplore)
	role := tmpl.Role("find Esc handlers")
	if role.Description != "find Esc handlers" {
		t.Errorf("Role.Description = %q", role.Description)
	}
	if !strings.Contains(role.Extra, "research-only") {
		t.Errorf("Role.Extra missing research-only clause: %q", role.Extra)
	}
}
