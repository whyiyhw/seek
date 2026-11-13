package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm/compatible"
)

// fakeLane is an httptest stand-in for one provider lane's backend.
// It captures the `model` and tool-function names of every chat
// request it serves, then replies with a canned SSE stream — so a
// single test can assert BOTH routing (which backend / which model
// id) and the D8 tool filter (which tools the child was offered).
type fakeLane struct {
	srv *httptest.Server

	mu       sync.Mutex
	models   [][]string   // per-request model field (1 element each)
	toolSets []([]string) // per-request tool function names
}

type capturedRequest struct {
	Model string `json:"model"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func newFakeLane(t *testing.T, sseBody string) *fakeLane {
	t.Helper()
	fl := &fakeLane{}
	fl.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var creq capturedRequest
		_ = json.Unmarshal(body, &creq)
		names := make([]string, 0, len(creq.Tools))
		for _, tl := range creq.Tools {
			names = append(names, tl.Function.Name)
		}
		sort.Strings(names)
		fl.mu.Lock()
		fl.models = append(fl.models, []string{creq.Model})
		fl.toolSets = append(fl.toolSets, names)
		fl.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody)
	}))
	t.Cleanup(fl.srv.Close)
	return fl
}

func (fl *fakeLane) requests() (models [][]string, toolSets [][]string) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return append([][]string{}, fl.models...), append([][]string{}, fl.toolSets...)
}

// stubParentTool is a minimal tools.Tool so parentReg can back names
// the runner reuses verbatim (think / fim_complete in the D8 filter
// test — the runner's default branch needs a non-nil Lookup hit).
type stubParentTool struct{ name string }

func (s stubParentTool) Name() string            { return s.name }
func (s stubParentTool) Description() string     { return "stub " + s.name }
func (s stubParentTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubParentTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "stub", nil
}

// deepSeekSSE / glmSSE are single-turn replies in each lane's wire
// format. Usage blocks are deliberately DIFFERENT shapes and numbers:
// DeepSeek reports hit/miss split directly; the GLM lane reports
// prompt_tokens + cached_tokens detail (mapped to hit/miss by
// runTurnLLM). Distinct numbers make a cross-lane pricing mixup
// (e.g. GLM usage priced at DeepSeek-Flash fallback rates) fail the
// exact-cost assertion below.
const deepSeekSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"DeepSeek says hi.\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8000,\"completion_tokens\":100,\"total_tokens\":8100,\"prompt_cache_hit_tokens\":7500,\"prompt_cache_miss_tokens\":500}}\n\n" +
	"data: [DONE]\n\n"

const glmSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"GLM says hi.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9000,\"completion_tokens\":200,\"prompt_tokens_details\":{\"cached_tokens\":6000}}}\n\n" +
	"data: [DONE]\n\n"

// TestBuildSubagentRunner_MixedFleetCostRollup is the P1-c acceptance
// test for cost aggregation: one parent tracker adopts two children —
// one inheriting the parent's DeepSeek lane, one explicitly routed to
// GLM — and CumulativeCost() must equal each child's usage priced at
// its OWN model's card (httptest fake backends, zero real keys).
func TestBuildSubagentRunner_MixedFleetCostRollup(t *testing.T) {
	dsLane := newFakeLane(t, deepSeekSSE)
	glmLane := newFakeLane(t, glmSSE)

	parentReg := tools.New()
	parentReg.Add(stubParentTool{name: "think"})
	parentReg.Add(stubParentTool{name: "fim_complete"})

	runner := buildSubagentRunner(subagentRunnerOpts{
		client:    deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(dsLane.srv.URL)),
		parentReg: parentReg,
		hooksReg:  hooks.NewRegistry(),
		getModel:  func() string { return deepseek.ModelV41Flash },
		getEffort: func() string { return "" },
		routes: map[string]subagentRoute{
			subagent.ProviderGLM: {provider: compatible.New("test", glmLane.srv.URL, "GLM")},
		},
	})

	parent := cache.New()
	runJob := func(provider string) subagent.RunnerResult {
		t.Helper()
		policy, err := permission.New(t.TempDir(), permission.PrefYolo)
		if err != nil {
			t.Fatal(err)
		}
		child := cache.New()
		parent.AdoptChild(child)
		res, err := runner(context.Background(), subagent.RunnerJob{
			SubSid:       "20261113-100000-r" + provider,
			SystemPrompt: "you are a subagent",
			UserPrompt:   "say hi",
			SessionDir:   t.TempDir(),
			Tracker:      child,
			Policy:       policy,
			// Includes both D8 DeepSeek-only names plus a normal tool
			// so the filter assertion has signal in both directions.
			ToolNames: []string{"read", "think", "fim_complete"},
			Provider:  provider,
			Model:     map[string]string{"": "", "glm": pricing.ModelGLM53}[provider],
		})
		if err != nil {
			t.Fatalf("runner(provider=%q): %v", provider, err)
		}
		return res
	}

	dsRes := runJob("")     // inherit → parent's DeepSeek lane
	glmRes := runJob("glm") // explicit GLM route
	if dsRes.Summary != "DeepSeek says hi." || glmRes.Summary != "GLM says hi." {
		t.Errorf("summaries = %q / %q, want lane-specific replies", dsRes.Summary, glmRes.Summary)
	}

	// Routing assertions: each backend saw exactly its own model id.
	dsModels, dsTools := dsLane.requests()
	glmModels, glmTools := glmLane.requests()
	if len(dsModels) != 1 || dsModels[0][0] != deepseek.ModelV41Flash {
		t.Errorf("deepseek lane models = %v, want [[deepseek-flash]] (session model inherit)", dsModels)
	}
	if len(glmModels) != 1 || glmModels[0][0] != pricing.ModelGLM53 {
		t.Errorf("glm lane models = %v, want [[glm-5.3]] (routed lane default)", glmModels)
	}

	// D8: DeepSeek-only tools ride the DeepSeek lane but are stripped
	// from the GLM child's registry.
	if len(dsTools) != 1 || strings.Join(dsTools[0], ",") != "fim_complete,read,think" {
		t.Errorf("deepseek lane tools = %v, want [fim_complete read think]", dsTools)
	}
	if len(glmTools) != 1 || strings.Join(glmTools[0], ",") != "read" {
		t.Errorf("glm lane tools = %v, want [read] — think/fim_complete must be filtered", glmTools)
	}

	// Cost aggregation: each child priced at its own card. The
	// runner records with pricing.CurrentTier(time.Now()); GLM is
	// flat-rate (NoOffPeakDiscount) so only the DeepSeek leg depends
	// on the tier — accept either tier to stay deterministic at the
	// peak/off-peak boundary.
	dsUsage := deepseek.Usage{PromptTokens: 8000, CompletionTokens: 100,
		PromptCacheHitTokens: 7500, PromptCacheMissTokens: 500}
	glmUsage := deepseek.Usage{PromptTokens: 9000, CompletionTokens: 200,
		PromptCacheHitTokens: 6000, PromptCacheMissTokens: 3000}
	glmCost := pricing.Cost(pricing.ModelGLM53, pricing.TierStandard, glmUsage)
	wantPeak := pricing.Cost(deepseek.ModelV41Flash, pricing.TierStandard, dsUsage) + glmCost
	wantOff := pricing.Cost(deepseek.ModelV41Flash, pricing.TierOffPeak, dsUsage) + glmCost
	got := parent.CumulativeCost()
	if got != wantPeak && got != wantOff {
		t.Errorf("CumulativeCost = %v, want %v (peak) or %v (off-peak)", got, wantPeak, wantOff)
	}

	// Token rollup also per child (wire footer inputs).
	if glmRes.Tokens.Prompt != 9000 || glmRes.Tokens.Completion != 200 || glmRes.Tokens.CacheHit != 6000 {
		t.Errorf("glm tokens = %+v, want 9000/200/6000", glmRes.Tokens)
	}
}

// TestBuildSubagentRunner_RouteWithoutKeyFails: a lane name that
// resolves in the routing table (Manager validation passed) but has
// no backend in the route table fails the spawn with a structured
// error — never a silent fallback to the parent's lane.
func TestBuildSubagentRunner_RouteWithoutKeyFails(t *testing.T) {
	runner := buildSubagentRunner(subagentRunnerOpts{
		client:    deepseek.New(deepseek.WithAPIKey("test")),
		parentReg: tools.New(),
		hooksReg:  hooks.NewRegistry(),
		getModel:  func() string { return deepseek.ModelV41Flash },
		getEffort: func() string { return "" },
		routes:    nil, // no GLM key configured
	})
	policy, err := permission.New(t.TempDir(), permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner(context.Background(), subagent.RunnerJob{
		SubSid: "20261113-100000-nokey", SystemPrompt: "s", UserPrompt: "p",
		SessionDir: t.TempDir(), Tracker: cache.New(), Policy: policy,
		Provider: subagent.ProviderGLM, Model: pricing.ModelGLM53,
	})
	if err == nil {
		t.Fatal("expected error for glm route with no configured key")
	}
	if !strings.Contains(err.Error(), "no configured API key") {
		t.Errorf("error %q missing no-key hint", err)
	}
	if !strings.Contains(err.Error(), "glm") {
		t.Errorf("error %q missing lane name", err)
	}
}
