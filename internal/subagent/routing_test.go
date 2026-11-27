package subagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/modelcap"
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
			name:      "opencode provider only takes lane default",
			provider:  "opencode",
			model:     "",
			wantRoute: Route{Provider: ProviderOpenCode, Model: pricing.ModelOpenCodeV41Flash},
		},
		{
			// c_tnzudu ①: the Go entry is the lane's MENU now, not a
			// one-row allow-list. The picker offers kimi-k2.7-code, so a
			// spawn naming it must resolve — before this the only way to
			// reach it was via the OpenDesign lane (or an outright refusal).
			name:      "opencode + Go menu id accepted",
			provider:  ProviderOpenCode,
			model:     "kimi-k2.7-code",
			wantRoute: Route{Provider: ProviderOpenCode, Model: "kimi-k2.7-code"},
		},
		{
			// c_tnzudu ②: deepseek-v4-pro is on the official API's own
			// supported-name list — "The supported API model names are
			// deepseek-flash, deepseek-v4-pro" — and modelcap.LaneOf
			// resolves the id to THIS lane, so the first-tier entry must
			// carry it. Without it the cross-lane refusal for e.g.
			// (glm, v4-pro) said "pass provider=deepseek" and that pair
			// was refused again.
			name:      "deepseek + v4-pro accepted",
			provider:  ProviderDeepSeek,
			model:     pricing.ModelV4Pro,
			wantRoute: Route{Provider: ProviderDeepSeek, Model: pricing.ModelV4Pro},
		},
		{
			// ...and model-only v4-pro lands on the lane that owns its
			// rate card (LaneOf's sharedFirstTier tie-break), not on
			// whichever gateway also lists it.
			name:      "model only v4-pro infers deepseek",
			provider:  "",
			model:     pricing.ModelV4Pro,
			wantRoute: Route{Provider: ProviderDeepSeek, Model: pricing.ModelV4Pro},
		},
		{
			name:      "model only opencode id (distinct from direct deepseek-flash)",
			provider:  "",
			model:     "deepseek-v4.1-flash",
			wantRoute: Route{Provider: ProviderOpenCode, Model: "deepseek-v4.1-flash"},
		},
		{
			// c_dxrjjl: the OpenDesign lane's own default, used by menu ids
			// whose only claim is that lane (pricing.OpenDesignModels).
			name:      "opendesign provider only takes lane default",
			provider:  ProviderOpenDesign,
			model:     "",
			wantRoute: Route{Provider: ProviderOpenDesign, Model: pricing.ModelOpenDesignV41Flash},
		},
		{
			// The eco id is the card's whole reason for existing: an
			// OpenDesign-exclusive wire id that used to die as "unknown
			// provider" on an explicit opendesign spawn.
			name:      "opendesign provider + eco model accepted",
			provider:  ProviderOpenDesign,
			model:     "deepseek-v4.1-flash-eco",
			wantRoute: Route{Provider: ProviderOpenDesign, Model: "deepseek-v4.1-flash-eco"},
		},
		{
			name:      "model only eco id infers opendesign",
			provider:  "",
			model:     "deepseek-v4.1-flash-eco",
			wantRoute: Route{Provider: ProviderOpenDesign, Model: "deepseek-v4.1-flash-eco"},
		},
		{
			name:      "openDesign shared id routable on that lane too",
			provider:  ProviderOpenDesign,
			model:     pricing.ModelOpenDesignV41Flash,
			wantRoute: Route{Provider: ProviderOpenDesign, Model: pricing.ModelOpenDesignV41Flash},
		},
		{
			name:     "unknown provider rejected",
			provider: "anthropic",
			model:    "",
			wantErr:  `unknown provider "anthropic"`,
		},
		{
			// Acceptance ② of c_dxrjjl (routing half): the DeepSeek lane
			// receiving the eco id must fail HERE, structurally, naming the
			// lane — never hand it to api.deepseek.com for a 400.
			name:     "deepseek lane refuses the eco id and names the lane",
			provider: ProviderDeepSeek,
			model:    "deepseek-v4.1-flash-eco",
			wantErr:  `does not belong to provider "deepseek"`,
		},
		{
			name:     "deepseek lane refuses the eco id and points at opendesign",
			provider: ProviderDeepSeek,
			model:    "deepseek-v4.1-flash-eco",
			wantErr:  `belongs to lane "opendesign"`,
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

// TestSpawnBackground_RoutePlumbedToRunnerJob pins the P1-a × P1-c
// merge point (card c_bickfj 二次收口): SpawnBackground runs the same
// begin-step-1 ResolveRoute as sync Spawn, and the resolved route
// rides the spawnSetup struct into the background RunnerJob — routing
// a background subagent to another lane must not silently fall back
// to the parent's lane.
func TestSpawnBackground_RoutePlumbedToRunnerJob(t *testing.T) {
	release := make(chan struct{})
	jobCh := make(chan RunnerJob, 1)
	runner := func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		jobCh <- job
		<-release
		return RunnerResult{Summary: "routed findings", Turns: 1}, nil
	}
	mgr, _ := newBackgroundManager(t, runner)

	res := mgr.SpawnBackground(SpawnArgs{
		Description: "glm bulk research",
		Prompt:      "p",
		Type:        TypeExplore,
		Provider:    "glm",
	})
	if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("bad handle summary:\n%s", res.Summary)
	}

	// Receive (not a shared variable) so the happens-before edge is
	// explicit under -race.
	var gotJob RunnerJob
	select {
	case gotJob = <-jobCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runner never received the background job")
	}
	close(release)
	waitFor(t, 5*time.Second, func() bool { return mgr.ActiveCount() == 0 })

	if gotJob.Provider != ProviderGLM {
		t.Errorf("background job.Provider = %q, want glm", gotJob.Provider)
	}
	if gotJob.Model != pricing.ModelGLM53 {
		t.Errorf("background job.Model = %q, want glm lane default glm-5.3", gotJob.Model)
	}
}

