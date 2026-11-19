// Package budget exposes per-model context-window limits and the
// thresholds at which seek's UI surfaces a "you're about to hit the
// limit" warning.
//
// We deliberately keep these as hard-coded constants — vendor docs
// can change, but a stale table failing safe (under-reporting capacity)
// is much better than a runtime probe that could leak the OSC-11 / TTY
// detection class of problems we already fought once (docs/pitfalls.md
// §"Garbage `]11;rgb…").
package budget

// contextLimits is the input+context size (in tokens) for the models
// seek can talk to. Values reflect what each provider advertises as of
// 2026-05; bump them explicitly when the vendor publishes a new limit
// (no auto-discovery — see the package comment for why).
//
// DeepSeek V4 (Jan 2026 launch) ships with a 1M context for both
// flash and pro. The legacy `deepseek-chat` / `deepseek-reasoner`
// aliases were removed server-side on 2026-07-24; old session files
// carrying those names now hit the Default fallback below.
var contextLimits = map[string]int{
	// DeepSeek — V4.1 Flash is the one live id (1M context since the
	// V4 launch). The retired V4 ids are unknown here and hit the
	// Default fallback below; the legacy deepseek-chat /
	// deepseek-reasoner aliases (removed 2026-07-24) do too.
	"deepseek-flash": 1_000_000,
	// M6 additions (already declared so the surface doesn't shift
	// when the second-tier providers light up).
	"claude-3-5-sonnet-20241022": 200_000,
	"claude-sonnet-4-20250514":   200_000,
	"gpt-4o":                     128_000,
	"gpt-4o-mini":                128_000,
	"gemini-2.0-flash":           1_000_000,
	"gemini-1.5-pro":             2_000_000,
	// GLM lane (per-agent routing, feature-orchestration.md D9):
	// glm-5.3 on open.bigmodel.cn ships a single 1M-context tier —
	// same source as the pricing table's GLM row.
	"glm-5.3": 1_000_000,
	// OpenCode Go lane (--provider=opencode): the same DeepSeek V4.1
	// Flash as the entry above, addressed by the gateway's own id
	// string (1M context). The rest of the Go pool speaks
	// Chat-Completions too and is offered in the lane's model menus
	// (pricing.OpenCodeGoModels) — each gets its own limit here, taken
	// from the same GET /v1/models `limit.context` field (checked
	// 2026-11-18). Missing entries would hit Default (128K) and nag
	// /compact at an eighth of the real window.
	"deepseek-v4.1-flash": 1_000_000,
	"deepseek-v4-pro":     1_000_000,
	"glm-5.2":             1_000_000,
	"glm-5.3-flash":       1_000_000,
	"kimi-k3":             1_048_576,
	"kimi-k3.1-pro":       1_048_576,
	"kimi-k2.7-code":      262_144,
	"minimax-m3.1-flash":  1_000_000,
	"mimo-v2.6-flash":     1_048_576,
	"mimo-v2.6-pro":       1_048_576,
	"longcat-2.0":         1_000_000,
	"hy4":                 1_048_576,
	"hy4-preview":         1_024_000,
	"hy3":                 256_000,
	"step-5-preview":      1_000_000,
	// Free (time-limited) pool rows — same source, same field.
	"beam-free":               1_048_576,
	"hy4-flash-free":          1_048_576,
	"longcat-2.5-free":        1_000_000,
	"iceman-contributor-free": 262_144,
}

// Default is returned when an unknown model is queried. Since most
// modern coding models now sit in the 100K+ range, a conservative
// default of 128K avoids spurious warnings for models we haven't
// catalogued yet, while still firing the budget warning before a
// genuinely small model overflows.
const Default = 128_000

// Limit returns the context window size for a model. Unknown models
// fall back to Default.
func Limit(model string) int {
	if v, ok := contextLimits[model]; ok {
		return v
	}
	return Default
}

// Severity describes how worried seek should be about a given token
// usage relative to the model's context window.
type Severity int

const (
	SeverityOK Severity = iota
	SeverityWarn
	SeverityCritical
)

// Thresholds (as fractions of the limit).
//
//	OK       → 0.00–0.60  no warning
//	Warn     → 0.60–0.75  status bar tinted; user notices
//	Critical → ≥0.75      explicit "consider /compact" prompt
//
// Lowered from 0.80/0.95 → 0.60/0.75 so the /compact nudge fires
// early enough to actually act on: with a 1M context model, 95% is
// 950K tokens — by then the summary call itself costs real money and
// the model quality is already degraded by the huge context.
const (
	WarnFraction     = 0.60
	CriticalFraction = 0.75
)

// Classify returns the severity for usedTokens against the given
// model's limit.
func Classify(model string, usedTokens int) Severity {
	limit := Limit(model)
	if limit <= 0 {
		return SeverityOK
	}
	frac := float64(usedTokens) / float64(limit)
	switch {
	case frac >= CriticalFraction:
		return SeverityCritical
	case frac >= WarnFraction:
		return SeverityWarn
	default:
		return SeverityOK
	}
}

// Fraction returns the raw usedTokens/limit ratio, clipped to [0,1+]
// (callers shouldn't depend on it being capped — overshoot is a
// signal).
func Fraction(model string, usedTokens int) float64 {
	limit := Limit(model)
	if limit <= 0 {
		return 0
	}
	return float64(usedTokens) / float64(limit)
}
