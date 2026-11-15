package subagent

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/agenttypes"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
)

// customParentTools is the fake parent registry the gate tests run
// against — a plausible seek registry INCLUDING the forbidden names
// (agent, ask_user) so the tests prove the gate refuses them even
// though the parent has them.
var customParentTools = []string{
	"read", "grep", "list_dir", "git", "webfetch", "think",
	"agent", "ask_user", "bash", "write", "edit", "fim_complete",
}

// generalPurposeSet is what a nil-Tools (or degraded) custom type
// sees: the parent registry minus the universal exclusions.
func generalPurposeSet() []string {
	var out []string
	for _, n := range customParentTools {
		if n != "agent" && n != "ask_user" {
			out = append(out, n)
		}
	}
	return out
}

// def is a terse Definition builder for the tables below. Origin
// defaults to project — the custom spawn path keys on non-builtin
// origin (Manager.resolveType), so hand-built defs must carry one.
func def(name string, mut func(*agenttypes.Definition)) *agenttypes.Definition {
	d := &agenttypes.Definition{
		Name:        name,
		Description: "test type",
		Body:        "Test role prompt.",
		Source:      "test:" + name,
		Origin:      agenttypes.OriginProject,
	}
	if mut != nil {
		mut(d)
	}
	return d
}

// TestValidateCustomType_IllegalShapes is D13's full illegal-shape
// table: every shape the PRD names (放宽 pref 见
// TestCustomType_PolicyGateTightenOnly — the pref axis has no
// frontmatter field by design, so its clear error lives at the
// permission hard gate) must produce a distinct, greppable error.
func TestValidateCustomType_IllegalShapes(t *testing.T) {
	cases := []struct {
		name    string
		def     *agenttypes.Definition
		wantSub string // empty = must pass
	}{
		{"no name", def("", nil), "no name"},
		{"no name via nil", def("", func(d *agenttypes.Definition) { d.Name = "" }), "no name"},
		{"workflow plan-execute (loosening)", def("v", func(d *agenttypes.Definition) { d.Workflow = "plan-execute" }),
			`only "plan-analyze" is allowed`},
		{"workflow garbage", def("v", func(d *agenttypes.Definition) { d.Workflow = "none" }),
			`only "plan-analyze" is allowed`},
		{"forbidden tool agent", def("v", func(d *agenttypes.Definition) { d.Tools = []string{"read", "agent"} }),
			`"agent" is never allowed`},
		{"forbidden tool ask_user", def("v", func(d *agenttypes.Definition) { d.Tools = []string{"ask_user"} }),
			`"ask_user" is never allowed`},
		{"unknown tool names", def("v", func(d *agenttypes.Definition) { d.Tools = []string{"read", "grpe", "reed"} }),
			"unknown to the parent registry: grpe, reed"},
		{"unknown provider", def("v", func(d *agenttypes.Definition) { d.Provider = "claude" }),
			`unknown provider "claude"`},
		{"model from wrong lane", def("v", func(d *agenttypes.Definition) { d.Provider = "glm"; d.Model = "deepseek-flash" }),
			"does not belong to provider"},
		{"model from no lane", def("v", func(d *agenttypes.Definition) { d.Model = "gpt-9" }),
			"unknown model"},

		// Legal shapes — no error.
		{"known whitelist", def("v", func(d *agenttypes.Definition) { d.Tools = []string{"read", "bash"} }), ""},
		{"omitted tools inherit", def("v", nil), ""},
		{"provider glm", def("v", func(d *agenttypes.Definition) { d.Provider = "glm" }), ""},
		{"provider+model", def("v", func(d *agenttypes.Definition) { d.Provider = "glm"; d.Model = "glm-5.3" }), ""},
		{"workflow plan-analyze (tightening)", def("v", func(d *agenttypes.Definition) { d.Workflow = "plan-analyze" }), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateCustomType(c.def, customParentTools)
			if c.wantSub == "" {
				if err != nil {
					t.Fatalf("ValidateCustomType rejected legal shape: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCustomType accepted illegal shape")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not contain %q", err, c.wantSub)
			}
		})
	}
}

