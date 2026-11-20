// Package pricing models DeepSeek's peak/off-peak pricing windows plus
// the flat-rate cards of providers without such windows (GLM, and the
// OpenCode Go pool).
//
// Embedded rates are the peak rate card (off-peak is exactly half
// the peak rate). Each card's verification date, source URL, and
// native currency live in the verified-on ledger (ledger.go) — the
// single source for dates, patrolled by scripts/price-audit.sh.
// Bump them with each release rather than fetching at
// runtime — a price-list HTTP call adds a failure mode for an
// exclusively defensive feature (PRD §4.8.4).
//
// One lane is exempt from that rule for the same reason it is exempt in
// budget: OpenCode Go publishes per-model cost blocks at a documented
// endpoint, and its pool churns weekly (models appear and are deprecated
// on a days-long cadence). internal/opencatalog refreshes that list at
// session start and installs the result through SetModelRates; the
// embedded cards below remain the offline baseline and the merge target.
//
// All times use Asia/Shanghai (UTC+8, no DST). We hardcode the offset to
// avoid depending on the system tzdata, which is often missing on minimal
// Linux containers.
package pricing

import (
	"fmt"
	"math"
	"sync"
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

// OpenCodeGoModel is one row of the Go lane's chat-completions
// catalogue.
type OpenCodeGoModel struct {
	ID string // wire model id — also the standardRates key
	// Label is a short picker description. The TUI renders a 32-column
	// id followed by it, so it must stay short enough for a ~100-col
	// terminal even with the " (current)" suffix.
	Label string
}

// OpenCodeGoModels is the lane's entire menu: ACP's session/new dropdown
// and the TUI /model picker both render from this slice, so a model can't
// exist in one surface and not the other. Verified against the
// gateway's public (no key) GET https://opencode.ai/inference/go/v1/models
// (date in ledger.go): these are the Go-allowance models that speak
// Chat-Completions — seek's
// compatible-adapter wire. Deliberately absent:
//   - grok-4.7, gpt-5.6-luna, gpt-6-luna, muse-spark-1.{2,3}-contributor:
//     Responses API only;
//   - claude-haiku-5-5, minimax-m3, qwen3.7-plus, qwen3.8-max,
//     qwen3.8-flash, qwen4-max: Anthropic messages wire;
//   - gemini-4-argon: generate-content wire;
//   - nothing else: the four *-Free ids (beam-free, iceman-contributor-free,
//     longcat-2.5-free, hy4-flash-free) ARE part of this provider's
//     catalogue. "Free" is defined by the docs as an opencode /
//     opencode-go model with no input price: same gateway, same key,
//     just not metered against the Go allowance (their
//     go.monthly_usage is null and their cost block is all zeros).
//     They only become unavailable when a workspace sets
//     OPENCODE_DISABLE_FREE_MODELS — the gateway then answers "Model
//     unavailable" in-band, which is an honest error rather than a
//     silent fallback.
var OpenCodeGoModels = []OpenCodeGoModel{
	{ModelOpenCodeV41Flash, "1M ctx · vision · lane default"},
	{ModelV4Pro, "1M ctx · pro"},
	{ModelGLM53, "1M ctx · top GLM"},
	{"glm-5.2", "1M ctx · previous GLM"},
	{"glm-5.3-flash", "1M ctx · fast GLM"},
	{"kimi-k3", "1M ctx · long output"},
	{"kimi-k3.1-pro", "1M ctx · top Kimi"},
	{"kimi-k2.7-code", "256K ctx · code-tuned"},
	{"minimax-m3.1-flash", "1M ctx · fast"},
	{"mimo-v2.6-flash", "1M ctx · fast"},
	{"mimo-v2.6-pro", "1M ctx"},
	{"longcat-2.0", "1M ctx"},
	{"hy4", "1M ctx"},
	{"hy4-preview", "1M ctx · preview"},
	{"hy3", "256K ctx"},
	{"step-5-preview", "1M ctx · preview"},
	// Free (time-limited) rows — $0 cards, no monthly allowance.
	{"beam-free", "free · 1M ctx"},
	{"hy4-flash-free", "free · 1M ctx · vision"},
	{"longcat-2.5-free", "free · 1M ctx · 100/day"},
	{"iceman-contributor-free", "free · 256K ctx"},
}

// ModelV4Pro is DeepSeek's independent pro build (served by
// DeepSeek-V4-Pro-0813). It retired into V4.1-Flash routing at the
// 2026-09-10 V4.1 launch, but the pricing page has since re-listed
// it with its own — much higher — rate card, and its
// legacy-name note now names only the two Flash ids. Hence a registry
// entry: the unknown-model fallback would charge it Flash prices.
const ModelV4Pro = "deepseek-v4-pro"

// standardRates is the full-rate card per model during DeepSeek's peak
// windows (a.k.a. the "standard" tier). Off-peak is derived via
// offPeakDiscount.
//
// Numbers track the official pricing page (api-docs.deepseek.com/
// quick_start/pricing; per-card verification dates in ledger.go). The
// English page is the anchor: USD cards below, peak tier, off-peak
// exactly half. The Chinese
// page has since diverged into a different regional card — flat CNY,
// no windows (¥1/¥0.02/¥2 flash · ¥3/¥0.025/¥6 pro) — so cross-page
// comparison reads as a phantom rate change; billing follows the
// account's region. Diff like-for-like currency before bumping.
//
//	V4.1-Flash (deepseek-flash, served by DeepSeek-V4.1-Flash-1017):
//	    $0.30 miss · $0.006 hit · $1.20 output — effective
//	    2026-09-10 12:00 CST
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
// docs.bigmodel.cn/cn/guide/start/pricing (verification date in
// ledger.go): ¥8 input
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
	// --- OpenCode Go lane (--provider=opencode) ---
	// A flat $10/month REQUEST-WINDOW subscription (5h / weekly /
	// monthly request quotas), so the per-token values below are never
	// what the user pays — they exist only so the defensive cost
	// display shows a sensible number. NoOffPeakDiscount on every row:
	// a subscription has no DeepSeek peak clock.
	//
	// Source: the gateway's own GET /v1/models (date in ledger.go) —
	// each model carries a cost.text block in USD per 1M tokens, which
	// matches the docs' pricing table except that the docs print the
	// PEAK figure for the two DeepSeek ids: the API quotes
	// deepseek-v4.1-flash at $0.15 / $0.003 / $0.60 with a 2×
	// Mon–Fri 01:00–04:00 & 06:00–10:00 UTC multiplier, and v4-pro at
	// $0.66 / $0.022 / $1.98 with the same 2×. Both cards below keep
	// the peak figure (err high, same posture as search/ASR/TTS). None
	// of the other pool ids carries a multiplier, so listed == peak.
	//
	// glm-5.3 and deepseek-v4-pro already have rows of their own
	// (ModelGLM53 / ModelV4Pro) from the direct-API lanes; they are NOT
	// repeated here, because the registry is keyed by wire id alone.
	// Display-only either way — ModelGLM53's CNY-derived card
	// (~$1.14/$0.29/$4.00) sits close to the Go pool's
	// $1.40/$0.26/$4.40, and ModelV4Pro's direct card already equals
	// the Go peak price exactly. The shared key also means those two
	// inherit the DeepSeek peak clock: their Go-lane figure halves
	// off-peak even though the Go plan has no off-peak. A known
	// display-only artifact — fixing it needs a lane-aware pricing key,
	// which is not worth the surface for a subscription's estimate.
	ModelOpenCodeV41Flash: {
		InputMissPerMTok:  0.30,
		InputHitPerMTok:   0.006,
		OutputPerMTok:     1.20,
		NoOffPeakDiscount: true,
	},
	"glm-5.2": {
		InputMissPerMTok:  1.40,
		InputHitPerMTok:   0.26,
		OutputPerMTok:     4.40,
		NoOffPeakDiscount: true,
	},
	"glm-5.3-flash": {
		InputMissPerMTok:  0.15,
		InputHitPerMTok:   0.03,
		OutputPerMTok:     0.50,
		NoOffPeakDiscount: true,
	},
	"kimi-k3": {
		InputMissPerMTok:  3.00,
		InputHitPerMTok:   0.30,
		OutputPerMTok:     15.00,
		NoOffPeakDiscount: true,
	},
	"kimi-k3.1-pro": {
		InputMissPerMTok:  2.00,
		InputHitPerMTok:   0.20,
		OutputPerMTok:     10.00,
		NoOffPeakDiscount: true,
	},
	"kimi-k2.7-code": {
		InputMissPerMTok:  0.95,
		InputHitPerMTok:   0.19,
		OutputPerMTok:     4.00,
		NoOffPeakDiscount: true,
	},
	"minimax-m3.1-flash": {
		InputMissPerMTok:  0.15,
		InputHitPerMTok:   0.015,
		OutputPerMTok:     0.60,
		NoOffPeakDiscount: true,
	},
	"mimo-v2.6-flash": {
		InputMissPerMTok:  0.14,
		InputHitPerMTok:   0.0028,
		OutputPerMTok:     0.28,
		NoOffPeakDiscount: true,
	},
	"mimo-v2.6-pro": {
		InputMissPerMTok:  0.435,
		InputHitPerMTok:   0.003625,
		OutputPerMTok:     0.87,
		NoOffPeakDiscount: true,
	},
	"longcat-2.0": {
		InputMissPerMTok:  0.30,
		InputHitPerMTok:   0.006,
		OutputPerMTok:     1.20,
		NoOffPeakDiscount: true,
	},
	"hy4": {
		InputMissPerMTok:  0.834,
		InputHitPerMTok:   0.042,
		OutputPerMTok:     2.501,
		NoOffPeakDiscount: true,
	},
	"hy4-preview": {
		InputMissPerMTok:  0.834,
		InputHitPerMTok:   0.042,
		OutputPerMTok:     2.501,
		NoOffPeakDiscount: true,
	},
	"hy3": {
		InputMissPerMTok:  0.14,
		InputHitPerMTok:   0.035,
		OutputPerMTok:     0.58,
		NoOffPeakDiscount: true,
	},
	"step-5-preview": {
		InputMissPerMTok:  1.00,
		InputHitPerMTok:   0.10,
		OutputPerMTok:     2.70,
		NoOffPeakDiscount: true,
	},
	// Free (time-limited) pool rows: the API's cost block is all zeros,
	// so the card is too — a Go-lane turn on one of these displays $0,
	// which is the truth. An explicit entry (rather than letting the
	// unknown-model fallback price them as Flash) keeps a free model from
	// showing a non-zero estimate.
	"beam-free": {
		NoOffPeakDiscount: true,
	},
	"hy4-flash-free": {
		NoOffPeakDiscount: true,
	},
	"longcat-2.5-free": {
		NoOffPeakDiscount: true,
	},
	"iceman-contributor-free": {
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
// (the Chinese page shows ¥0.78/h — the known
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
// English pricing page (the Chinese page shows
// ¥3/万 — the known regional-card divergence, see standardRates
// above). A limited-time 50% discount is running ($0.225 / ¥1.5) with
// no published end date and is deliberately NOT modelled — same
// posture as search and ASR: the constant stays at list price so the
// defensive display never undercharges. A billing character is one
// rune with Han runes counting two; count them with
// deepseek.TTSBillableChars. Ledger row: tool:tts (ledger.go).
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
// modelled — the defensive display errs high. Ledger row:
// tool:parse (ledger.go).
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

// PricingFor returns the per-token rate card for a model+tier. Cards
// installed by SetModelRates (OpenCode Go catalogue refresh) win over the
// embedded table. Unknown models fall back to V4.1-Flash rates — which is
// also the card the server bills the retired Flash ids
// (deepseek-v4-flash / -flash-vision-exp) at, so legacy sessions price
// correctly without their own entries. deepseek-v4-pro is NOT in that
// set: it bills at its own registered card above.
func PricingFor(model string, tier Tier) ModelPricing {
	p, ok := LookupRate(model)
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

// overrides, when non-nil, wins over standardRates for the ids it names.
// Installed once per session by the OpenCode catalogue refresh
// (internal/opencatalog); see the package comment. Guarded because
// PricingFor runs on the per-turn cost path while a background refresh
// may be installing the new table.
var (
	overrideMu sync.RWMutex
	overrides  map[string]ModelPricing
)

// SetModelRates installs runtime rate cards, overriding standardRates for
// the named ids; nil clears them. Sanctioned caller:
// internal/opencatalog. Cards are copied, and every installed card is
// forced flat-rate (NoOffPeakDiscount) — a subscription lane has no
// DeepSeek peak clock, and a remote cost block must never be halved by
// one.
func SetModelRates(m map[string]ModelPricing) {
	clean := make(map[string]ModelPricing, len(m))
	for id, card := range m {
		if card.InputMissPerMTok < 0 || card.InputHitPerMTok < 0 || card.OutputPerMTok < 0 {
			continue
		}
		card.NoOffPeakDiscount = true
		clean[id] = card
	}
	overrideMu.Lock()
	defer overrideMu.Unlock()
	if len(clean) == 0 {
		overrides = nil
		return
	}
	overrides = clean
}

// LookupRate resolves a model's card: runtime overrides first, then the
// embedded table. The bool reports whether ANY card exists — callers that
// must distinguish "priced" from "fallback" (the catalogue merge, which
// seeds its baseline from here) use this, not a comparison against the
// Flash card's numbers.
func LookupRate(model string) (ModelPricing, bool) {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	if p, ok := overrides[model]; ok {
		return p, true
	}
	p, ok := standardRates[model]
	return p, ok
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
