// Package pricing models DeepSeek's peak/off-peak pricing windows plus
// the flat-rate cards of providers without such windows (GLM).
// Embedded rates are the peak rate card verified 2026-11-14 against
// api-docs.deepseek.com/quick_start/pricing (off-peak is exactly half
// the peak rate). Bump them with each release
// rather than fetching at runtime — a price-list HTTP call adds a failure
// mode for an exclusively defensive feature (PRD §4.8.4).
//
// All times use Asia/Shanghai (UTC+8, no DST). We hardcode the offset to
// avoid depending on the system tzdata, which is often missing on minimal
// Linux containers.
package pricing

import (
	"fmt"
	"math"
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

	// NoOffPeakDiscount marks providers without DeepSeek's peak/off-peak
	// windows (GLM bills the same rate around the clock). Set on their
	// rows so PricingFor never halves them, whichever tier the clock
	// says — CurrentTier is a DeepSeek-only concept but callers pass it
	// unconditionally.
	NoOffPeakDiscount bool
}

// ModelGLM53 is Zhipu's GLM-5.3 as addressed on bigmodel.cn's paas/v4
// endpoint — the default model of seek's --provider=glm lane.
const ModelGLM53 = "glm-5.3"

// ModelOpenCodeV41Flash is DeepSeek's V4.1 Flash as addressed on
// OpenCode's Go-plan gateway — the default model of seek's
// --provider=opencode lane. A DIFFERENT id string from the direct
// API's deepseek.ModelV41Flash ("deepseek-flash"): same model, two
// lanes, two rate entries so neither falls to the other's fallback.
const ModelOpenCodeV41Flash = "deepseek-v4.1-flash"

// ModelV4Pro is DeepSeek's independent pro build (served by
// DeepSeek-V4-Pro-0813). It retired into V4.1-Flash routing at the
// 2026-09-10 V4.1 launch, but the pricing page (checked 2026-11-14)
// re-lists it with its own — much higher — rate card, and its
// legacy-name note now names only the two Flash ids. Hence a registry
// entry: the unknown-model fallback would charge it Flash prices.
const ModelV4Pro = "deepseek-v4-pro"

// standardRates is the full-rate card per model during DeepSeek's peak
// windows (a.k.a. the "standard" tier). Off-peak is derived via
// offPeakDiscount.
//
// Numbers track the official pricing page (api-docs.deepseek.com/
// quick_start/pricing), re-checked 2026-11-15. The English page is the
// anchor: USD cards below, peak tier, off-peak exactly half. The Chinese
// page has since diverged into a different regional card — flat CNY,
// no windows (¥1/¥0.02/¥2 flash · ¥3/¥0.025/¥6 pro) — so cross-page
// comparison reads as a phantom rate change; billing follows the
// account's region. Diff like-for-like currency before bumping.
//
//	V4.1-Flash (deepseek-flash, served by DeepSeek-V4.1-Flash-1017):
//	    $0.30 miss · $0.006 hit · $1.20 output — effective
//	    2026-09-10 12:00 CST, unchanged at the 2026-11-14 check
//	V4-Pro (deepseek-v4-pro, served by DeepSeek-V4-Pro-0813):
//	    $1.32 miss · $0.044 hit · $3.96 output
//
// Off-peak is exactly half of both cards. deepseek-flash is the only
// live Flash id; the retired deepseek-v4-flash / -flash-vision-exp are
// server-routed to V4.1 Flash and billed at Flash prices, which is
// exactly what the unknown-model fallback below charges them — no
// per-id entries needed. deepseek-v4-pro left that legacy list (see
// ModelV4Pro): it bills at its own card, roughly 4× Flash on miss and
// output — the fallback would undercharge it badly. The legacy
// deepseek-chat / deepseek-reasoner aliases were removed server-side
// on 2026-07-24.
//
// The GLM row prices Zhipu's GLM-5.3 on open.bigmodel.cn (paas/v4),
// flat-rate — no peak/off-peak windows, hence NoOffPeakDiscount. Source:
// docs.bigmodel.cn/cn/guide/start/pricing, checked 2026-11-12: ¥8 input
// · ¥2 cache-hit · ¥28 output per 1M tokens (single 1M-context tier;
// cache storage was 限时免费 at check time). Converted to USD at 7.0
// CNY/USD — the table's unit is USD because Cost feeds the $ status
// display; re-check the conversion when bumping rates. GLM's other ids
// (glm-5.3-flash / -flashx) have no entries and fall back to the
// DeepSeek-Flash card below — wrong provider, but they are not models
// this lane addresses today.
var standardRates = map[string]ModelPricing{
	deepseek.ModelV41Flash: {
		InputMissPerMTok: 0.30,
		InputHitPerMTok:  0.006,
		OutputPerMTok:    1.20,
	},
	ModelV4Pro: {
		InputMissPerMTok: 1.32,
		InputHitPerMTok:  0.044,
		OutputPerMTok:    3.96,
	},
	ModelGLM53: {
		InputMissPerMTok:  8 / 7.0,  // ¥8
		InputHitPerMTok:   2 / 7.0,  // ¥2
		OutputPerMTok:     28 / 7.0, // ¥28
		NoOffPeakDiscount: true,
	},
	// OpenCode Go lane (--provider=opencode): a flat $10/month
	// REQUEST-WINDOW plan (5h/weekly/monthly request quotas) — the
	// per-token values below are never what the user pays; they exist
	// only so the defensive cost display shows a sensible number.
	// NoOffPeakDiscount because a subscription has no windows. Source:
	// opencode.ai/docs/console/models, checked 2026-11-17 —
	// deepseek-v4.1-flash $0.30 miss · $0.006 cached · $1.20 output
	// (happens to equal the direct DeepSeek card above). NB the Go
	// pool's glm-5.3 ($1.40/$0.26/$4.40 on the same page) COLLIDES
	// with the ModelGLM53 key, which stays at Zhipu's direct card —
	// that row prices the glm lane's real per-token billing, and Go-
	// lane glm-5.3 reuse of it is display-only either way. Other
	// Go-pool ids fall to the Flash fallback until a verified
	// /v1/models list lands (post-merge follow-up card).
	ModelOpenCodeV41Flash: {
		InputMissPerMTok:  0.30,
		InputHitPerMTok:   0.006,
		OutputPerMTok:     1.20,
		NoOffPeakDiscount: true,
	},
}