// TestValidateCustomType_UnknownToolsWarningDocumentsDegradation: the
// startup warning must tell the user what happens at spawn — D13's
// "not silent" half. (The degrade behaviour itself is pinned in
// TestSpawn_CustomType below.)
func TestValidateCustomType_UnknownToolsWarningDocumentsDegradation(t *testing.T) {
	err := ValidateCustomType(def("v", func(d *agenttypes.Definition) {
		d.Tools = []string{"grpe"}
	}), customParentTools)
	if err == nil {
		t.Fatal("expected config error for unknown tool name")
	}
	for _, sub := range []string{"config error", "degrade", "general-purpose", "never silently narrowed"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q does not mention %q", err, sub)
		}
	}
}

// TestResolveCustomType adapts legal defs: exact whitelist preserved
// (def order), restriction from workflow, body → Extra.
func TestResolveCustomType(t *testing.T) {
	tmpl, err := ResolveCustomType(def("verifier", func(d *agenttypes.Definition) {
		d.Tools = []string{"bash", "read"}
		d.Workflow = "plan-analyze"
		d.Body = "Verify everything."
	}), customParentTools)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Type != Type("verifier") {
		t.Errorf("Type = %q", tmpl.Type)
	}
	if !reflect.DeepEqual(tmpl.ToolNames, []string{"bash", "read"}) {
		t.Errorf("ToolNames = %v, want the declared whitelist in declaration order", tmpl.ToolNames)
	}
	if tmpl.Restriction.Workflow == nil || *tmpl.Restriction.Workflow != permission.WorkflowPlanAnalyze {
		t.Errorf("Restriction.Workflow = %v, want plan-analyze", tmpl.Restriction.Workflow)
	}
	if tmpl.Restriction.Pref != nil {
		t.Errorf("Restriction.Pref = %v, want nil (defs cannot touch the pref axis)", tmpl.Restriction.Pref)
	}
	if tmpl.Extra != "Verify everything." {
		t.Errorf("Extra = %q, want the def body verbatim", tmpl.Extra)
	}
	// Filter projects the whitelist onto the parent registry without
	// the universal exclusions.
	got := tmpl.Filter(customParentTools)
	if !reflect.DeepEqual(got, []string{"bash", "read"}) {
		t.Errorf("Filter = %v, want [bash read]", got)
	}
}

// TestResolveCustomType_HardErrorsMirrorValidate: every illegal shape
// EXCEPT unknown tool names must hard-fail the adaptation (the spawn
// gate and the startup warning agree).
func TestResolveCustomType_HardErrorsMirrorValidate(t *testing.T) {
	for _, c := range []struct {
		name    string
		def     *agenttypes.Definition
		wantSub string
	}{
		{"no name", def("", nil), "no name"},
		{"plan-execute", def("v", func(d *agenttypes.Definition) { d.Workflow = "plan-execute" }), `only "plan-analyze"`},
		{"forbidden agent", def("v", func(d *agenttypes.Definition) { d.Tools = []string{"agent"} }), "never allowed"},
		{"unknown provider", def("v", func(d *agenttypes.Definition) { d.Provider = "claude" }), "unknown provider"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ResolveCustomType(c.def, customParentTools); err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("ResolveCustomType error = %v, want substring %q", err, c.wantSub)
			}
		})
	}
}

