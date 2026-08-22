// Package pricing models DeepSeek's peak/off-peak pricing windows.
// Embedded rates are the full (peak) rate card effective 2026-08-16
// 16:00 UTC (verified against api-docs.deepseek.com/quick_start/pricing);
// off-peak is exactly half the peak rate. Bump them with each release
// rather than fetching at runtime — a price-list HTTP call adds a failure
// mode for an exclusively defensive feature (PRD §4.8.4).
//
// All times use Asia/Shanghai (UTC+8, no DST). We hardcode the offset to
// avoid depending on the system tzdata, which is often missing on minimal
// Linux containers.
package pricing

import (
	"fmt"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tier discriminates DeepSeek's pricing windows.
type Tier int

const (
	TierStandard Tier = iota
	TierOffPeak
)

// ModelPricing is a single rate card. All rates are USD per 1M tokens.
type ModelPricing struct {
	InputMissPerMTok float64 // cache miss
	InputHitPerMTok  float64 // cache hit (~5–10% of miss for DeepSeek)
	OutputPerMTok    float64
}

// standardRates is the full-rate card per model during DeepSeek's peak
// windows (a.k.a. the "standard" tier). Off-peak is derived via
// offPeakDiscount.
//
// Numbers track DeepSeek's V4 peak/off-peak pricing, effective
// 2026-08-16 16:00 UTC (api-docs.deepseek.com/quick_start/pricing,
// checked 2026-08-13 against V4-Flash-0731 / V4-Pro-0813):
//
//	V4-Flash: $0.44 miss · $0.014 hit · $1.32 output (peak, per 1M tokens)
//	V4-Pro:   $1.32 miss · $0.044 hit · $3.96 output
//
// Off-peak is exactly half of these. The pre-2026-08-16 promotional
// rates ($0.14/$0.0028/$0.28 flash, $0.435/$0.003625/$0.87 pro) are
// retired; the legacy deepseek-chat / deepseek-reasoner aliases were
// removed server-side on 2026-07-24, and unknown model names fall back
// to V4-Flash rates via the fallback path below.
var standardRates = map[string]ModelPricing{
	deepseek.ModelV4Flash: {
		InputMissPerMTok: 0.44,
		InputHitPerMTok:  0.014,
		OutputPerMTok:    1.32,
	},
	deepseek.ModelV4Pro: {
		InputMissPerMTok: 1.32,
		InputHitPerMTok:  0.044,
		OutputPerMTok:    3.96,
	},
	// Vision-exp bills at Flash rates; each image normalises to ≤384
	// prompt tokens regardless of original size, so image cost flows
	// through the existing prompt-token accounting untouched
	// (feature-vision §四).
	deepseek.ModelV4FlashVisionExp: {
		InputMissPerMTok: 0.44,
		InputHitPerMTok:  0.014,
		OutputPerMTok:    1.32,
	},
}

const offPeakDiscount = 0.5

// Shanghai (CST = UTC+8) is DeepSeek's reference timezone. Exported so
// tests can construct local times consistently.
var Shanghai = time.FixedZone("CST", 8*60*60)

// Peak windows (Beijing time): 09:00–12:00 and 14:00–18:00, matching
// DeepSeek's published peak hours of 01:00–04:00 and 06:00–10:00 UTC.
// Everything else is off-peak (half price).
const (
	peak1StartMins = 9 * 60
	peak1EndMins   = 12 * 60
	peak2StartMins = 14 * 60
	peak2EndMins   = 18 * 60
)

// CurrentTier reports the pricing tier in effect at the given instant.
// Pass time.Now() in production; tests pass a fixed instant.
func CurrentTier(now time.Time) Tier {
	b := now.In(Shanghai)
	mins := b.Hour()*60 + b.Minute()
	if (mins >= peak1StartMins && mins < peak1EndMins) ||
		(mins >= peak2StartMins && mins < peak2EndMins) {
		return TierStandard
	}
	return TierOffPeak
}

// PricingFor returns the per-token rate card for a model+tier. Unknown
// models fall back to V4-Flash rates (the same card the retired
// deepseek-chat alias used to map to).
func PricingFor(model string, tier Tier) ModelPricing {
	p, ok := standardRates[model]
	if !ok {
		p = standardRates[deepseek.ModelV4Flash]
	}
	if tier == TierOffPeak {
		p.InputMissPerMTok *= offPeakDiscount
		p.InputHitPerMTok *= offPeakDiscount
		p.OutputPerMTok *= offPeakDiscount
	}
	return p
}

// Cost returns the USD cost of running one Usage block at the given
// tier. Cache-hit input tokens are accounted at the (much cheaper) hit
// rate; this is where seek's prefix-cache optimisation shows up.
func Cost(model string, tier Tier, u deepseek.Usage) float64 {
	p := PricingFor(model, tier)
	const million = 1_000_000.0
	return float64(u.PromptCacheMissTokens)*p.InputMissPerMTok/million +
		float64(u.PromptCacheHitTokens)*p.InputHitPerMTok/million +
		float64(u.CompletionTokens)*p.OutputPerMTok/million
}

// TierLabel is a short display string.
func TierLabel(t Tier) string {
	switch t {
	case TierOffPeak:
		return "off-peak -50%"
	default:
		return "peak"
	}
}

// NextTransition returns the next tier change and the wall-clock at
// which it begins. Use this to power "wait for off-peak?" prompts.
//
// Examples (all Beijing time):
//
//	at 09:00 peak     → (off-peak, today 12:00)
//	at 13:00 off-peak → (peak, today 14:00)
//	at 03:00 off-peak → (peak, today 09:00)
//	at 23:59 off-peak → (peak, tomorrow 09:00)
func NextTransition(now time.Time) (Tier, time.Time) {
	b := now.In(Shanghai)
	at := func(hour, min int) time.Time {
		return time.Date(b.Year(), b.Month(), b.Day(), hour, min, 0, 0, Shanghai)
	}
	mins := b.Hour()*60 + b.Minute()

	switch {
	case mins < peak1StartMins: // [00:00, 09:00) off-peak → peak 1
		return TierStandard, at(9, 0)
	case mins < peak1EndMins: // [09:00, 12:00) peak 1 → off-peak
		return TierOffPeak, at(12, 0)
	case mins < peak2StartMins: // [12:00, 14:00) off-peak → peak 2
		return TierStandard, at(14, 0)
	case mins < peak2EndMins: // [14:00, 18:00) peak 2 → off-peak
		return TierOffPeak, at(18, 0)
	default: // [18:00, 24:00) off-peak → peak 1 tomorrow
		return TierStandard, at(9, 0).AddDate(0, 0, 1)
	}
}

// FormatCost renders a USD amount as a compact "$0.0123" string (4
// decimal places — DeepSeek's per-call costs are routinely sub-cent).
func FormatCost(usd float64) string {
	return fmt.Sprintf("$%.4f", usd)
}
