package budget

import (
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
)

// pct returns floor(limit * fraction). Helper because Go's constant
// folding refuses to truncate a constant float to int at compile time,
// even when both operands are known.
func pct(limit int, frac float64) int {
	return int(float64(limit) * frac)
}

func TestLimit_KnownModelAndFallback(t *testing.T) {
	// deepseek-flash is the one live id — 1M context since the V4
	// launch. Retired V4 ids are plain unknowns and hit the
	// conservative Default, same as any custom model.
	if got := Limit("deepseek-flash"); got != 1_000_000 {
		t.Errorf("deepseek-flash = %d, want 1M", got)
	}
	// GLM lane (per-agent routing, feature-orchestration.md D9): the
	// single 1M-context tier — keeps /compact warnings honest for
	// GLM-routed subagents.
	if got := Limit("glm-5.3"); got != 1_000_000 {
		t.Errorf("glm-5.3 = %d, want 1M", got)
	}
	// deepseek-v4-pro is NOT retired any more (own model since the
	// 2026-11 re-listing — see pricing.ModelV4Pro), so it carries a
	// real limit rather than the conservative fallback.
	if got := Limit("deepseek-v4-pro"); got != 1_000_000 {
		t.Errorf("deepseek-v4-pro = %d, want 1M", got)
	}
	for _, retired := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		if got := Limit(retired); got != Default {
			t.Errorf("%s = %d, want Default %d", retired, got, Default)
		}
	}
	if got := Limit("unknown-model-x"); got != Default {
		t.Errorf("fallback = %d, want %d", got, Default)
	}
}

// TestLimit_OpenCodeGoCatalogue pins the lane against the other half of
// the same invariant pricing tests: every model the lane's menus offer
// must have an explicit window. A missing entry silently falls to
// Default (128K), which would nag /compact at an eighth of a 1M window.
func TestLimit_OpenCodeGoCatalogue(t *testing.T) {
	for _, m := range pricing.OpenCodeGoModels {
		got, ok := contextLimits[m.ID]
		if !ok {
			t.Errorf("%s: no explicit context limit (would fall to Default %d)", m.ID, Default)
			continue
		}
		if got <= Default {
			t.Errorf("%s = %d, want > Default %d", m.ID, got, Default)
		}
	}
}

func TestClassify_Boundaries(t *testing.T) {
	m := "deepseek-v4-flash"
	limit := Limit(m) // 1M after V4
	cases := []struct {
		used int
		want Severity
	}{
		{0, SeverityOK},
		// Strictly above/below the threshold so int truncation in pct()
		// doesn't put us a fraction-of-a-token across the boundary.
		{pct(limit, 0.59), SeverityOK},
		{pct(limit, 0.61), SeverityWarn},
		{pct(limit, 0.70), SeverityWarn},
		{pct(limit, 0.76), SeverityCritical},
		{limit + 1, SeverityCritical},
	}
	for _, c := range cases {
		if got := Classify(m, c.used); got != c.want {
			t.Errorf("Classify(%d/%d) = %v, want %v", c.used, limit, got, c.want)
		}
	}
}

func TestFraction(t *testing.T) {
	limit := Limit("deepseek-v4-flash")
	if got := Fraction("deepseek-v4-flash", limit); got != 1.0 {
		t.Errorf("at limit = %v, want 1.0", got)
	}
	if got := Fraction("unknown", 0); got != 0 {
		t.Errorf("zero usage = %v", got)
	}
}
