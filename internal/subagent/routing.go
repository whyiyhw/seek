package subagent

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Per-agent model routing (feature-orchestration.md §4.2, P1-c).
//
// The `agent` tool accepts optional provider/model params so a subagent
// can run on a different LLM lane than the parent session (e.g. parent
// on DeepSeek, explore children on GLM's subscription lane). Resolution
// is table-driven and purely static: the provider→model table lives
// here as code constants (same discipline as D8's provider-capability
// table — no runtime probing, no key sniffing; the paseo "provider
// 首次无头冒烟" pitfall is exactly what explicit naming avoids).
//
// Key availability is NOT resolved here — Manager validates names
// against this table; the Runner (cmd/seek's buildSubagentRunner) maps
// the resolved name onto a live client via its injected route table
// and fails the spawn with a structured error when the lane has no
// configured API key.

// Provider lane names accepted by the `agent` tool's provider param.
// Wire-format strings — additions must extend the tool schema's enum
// AND the route table in cmd/seek in lock-step.
const (
	// ProviderDeepSeek is the first-class DeepSeek lane (pkg/deepseek
	// client, cache metadata).
	ProviderDeepSeek = "deepseek"
	// ProviderGLM is Zhipu's GLM lane (pkg/llm/compatible on the
	// pinned bigmodel.cn endpoint, default glm-5.3).
	ProviderGLM = "glm"
	// ProviderOpenCode is the OpenCode Go subscription lane
	// (pkg/llm/compatible on opencode.ai/inference/go, flat
	// request-window plan, default deepseek-v4.1-flash; requires
	// OPENCODE_API_KEY).
	ProviderOpenCode = "opencode"
	// ProviderOpenDesign is the OpenDesign Cloud subscription lane
	// (pkg/llm/compatible on amr-link.open-design.ai/v1, monthly
	// allowance; requires OPENDESIGN_API_KEY). Its menu overlaps the Go
	// lane's on several wire ids — that is fine here, because routing is
	// keyed by (provider, model): both lanes list the shared ids under
	// their own entry (c_dxrjjl).
	ProviderOpenDesign = "opendesign"
)

// providerOrder is the deterministic lane order used for MODEL-ONLY
// inference (a spawn that names a model but no provider). It exists
// because knownModels is a map: iterating it made the inferred lane
// depend on Go's map order, which is randomised per run — and now that
// two lanes share several wire ids (deepseek-v4.1-flash, deepseek-v4-pro,
// kimi-k2.7-code, …) a randomised answer would route the same call to
// different gateways on different runs. The order is the OLDER claim
// first, matching modelcap.LaneOf's documented tie-break for shared ids.
var providerOrder = []string{ProviderDeepSeek, ProviderGLM, ProviderOpenCode, ProviderOpenDesign}

// knownModels is the provider→model routing table. Slice order
// matters: the first entry is the lane's default model (used when
// provider is given without model). A model id must appear under its
// own provider — model ids from another lane are rejected so a call
// can never silently bill the wrong rate card (pricing.Cost keys off
// the model id; deepseek ids on the GLM lane would fall to the
// DeepSeek-Flash fallback and misprice).
var knownModels = map[string][]string{
	ProviderDeepSeek:   {deepseek.ModelV41Flash},
	ProviderGLM:        {pricing.ModelGLM53},
	ProviderOpenCode:   {pricing.ModelOpenCodeV41Flash},
	ProviderOpenDesign: providerOpenDesignModels(),
}

// providerOpenDesignModels expands the OpenDesign lane's menu
// (pricing.OpenDesignModels — the one authoritative list the ACP dropdown
// and the TUI /model picker also render) into the routing table's shape.
// Legal here for the same reason the two lanes' menus overlap legally:
// ResolveRoute validates the (provider, model) PAIR, so an id listed under
// both entries is routable on either — naming the provider is what picks
// the gateway. Without this entry an explicit provider="opendesign" spawn
// died as "unknown provider", which is the path butler's verification
// children wanted to take.
func providerOpenDesignModels() []string {
	out := make([]string, 0, len(pricing.OpenDesignModels))
	for _, row := range pricing.OpenDesignModels {
		out = append(out, row.ID)
	}
	return out
}

