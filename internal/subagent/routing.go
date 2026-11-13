package subagent

import (
	"fmt"

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
	// client, FIM + cache metadata + think tool).
	ProviderDeepSeek = "deepseek"
	// ProviderGLM is Zhipu's GLM lane (pkg/llm/compatible on the
	// pinned bigmodel.cn endpoint, default glm-5.3).
	ProviderGLM = "glm"
)

// knownModels is the provider→model routing table. Slice order
// matters: the first entry is the lane's default model (used when
// provider is given without model). A model id must appear under its
// own provider — model ids from another lane are rejected so a call
// can never silently bill the wrong rate card (pricing.Cost keys off
// the model id; deepseek ids on the GLM lane would fall to the
// DeepSeek-Flash fallback and misprice).
var knownModels = map[string][]string{
	ProviderDeepSeek: {deepseek.ModelV41Flash},
	ProviderGLM:      {pricing.ModelGLM53},
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
		// Model-only call: infer the lane from the id. A model that
		// belongs to no lane is an error (not a parent-model guess) —
		// the caller named a model seek can't route.
		for p, models := range knownModels {
			for _, m := range models {
				if m == model {
					return Route{Provider: p, Model: m}, nil
				}
			}
		}
		return Route{}, fmt.Errorf("unknown model %q — known: %s", model, allKnownModels())
	}
	models, ok := knownModels[provider]
	if !ok {
		return Route{}, fmt.Errorf("unknown provider %q — valid: deepseek, glm", provider)
	}
	if model == "" {
		return Route{Provider: provider, Model: models[0]}, nil
	}
	for _, m := range models {
		if m == model {
			return Route{Provider: provider, Model: m}, nil
		}
	}
	return Route{}, fmt.Errorf("model %q does not belong to provider %q — known: %s",
		model, provider, joinModels(models))
}

// allKnownModels renders every known model id across lanes for error
// messages ("glm-5.3, deepseek-flash").
func allKnownModels() string {
	var out string
	first := true
	for _, models := range knownModels {
		for _, m := range models {
			if !first {
				out += ", "
			}
			out += m
			first = false
		}
	}
	return out
}

func joinModels(models []string) string {
	out := ""
	for i, m := range models {
		if i > 0 {
			out += ", "
		}
		out += m
	}
	return out
}

// deepSeekOnlyTools lists tool names that only work against the
// first-class DeepSeek client (D8). `think` reads DeepSeek's reasoner
// semantics; `fim_complete` hits the DeepSeek FIM endpoint. A child
// routed to any llm.Provider lane must not see them in its Registry —
// the call would fail at runtime at best and mislead the model at
// worst. Filtered by the Runner (which knows the child's effective
// backend), AFTER the template Filter.
var deepSeekOnlyTools = map[string]bool{
	"think":        true,
	"fim_complete": true,
}

// IsDeepSeekOnlyTool reports whether a tool name requires the
// first-class DeepSeek client. Exported for the Runner's provider
// compatibility filter (D8) — see buildSubagentRunner in cmd/seek.
func IsDeepSeekOnlyTool(name string) bool { return deepSeekOnlyTools[name] }
