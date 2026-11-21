package modelcap

// Lane determination: which provider lane speaks a given model wire id.
// The web ContextMeter's lane chip (W5 card c_oyp2qf) is the first display
// consumer; the question is the same shape as Vision — "a model-level fact
// a surface needs without knowing any provider package" — so it lives here
// next to the capability table, per the AGENTS.md rule that model facts
// don't live in whichever provider package shipped them first.
//
// The wire id is the only key available at the point of display (the serve
// process never sees the child's --provider; it only sees agent_start's
// model id), so lane membership is resolved BY ID:
//
//   - shared ids first ("glm-5.3", "deepseek-v4-pro" also sit in the Go
//     pool but their rate card belongs to the first-tier lane — the same
//     tie-break internal/opencatalog's directLaneIDs makes for pricing);
//   - then the Go pool (pricing.OpenCodeGoModels — the lane's menu, the
//     one authoritative list; no second table here);
//   - then vendor prefixes for the curated second-tier defaults;
//   - anything else is unknown (""), and the display surface hides.

import "github.com/whyiyhw/seek/internal/pricing"

// laneLabels pairs each lane id (the config provider name) with the
// human-readable label display surfaces render. Chinese labels match the
// web page's language; ids stay ASCII for any future keyed consumer.
var laneLabels = []struct{ id, label, prefix string }{
	{"deepseek", "DeepSeek 直连", "deepseek-"},
	{"glm", "GLM", "glm-"},
	{"anthropic", "Anthropic", "claude-"},
	{"openai", "OpenAI", "gpt-"},
	{"gemini", "Gemini", "gemini-"},
}

// sharedFirstTier lists Go-pool ids whose rate card belongs to a
// first-tier lane (opencatalog.directLaneIDs, mirrored here because that
// map is unexported and about pricing installs, not display). They resolve
// to their first-tier lane even though the pool also carries them.
var sharedFirstTier = map[string]string{
	"glm-5.3":         "glm",
	"deepseek-v4-pro": "deepseek",
}

// goPoolIDs is the OpenCode Go lane's menu, indexed once at init.
var goPoolIDs = func() map[string]bool {
	m := make(map[string]bool, len(pricing.OpenCodeGoModels))
	for _, row := range pricing.OpenCodeGoModels {
		m[row.ID] = true
	}
	return m
}()

// LaneOf reports the provider lane speaking model's wire id and its
// display label. Both are "" for an unknown id (or empty model) — callers
// hide the lane surface rather than guess.
func LaneOf(model string) (lane, label string) {
	if model == "" {
		return "", ""
	}
	if id, ok := sharedFirstTier[model]; ok {
		return id, laneLabel(id)
	}
	if goPoolIDs[model] {
		return "opencode", "OpenCode 网关"
	}
	for _, l := range laneLabels {
		if len(model) > len(l.prefix) && model[:len(l.prefix)] == l.prefix {
			return l.id, l.label
		}
	}
	return "", ""
}

// laneLabel resolves a lane id to its display label ("" when unknown).
func laneLabel(id string) string {
	for _, l := range laneLabels {
		if l.id == id {
			return l.label
		}
	}
	return ""
}
