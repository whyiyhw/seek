// Package pricing models DeepSeek's tiered pricing and off-peak window.
// Embedded rates are accurate as of 2025-05; bump them with each release
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

// standardRates is the price card for each supported model during the
// standard pricing window. Off-peak is derived via offPeakDiscount.
var standardRates = map[string]ModelPricing{
	deepseek.ModelChat: {
		InputMissPerMTok: 0.27,
		InputHitPerMTok:  0.014,
		OutputPerMTok:    1.10,
	},
	deepseek.ModelReasoner: {
		InputMissPerMTok: 0.55,
		InputHitPerMTok:  0.14,
		OutputPerMTok:    2.19,
	},
}

const offPeakDiscount = 0.5

// Shanghai (CST = UTC+8) is DeepSeek's reference timezone. Exported so
// tests can construct local times consistently.
var Shanghai = time.FixedZone("CST", 8*60*60)

// Off-peak window: [00:30, 08:30) Beijing time.
const (
	offPeakStartMins = 0*60 + 30
	offPeakEndMins   = 8*60 + 30
)

// CurrentTier reports the pricing tier in effect at the given instant.
// Pass time.Now() in production; tests pass a fixed instant.
func CurrentTier(now time.Time) Tier {
	b := now.In(Shanghai)
	mins := b.Hour()*60 + b.Minute()
	if mins >= offPeakStartMins && mins < offPeakEndMins {
		return TierOffPeak
	}
	return TierStandard
}

// PricingFor returns the per-token rate card for a model+tier. Unknown
// models fall back to deepseek-chat rates.
func PricingFor(model string, tier Tier) ModelPricing {
	p, ok := standardRates[model]
	if !ok {
		p = standardRates[deepseek.ModelChat]
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
		return "standard"
	}
}

// NextTransition returns the next tier change and the wall-clock at
// which it begins. Use this to power "wait for off-peak?" prompts.
//
// Examples (all Beijing time):
//   at 09:00 standard → (off-peak, tomorrow 00:30)
//   at 00:15 standard → (off-peak, today 00:30)
//   at 03:00 off-peak → (standard, today 08:30)
func NextTransition(now time.Time) (Tier, time.Time) {
	b := now.In(Shanghai)
	today0030 := time.Date(b.Year(), b.Month(), b.Day(), 0, 30, 0, 0, Shanghai)
	today0830 := time.Date(b.Year(), b.Month(), b.Day(), 8, 30, 0, 0, Shanghai)

	switch CurrentTier(now) {
	case TierOffPeak:
		return TierStandard, today0830
	default: // TierStandard
		// We're either in [00:00, 00:30) or [08:30, 24:00).
		if b.Hour() == 0 && b.Minute() < 30 {
			return TierOffPeak, today0030
		}
		return TierOffPeak, today0030.AddDate(0, 0, 1)
	}
}

// FormatCost renders a USD amount as a compact "$0.0123" string (4
// decimal places — DeepSeek's per-call costs are routinely sub-cent).
func FormatCost(usd float64) string {
	return fmt.Sprintf("$%.4f", usd)
}