// newCustomManager is newManager plus an AgentTypes set and a parent
// preference knob — the P2-b spawn wiring under test.
func newCustomManager(t *testing.T, runner Runner, pref permission.Preference, set *agenttypes.Set) *Manager {
	t.Helper()
	withHome(t)
	parentCwd := t.TempDir()
	policy, err := permission.New(parentCwd, pref)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ManagerOpts{
		ProjectAbsPath:    parentCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return customParentTools },
		MaxConcurrent:     3,
		Runner:            runner,
		AgentTypes:        set,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

// TestSpawn_CustomType is the P2 acceptance #2 core: spawn a
// file-defined type through the Manager and the child sees exactly
// what the frontmatter declared — tools, workflow, provider/model —
// plus the D13 special cases (degrade, call-over-def routing, file
// shadowing a built-in, gate rejections).
func TestSpawn_CustomType(t *testing.T) {
	type want struct {
		toolNames []string
		workflow  permission.Workflow
		pref      permission.Preference
		provider  string
		model     string
		promptSub string // substring the child system prompt must contain
	}
	cases := []struct {
		name string
		def  *agenttypes.Definition
		args SpawnArgs
		want want
	}{
		{
			name: "declared tools workflow and body",
			def: def("verifier", func(d *agenttypes.Definition) {
				d.Tools = []string{"read", "bash"}
				d.Workflow = "plan-analyze"
				d.Body = "Verify everything."
			}),
			want: want{
				toolNames: []string{"read", "bash"},
				workflow:  permission.WorkflowPlanAnalyze,
				promptSub: "Verify everything.",
			},
		},
		{
			name: "omitted tools inherit general-purpose set",
			def:  def("librarian", nil),
			want: want{toolNames: generalPurposeSet()},
		},
		{
			name: "unknown tool names degrade to general-purpose set",
			def: def("typo-clerk", func(d *agenttypes.Definition) {
				d.Tools = []string{"read", "grpe"}
			}),
			want: want{toolNames: generalPurposeSet()},
		},
		{
			name: "def provider and model flow to the job",
			def: def("glmbot", func(d *agenttypes.Definition) {
				d.Provider = "glm"
			}),
			want: want{
				toolNames: generalPurposeSet(),
				provider:  "glm", model: "glm-5.3",
			},
		},
		{
			name: "per-call params override the def",
			def: def("glmbot", func(d *agenttypes.Definition) {
				d.Provider = "glm"
			}),
			args: SpawnArgs{Provider: "deepseek"},
			want: want{
				toolNames: generalPurposeSet(),
				provider:  "deepseek", model: "deepseek-flash",
			},
		},
		{
			name: "file shadowing a built-in name wins",
			def: def("explore", func(d *agenttypes.Definition) {
				d.Tools = []string{"read"}
			}),
			want: want{toolNames: []string{"read"}},
		},
		{
			name: "pref inherited unchanged under plan-analyze tighten",
			def: def("v", func(d *agenttypes.Definition) { d.Workflow = "plan-analyze" }),
			want: want{
				toolNames: generalPurposeSet(),
				workflow:  permission.WorkflowPlanAnalyze, pref: permission.PrefDeny,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := agenttypes.NewSet()
			set.Add(c.def)
			var job RunnerJob
			ran := false
			mgr := newCustomManager(t, func(_ context.Context, j RunnerJob) (RunnerResult, error) {
				ran = true
				job = j
				return RunnerResult{Summary: "done", Turns: 1}, nil
			}, permission.PrefDeny, set)

			args := SpawnArgs{Description: "d", Prompt: "p", Type: Type(c.def.Name), ParentTurn: 1}
			if c.args.Provider != "" {
				args.Provider = c.args.Provider
			}
			if c.args.Model != "" {
				args.Model = c.args.Model
			}
			res := mgr.Spawn(context.Background(), args)
			if res.Status != StatusCompleted {
				t.Fatalf("Status = %s (%s)", res.Status, res.Summary)
			}
			if !ran {
				t.Fatal("Runner never ran")
			}
			if !reflect.DeepEqual(job.ToolNames, c.want.toolNames) {
				t.Errorf("ToolNames = %v, want %v", job.ToolNames, c.want.toolNames)
			}
			if got := job.Policy.Workflow(); got != c.want.workflow {
				t.Errorf("child workflow = %s, want %s", got, c.want.workflow)
			}
			if got := job.Policy.Pref(); got != c.want.pref {
				t.Errorf("child pref = %s, want %s", got, c.want.pref)
			}
			if job.Provider != c.want.provider || job.Model != c.want.model {
				t.Errorf("route = %q/%q, want %q/%q", job.Provider, job.Model, c.want.provider, c.want.model)
			}
			if c.want.promptSub != "" && !strings.Contains(job.SystemPrompt, c.want.promptSub) {
				t.Errorf("SystemPrompt missing def body %q", c.want.promptSub)
			}
		})
	}
}

// TestSpawn_CustomTypeGateRejections: illegal shapes that the gate
// hard-rejects fail the spawn with the gate's message and never reach
// the Runner — hand-built Sets are exactly the feeder these re-checks
// guard (files were already vetted by agenttypes.Parse).
func TestSpawn_CustomTypeGateRejections(t *testing.T) {
	cases := []struct {
		name    string
		def     *agenttypes.Definition
		wantSub string
	}{
		{"plan-execute rejected", def("v", func(d *agenttypes.Definition) { d.Workflow = "plan-execute" }),
			`only "plan-analyze" is allowed`},
		{"forbidden tool rejected", def("v", func(d *agenttypes.Definition) { d.Tools = []string{"agent"} }),
			"never allowed"},
		{"unknown provider rejected", def("v", func(d *agenttypes.Definition) { d.Provider = "claude" }),
			`unknown provider "claude"`},
		{"cross-lane provider+model rejected", def("v", func(d *agenttypes.Definition) { d.Provider = "glm"; d.Model = "deepseek-flash" }),
			"does not belong to provider"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := agenttypes.NewSet()
			set.Add(c.def)
			ran := false
			mgr := newCustomManager(t, func(_ context.Context, _ RunnerJob) (RunnerResult, error) {
				ran = true
				return RunnerResult{Summary: "should not run"}, nil
			}, permission.PrefYolo, set)

			res := mgr.Spawn(context.Background(), SpawnArgs{
				Description: "d", Prompt: "p", Type: Type(c.def.Name), ParentTurn: 1,
			})
			if ran {
				t.Fatal("Runner ran despite gate rejection")
			}
			if res.Status != StatusFailed {
				t.Fatalf("Status = %s, want failed", res.Status)
			}
			if !strings.Contains(res.Summary, "spawn_error") || !strings.Contains(res.Summary, c.wantSub) {
				t.Errorf("Summary %q missing spawn_error / %q", res.Summary, c.wantSub)
			}
		})
	}
}

