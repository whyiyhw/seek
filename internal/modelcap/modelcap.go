// Package modelcap answers capability questions about a model id —
// "does this model accept image input" (Vision below) and "which
// provider lane speaks this id" (lane.go's LaneOf).
//
// Why it exists: the fact used to live in pkg/deepseek as
// IsVisionModel, a one-id allow-list ("deepseek-flash", the direct-API
// lane). Every call site (the submit-time image router shared by
// TUI/print/ACP, and both send paths' history gates) asked that function
// without knowing the provider lane, so every second-tier lane —
// OpenCode Go's deepseek-v4.1-flash above all — was told "no vision" and
// the user's pasted screenshot was silently replaced by a switch-model
// note. The question is not DeepSeek-specific, and pkg/deepseek must
// stay dependency-free (it cannot import internal/), hence this package.
//
// Two layers, the same shape internal/pricing and internal/budget use:
//
//  1. **Static table** (visionModels): the compiled floor, covering the
//     default model of every lane seek ships so an offline binary still
//     answers correctly.
//  2. **Runtime grants** (SetVision): installed once per session by
//     internal/opencatalog from the OpenCode Go lane's live catalogue,
//     whose per-model `modalities.input` is authoritative for ids the
//     static table has never heard of.
//
// Grants are ADDITIVE by design — there is no way to revoke a model's
// vision. A false can't be told apart from a missing field (a catalogue
// cache written by an older binary has no vision key at all), so
// "revoke everything the fresh list doesn't mention" would demote
// working models on the first start after an upgrade. The static table
// is the deny-by-default floor; the catalogue may only promote.
//
// Unknown ids are NOT vision: sending an image to a model that answers
// 400 costs the whole turn, while the in-band note costs a line of
// prose. That asymmetry is the one the original allow-list chose.
package modelcap

import "sync"

// visionModels is the static capability table. Explicit ids only, never
// substring matching — vendors rotate "-exp"/"-vision" suffixes and a
// pattern would silently drift into a 400 (the lesson recorded in
// pkg/deepseek/types.go for the retired alias ids).
var visionModels = map[string]bool{
	// DeepSeek: V4.1 Flash made vision native on the default model
	// (2026-09-10). Retired ids (-flash-vision-exp and friends) are
	// plain unknowns and stay absent.
	"deepseek-flash": true,
	// The same model addressed on the OpenCode Go gateway — a different
	// wire id string, hence its own row (pricing.ModelOpenCodeV41Flash).
	"deepseek-v4.1-flash": true,
	// The second-tier lanes' curated defaults and their picker siblings
	// (internal/tui commands.go knownModelsForProvider, cmd/seek
	// acpLaneModels).
	"claude-sonnet-4-20250514":   true,
	"claude-3-5-sonnet-20241022": true,
	"gpt-4o":                     true,
	"gpt-4o-mini":                true,
	"gemini-2.0-flash":           true,
	"gemini-1.5-pro":             true,
}

var (
	grantMu sync.RWMutex
	grants  map[string]bool // model id → accepts images (promotions only)
)

// SetVision installs the runtime vision grants, replacing any previous
// set; nil clears them back to the static table. Sanctioned caller:
// internal/opencatalog (the OpenCode Go catalogue refresh), same
// contract as pricing.SetModelRates and budget.SetLimits.
//
// Only true entries are meaningful — a grant, never a revocation (see
// the package comment). Guarded because a background refresh writes
// while per-turn submit/send gates read.
func SetVision(m map[string]bool) {
	clean := make(map[string]bool, len(m))
	for id, ok := range m {
		if id != "" && ok {
			clean[id] = true
		}
	}
	grantMu.Lock()
	if len(clean) == 0 {
		grants = nil
	} else {
		grants = clean
	}
	grantMu.Unlock()
}

// Vision reports whether model natively accepts image content parts.
// A grant wins over the static table; an id in neither is not vision.
func Vision(model string) bool {
	if model == "" {
		return false
	}
	grantMu.RLock()
	granted := grants[model]
	grantMu.RUnlock()
	return granted || visionModels[model]
}