// searchPerCallUSD is the standalone Search API's per-call rate:
// $0.75 per 1,000 successful requests (listed directly on the English
// pricing page; the Chinese page shows ¥5/1K. The earlier derivation —
// converting ¥5 at the GLM row's 7.0 CNY/USD — is retired: USD is
// listed natively now, no conversion needed). Opened 2026-10-17 with
// the tool-API family; unlike the token table above, only HTTP-200
// calls are billable.
const searchPerCallUSD = 0.75 / 1000

// SearchCost returns the USD cost of n successful Search API calls.
// Failed requests cost nothing — pass only the count that returned 2xx.
func SearchCost(calls int) float64 {
	return float64(calls) * searchPerCallUSD
}

// asrPerHourUSD is the standalone ASR API's per-hour-of-audio rate:
// $0.116 per hour, listed natively on the English pricing page
// (2026-11-16 check; the Chinese page shows ¥0.78/h — the known
// regional-card divergence, see standardRates above). A limited-time
// 50% discount is running ($0.058 / ¥0.39) with no published end
// date and is deliberately NOT modelled — same posture as search:
// the constant stays at list price so the defensive display never
// undercharges. Billing is per second of audio, rounded up.
const asrPerHourUSD = 0.116

// ASRCost returns the USD cost of transcribing seconds of audio with
// the ASR API. The API bills per second with fractional seconds
// counting as a full second (不足 1 秒按 1 秒) — the ceil is INTERNAL,
// so pass the raw duration_ms/1000 from the ASRResponse. Zero or
// negative duration (failed calls) costs nothing.
func ASRCost(seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return math.Ceil(seconds) * asrPerHourUSD / 3600
}

// ttsPer10kCharsUSD is the standalone TTS API's per-10k-billing-chars
// rate: $0.45 per 10,000 billing characters, listed natively on the
// English pricing page (2026-11-16 check; the Chinese page shows
// ¥3/万 — the known regional-card divergence, see standardRates
// above). A limited-time 50% discount is running ($0.225 / ¥1.5) with
// no published end date and is deliberately NOT modelled — same
// posture as search and ASR: the constant stays at list price so the
// defensive display never undercharges. A billing character is one
// rune with Han runes counting two; count them with
// deepseek.TTSBillableChars.
const ttsPer10kCharsUSD = 0.45

// TTSCost returns the USD cost of synthesizing billableChars billing
// characters with the TTS API. The TTS response is raw audio with no
// JSON envelope, so there is no server-reported count — callers count
// locally: TTSCost(deepseek.TTSBillableChars(text)) on successful
// calls only. Failed requests cost nothing.
func TTSCost(billableChars int) float64 {
	if billableChars <= 0 {
		return 0
	}
	return float64(billableChars) * ttsPer10kCharsUSD / 10_000
}

// parsePerPageUSD is the Files-API document-parsing rate: $0.225 per
// 1,000 pages, listed natively on the English pricing page (2026-11-18
// check — the tools family IS on the EN page again, retiring the old
// ¥→$ derivation note; ¥1.5/千页 on the Chinese page, same value). Billed
// once, at upload: the parse IS the upload (a returned file id means the
// document already parsed), same price for text_recognition and
// rich_layout_recognition (PRD feature-deepseek-app-api §1.2/§6 row 3).
// Same-posture note as search/ASR/TTS: list price only, no discounts
// modelled — the defensive display errs high.
const parsePerPageUSD = 0.225 / 1000

// ParseCost returns the USD cost of parsing pages document pages via
// the Files API. A page count of 0 or less (server omitted it / failed
// upload) costs nothing — the router records this off the FileObject
// the upload RETURNED, never off "an upload was attempted".
func ParseCost(pages int) float64 {
	if pages <= 0 {
		return 0
	}
	return float64(pages) * parsePerPageUSD
}

const offPeakDiscount = 0.5

// Shanghai (CST = UTC+8) is DeepSeek's reference timezone. Exported so
// tests can construct local times consistently.
var Shanghai = time.FixedZone("CST", 8*60*60)

// Peak windows (Beijing time): 09:00–12:00 and 14:00–18:00, MONDAY
// through FRIDAY, matching DeepSeek's published peak hours of
// 01:00–04:00 and 06:00–10:00 UTC on weekdays (re-stated on the
// 2026-11-14 pricing page). The page also excludes Chinese public
// holidays from peak; we approximate that away on purpose — a holiday
// table is a data dependency the embedded-card design rejects, and
// pricing a holiday weekday at peak only overestimates a defensive
// cost display (err high). Everything else — evenings, mornings,
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
// server bills the retired Flash ids (deepseek-v4-flash /
// -flash-vision-exp) at, so legacy sessions price correctly without
// their own entries. deepseek-v4-pro is NOT in that set: it bills at
// its own registered card above.
func PricingFor(model string, tier Tier) ModelPricing {
	p, ok := standardRates[model]
	if !ok {
		p = standardRates[deepseek.ModelV41Flash]
	}
	if tier == TierOffPeak && !p.NoOffPeakDiscount {
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