// TestSpawn_UnknownTypeStillRejectedWithSet: a Set that doesn't carry
// the name and no built-in matches keeps the pre-P2-b error — the
// custom path must not widen type validity.
func TestSpawn_UnknownTypeStillRejectedWithSet(t *testing.T) {
	set := agenttypes.NewSet()
	set.Add(def("verifier", nil))
	ran := false
	mgr := newCustomManager(t, func(_ context.Context, _ RunnerJob) (RunnerResult, error) {
		ran = true
		return RunnerResult{Summary: "x"}, nil
	}, permission.PrefYolo, set)

	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "d", Prompt: "p", Type: Type("no-such-type"), ParentTurn: 1,
	})
	if ran || res.Status != StatusFailed {
		t.Fatalf("ran=%v Status=%s, want rejected spawn", ran, res.Status)
	}
	if !strings.Contains(res.Summary, `unknown subagent_type "no-such-type"`) {
		t.Errorf("Summary %q missing unknown subagent_type message", res.Summary)
	}
}

// TestSpawn_BuiltinBytePathWithSetWired: with a Set wired (builtins
// only), spawning a built-in must produce EXACTLY the compile-time
// template's output — the Set must not divert built-ins onto the
// custom adaptation path (P2-a byte-identity, restated for P2-b).
func TestSpawn_BuiltinBytePathWithSetWired(t *testing.T) {
	set := agenttypes.NewSet()
	for _, b := range agenttypes.Builtins() {
		set.Add(b)
	}
	var job RunnerJob
	mgr := newCustomManager(t, func(_ context.Context, j RunnerJob) (RunnerResult, error) {
		job = j
		return RunnerResult{Summary: "done"}, nil
	}, permission.PrefYolo, set)

	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "d", Prompt: "p", Type: TypeExplore, ParentTurn: 1,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("Status = %s (%s)", res.Status, res.Summary)
	}
	tmpl, _ := TemplateFor(TypeExplore)
	if want := tmpl.Filter(customParentTools); !reflect.DeepEqual(job.ToolNames, want) {
		t.Errorf("ToolNames = %v, want compile-time template Filter %v", job.ToolNames, want)
	}
	if job.Policy.Workflow() != permission.WorkflowPlanAnalyze {
		t.Errorf("workflow = %s, want plan-analyze from the compile-time template", job.Policy.Workflow())
	}
}