// Route is the resolved per-spawn routing decision. Empty Provider
// means "inherit the parent session's provider/model unchanged" — the
// pre-routing path, byte-identical to v0.6.0 behaviour.
type Route struct {
	// Provider is the resolved lane name ("" = inherit parent).
	Provider string
	// Model is the resolved model id ("" = inherit parent's session
	// model; non-empty whenever Provider is non-empty).
	Model string
}

// ResolveRoute validates the raw provider/model params from a spawn
// call and fills in defaults:
//
//   - both empty          → inherit parent (Route{})
//   - provider only       → lane's default model
//   - model only          → provider inferred from the model id
//   - both                → model must belong to the named provider
//
// Unknown provider names and model ids that don't appear in the table
// return an error — surfaced by Manager.Spawn as a wire-format
// [agent: failed reason=spawn_error] result BEFORE any index event is
// written (the spawn never happened).
func ResolveRoute(provider, model string) (Route, error) {
	if provider == "" && model == "" {
		return Route{}, nil
	}
	if provider == "" {
		// Model-only call: infer the lane.
		//
		// The lane authority comes FIRST (modelcap.LaneOf). The routing
		// table below is a curated allow-list, not a menu copy — the Go
		// entry lists only the lane default — so scanning it first would
		// answer "opendesign" for Go-pool ids (kimi-k2.7-code,
		// glm-5.3-flash, …) that merely happen to also sit on the
		// OpenDesign menu, and would answer "opendesign" for
		// deepseek-v4-pro, which the first-tier lane owns. LaneOf applies
		// the documented tie-breaks (shared first-tier ids, then the older
		// gateway claim), so inference and every display surface agree.
		//
		// Only lanes the process can actually route are accepted (a
		// LaneOf answer like "openai" with no route entry falls through to
		// the table scan, which then reports the unknown id — unchanged
		// from before).
		if lane, _ := modelcap.LaneOf(model); lane != "" {
			if _, routable := knownModels[lane]; routable {
				return Route{Provider: lane, Model: model}, nil
			}
		}
		// Fallback: table scan in providerOrder (NOT map order) so a
		// table-only id — one LaneOf has never heard of — always infers the
		// same lane. A model that belongs to no lane is an error (not a
		// parent-model guess): the caller named a model seek can't route.
		for _, p := range providerOrder {
			for _, m := range knownModels[p] {
				if m == model {
					return Route{Provider: p, Model: m}, nil
				}
			}
		}
		return Route{}, fmt.Errorf("unknown model %q — known: %s", model, allKnownModels())
	}
	models, ok := knownModels[provider]
	if !ok {
		return Route{}, fmt.Errorf("unknown provider %q — valid: %s", provider, strings.Join(providerOrder, ", "))
	}
	if model == "" {
		return Route{Provider: provider, Model: models[0]}, nil
	}
	for _, m := range models {
		if m == model {
			return Route{Provider: provider, Model: m}, nil
		}
	}
	// Cross-lane refusal, with the lane named when an authority knows it
	// (c_dxrjjl): a model id alone is not enough to route — the host that
	// passes only a name is exactly how a gateway wire id ended up on the
	// DeepSeek lane and 400'd upstream. modelcap.LaneOf is the single lane
	// authority, so the hint can't drift from every other surface. The
	// leading phrase stays "does not belong to provider" — it is wire
	// format to callers that substring-match it.
	if lane, _ := modelcap.LaneOf(model); lane != "" && lane != provider {
		return Route{}, fmt.Errorf("model %q does not belong to provider %q — it belongs to lane %q; pass provider=%q (a gateway wire id sent to another lane answers an upstream error that costs the whole turn). %q offers: %s",
			model, provider, lane, lane, provider, joinModels(models))
	}
	return Route{}, fmt.Errorf("model %q does not belong to provider %q — known: %s",
		model, provider, joinModels(models))
}

// allKnownModels renders every known model id across lanes for error
// messages ("glm-5.3, deepseek-flash"). Walks providerOrder so the text is
// stable run to run — the map it reads from is not.
func allKnownModels() string {
	var out strings.Builder
	first := true
	for _, p := range providerOrder {
		for _, m := range knownModels[p] {
			if !first {
				out.WriteString(", ")
			}
			out.WriteString(m)
			first = false
		}
	}
	return out.String()
}

func joinModels(models []string) string {
	var out strings.Builder
	for i, m := range models {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(m)
	}
	return out.String()
}
