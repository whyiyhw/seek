package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestResolveRoute is the routing table's contract test (P1-c
// acceptance: 路由参数解析 / 默认继承 / 非法模型报错). Every
// combination of provider/model params maps to exactly one outcome.
func TestResolveRoute(t *testing.T) {
	cases := []struct {
		name            string
		provider, model string
		wantRoute       Route
		wantErr         string // substring; "" = expect success
	}{
		{
			name:      "both empty inherits parent",
			provider:  "",
			model:     "",
			wantRoute: Route{},
		},
		{
			name:      "provider only takes lane default model",
			provider:  "glm",
			model:     "",
			wantRoute: Route{Provider: ProviderGLM, Model: pricing.ModelGLM53},
		},
		{
			name:      "deepseek provider only",
			provider:  "deepseek",
			model:     "",
			wantRoute: Route{Provider: ProviderDeepSeek, Model: deepseek.ModelV41Flash},
		},
		{
			name:      "both explicit valid",
			provider:  "glm",
			model:     "glm-5.3",
			wantRoute: Route{Provider: ProviderGLM, Model: "glm-5.3"},
		},
		{
			name:      "model only infers provider",
			provider:  "",
			model:     "glm-5.3",
			wantRoute: Route{Provider: ProviderGLM, Model: "glm-5.3"},
		},
		{
			name:      "model only deepseek id",
			provider:  "",
			model:     "deepseek-flash",
			wantRoute: Route{Provider: ProviderDeepSeek, Model: "deepseek-flash"},
		},
		{
			name:     "unknown provider rejected",
			provider: "anthropic",
			model:    "",
			wantErr:  `unknown provider "anthropic"`,
		},
		{
			name:     "cross-lane model rejected",
			provider: "glm",
			model:    "deepseek-flash",
			wantErr:  `does not belong to provider "glm"`,
		},
		{
			name:     "unknown model with provider rejected",
			provider: "glm",
			model:    "glm-4.6",
			wantErr:  `does not belong to provider "glm"`,
		},
		{
			name:     "unknown model without provider rejected",
			provider: "",
			model:    "gpt-4o",
			wantErr:  `unknown model "gpt-4o"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRoute(tc.provider, tc.model)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveRoute(%q,%q) = %+v, want error containing %q",
						tc.provider, tc.model, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRoute(%q,%q) unexpected error: %v", tc.provider, tc.model, err)
			}
			if got != tc.wantRoute {
				t.Errorf("ResolveRoute(%q,%q) = %+v, want %+v", tc.provider, tc.model, got, tc.wantRoute)
			}
		})
	}
}

// TestSpawn_RoutePlumbedToRunnerJob: valid routing params resolve to
// lane defaults and land on the RunnerJob the Runner consumes.
func TestSpawn_RoutePlumbedToRunnerJob(t *testing.T) {
	var gotJob RunnerJob
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		gotJob = j
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "glm research",
		Prompt:      "p",
		Type:        TypeExplore,
		Provider:    "glm",
	})
	if res.Status != StatusCompleted {
		t.Fatalf("Status = %s (%s), want completed", res.Status, res.Summary)
	}
	if gotJob.Provider != ProviderGLM {
		t.Errorf("job.Provider = %q, want glm", gotJob.Provider)
	}
	if gotJob.Model != pricing.ModelGLM53 {
		t.Errorf("job.Model = %q, want glm lane default glm-5.3", gotJob.Model)
	}
}

// TestSpawn_InheritRouteEmptyInJob: omitted routing params keep the
// RunnerJob's route fields empty — the Runner's inherit path, which
// must be byte-identical to the pre-routing behaviour.
func TestSpawn_InheritRouteEmptyInJob(t *testing.T) {
	var gotProvider, gotModel string
	mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
		gotProvider, gotModel = j.Provider, j.Model
		return RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	res := mgr.Spawn(context.Background(), SpawnArgs{
		Description: "plain spawn",
		Prompt:      "p",
		Type:        TypeGeneralPurpose,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("Status = %s (%s), want completed", res.Status, res.Summary)
	}
	if gotProvider != "" || gotModel != "" {
		t.Errorf("inherit path job.Provider/Model = %q/%q, want empty", gotProvider, gotModel)
	}
}

// TestSpawn_RejectsInvalidRoute: invalid routing params fail with the
// wire-format spawn_error BEFORE any index event is written (same
// class as unknown subagent_type).
func TestSpawn_RejectsInvalidRoute(t *testing.T) {
	for _, tc := range []struct {
		name            string
		provider, model string
		wantErr         string
	}{
		{"unknown provider", "sonnet", "", `unknown provider "sonnet"`},
		{"cross-lane model", "deepseek", "glm-5.3", "does not belong"},
		{"unknown model", "", "claude-opus-9", `unknown model "claude-opus-9"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runnerRan := false
			mgr := newManager(t, func(ctx context.Context, j RunnerJob) (RunnerResult, error) {
				runnerRan = true
				return RunnerResult{Summary: "should not run"}, nil
			})
			res := mgr.Spawn(context.Background(), SpawnArgs{
				Description: "bad route",
				Prompt:      "p",
				Type:        TypeExplore,
				Provider:    tc.provider,
				Model:       tc.model,
			})
			if res.Status != StatusFailed {
				t.Errorf("Status = %s, want failed", res.Status)
			}
			if !strings.HasPrefix(res.Summary, "[agent: failed reason=spawn_error]") {
				t.Errorf("Summary missing spawn_error wire format:\n%s", res.Summary)
			}
			if !strings.Contains(res.Summary, tc.wantErr) {
				t.Errorf("Summary %q missing %q", res.Summary, tc.wantErr)
			}
			if runnerRan {
				t.Error("Runner executed despite invalid route")
			}
			// No index event: validation precedes the started event.
			got, err := mgr.List(ListFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("index has %d events after rejected spawn, want 0", len(got))
			}
		})
	}
}

// TestIsDeepSeekOnlyTool pins the D8 filter set: these names must be
// stripped from any child not running on the first-class DeepSeek
// client.
func TestIsDeepSeekOnlyTool(t *testing.T) {
	for _, name := range []string{"think", "fim_complete"} {
		if !IsDeepSeekOnlyTool(name) {
			t.Errorf("IsDeepSeekOnlyTool(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"read", "bash", "grep", "agent", ""} {
		if IsDeepSeekOnlyTool(name) {
			t.Errorf("IsDeepSeekOnlyTool(%q) = true, want false", name)
		}
	}
}
