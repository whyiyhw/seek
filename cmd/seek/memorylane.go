package main

// Background memory lane (c_eha42g): the write-side memory pipeline —
// auto-dream, memory distill / memory_observe, skill distill (auto +
// /distill), and the -dream CLI — bills the OpenDesign subscription
// gateway by default instead of the first-class DeepSeek per-token
// key, so background consolidation never burns api.deepseek.com
// unnoticed while the chat lane rides a subscription. Same standing
// rule as the autopilot cron lane pins (c_kdblc4): unattended
// background work rides a request-window subscription lane unless
// someone explicitly repins it.
//
// Resolution precedence, mirroring the glm/opencode/opendesign
// endpoint precedent (env > config > compiled default):
//
//	base  SEEK_MEMORY_BASE_URL > providers.memory.base_url > opendesign gateway
//	key   —                     > providers.memory.api_key  > providers.opendesign.api_key
//	model SEEK_MEMORY_MODEL     > —                         > deepseek-v4.1-flash-eco
//
// The default model is (a) an id the gateway actually serves (GET
// /v1/models, verified 2026-11-26) and (b) NOT
// deepseek.ModelV41Flash, so ShouldEnableThinking is false and the
// wire stays a plain OpenAI chat-completions POST — no thinking field
// for the gateway to weigh. The pipeline keeps working on every
// interactive chat lane (the client no longer piggybacks on dsClient,
// which only exists when the chat lane IS deepseek).
//
// ACP sessions are deliberately excluded (acpLane): bot/embedded
// sessions must not distill traits into the user's soul.md. They ran
// write-silent on every non-deepseek lane before this lane existed —
// keeping them silent is the point, not an accident.

import (
	"os"
	"strings"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	// memoryLaneDefaultModel is the gateway-served id the pipeline
	// addresses by default. Cheap tier on purpose: dream/distill are
	// background consolidation, not interactive quality surface.
	memoryLaneDefaultModel = "deepseek-v4.1-flash-eco"

	// memoryLaneBaseURLEnv / memoryLaneModelEnv are the escape hatches,
	// same shape as OPENDESIGN_BASE_URL et al. The API key has no
	// dedicated env: config.KeyFor already reads the canonical env for
	// the resolved provider name, and providers.memory.api_key covers
	// the "pin the pipeline elsewhere" case.
	memoryLaneBaseURLEnv = "SEEK_MEMORY_BASE_URL"
	memoryLaneModelEnv   = "SEEK_MEMORY_MODEL"
)

// memoryLaneClient resolves the background memory client. nil means
// "no write-side pipeline" — exactly what a nil dsClient meant at
// these call sites before, so every existing nil-guard keeps its
// meaning.
func memoryLaneClient(cfg config.Config, acpLane bool) *deepseek.Client {
	if acpLane {
		return nil
	}
	key := config.KeyFor(cfg, "memory")
	if key == "" {
		key = config.KeyFor(cfg, "opendesign")
	}
	if key == "" {
		return nil
	}
	return deepseek.New(
		deepseek.WithAPIKey(key),
		deepseek.WithBaseURL(memoryLaneBaseURL(cfg)),
	)
}

// memoryLaneModel resolves the model id the pipeline addresses.
func memoryLaneModel() string {
	if v := strings.TrimSpace(os.Getenv(memoryLaneModelEnv)); v != "" {
		return v
	}
	return memoryLaneDefaultModel
}

// memoryLaneBaseURL resolves the pipeline's endpoint. pkg/deepseek
// appends "/chat/completions" itself, while the opendesign lane's
// endpoint resolver returns the FULL POST URL for the compatible
// adapter — trim the suffix when inheriting, so a
// providers.opendesign.base_url override applies to both lanes with
// one config line.
func memoryLaneBaseURL(cfg config.Config) string {
	if v := strings.TrimSpace(os.Getenv(memoryLaneBaseURLEnv)); v != "" {
		return v
	}
	if v := config.BaseURLFor(cfg, "memory"); v != "" {
		return v
	}
	return strings.TrimSuffix(opendesignEndpoint(cfg, ""), "/chat/completions")
}
