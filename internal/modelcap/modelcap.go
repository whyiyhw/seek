// Package modelcap answers capability questions about a model id —
// today "does this model accept image input" (Vision), "does it
// accept document file input" (Files), and "which provider lane
// speaks this id" (lane.go's LaneOf).
//
// Why it exists: the vision fact used to live in pkg/deepseek as
// IsVisionModel, a one-id allow-list ("deepseek-flash", the direct-API
// lane). Every call site (the submit-time image router shared by
// TUI/print/ACP, and both send paths' history gates) asked that function
// without knowing the provider lane, so every second-tier lane —
// OpenCode Go's deepseek-v4.1-flash above all — was told "no vision" and
// the user's pasted screenshot was silently replaced by a switch-model
// note. The question is not DeepSeek-specific, and pkg/deepseek must
// stay dependency-free (it cannot import internal/), hence this package.
// Files grew here for the same reason: document input is a per-model
// fact on every lane (the DeepSeek Files API lane and the three
// second-tier wire formats each have their own set), so it must not
// park inside whichever package shipped it first.
//
// Two layers per capability, the same shape internal/pricing and
// internal/budget use:
//
//  1. **Static table** (dimension.static): the compiled floor, covering
//     the default model of every lane seek ships so an offline binary
//     still answers correctly.
//  2. **Runtime grants** (dimension.set): installed once per session by
//     internal/opencatalog from the OpenCode Go lane's live catalogue,
//     whose per-model `modalities.input` is authoritative for ids the
//     static table has never heard of.
//
// Grants are ADDITIVE by design — there is no way to revoke a model's
// capability. A false can't be told apart from a missing field (a
// catalogue cache written by an older binary has no capability key at
// all), so "revoke everything the fresh list doesn't mention" would
// demote working models on the first start after an upgrade. The static
// table is the deny-by-default floor; the catalogue may only promote.
//
// Unknown ids are NOT capable: sending an attachment to a model that
// answers 400 costs the whole turn, while the in-band note costs a line
// of prose. That asymmetry is the one the original allow-list chose.
package modelcap

import "sync"

// dimension is one capability axis: a compiled static table plus the
// catalogue's additive runtime grants over it. Vision and Files are two
// instances; a third capability copies this shape, not the code around
// it (the mutex/map pair is the whole mechanism).
type dimension struct {
	static map[string]bool
	mu     sync.RWMutex
	grants map[string]bool // model id → capability granted (promotions only)
}

// set installs the runtime grants, replacing any previous set; nil
// clears them back to the static table. Only true entries are
// meaningful — a grant, never a revocation (see the package comment).
// Guarded because a background refresh writes while per-turn
// submit/send gates read.
func (d *dimension) set(m map[string]bool) {
	clean := make(map[string]bool, len(m))
	for id, ok := range m {
		if id != "" && ok {
			clean[id] = true
		}
	}
	d.mu.Lock()
	if len(clean) == 0 {
		d.grants = nil
	} else {
		d.grants = clean
	}
	d.mu.Unlock()
}

// has answers the capability question: a grant wins over the static
// table; an id in neither is not capable.
func (d *dimension) has(model string) bool {
	if model == "" {
		return false
	}
	d.mu.RLock()
	granted := d.grants[model]
	d.mu.RUnlock()
	return granted || d.static[model]
}

// visionModels is the compiled vision table. Explicit ids only, never
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
	// The OpenDesign Cloud lane uses the same id string for the same
	// model, so this row covers both.
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
	// OpenDesign Cloud lane (pricing.OpenDesignModels). Every row below
	// was probed 2026-11-24 with a real sk-amr key: a 1×1 PNG went in as
	// an `image_url` data URL and the reply described the pixel. Note
	// deepseek-v4.1-flash-eco is here DESPITE the gateway's own
	// `architecture.input_modalities` saying text-only — it answered the
	// image anyway (the same catalogue gap the OpenCode lane's plugin
	// documents: "the catalog left image out of DeepSeek V4.1 Flash Eco,
	// which takes images"). Measured beats declared when the two
	// disagree and the measurement is a 200 with a correct answer.
	"deepseek-v4.1-flash-eco": true,
	"glm-5.3-flash":           true,
	"glm-5.3-flashx":          true,
	"claude-haiku-5.5":        true,
	"gpt-6-luna":              true,
	"kimi-k2.7-code":          true,
	"minimax-m3.1-flash":      true,
	"mimo-v2.6-flash":         true,
	"mimo-v2.6-pro":           true,
}

// fileModels is the compiled document-input table — same id set as
// visionModels, because every model seek ships by default accepts
// documents on its lane: the DeepSeek ids via the Files API
// (feature-deepseek-app-api), gpt-4o/-mini via the chat-completions
// `file` content part, the Claude ids via the `document` block, the
// Gemini ids via inline document data. Note the capability says the
// MODEL accepts documents — WHICH formats each wire accepts is a
// transport fact (pkg/llm's PDF-only second-tier set), not a modelcap
// question.
var fileModels = map[string]bool{
	"deepseek-flash":             true,
	"deepseek-v4.1-flash":        true,
	"claude-sonnet-4-20250514":   true,
	"claude-3-5-sonnet-20241022": true,
	"gpt-4o":                     true,
	"gpt-4o-mini":                true,
	"gemini-2.0-flash":           true,
	"gemini-1.5-pro":             true,
	// OpenDesign Cloud lane: the ids whose gateway metadata declares the
	// `file` input modality AND which read a real PDF back coherently in
	// the 2026-11-24 probe.
	//
	// KNOWN LIMITATION on the shared id: deepseek-v4.1-flash is already
	// true above for the OpenCode lane (which does pass documents), but
	// the OpenDesign gateway silently DROPS the `file` part on that same
	// id — the probe answered "no PDF is attached" with a 200. The table
	// is keyed by wire id alone, so one lane's "yes" wins; a document
	// pasted on --provider=opendesign/deepseek-v4.1-flash therefore
	// reaches the model as nothing rather than as an in-band note. Fixing
	// it needs a lane-aware capability key, the same surface
	// pricing.ModelPricing deliberately does not have (see its
	// "display-only artifact" note). Recorded in docs/pitfalls.
	"claude-haiku-5.5": true,
	"glm-5.3-flashx":   true,
	"gpt-6-luna":       true,
}

var (
	vision = dimension{static: visionModels}
	files  = dimension{static: fileModels}
)

// SetVision installs the runtime vision grants, replacing any previous
// set; nil clears them back to the static table. Sanctioned caller:
// internal/opencatalog (the OpenCode Go catalogue refresh), same
// contract as pricing.SetModelRates and budget.SetLimits.
func SetVision(m map[string]bool) { vision.set(m) }

// Vision reports whether model natively accepts image content parts.
// A grant wins over the static table; an id in neither is not vision.
func Vision(model string) bool { return vision.has(model) }

// SetFiles installs the runtime document-input grants (same contract
// as SetVision; sanctioned caller internal/opencatalog).
func SetFiles(m map[string]bool) { files.set(m) }

// Files reports whether model natively accepts document file input on
// its lane. A grant wins over the static table; an id in neither is
// not file-capable.
func Files(model string) bool { return files.has(model) }