// TestSpawnBackground_RejectsInvalidRoute: the background entry runs
// the identical single-point validation — an unknown provider fails
// with the wire-format spawn_error (NOT a background handle), the
// Runner never executes, and no index event is written.
func TestSpawnBackground_RejectsInvalidRoute(t *testing.T) {
	runnerRan := false
	runner := func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		runnerRan = true
		return RunnerResult{Summary: "should not run"}, nil
	}
	mgr, _ := newBackgroundManager(t, runner)

	res := mgr.SpawnBackground(SpawnArgs{
		Description: "bad route",
		Prompt:      "p",
		Type:        TypeExplore,
		Provider:    "sonnet",
	})
	if res.Status != StatusFailed {
		t.Errorf("Status = %s, want failed", res.Status)
	}
	if !strings.HasPrefix(res.Summary, "[agent: failed reason=spawn_error]") {
		t.Errorf("Summary missing spawn_error wire format:\n%s", res.Summary)
	}
	if !strings.Contains(res.Summary, `unknown provider "sonnet"`) {
		t.Errorf("Summary %q missing unknown-provider detail", res.Summary)
	}
	if runnerRan {
		t.Error("Runner executed despite invalid route on background path")
	}
	got, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("index has %d events after rejected background spawn, want 0", len(got))
	}
}

// TestResolveRoute_ModelOnlyAgreesWithLaneOf is the drift guard for the two
// lane authorities (c_dxrjjl): the routing table's providerOrder inference
// and modelcap.LaneOf must name the SAME lane for every menu id, otherwise a
// spawn that passes only a model would land somewhere the ACP startup gate
// (and the web lane chip) disagree with. Both derive from pricing's menus —
// this test is what keeps it that way.
func TestResolveRoute_ModelOnlyAgreesWithLaneOf(t *testing.T) {
	check := func(id string) {
		wantLane, _ := modelcap.LaneOf(id)
		if wantLane == "" {
			t.Fatalf("%q has no lane — the menus and LaneOf drifted", id)
		}
		got, err := ResolveRoute("", id)
		if err != nil {
			t.Errorf("ResolveRoute(model-only %q): %v (LaneOf says %q)", id, err, wantLane)
			return
		}
		if got.Provider != wantLane {
			t.Errorf("model-only inference of %q = %q, LaneOf says %q", id, got.Provider, wantLane)
		}
	}
	for _, row := range pricing.OpenCodeGoModels {
		if pricing.ModelGLM53 == row.ID || pricing.ModelV4Pro == row.ID {
			continue // shared first-tier ids: both authorities name the first tier
		}
		check(row.ID)
	}
	// The OpenDesign menu too — its exclusive rows (the eco id, flashx,
	// claude-haiku-5.5, gpt-6-luna) are exactly the ones that only LaneOf
	// can place, so a menu row appearing there without a matching LaneOf
	// answer would be invisible to the loop above.
	for _, row := range pricing.OpenDesignModels {
		check(row.ID)
	}
	check(pricing.ModelGLM53)
	check(pricing.ModelV4Pro)
	check(deepseek.ModelV41Flash)
	check("deepseek-v4.1-flash-eco")
	check(pricing.ModelOpenDesignV41Flash)
}

