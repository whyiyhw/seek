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
// Numbers track DeepSeek's V4.1 peak/off-peak pricing, effective
// 2026-09-10 12:00 CST (api-docs.deepseek.com/quick_start/pricing,
// checked 2026-09-10 against DeepSeek-V4.1-Flash):
//
//	V4.1-Flash: $0.30 miss · $0.006 hit · $1.20 output (peak, per 1M tokens)
//
// Off-peak is exactly half of these. deepseek-flash is the only live
// id; the retired V4 ids (deepseek-v4-flash / -pro /
// -flash-vision-exp) are server-routed to V4.1 Flash and billed at
// Flash prices, which is exactly what the unknown-model fallback
// below charges them — no per-id entries needed. The legacy
// deepseek-chat / deepseek-reasoner aliases were removed server-side
// on 2026-07-24.
var standardRates = map[string]ModelPricing{
	deepseek.ModelV41Flash: {
		InputMissPerMTok: 0.30,
		InputHitPerMTok:  0.006,
		OutputPerMTok:    1.20,
	},
}

const offPeakDiscount = 0.5

// Shanghai (CST = UTC+8) is DeepSeek's reference timezone. Exported so
// tests can construct local times consistently.
var Shanghai = time.FixedZone("CST", 8*60*60)

// Peak windows (Beijing time): 09:00–12:00 and 14:00–18:00, MONDAY
// through FRIDAY, matching DeepSeek's published peak hours of
// 01:00–04:00 and 06:00–10:00 UTC on weekdays (stated on the
// 2026-09-10 pricing page). Everything else — evenings, mornings,
// weekends — is off-peak (half price).
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
	if wd := b.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return TierOffPeak
	}
	mins := b.Hour()*60 + b.Minute()
	if (mins >= peak1StartMins && mins < peak1EndMins) ||
		(mins >= peak2StartMins && mins < peak2EndMins) {
		return TierStandard
	}
	return TierOffPeak
}

// PricingFor returns the per-token rate card for a model+tier. Unknown
// models fall back to V4.1-Flash rates — which is also the card the
// server bills the retired V4 ids at, so legacy sessions price
// correctly without their own entries.
func PricingFor(model string, tier Tier) ModelPricing {
	p, ok := standardRates[model]
	if !ok {
		p = standardRates[deepseek.ModelV41Flash]
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
//	at 23:59 off-peak → (peak, tomorrow 09:00 — or Monday 09:00 if
//	                    tomorrow is a weekend day)
func NextTransition(now time.Time) (Tier, time.Time) {
	b := now.In(Shanghai)
	// Weekends are off-peak all day: the next tier change from
	// anywhere inside a weekend is peak resuming Monday 09:00 —
	// NOT the nearest calendar boundary (Saturday 12:00 is no
	// transition at all).
	if wd := b.Weekday(); wd == time.Saturday || wd == time.Sunday {
		next := time.Date(b.Year(), b.Month(), b.Day(), 9, 0, 0, 0, Shanghai).AddDate(0, 0, 1)
		for next.Weekday() != time.Monday {
			next = next.AddDate(0, 0, 1)
		}
		return TierStandard, next
	}
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
		// Tomorrow may be Saturday (Friday evening) — skip the
		// whole weekend to Monday 09:00.
		next := at(9, 0).AddDate(0, 0, 1)
		for next.Weekday() == time.Saturday || next.Weekday() == time.Sunday {
			next = next.AddDate(0, 0, 1)
		}
		return TierStandard, next
	}
}

// FormatCost renders a USD amount with precision scaled to magnitude:
// 4 decimals below a cent (DeepSeek's per-call costs are routinely
// sub-cent), 3 decimals in the sub-dollar band, 2 from a dollar up.
// Zero renders as "$0" — a fresh session has spent nothing, and
// "$0.0000" was precision theater.
func FormatCost(usd float64) string {
	switch {
	case usd == 0:
		return "$0"
	case usd < 0.01:
		return fmt.Sprintf("$%.4f", usd)
	case usd < 1:
		return fmt.Sprintf("$%.3f", usd)
	default:
		return fmt.Sprintf("$%.2f", usd)
	}
}
