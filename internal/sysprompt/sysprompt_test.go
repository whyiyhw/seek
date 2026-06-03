package sysprompt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

// TestCompose_ByteEquivalentToLegacy locks in byte-for-byte equality
// with the legacy cmd/seek inline assembly. This is the load-bearing
// test for the prefix-cache invariant: any future refactor that
// reorders segments or tweaks whitespace must update this test (and
// accept a one-time cache bust for every session in the wild).
//
// The legacy assembly was:
//
//	sp := fmt.Sprintf(systemPromptTpl, abs, modeLabel)
//	if section != "" { sp = sp + "\n" + section }
//	if manifest != "" { sp = sp + "\n" + manifest }
func TestCompose_ByteEquivalentToLegacy(t *testing.T) {
	cases := []struct {
		name      string
		cwd       string
		modeLabel string
		section   string
		manifest  string
	}{
		{
			name:      "all segments present",
			cwd:       "/Users/test/proj",
			modeLabel: "ask",
			section:   "## Project conventions\nUse tabs.\n",
			manifest:  "## Available skills\n- foo: bar\n",
		},
		{
			name:      "no section, no manifest",
			cwd:       "/tmp/empty-proj",
			modeLabel: "yolo",
			section:   "",
			manifest:  "",
		},
		{
			name:      "section only",
			cwd:       "/tmp/p",
			modeLabel: "plan-analyze",
			section:   "## Local rules\nDon't push.\n",
			manifest:  "",
		},
		{
			name:      "manifest only",
			cwd:       "/tmp/p",
			modeLabel: "plan-execute",
			section:   "",
			manifest:  "## Available skills\n- x: y\n",
		},
	}
	// Date is orthogonal to the section/manifest matrix (it's an
	// unconditional %s slot like Cwd/Mode), so a single representative
	// value threaded through both sides fully exercises the new slot.
	const date = "Wednesday, 2026-06-03"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Compose(Header{
				Cwd:            c.cwd,
				ProjectSection: c.section,
				SkillManifest:  c.manifest,
				Date:           date,
			}, c.modeLabel)
			want := legacyAssemble(c.cwd, c.modeLabel, c.section, c.manifest, date)
			if got != want {
				t.Errorf("Compose output diverged from legacy assembly.\n--- got (%d bytes) ---\n%s\n--- want (%d bytes) ---\n%s",
					len(got), got, len(want), want)
			}
		})
	}
}

// legacyAssemble reproduces the inline assembly that lived in
// cmd/seek/main.go before this package was extracted. Kept here as
// the byte-equivalence oracle for the test above.
func legacyAssemble(cwd, modeLabel, section, manifest, date string) string {
	sp := fmt.Sprintf(rootTpl, cwd, modeLabel, date)
	if section != "" {
		sp = sp + "\n" + section
	}
	if manifest != "" {
		sp = sp + "\n" + manifest
	}
	return sp
}

// TestCompose_IsDeterministic: same input → same output, no hidden
// random sources (timestamps, map iteration, etc.). Trivial to pass
// today; lives as a regression guard against any future "add the
// current time to the prompt" temptation that would break the cache.
func TestCompose_IsDeterministic(t *testing.T) {
	h := Header{Cwd: "/x", ProjectSection: "A", SkillManifest: "B", Date: "Wednesday, 2026-06-03"}
	a := Compose(h, "yolo")
	b := Compose(h, "yolo")
	if a != b {
		t.Errorf("Compose not deterministic across calls (len %d vs %d)", len(a), len(b))
	}
}

// TestCompose_RendersDate confirms the Date field lands in the prompt
// verbatim (so the model actually sees today's date) and is sourced
// from the field, not a hidden time.Now() — the determinism test above
// guards the no-time.Now() half; this guards the it-shows-up half.
func TestCompose_RendersDate(t *testing.T) {
	const date = "Wednesday, 2026-06-03"
	got := Compose(Header{Cwd: "/x", Date: date}, "ask")
	if !strings.Contains(got, "Today's date: "+date) {
		t.Errorf("Compose output missing date line %q.\n--- got ---\n%s", date, got)
	}
}

