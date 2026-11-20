package pricing

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// at returns the given Beijing-time hour:min on a fixed reference day.
func at(hour, min int) time.Time {
	return time.Date(2026, time.January, 15, hour, min, 0, 0, Shanghai)
}

func TestCurrentTier_PeakBoundaries(t *testing.T) {
	cases := []struct {
		hour, min int
		want      Tier
		label     string
	}{
		{0, 0, TierOffPeak, "00:00 (off-peak)"},
		{8, 59, TierOffPeak, "08:59 (last minute before peak 1)"},
		{9, 0, TierStandard, "09:00 (peak 1 starts)"},
		{11, 59, TierStandard, "11:59 (last peak-1 minute)"},
		{12, 0, TierOffPeak, "12:00 (peak 1 ends)"},
		{13, 59, TierOffPeak, "13:59 (last minute before peak 2)"},
		{14, 0, TierStandard, "14:00 (peak 2 starts)"},
		{17, 59, TierStandard, "17:59 (last peak-2 minute)"},
		{18, 0, TierOffPeak, "18:00 (peak 2 ends)"},
		{23, 59, TierOffPeak, "23:59 (end of day)"},
	}
	for _, c := range cases {
		if got := CurrentTier(at(c.hour, c.min)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.label, got, c.want)
		}
	}
}

func TestCurrentTier_TimezoneAware(t *testing.T) {
	// 15:30 UTC == 23:30 Beijing → off-peak (outside both peak windows).
	t1530UTC := time.Date(2026, time.January, 14, 15, 30, 0, 0, time.UTC)
	if got := CurrentTier(t1530UTC); got != TierOffPeak {
		t.Errorf("23:30 Beijing = off-peak, got %v", got)
	}
	// 01:00 UTC == 09:00 Beijing → peak 1 starts.
	t0100UTC := time.Date(2026, time.January, 14, 1, 0, 0, 0, time.UTC)
	if got := CurrentTier(t0100UTC); got != TierStandard {
		t.Errorf("09:00 Beijing = peak, got %v", got)
	}
	// 16:30 UTC == 00:30 Beijing → off-peak.
	t1630UTC := time.Date(2026, time.January, 14, 16, 30, 0, 0, time.UTC)
	if got := CurrentTier(t1630UTC); got != TierOffPeak {
		t.Errorf("00:30 Beijing next day = off-peak, got %v", got)
	}
}

func TestPricingFor_OffPeakDiscounts(t *testing.T) {
	std := PricingFor(deepseek.ModelV41Flash, TierStandard)
	off := PricingFor(deepseek.ModelV41Flash, TierOffPeak)
	if math.Abs(off.InputMissPerMTok-std.InputMissPerMTok*0.5) > 1e-9 {
		t.Errorf("off-peak miss not 50%% of standard: std=%v off=%v", std.InputMissPerMTok, off.InputMissPerMTok)
	}
	if math.Abs(off.OutputPerMTok-std.OutputPerMTok*0.5) > 1e-9 {
		t.Errorf("off-peak output not 50%% of standard")
	}
}

func TestPricingFor_UnknownModelFallsBack(t *testing.T) {
	p := PricingFor("deepseek-unknown", TierStandard)
	std := PricingFor(deepseek.ModelV41Flash, TierStandard)
	if p != std {
		t.Errorf("fallback didn't equal flash pricing: %+v vs %+v", p, std)
	}
}

// TestPricingFor_RetiredIdsBilledAtFlashCard pins the V4.1 routing
// contract: the retired deepseek-v4-flash / deepseek-v4-flash-vision-exp
// ids are served by V4.1 Flash and billed at Flash prices — at BOTH
// tiers. They carry no registry entries, so this is literally the
// unknown-model fallback doing the vendor's billing for it.
// deepseek-v4-pro is deliberately NOT in this list: the 2026-11-14
// pricing page re-listed it with its own card (see TestPricingFor_V4Pro).
func TestPricingFor_RetiredIdsBilledAtFlashCard(t *testing.T) {
	for _, m := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		for _, tier := range []Tier{TierStandard, TierOffPeak} {
			if p, want := PricingFor(m, tier), PricingFor(deepseek.ModelV41Flash, tier); p != want {
				t.Errorf("PricingFor(%s, %v) = %+v, want the V4.1-Flash card %+v", m, tier, p, want)
			}
		}
	}
}

