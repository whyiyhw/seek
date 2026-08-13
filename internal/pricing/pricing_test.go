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
	std := PricingFor(deepseek.ModelV4Flash, TierStandard)
	off := PricingFor(deepseek.ModelV4Flash, TierOffPeak)
	if math.Abs(off.InputMissPerMTok-std.InputMissPerMTok*0.5) > 1e-9 {
		t.Errorf("off-peak miss not 50%% of standard: std=%v off=%v", std.InputMissPerMTok, off.InputMissPerMTok)
	}
	if math.Abs(off.OutputPerMTok-std.OutputPerMTok*0.5) > 1e-9 {
		t.Errorf("off-peak output not 50%% of standard")
	}
}

func TestPricingFor_UnknownModelFallsBack(t *testing.T) {
	p := PricingFor("deepseek-unknown", TierStandard)
	std := PricingFor(deepseek.ModelV4Flash, TierStandard)
	if p != std {
		t.Errorf("fallback didn't equal chat pricing: %+v vs %+v", p, std)
	}
}

func TestCost_TypicalChatCall(t *testing.T) {
	// Mixed-cache turn: 800 miss + 200 hit + 100 completion under
	// the V4-Flash peak rate card (the fallback for unknown models).
	u := deepseek.Usage{
		PromptTokens:          1000,
		PromptCacheMissTokens: 800,
		PromptCacheHitTokens:  200,
		CompletionTokens:      100,
	}
	want := 800*0.44/1e6 + 200*0.014/1e6 + 100*1.32/1e6
	got := Cost(deepseek.ModelV4Flash, TierStandard, u)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

func TestCost_OffPeakIsHalf(t *testing.T) {
	u := deepseek.Usage{PromptCacheMissTokens: 1_000_000, CompletionTokens: 1_000_000}
	std := Cost(deepseek.ModelV4Flash, TierStandard, u)
	off := Cost(deepseek.ModelV4Flash, TierOffPeak, u)
	if math.Abs(off-std*0.5) > 1e-6 {
		t.Errorf("off-peak cost != half: std=%v off=%v", std, off)
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
	if got := FormatCost(0.001234); got != "$0.0012" {
		t.Errorf("FormatCost = %q", got)
	}
}
