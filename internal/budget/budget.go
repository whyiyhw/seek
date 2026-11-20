// Package budget exposes per-model context-window limits and the
// thresholds at which seek's UI surfaces a "you're about to hit the
// limit" warning.
//
// The table is hard-coded — vendor docs can change, but a stale table
// failing safe (under-reporting capacity) is much better than a runtime
// probe that could leak the OSC-11 / TTY detection class of problems we
// already fought once (docs/pitfalls.md §"Garbage `]11;rgb…").
//
// The one sanctioned exception is SetLimits: the OpenCode Go lane
// publishes its whole catalogue — including `limit.context` — at a
// documented HTTPS endpoint (GET /v1/models), so internal/opencatalog
// refreshes that table at session start and installs the result here.
// The compiled entries stay as the offline baseline, and a failed refresh
// simply leaves them in place. Anything that needs a TTY probe still has
// no business here.
package budget

import "sync"

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

// overrides, when non-nil, wins over contextLimits. Installed once per
// session by the OpenCode catalogue refresh (internal/opencatalog), so a
// newly-listed model gets its real window instead of Default. Guarded
// because Limit runs on the status-bar render path while a background
// refresh may be installing the new table.
var (
	overrideMu sync.RWMutex
	overrides  map[string]int
)

// SetLimits installs a runtime limit override; nil clears it. Sanctioned
// caller: internal/opencatalog (see the package comment). Values <= 0 are
// dropped — a zero window would make Classify divide by zero.
func SetLimits(m map[string]int) {
	clean := make(map[string]int, len(m))
	for id, v := range m {
		if v > 0 {
			clean[id] = v
		}
	}
	overrideMu.Lock()
	defer overrideMu.Unlock()
	if len(clean) == 0 {
		overrides = nil
		return
	}
	overrides = clean
}

// Known returns the explicit window for model, if any (override first,
// then the compiled table), WITHOUT the Default fallback. Callers that
// must tell "catalogued" from "guessed" — the catalogue merge, mostly —
// use this instead of comparing against Default, which would misread a
// model that genuinely has a 128K window.
func Known(model string) (int, bool) {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	if v, ok := overrides[model]; ok {
		return v, true
	}
	v, ok := contextLimits[model]
	return v, ok
}

// Limit returns the context window size for a model. Unknown models
// fall back to Default.
func Limit(model string) int {
	if v, ok := Known(model); ok {
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

// warnRatioOfCritical keeps the amber tint in proportion to whatever
// critical point is in force. With the compiled defaults it reproduces
// WarnFraction/CriticalFraction (0.60/0.75) exactly; with a configured
// auto-compact trigger it puts the warning at 80% of the point where
// compaction actually happens, instead of leaving the bar mute until
// the trigger fires. TestClassifyAt_MatchesCompiledFractions pins the
// two paths together.
const warnRatioOfCritical = 0.8

// AutoCompactAt resolves the auto-compact trigger in TOKENS for a
// model. An explicit token count wins — it is model-independent, which
// is what you want when you know your working set ("compact at 350k"
// regardless of which lane is live). Otherwise the fraction scales the
// model's CURRENT window, so a /model switch moves the trigger with
// it. A non-positive result means "disabled".
//
// The fraction is deliberately not clamped: > 1 yields a trigger above
// the window, which simply never fires (fail-safe — a typo must not
// cause compaction storms).
func AutoCompactAt(model string, tokens int, fraction float64) int {
	if tokens > 0 {
		return tokens
	}
	if fraction <= 0 {
		return 0
	}
	limit := Limit(model)
	if limit <= 0 {
		return 0
	}
	return int(float64(limit) * fraction)
}

// ClassifyAt is Classify with an explicit critical threshold in tokens
// — the resolved auto-compact point, so the status bar escalates where
// compaction will actually happen rather than at a compiled constant.
// criticalAt <= 0 falls back to Classify (compiled fractions), which is
// also what a disabled auto-compact gives you.
func ClassifyAt(model string, usedTokens, criticalAt int) Severity {
	if criticalAt <= 0 {
		return Classify(model, usedTokens)
	}
	switch {
	case usedTokens >= criticalAt:
		return SeverityCritical
	case float64(usedTokens) >= warnRatioOfCritical*float64(criticalAt):
		return SeverityWarn
	default:
		return SeverityOK
	}
}

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