// TestPricingFor_V4Pro pins deepseek-v4-pro's own card against the
// 2026-11-14 pricing page (DeepSeek-V4-Pro-0813): $1.32 miss / $0.044
// hit / $3.96 output peak, exactly half off-peak. Pro left the
// Flash-routed legacy list, so a registry entry — not the fallback —
// must price it; without one the status bar would undercharge ~4×.
func TestPricingFor_V4Pro(t *testing.T) {
	std := PricingFor(ModelV4Pro, TierStandard)
	want := ModelPricing{InputMissPerMTok: 1.32, InputHitPerMTok: 0.044, OutputPerMTok: 3.96}
	if std != want {
		t.Errorf("v4-pro peak card = %+v, want %+v", std, want)
	}
	// Off-peak halves every bucket (pro shares DeepSeek's windows).
	off := PricingFor(ModelV4Pro, TierOffPeak)
	if math.Abs(off.InputMissPerMTok-1.32*0.5) > 1e-9 ||
		math.Abs(off.InputHitPerMTok-0.044*0.5) > 1e-9 ||
		math.Abs(off.OutputPerMTok-3.96*0.5) > 1e-9 {
		t.Errorf("v4-pro off-peak card = %+v, want exactly half of peak", off)
	}
	// And it must NOT equal the Flash card — the pre-2026-11 fallback
	// behaviour this entry exists to replace.
	if std == PricingFor(deepseek.ModelV41Flash, TierStandard) {
		t.Errorf("v4-pro card must differ from the Flash card")
	}
}