// TestCustomType_PolicyGateTightenOnly: D13's HARD door is
// permission.Policy.Spawn's Restriction monotonicity — the frontmatter
// is just one more source for a Restriction (custom.go can only ever
// produce a Workflow tighten or an empty Restriction, so a def alone
// can never loosen). The pref axis deliberately has NO frontmatter
// field (D11), so the 放宽-pref illegal shape is only reachable via
// non-file restriction sources; this test pins BOTH facts:
//
//   - through the wiring: custom spawns tighten (Yolo parent +
//     plan-analyze def → read-only child; Deny parent stays Deny);
//   - at the door: a loosening Pref Restriction is rejected with the
//     permission layer's clear "loosen" error — the same call begin
//     step 3 makes for every spawn, custom or not.
func TestCustomType_PolicyGateTightenOnly(t *testing.T) {
	set := agenttypes.NewSet()
	set.Add(def("analyzer", func(d *agenttypes.Definition) { d.Workflow = "plan-analyze" }))

	t.Run("yolo parent + plan-analyze def tightens to read-only", func(t *testing.T) {
		var job RunnerJob
		mgr := newCustomManager(t, func(_ context.Context, j RunnerJob) (RunnerResult, error) {
			job = j
			return RunnerResult{Summary: "done"}, nil
		}, permission.PrefYolo, set)
		res := mgr.Spawn(context.Background(), SpawnArgs{Description: "d", Prompt: "p", Type: "analyzer"})
		if res.Status != StatusCompleted {
			t.Fatalf("Status = %s (%s)", res.Status, res.Summary)
		}
		if job.Policy.Pref() != permission.PrefYolo || job.Policy.Workflow() != permission.WorkflowPlanAnalyze {
			t.Errorf("child = %s/%s, want yolo/plan-analyze", job.Policy.Pref(), job.Policy.Workflow())
		}
	})

	t.Run("deny parent inherits deny under the same def", func(t *testing.T) {
		var job RunnerJob
		mgr := newCustomManager(t, func(_ context.Context, j RunnerJob) (RunnerResult, error) {
			job = j
			return RunnerResult{Summary: "done"}, nil
		}, permission.PrefDeny, set)
		res := mgr.Spawn(context.Background(), SpawnArgs{Description: "d", Prompt: "p", Type: "analyzer"})
		if res.Status != StatusCompleted {
			t.Fatalf("Status = %s (%s)", res.Status, res.Summary)
		}
		if job.Policy.Pref() != permission.PrefDeny {
			t.Errorf("child pref = %s, want deny (never loosened)", job.Policy.Pref())
		}
	})

	t.Run("loosening pref restriction rejected at the spawn door", func(t *testing.T) {
		parent, err := permission.New(t.TempDir(), permission.PrefDeny)
		if err != nil {
			t.Fatal(err)
		}
		yolo := permission.PrefYolo
		_, err = parent.Spawn(parent.Cwd(), permission.Restriction{Pref: &yolo})
		if err == nil || !strings.Contains(err.Error(), "loosen") {
			t.Fatalf("Policy.Spawn error = %v, want the monotonic 'loosen' rejection", err)
		}
		// plan-execute from a plan-analyze parent — the workflow
		// flavour of the same loosening attempt.
		parent2, _ := permission.New(t.TempDir(), permission.PrefYolo)
		parent2.SetWorkflow(permission.WorkflowPlanAnalyze)
		exec := permission.WorkflowPlanExecute
		_, err = parent2.Spawn(parent2.Cwd(), permission.Restriction{Workflow: &exec})
		if err == nil || !strings.Contains(err.Error(), "must stay plan-analyze") {
			t.Fatalf("Policy.Spawn error = %v, want the plan-analyze escape rejection", err)
		}
	})
}

// TestSpawn_CustomTypeDegradeIsDeterministicAcrossSpawns: the degrade
// decision is a pure function of def.Tools × the live parent registry
// — two spawns of the same typo'd type degrade identically (the
// startup warning already told the user; the spawn itself stays
// predictable).
func TestSpawn_CustomTypeDegradeIsDeterministicAcrossSpawns(t *testing.T) {
	set := agenttypes.NewSet()
	set.Add(def("typo-clerk", func(d *agenttypes.Definition) {
		d.Tools = []string{"read", "grpe"}
	}))
	var seen [][]string
	mgr := newCustomManager(t, func(_ context.Context, j RunnerJob) (RunnerResult, error) {
		seen = append(seen, j.ToolNames)
		return RunnerResult{Summary: "done"}, nil
	}, permission.PrefYolo, set)
	for i := 0; i < 2; i++ {
		res := mgr.Spawn(context.Background(), SpawnArgs{
			Description: fmt.Sprintf("d%d", i), Prompt: "p", Type: "typo-clerk", ParentTurn: i + 1,
		})
		if res.Status != StatusCompleted {
			t.Fatalf("spawn %d: Status = %s (%s)", i, res.Status, res.Summary)
		}
	}
	if len(seen) != 2 || !reflect.DeepEqual(seen[0], seen[1]) {
		t.Fatalf("degraded toolsets differ across spawns: %v vs %v", seen[0], seen[1])
	}
	if reflect.DeepEqual(seen[0], []string{"read"}) {
		t.Fatal("toolset was silently narrowed to the known subset — D13 forbids this")
	}
}

