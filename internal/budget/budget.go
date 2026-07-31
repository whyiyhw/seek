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
	// DeepSeek V4 — current lineup.
	"deepseek-v4-flash": 1_000_000,
	"deepseek-v4-pro":   1_000_000,
	// M6 additions (already declared so the surface doesn't shift
	// when the second-tier providers light up).
	"claude-3-5-sonnet-20241022": 200_000,
	"claude-sonnet-4-20250514":   200_000,
	"gpt-4o":                     128_000,
	"gpt-4o-mini":                128_000,
	"gemini-2.0-flash":           1_000_000,
	"gemini-1.5-pro":             2_000_000,
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