// TestModeLabel covers every (Preference, Workflow) combination. The
// workflow axis wins when non-None — workflow ceremonies are user-
// chosen safety boundaries that trump pref (this is the same rule
// the permission.Check resolution order uses; if the two ever
// diverge, the model sees one mode in the prompt and another in its
// permission denials, which is confusing).
func TestModeLabel(t *testing.T) {
	cases := []struct {
		pref permission.Preference
		wf   permission.Workflow
		want string
	}{
		{permission.PrefDeny, permission.WorkflowNone, "deny"},
		{permission.PrefAsk, permission.WorkflowNone, "ask"},
		{permission.PrefYolo, permission.WorkflowNone, "yolo"},

		// Workflow trumps pref.
		{permission.PrefDeny, permission.WorkflowPlanAnalyze, "plan-analyze"},
		{permission.PrefAsk, permission.WorkflowPlanAnalyze, "plan-analyze"},
		{permission.PrefYolo, permission.WorkflowPlanAnalyze, "plan-analyze"},

		{permission.PrefDeny, permission.WorkflowPlanExecute, "plan-execute"},
		{permission.PrefAsk, permission.WorkflowPlanExecute, "plan-execute"},
		{permission.PrefYolo, permission.WorkflowPlanExecute, "plan-execute"},
	}
	for _, c := range cases {
		got := ModeLabel(c.pref, c.wf)
		if got != c.want {
			t.Errorf("ModeLabel(%s, %s) = %q, want %q", c.pref, c.wf, got, c.want)
		}
	}
}

// TestComposeSubagent_InheritsHeaderBytes verifies the v5 §3.6.1
// invariant: subagents inherit the parent's segments 1-3 (header +
// projmd + skills) byte-identically; only the trailing role +
// summary-length segment differs.
//
// "Inherit byte-identically" here means: ComposeSubagent's output
// must START with Compose(h, "subagent") verbatim. Anything after
// that is the role + summary tail.
func TestComposeSubagent_InheritsHeaderBytes(t *testing.T) {
	h := Header{
		Cwd:            "/proj",
		ProjectSection: "## Project rules\nFoo.",
		SkillManifest:  "## Skills\n- bar",
	}
	parent := Compose(h, "subagent")
	sub := ComposeSubagent(h, SubagentRole{
		Description: "find Esc handlers",
		Extra:       "You are in research-only mode. You cannot write or edit.",
	})
	if !strings.HasPrefix(sub, parent) {
		t.Errorf("ComposeSubagent output does not start with Compose(h, \"subagent\")\nsub:\n%s\nparent:\n%s", sub, parent)
	}
}

// TestComposeSubagent_HasRoleAndSummaryHint verifies that the
// trailing segment carries both:
//   - The standard "You are a subagent spawned by the parent agent
//     for: <description>." line
//   - The summary-length hint from §3.6 (~4000 character cap)
//
// Plus the Extra clause when supplied. Body content checks are
// substring-based — the goal is to lock in the *contract* (model
// receives the right pieces), not the exact prose.
func TestComposeSubagent_HasRoleAndSummaryHint(t *testing.T) {
	h := Header{Cwd: "/p"}
	sub := ComposeSubagent(h, SubagentRole{
		Description: "audit the codebase for stale TODOs",
		Extra:       "You are in research-only mode. You cannot write, edit, or run mutating commands.",
	})

	wantContains := []string{
		"You are a subagent spawned by the parent agent for: audit the codebase for stale TODOs.",
		"Complete the task and return a concise summary as your final message.",
		"You are in research-only mode.",
		"Keep it within ~4000 characters",
	}
	for _, w := range wantContains {
		if !strings.Contains(sub, w) {
			t.Errorf("ComposeSubagent missing fragment %q in:\n%s", w, sub)
		}
	}
}

// TestComposeSubagent_OmitsExtraWhenEmpty: general-purpose subagent
// has no Extra clause; the output should still be well-formed (no
// blank-line artifacts that the model might interpret as missing
// content).
func TestComposeSubagent_OmitsExtraWhenEmpty(t *testing.T) {
	sub := ComposeSubagent(Header{Cwd: "/p"}, SubagentRole{
		Description: "a task",
		Extra:       "",
	})
	// Must NOT contain the explore-mode clause.
	if strings.Contains(sub, "research-only mode") {
		t.Errorf("ComposeSubagent leaked Extra content despite empty input:\n%s", sub)
	}
	// Must still contain the role intro and the summary hint.
	for _, w := range []string{
		"You are a subagent spawned by",
		"Keep it within ~4000 characters",
	} {
		if !strings.Contains(sub, w) {
			t.Errorf("ComposeSubagent missing %q in:\n%s", w, sub)
		}
	}
}

// TestCompose_LargeInputsNoAllocPathologies is a soft check that the
// strings.Builder pre-allocation doesn't crash or panic on
// degenerate inputs (empty cwd, very large section). Not a benchmark
// — just a sanity probe.
func TestCompose_LargeInputsNoAllocPathologies(t *testing.T) {
	bigSection := strings.Repeat("x", 100_000)
	got := Compose(Header{Cwd: "", ProjectSection: bigSection, SkillManifest: ""}, "ask")
	if !strings.Contains(got, bigSection) {
		t.Error("large ProjectSection lost in assembly")
	}
}