// --- P2-c: config-layer definitions through the unified typed route ---

// TestSpawn_ConfigOverrideRoutesBuiltin: a config-layer clone
// (OriginConfig — what agenttypes.Load builds from the config
// `agents` 段) routes the built-in onto another lane while keeping the
// built-in's everything-else verbatim. Two load-bearing properties:
//
//   - def provider/model merge into the spawn route (glm/glm-5.3 on
//     the RunnerJob) — the "免每次传参的默认路由";
//   - NO unknown-tools degrade: with webfetch absent from the parent
//     registry the clone's whitelist INTERSECTS (5 of 6 tools) instead
//     of degrading to the general-purpose set — the intersect
//     semantics the compile-time built-in path always had
//     (acceptance #5's byte-path guarantee, restated for the config
//     layer).
func TestSpawn_ConfigOverrideRoutesBuiltin(t *testing.T) {
	set, stats, err := agenttypes.Load(agenttypes.LoadOptions{
		ProjectDir:    t.TempDir(),
		UserAgentsDir: filepath.Join(withHome(t), "nope"),
		ConfigAgents:  map[string]agenttypes.RouteOverride{"explore": {Provider: "glm"}},
	})
	if err != nil || len(stats.Errors) != 0 {
		t.Fatalf("Load: %v %v", err, stats.Errors)
	}
	if e := set.Get("explore"); e == nil || e.Origin != agenttypes.OriginConfig {
		t.Fatalf("explore origin = %+v, want config clone", e)
	}

	// Parent registry WITHOUT webfetch — proves intersect vs degrade.
	noWebfetch := []string{"read", "grep", "list_dir", "git", "think",
		"agent", "ask_user", "bash", "write", "edit"}
	var job RunnerJob
	mgr := newCustomManagerWithTools(t, func(_ context.Context, j RunnerJob) (RunnerResult, error) {
		job = j
		return RunnerResult{Summary: "done"}, nil
	}, permission.PrefYolo, set, noWebfetch)

	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "d", Prompt: "p", Type: TypeExplore, ParentTurn: 1,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("Status = %s (%s)", res.Status, res.Summary)
	}
	if job.Provider != "glm" || job.Model != "glm-5.3" {
		t.Errorf("route = %q/%q, want glm/glm-5.3 from the config override", job.Provider, job.Model)
	}
	want := []string{"read", "grep", "list_dir", "git", "think"}
	if !reflect.DeepEqual(job.ToolNames, want) {
		t.Errorf("ToolNames = %v, want intersect %v (webfetch dropped, NOT degraded to the general-purpose set)", job.ToolNames, want)
	}
	if job.Policy.Workflow() != permission.WorkflowPlanAnalyze {
		t.Errorf("workflow = %s, want plan-analyze inherited from the cloned builtin", job.Policy.Workflow())
	}
	if !strings.Contains(job.SystemPrompt, agenttypes.Builtins()[1].Body) {
		t.Errorf("SystemPrompt missing the built-in explore body (config cannot re-prompt)")
	}
}

// newCustomManagerWithTools is newCustomManager with a configurable
// parent registry — the fixed helper pins customParentTools, and the
// config-layer test needs a webfetch-less parent to distinguish
// intersect from degrade.
func newCustomManagerWithTools(t *testing.T, runner Runner, pref permission.Preference, set *agenttypes.Set, parentTools []string) *Manager {
	t.Helper()
	parentCwd := t.TempDir()
	policy, err := permission.New(parentCwd, pref)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ManagerOpts{
		ProjectAbsPath:    parentCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return parentTools },
		MaxConcurrent:     3,
		Runner:            runner,
		AgentTypes:        set,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}