func TestCost_TypicalChatCall(t *testing.T) {
	// Mixed-cache turn: 800 miss + 200 hit + 100 completion under
	// the V4.1-Flash peak rate card.
	u := deepseek.Usage{
		PromptTokens:          1000,
		PromptCacheMissTokens: 800,
		PromptCacheHitTokens:  200,
		CompletionTokens:      100,
	}
	want := 800*0.30/1e6 + 200*0.006/1e6 + 100*1.20/1e6
	got := Cost(deepseek.ModelV41Flash, TierStandard, u)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

func TestCost_OffPeakIsHalf(t *testing.T) {
	u := deepseek.Usage{PromptCacheMissTokens: 1_000_000, CompletionTokens: 1_000_000}
	std := Cost(deepseek.ModelV41Flash, TierStandard, u)
	off := Cost(deepseek.ModelV41Flash, TierOffPeak, u)
	if math.Abs(off-std*0.5) > 1e-6 {
		t.Errorf("off-peak cost != half: std=%v off=%v", std, off)
	}
}

// TestCurrentTier_WeekendOffPeak pins the weekday qualifier on the
// peak windows (Monday through Friday, per the 2026-09-10 pricing
// page): the same wall-clock inside a peak window is peak on a Friday
// and off-peak on the following Saturday/Sunday.
func TestCurrentTier_WeekendOffPeak(t *testing.T) {
	fri := time.Date(2026, time.September, 11, 10, 0, 0, 0, Shanghai)  // Friday, inside peak 1
	sat := time.Date(2026, time.September, 12, 10, 0, 0, 0, Shanghai)  // Saturday
	sun := time.Date(2026, time.September, 13, 15, 30, 0, 0, Shanghai) // Sunday, inside peak 2
	if got := CurrentTier(fri); got != TierStandard {
		t.Errorf("Friday 10:00 Beijing = peak, got %v", got)
	}
	if got := CurrentTier(sat); got != TierOffPeak {
		t.Errorf("Saturday 10:00 Beijing = off-peak, got %v", got)
	}
	if got := CurrentTier(sun); got != TierOffPeak {
		t.Errorf("Sunday 15:30 Beijing = off-peak, got %v", got)
	}
}

// TestNextTransition_SkipsWeekend: weekends are off-peak all day, so
// the next peak resumption from anywhere in a weekend — and from a
// Friday evening — is Monday 09:00, not the nearest calendar 09:00.
func TestNextTransition_SkipsWeekend(t *testing.T) {
	mon := time.Date(2026, time.September, 14, 9, 0, 0, 0, Shanghai)
	for _, now := range []time.Time{
		time.Date(2026, time.September, 11, 23, 59, 0, 0, Shanghai), // Friday evening
		time.Date(2026, time.September, 12, 10, 0, 0, 0, Shanghai),  // Saturday, inside would-be peak 1
		time.Date(2026, time.September, 13, 20, 0, 0, 0, Shanghai),  // Sunday evening
	} {
		tier, when := NextTransition(now)
		if tier != TierStandard {
			t.Errorf("%s: tier = %v, want peak (resumes Monday)", now, tier)
		}
		if !when.Equal(mon) {
			t.Errorf("%s: when = %v, want Monday 09:00 %v", now, when, mon)
		}
	}
}

func TestNextTransition_OffPeakMorning(t *testing.T) {
	// 03:00 — off-peak, peak 1 comes at 09:00.
	now := at(3, 0)
	tier, when := NextTransition(now)
	if tier != TierStandard {
		t.Errorf("tier = %v, want peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 15, 9, 0, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestNextTransition_Peak1(t *testing.T) {
	// 09:00 — peak 1 starts, off-peak comes at 12:00.
	now := at(9, 0)
	tier, when := NextTransition(now)
	if tier != TierOffPeak {
		t.Errorf("tier = %v, want off-peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 15, 12, 0, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestNextTransition_OffPeakMidday(t *testing.T) {
	// 13:00 — off-peak between peaks, peak 2 comes at 14:00.
	now := at(13, 0)
	tier, when := NextTransition(now)
	if tier != TierStandard {
		t.Errorf("tier = %v, want peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 15, 14, 0, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestNextTransition_Peak2(t *testing.T) {
	// 14:00 — peak 2 starts, off-peak comes at 18:00.
	now := at(14, 0)
	tier, when := NextTransition(now)
	if tier != TierOffPeak {
		t.Errorf("tier = %v, want off-peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 15, 18, 0, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestNextTransition_OffPeakEvening(t *testing.T) {
	// 23:59 — off-peak, peak 1 comes tomorrow 09:00.
	now := at(23, 59)
	tier, when := NextTransition(now)
	if tier != TierStandard {
		t.Errorf("tier = %v, want peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 16, 9, 0, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestFormatCost(t *testing.T) {
	// Precision scales with magnitude: sub-cent amounts keep 4 decimals
	// (they are routinely the whole story for one call), the sub-dollar
	// band reads in thousandths, dollars and up in cents. Zero is
	// exactly "$0" — not "$0.0000".
	cases := []struct {
		in   float64
		want string
	}{
		{0, "$0"},
		{0.001234, "$0.0012"},
		{0.0099, "$0.0099"},
		{0.01, "$0.010"},
		{0.42, "$0.420"},
		{1.5, "$1.50"},
		{9.99, "$9.99"},
	}
	for _, c := range cases {
		if got := FormatCost(c.in); got != c.want {
			t.Errorf("FormatCost(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPricingFor_GLM53 pins the GLM-5.3 card against bigmodel.cn's
// paas listing: ¥8 miss / ¥2 hit / ¥28 output per
// 1M tokens, converted at 7.0 CNY/USD. GLM has no peak/off-peak
// windows, so the card must be identical at both tiers — CurrentTier
// describes DeepSeek's clock, and callers pass it unconditionally.
func TestPricingFor_GLM53(t *testing.T) {
	std := PricingFor(ModelGLM53, TierStandard)
	if math.Abs(std.InputMissPerMTok-8/7.0) > 1e-9 {
		t.Errorf("glm-5.3 miss = %v, want 8/7.0 (%v)", std.InputMissPerMTok, 8/7.0)
	}
	if math.Abs(std.InputHitPerMTok-2/7.0) > 1e-9 {
		t.Errorf("glm-5.3 hit = %v, want 2/7.0 (%v)", std.InputHitPerMTok, 2/7.0)
	}
	if math.Abs(std.OutputPerMTok-28/7.0) > 1e-9 {
		t.Errorf("glm-5.3 output = %v, want 28/7.0 (%v)", std.OutputPerMTok, 28/7.0)
	}

	off := PricingFor(ModelGLM53, TierOffPeak)
	if off != std {
		t.Errorf("glm-5.3 must be flat-rate: off-peak card %+v differs from standard %+v", off, std)
	}

	// Regression guard in the same breath: DeepSeek's off-peak halving
	// must survive the NoOffPeakDiscount branch.
	dsStd := PricingFor(deepseek.ModelV41Flash, TierStandard)
	dsOff := PricingFor(deepseek.ModelV41Flash, TierOffPeak)
	if math.Abs(dsOff.InputMissPerMTok-dsStd.InputMissPerMTok*0.5) > 1e-9 {
		t.Errorf("deepseek off-peak discount regressed: std=%v off=%v", dsStd.InputMissPerMTok, dsOff.InputMissPerMTok)
	}
}

// TestPricingFor_OpenCodeGoLane pins the OpenCode Go lane's default
// model card against opencode.ai/docs/console/models (checked
// 2026-11-17): deepseek-v4.1-flash $0.30 miss / $0.006 cached /
// $1.20 output — numerically the direct DeepSeek Flash card, but a
// separate id string and entry, and flat across tiers: the Go plan is
// a request-window subscription, so per-token values are display-only
// and off-peak halving would be fictional precision.
func TestPricingFor_OpenCodeGoLane(t *testing.T) {
	std := PricingFor(ModelOpenCodeV41Flash, TierStandard)
	if math.Abs(std.InputMissPerMTok-0.30) > 1e-9 {
		t.Errorf("deepseek-v4.1-flash miss = %v, want 0.30", std.InputMissPerMTok)
	}
	if math.Abs(std.InputHitPerMTok-0.006) > 1e-9 {
		t.Errorf("deepseek-v4.1-flash hit = %v, want 0.006", std.InputHitPerMTok)
	}
	if math.Abs(std.OutputPerMTok-1.20) > 1e-9 {
		t.Errorf("deepseek-v4.1-flash output = %v, want 1.20", std.OutputPerMTok)
	}

	off := PricingFor(ModelOpenCodeV41Flash, TierOffPeak)
	if off != std {
		t.Errorf("Go-lane card must be flat-rate: off-peak %+v differs from standard %+v", off, std)
	}

	// The lane default must NOT resolve through the unknown-model
	// fallback (same numbers today, but a future Flash bump on the
	// direct card must not silently move the Go lane with it).
	if _, ok := standardRates[ModelOpenCodeV41Flash]; !ok {
		t.Error("deepseek-v4.1-flash needs its own registry entry, not the Flash fallback")
	}
}

// TestOpenCodeGoCatalogue guards the menu/rate-card invariant for the
// lane's whole catalogue (pricing.OpenCodeGoModels is what ACP's dropdown
// and the TUI /model picker render): every offered id must resolve
// through its OWN registry entry — falling back would price a $15/M
// kimi-k3 turn at Flash rates and understate the display by 10× — and
// every card must be flat-rate, because the Go plan is a request-window
// subscription with no DeepSeek peak clock. Labels are also width-budget
// checked: the TUI pads the id column to 32 and appends " (current)", so
// an overlong label soft-wraps the picker.
func TestOpenCodeGoCatalogue(t *testing.T) {
	// Two Go-pool ids are also live ids on a direct-API lane, and the
	// registry is keyed by wire id alone — so they reuse that lane's
	// card and inherit its peak/off-peak clock. The Go display is
	// display-only, so the artifact is documented rather than solved
	// with a second key the pricing API can't express.
	sharedWithDirectLane := map[string]bool{ModelGLM53: true, ModelV4Pro: true}

	seen := make(map[string]bool, len(OpenCodeGoModels))
	for _, m := range OpenCodeGoModels {
		if seen[m.ID] {
			t.Errorf("%s: duplicate row in OpenCodeGoModels", m.ID)
		}
		seen[m.ID] = true
		if m.Label == "" {
			t.Errorf("%s: empty picker label", m.ID)
		}
		// 32-col id + label + " (current)" must fit a 100-col terminal
		// with room for the "▸ " marker and padding.
		if len(m.Label) > 55 {
			t.Errorf("%s: label %q is %d chars, want <= 55 (picker row width)", m.ID, m.Label, len(m.Label))
		}

		card, ok := standardRates[m.ID]
		if !ok {
			t.Errorf("%s: no registry entry — the unknown-model fallback would price it as Flash", m.ID)
			continue
		}
		// The pool's "-free" rows are the docs' zero-input-price models:
		// their card must be exactly $0 (the fallback would otherwise
		// bill a free turn at Flash rates). Everything else must be a
		// real, positive card.
		if isFree := strings.HasSuffix(m.ID, "-free"); isFree {
			if card.InputMissPerMTok != 0 || card.InputHitPerMTok != 0 || card.OutputPerMTok != 0 {
				t.Errorf("%s: free row must carry a $0 card, got %+v", m.ID, card)
			}
			if got := Cost(m.ID, TierStandard, deepseek.Usage{PromptCacheMissTokens: 1_000_000, CompletionTokens: 1_000_000}); got != 0 {
				t.Errorf("%s: Cost = %v, want 0 for a free model", m.ID, got)
			}
		} else if card.InputMissPerMTok <= 0 || card.InputHitPerMTok < 0 || card.OutputPerMTok <= 0 {
			t.Errorf("%s: non-positive rate card %+v", m.ID, card)
		}
		if !card.NoOffPeakDiscount && !sharedWithDirectLane[m.ID] {
			t.Errorf("%s: Go-pool card must be flat-rate (NoOffPeakDiscount)", m.ID)
		}
		if !sharedWithDirectLane[m.ID] {
			if off := PricingFor(m.ID, TierOffPeak); off != card {
				t.Errorf("%s: off-peak %+v differs from standard %+v", m.ID, off, card)
			}
		}
	}
	if !seen[ModelOpenCodeV41Flash] {
		t.Errorf("lane default %s missing from OpenCodeGoModels", ModelOpenCodeV41Flash)
	}
}

// TestCost_GLM53 prices one usage block across all three buckets. This
// is the shape runTurnLLM produces for the GLM lane: full prompt split
// into hit/miss from prompt_tokens_details.cached_tokens.
func TestCost_GLM53(t *testing.T) {
	u := deepseek.Usage{
		PromptTokens:          100_000,
		PromptCacheHitTokens:  60_000,
		PromptCacheMissTokens: 40_000,
		CompletionTokens:      5_000,
	}
	got := Cost(ModelGLM53, TierStandard, u)
	want := 60_000.0*(2/7.0)/1e6 + 40_000.0*(8/7.0)/1e6 + 5_000.0*(28/7.0)/1e6
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Cost(glm-5.3) = %v, want %v", got, want)
	}
	if got <= 0 {
		t.Errorf("Cost(glm-5.3) = %v, must be > 0 — this is the $0-statistics regression", got)
	}
	// Off-peak must price identically (flat-rate provider).
	if off := Cost(ModelGLM53, TierOffPeak, u); math.Abs(off-got) > 1e-9 {
		t.Errorf("Cost(glm-5.3, off-peak) = %v, want same %v (no peak windows)", off, got)
	}
}

// TestSearchCost prices the standalone Search API's per-call billing:
// $0.75 per 1,000 successful calls (listed natively on the English
// pricing page, 2026-11-14 check; the Chinese page shows ¥5/1K).
func TestSearchCost(t *testing.T) {
	if got := SearchCost(1); math.Abs(got-0.75/1000) > 1e-12 {
		t.Errorf("SearchCost(1) = %v, want 0.75/1000", got)
	}
	if got := SearchCost(1000); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("SearchCost(1000) = %v, want 0.75 (per 1k calls)", got)
	}
	if got := SearchCost(0); got != 0 {
		t.Errorf("SearchCost(0) = %v, want 0 — failed calls are not billable", got)
	}
	// Per-call rate is far below FormatCost's sub-cent precision band;
	// a single search must not round up to a visible dollar amount.
	if s := FormatCost(SearchCost(1)); s != "$0.0008" {
		t.Errorf("FormatCost(SearchCost(1)) = %q, want $0.0008", s)
	}
}

// TestASRCost prices the standalone ASR API's per-second-of-audio
// billing: $0.116/hour listed natively on the English pricing page
// (2026-11-16 check; zh page ¥0.78/h, the known regional divergence).
// The API bills per second rounded UP — 不足 1 秒按 1 秒 — so the ceil
// is internal and callers pass raw duration_ms/1000.
func TestASRCost(t *testing.T) {
	perSec := 0.116 / 3600
	// Fractional seconds bill as a full second; exact seconds don't
	// round further.
	cases := []struct {
		seconds  float64
		billable float64
		label    string
	}{
		{0.1, 1, "sub-second rounds up to one second"},
		{1.0, 1, "exactly one second"},
		{1.001, 2, "just past one second bills two"},
		{3.2, 4, "3.2 s bills four seconds"},
		{120, 120, "the full 120 s clip cap"},
		{30, 30, "a 30 s voice note (PRD §4.3 sizing basis)"},
	}
	for _, c := range cases {
		if got := ASRCost(c.seconds); math.Abs(got-c.billable*perSec) > 1e-12 {
			t.Errorf("%s: ASRCost(%v) = %v, want %v billable seconds (%v)", c.label, c.seconds, got, c.billable, c.billable*perSec)
		}
	}
	// Failed / zero-duration calls are not billable, and a stray
	// negative must not pay out.
	if got := ASRCost(0); got != 0 {
		t.Errorf("ASRCost(0) = %v, want 0", got)
	}
	if got := ASRCost(-3); got != 0 {
		t.Errorf("ASRCost(-3) = %v, want 0", got)
	}
	// Integration shape the response feeds: duration_ms 3200 →
	// 3.2 s → billed as 4 seconds.
	if got := ASRCost(3200.0 / 1000); math.Abs(got-4*perSec) > 1e-12 {
		t.Errorf("ASRCost(3.2) = %v, want 4 seconds' worth", got)
	}
}

// TestTTSCost prices the standalone TTS API's per-billing-character
// billing: $0.45 per 10,000 billing characters listed natively on the
// English pricing page (2026-11-16 check; zh page ¥3/万, the known
// regional divergence). Han runes count 2 — the counting helper lives
// in pkg/deepseek (TTSBillableChars); here the input is already the
// billable count.
func TestTTSCost(t *testing.T) {
	if got := TTSCost(10_000); math.Abs(got-0.45) > 1e-12 {
		t.Errorf("TTSCost(10000) = %v, want 0.45 per 10k billing chars", got)
	}
	// PRD §4.3's sizing basis: a 100-Han-char notification = 200
	// billable ≈ ¥0.06 list / $0.009.
	if got := TTSCost(200); math.Abs(got-0.45/50) > 1e-12 {
		t.Errorf("TTSCost(200) = %v, want 0.45×200/10000", got)
	}
	// The per-request cap is the most a single call can bill.
	if got := TTSCost(deepseek.TTSMaxBillableChars); got != TTSCost(10_000) {
		t.Errorf("TTSCost(cap) = %v, want the full 10k rate %v", got, TTSCost(10_000))
	}
	// Failed / empty calls are not billable, and a stray negative must
	// not pay out.
	if got := TTSCost(0); got != 0 {
		t.Errorf("TTSCost(0) = %v, want 0", got)
	}
	if got := TTSCost(-5); got != 0 {
		t.Errorf("TTSCost(-5) = %v, want 0", got)
	}
	// Sub-cent precision band: one notification must render visibly,
	// not round away to nothing.
	if s := FormatCost(TTSCost(200)); s != "$0.0090" {
		t.Errorf("FormatCost(TTSCost(200)) = %q, want $0.0090", s)
	}
}

// TestParseCost prices the Files-API document parsing: $0.225 per 1,000
// pages (zh page ¥1.5/千页, PRD feature-deepseek-app-api §6 row 3), billed
// once at upload off the page count the FileObject RETURNED.
func TestParseCost(t *testing.T) {
	if got := ParseCost(1000); math.Abs(got-0.225) > 1e-12 {
		t.Errorf("ParseCost(1000) = %v, want 0.225 per 1k pages", got)
	}
	if got := ParseCost(1); math.Abs(got-0.225/1000) > 1e-12 {
		t.Errorf("ParseCost(1) = %v, want 0.225/1000", got)
	}
	// A thick document parses for well under a cent — the F3 router's
	// per-submit line item.
	if got := ParseCost(34); math.Abs(got-34*0.225/1000) > 1e-12 {
		t.Errorf("ParseCost(34) = %v, want 34×0.225/1000", got)
	}
	// Server omitted the page count / upload failed → nothing billable;
	// a stray negative must not pay out.
	if got := ParseCost(0); got != 0 {
		t.Errorf("ParseCost(0) = %v, want 0", got)
	}
	if got := ParseCost(-2); got != 0 {
		t.Errorf("ParseCost(-2) = %v, want 0", got)
	}
	// Sub-cent band: one document must render visibly, not round away.
	if s := FormatCost(ParseCost(34)); s != "$0.0076" {
		t.Errorf("FormatCost(ParseCost(34)) = %q, want $0.0076", s)
	}
}
