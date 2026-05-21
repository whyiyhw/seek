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
// 2026-05; they should be bumped explicitly when the vendor publishes
// a new limit, not auto-discovered.
var contextLimits = map[string]int{
	"deepseek-chat":     65_536,
	"deepseek-reasoner": 65_536,
	// M6 additions (already declared so the surface doesn't shift
	// when the second-tier providers light up).
	"claude-3-5-sonnet-20241022": 200_000,
	"claude-sonnet-4-20250514":   200_000,
	"gpt-4o":                     128_000,
	"gpt-4o-mini":                128_000,
	"gemini-2.0-flash":           1_000_000,
	"gemini-1.5-pro":             2_000_000,
}

// Default is returned when an unknown model is queried. Picked
// conservatively so the warning fires sooner rather than later.
const Default = 65_536

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
//	OK       → 0.00–0.80  no warning
//	Warn     → 0.80–0.95  status bar tinted; user notices
//	Critical → ≥0.95      explicit "consider /compact" prompt
//
// 0.80 is conservative on purpose — long-running coding sessions with
// big tool results can spike fast.
const (
	WarnFraction     = 0.80
	CriticalFraction = 0.95
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
