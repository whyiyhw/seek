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
// contract: the retired deepseek-v4-flash / deepseek-v4-flash-vision-exp /
// deepseek-v4-pro ids are served by V4.1 Flash and billed at Flash
// prices — at BOTH tiers. They carry no registry entries, so this is
// literally the unknown-model fallback doing the vendor's billing for
// it.
func TestPricingFor_RetiredIdsBilledAtFlashCard(t *testing.T) {
	for _, m := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"} {
		for _, tier := range []Tier{TierStandard, TierOffPeak} {
			if p, want := PricingFor(m, tier), PricingFor(deepseek.ModelV41Flash, tier); p != want {
				t.Errorf("PricingFor(%s, %v) = %+v, want the V4.1-Flash card %+v", m, tier, p, want)
			}
		}
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