// TestResolveRoute_NoDeadEndHints is acceptance ③ of c_tnzudu: every
// refusal that advises "pass provider=X" must be a step the caller can
// actually take — following it and retrying the SAME model must resolve.
// A hint that leads to another refusal is worse than no hint: the model
// (or the human) burns a turn on it and has no way to tell an advice
// problem from a routing problem.
//
// The sweep is exhaustive over the routing table minus nothing: every
// (lane, id) pair in the cross-product of providerOrder × all known ids,
// plus first-tier freeform ids that only LaneOf's prefix fallback
// recognises (the retired deepseek-v4-flash family, unfetched vendor
// defaults), plus ids no lane knows. Where the advice is emitted, it must
// work; where it would not work, it must not be emitted.
func TestResolveRoute_NoDeadEndHints(t *testing.T) {
	ids := map[string]bool{
		deepseek.ModelV41Flash: true,
		pricing.ModelV4Pro:     true,
		// Freeform first-tier ids: LaneOf resolves these by vendor prefix
		// but NO routing entry lists them, so "pass provider=deepseek"
		// would be a lie.
		"deepseek-v4-flash": true,
		"deepseek-reasoner": true,
		"glm-5.3-flashx2":   true,
		// Second-tier lanes this process cannot route at all.
		"claude-opus-9":    true,
		"gpt-4o":           true,
		"gemini-2.0-flash": true,
		// Nothing knows this one.
		"totally-unknown": true,
	}
	for _, lane := range providerOrder {
		for _, m := range knownModels[lane] {
			ids[m] = true
		}
	}
	for id := range ids {
		for _, provider := range providerOrder {
			if _, err := ResolveRoute(provider, id); err != nil {
				advice := passProviderAdvice(err.Error())
				if advice == "" {
					continue
				}
				if _, retryErr := ResolveRoute(advice, id); retryErr != nil {
					t.Errorf("dead-end hint: ResolveRoute(%q,%q) says pass provider=%q, but following it fails: %v",
						provider, id, advice, retryErr)
				}
			}
		}
	}
}

// passProviderAdvice pulls the lane name out of a "pass provider=%q"
// hint ("" when the refusal carries no advice). Substring-anchored rather
// than a full regexp: the surrounding prose is free to change, the quoted
// lane name is the part callers act on.
func passProviderAdvice(msg string) string {
	const marker = `pass provider="`
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// TestResolveRoute_MenusAreRoutable is acceptance ①/④ of c_tnzudu: a lane's
// entry IS its menu, so every id either subscription surface offers must be
// spawnable on that lane — and, because the agent tool's description tells
// the model to "pass both when the gateway matters", every id BOTH gateway
// menus carry must resolve under both entries. The enum in the tool schema
// and the menus in pricing are the inputs; the routing table must accept
// their cross-product without a second table drifting away from them.
func TestResolveRoute_MenusAreRoutable(t *testing.T) {
	for _, lane := range []string{ProviderOpenCode, ProviderOpenDesign} {
		for _, row := range knownModels[lane] {
			if _, err := ResolveRoute(lane, row); err != nil {
				t.Errorf("ResolveRoute(%q,%q) refused a row of that lane's own menu: %v", lane, row, err)
			}
		}
	}

	// The overlap is what "pass both" is FOR: a shared id must be routable
	// on whichever gateway the caller names.
	for _, menu := range [][]pricing.LaneModel{pricing.OpenCodeGoModels, pricing.OpenDesignModels} {
		for _, row := range menu {
			for _, lane := range []string{ProviderOpenCode, ProviderOpenDesign} {
				if !modelcap.Serves(lane, row.ID) {
					continue // that gateway's plan does not carry it
				}
				if _, err := ResolveRoute(lane, row.ID); err != nil {
					t.Errorf("shared menu id %q refused on %q although modelcap.Serves says yes: %v", row.ID, lane, err)
				}
			}
		}
	}

	// And the default is still the lane's own row-0 model, so a
	// provider-only spawn keeps its documented meaning.
	for _, lane := range providerOrder {
		got, err := ResolveRoute(lane, "")
		if err != nil {
			t.Errorf("ResolveRoute(%q,\"\") = %v", lane, err)
			continue
		}
		if got.Model != knownModels[lane][0] {
			t.Errorf("lane default for %q = %q, want table row 0 %q", lane, got.Model, knownModels[lane][0])
		}
	}
}

// TestResolveRoute_ModelOnlyStaysBounded pins the EDGE of the LaneOf-first
// inference: an id whose lane has no route entry in this process (openai /
// anthropic / gemini children are not routed at all) must still be refused,
// not silently mapped onto some lane. Widening inference to "any lane the id
// looks like" would let a typo spawn on the wrong gateway.
func TestResolveRoute_ModelOnlyStaysBounded(t *testing.T) {
	for _, model := range []string{"gpt-4o", "claude-opus-9", "gemini-2.0-flash", "totally-unknown"} {
		if _, err := ResolveRoute("", model); err == nil {
			t.Errorf("model-only %q resolved to a lane this process cannot route", model)
		}
	}
}
