// acp_lane_gate.go — the model×lane consistency check for the ACP lane
// (card c_dxrjjl).
//
// The bug it closes: an embedding host (paseo) sent the session's model
// back to a freshly spawned `seek acp` child as a model NAME only —
// `deepseek-v4.1-flash-eco`, an OpenDesign Cloud gateway wire id — while
// the child's lane fell to buildProvider's auto-detect (deepseek, the
// first key in priority order). seek has no upstream knowledge of which
// vendor serves which id, so the request went out with the eco id on the
// DeepSeek first-class client and the official API answered
//
//	invalid_request_error: The supported API model names are
//	deepseek-flash, deepseek-v4-pro, but you passed deepseek-v4.1-flash-eco
//
// Verified 2026-11-25 both ways: the same id on the OpenDesign gateway
// (amr-link.open-design.ai/v1) answers 200, and on api.deepseek.com it
// reproduces the error byte for byte. The model never went away — the
// request was routed to the wrong lane, which is a fact seek already
// knows from the wire id (modelcap.LaneOf, the single lane authority).
// What was missing was anybody asking.
//
// Posture: refuse loudly at startup, never silently re-lane and never
// hand a mismatched id to an upstream that will 400. Same philosophy as
// agent.go's provider routing ("the named provider must have its key
// configured … never silently falls back") — a startup failure the host
// can read and fix beats a mid-conversation 400 the model can't.
//
// Callers: run() on the `seek acp` lane, AFTER the SEEK_ACP_PROVIDER /
// SEEK_ACP_MODEL pins land and BEFORE buildProvider builds anything, so
// the check sees the same auto-detect outcome the lane will actually get.
package main

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/modelcap"
)

// acpStartupLaneGate rejects a startup pair whose model id belongs to a
// subscription-gateway lane that the effective provider lane does not
// carry. Returns nil when the pair is consistent, when the model id is
// unknown (freeform ids on a compatible/self-hosted endpoint are the
// operator's namespace — seek cannot know them and must not guess), or
// when the model is only a zero-model pin that will be replaced by the
// lane's own default.
//
// provFlag may be "" (auto-detect: resolveProviderName runs the same
// priority chain buildProvider uses, which is why that helper was
// extracted — two copies would let the gate and the built lane disagree).
func acpStartupLaneGate(model, provFlag string, cfg config.Config) error {
	if model == "" {
		// No pin: buildProvider's modelDefault is the lane's own default,
		// so the pair is consistent by construction.
		return nil
	}
	lane, label := modelcap.LaneOf(model)
	if lane == "" {
		// Unknown id. This is the documented escape hatch: a
		// --provider=compatible endpoint names its own models, and a
		// self-hosted mirror may reuse any string it likes.
		return nil
	}
	if lane != modelcap.LaneOpenCode && lane != modelcap.LaneOpenDesign {
		// First-tier lane (deepseek / glm / anthropic / openai / gemini).
		// Those lanes accept ids seek knows nothing about (the GLM lane
		// serves more than the one curated default), so a first-tier
		// resolution is not evidence of a mismatch worth refusing.
		return nil
	}
	provider := resolveProviderName(provFlag, cfg)
	if provider == lane || modelcap.Serves(provider, model) {
		// Same lane, or the other subscription gateway whose menu also
		// carries the id (both pool deepseek-v4.1-flash, deepseek-v4-pro,
		// kimi-k2.7-code, …). LaneOf's tie-break names the OLDER claim,
		// so a --provider=opendesign session on a shared id must pass.
		return nil
	}
	if provider == "compatible" {
		// The compatible lane IS an operator-declared endpoint: it has no
		// menu to check and no vendor identity, so its model ids are its
		// own namespace (same reasoning as the unknown-id branch above).
		return nil
	}
	if familyLane(model) == provider {
		// A gateway-menu id that also carries the provider's own vendor
		// prefix (glm-5.3-flash on the GLM lane, claude-haiku-5.5 on the
		// Anthropic lane) is plausibly served by that first tier — seek
		// has no per-vendor model list to prove it either way. Tolerated
		// deliberately; deepseek- ids are NOT in this set because the
		// official DeepSeek API publishes a closed two-id list, which is
		// exactly the failure this gate exists for.
		return nil
	}
	keyVar := "its API key"
	switch lane {
	case modelcap.LaneOpenDesign:
		keyVar = "OPENDESIGN_API_KEY"
	case modelcap.LaneOpenCode:
		keyVar = "OPENCODE_API_KEY"
	}
	return fmt.Errorf(
		"acp startup: model %q belongs to lane %q (%s) but this session would start on lane %q — "+
			"set SEEK_ACP_PROVIDER=%s (plus %s) so the request reaches the gateway that serves it. "+
			"A gateway wire id sent to another lane answers an upstream 400 that nobody can act on mid-conversation, "+
			"so seek refuses the launch instead of re-laning silently",
		model, lane, label, provider, lane, keyVar)
}

// familyLane maps a model id's vendor prefix to the first-tier lane that
// plausibly serves it. Only prefixes whose first-tier lane ships a real
// implementation are listed — "deepseek-" is deliberately absent: the
// official API's accepted names are a closed set (deepseek-flash,
// deepseek-v4-pro), so a deepseek- prefixed gateway id on that lane is
// the mismatch, not a family match.
func familyLane(model string) string {
	switch {
	case strings.HasPrefix(model, "glm-"):
		return "glm"
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"):
		return "openai"
	case strings.HasPrefix(model, "gemini-"):
		return "gemini"
	}
	return ""
}
