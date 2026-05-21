package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// at returns the given Beijing-time hour:min on a fixed reference day.
func at(hour, min int) time.Time {
	return time.Date(2026, time.January, 15, hour, min, 0, 0, Shanghai)
}

func TestCurrentTier_StandardBoundaries(t *testing.T) {
	cases := []struct {
		hour, min int
		want      Tier
		label     string
	}{
		{0, 0, TierStandard, "00:00 (just before off-peak)"},
		{0, 29, TierStandard, "00:29 (last standard minute)"},
		{0, 30, TierOffPeak, "00:30 (off-peak starts)"},
		{4, 0, TierOffPeak, "04:00 (mid off-peak)"},
		{8, 29, TierOffPeak, "08:29 (last off-peak minute)"},
		{8, 30, TierStandard, "08:30 (standard resumes)"},
		{12, 0, TierStandard, "noon"},
		{23, 59, TierStandard, "end of day"},
	}
	for _, c := range cases {
		if got := CurrentTier(at(c.hour, c.min)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.label, got, c.want)
		}
	}
}

func TestCurrentTier_TimezoneAware(t *testing.T) {
	// 16:30 UTC == 00:30 Beijing → off-peak should kick in.
	t1530UTC := time.Date(2026, time.January, 14, 15, 30, 0, 0, time.UTC)
	if got := CurrentTier(t1530UTC); got != TierStandard {
		t.Errorf("23:30 Beijing = standard, got %v", got)
	}
	t1630UTC := time.Date(2026, time.January, 14, 16, 30, 0, 0, time.UTC)
	if got := CurrentTier(t1630UTC); got != TierOffPeak {
		t.Errorf("00:30 Beijing next day = off-peak, got %v", got)
	}
}

func TestPricingFor_OffPeakDiscounts(t *testing.T) {
	std := PricingFor(deepseek.ModelChat, TierStandard)
	off := PricingFor(deepseek.ModelChat, TierOffPeak)
	if math.Abs(off.InputMissPerMTok-std.InputMissPerMTok*0.5) > 1e-9 {
		t.Errorf("off-peak miss not 50%% of standard: std=%v off=%v", std.InputMissPerMTok, off.InputMissPerMTok)
	}
	if math.Abs(off.OutputPerMTok-std.OutputPerMTok*0.5) > 1e-9 {
		t.Errorf("off-peak output not 50%% of standard")
	}
}

func TestPricingFor_UnknownModelFallsBack(t *testing.T) {
	p := PricingFor("deepseek-unknown", TierStandard)
	std := PricingFor(deepseek.ModelChat, TierStandard)
	if p != std {
		t.Errorf("fallback didn't equal chat pricing: %+v vs %+v", p, std)
	}
}

func TestCost_TypicalChatCall(t *testing.T) {
	// Mixed-cache turn: 800 miss + 200 hit + 100 completion under
	// standard chat pricing.
	u := deepseek.Usage{
		PromptTokens:          1000,
		PromptCacheMissTokens: 800,
		PromptCacheHitTokens:  200,
		CompletionTokens:      100,
	}
	want := 800*0.27/1e6 + 200*0.014/1e6 + 100*1.10/1e6
	got := Cost(deepseek.ModelChat, TierStandard, u)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

func TestCost_OffPeakIsHalf(t *testing.T) {
	u := deepseek.Usage{PromptCacheMissTokens: 1_000_000, CompletionTokens: 1_000_000}
	std := Cost(deepseek.ModelChat, TierStandard, u)
	off := Cost(deepseek.ModelChat, TierOffPeak, u)
	if math.Abs(off-std*0.5) > 1e-6 {
		t.Errorf("off-peak cost != half: std=%v off=%v", std, off)
	}
}

func TestNextTransition_StandardEvening(t *testing.T) {
	now := at(9, 0)
	tier, when := NextTransition(now)
	if tier != TierOffPeak {
		t.Errorf("tier = %v, want off-peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 16, 0, 30, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestNextTransition_StandardLateNight(t *testing.T) {
	// 00:15 — still standard, off-peak comes in 15 minutes.
	now := at(0, 15)
	tier, when := NextTransition(now)
	if tier != TierOffPeak {
		t.Errorf("tier = %v, want off-peak", tier)
	}
	wantWhen := time.Date(2026, time.January, 15, 0, 30, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestNextTransition_OffPeak(t *testing.T) {
	now := at(3, 0)
	tier, when := NextTransition(now)
	if tier != TierStandard {
		t.Errorf("tier = %v, want standard", tier)
	}
	wantWhen := time.Date(2026, time.January, 15, 8, 30, 0, 0, Shanghai)
	if !when.Equal(wantWhen) {
		t.Errorf("when = %v, want %v", when, wantWhen)
	}
}

func TestFormatCost(t *testing.T) {
	if got := FormatCost(0.001234); got != "$0.0012" {
		t.Errorf("FormatCost = %q", got)
	}
}
