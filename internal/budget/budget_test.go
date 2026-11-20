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

// TestAutoCompactAt pins the trigger resolution: an explicit token count
// wins and ignores the model; a fraction scales the live window; zero /
// negative means off; and a fraction above 1 is NOT clamped (a trigger
// past the window simply never fires — the safe direction for a typo).
func TestAutoCompactAt(t *testing.T) {
	m := "deepseek-flash" // 1M
	cases := []struct {
		name     string
		model    string
		tokens   int
		fraction float64
		want     int
	}{
		{"off — both zero", m, 0, 0, 0},
		{"tokens only", m, 350_000, 0, 350_000},
		{"fraction only", m, 0, 0.35, 350_000},
		{"tokens win over fraction", m, 350_000, 0.5, 350_000},
		{"fraction follows a smaller window", "gpt-4o", 0, 0.35, 44_800},
		{"negative fraction is off", m, 0, -0.1, 0},
		{"fraction > 1 is not clamped", m, 0, 1.5, 1_500_000},
	}
	for _, c := range cases {
		if got := AutoCompactAt(c.model, c.tokens, c.fraction); got != c.want {
			t.Errorf("%s: AutoCompactAt(%q, %d, %v) = %d, want %d",
				c.name, c.model, c.tokens, c.fraction, got, c.want)
		}
	}
}

// TestClassifyAt_MatchesCompiledFractions pins ClassifyAt to Classify
// when no trigger is configured — the two paths must not drift, or the
// status bar's colours would change for users who never touch the
// setting. The warn point is 0.8 × critical, which reproduces the
// compiled 0.60/0.75 pair exactly.
func TestClassifyAt_MatchesCompiledFractions(t *testing.T) {
	m := "deepseek-flash"
	limit := Limit(m)
	for _, used := range []int{0, pct(limit, 0.3), pct(limit, 0.61), pct(limit, 0.70), pct(limit, 0.76), limit + 1} {
		if got, want := ClassifyAt(m, used, 0), Classify(m, used); got != want {
			t.Errorf("ClassifyAt(%d, 0) = %v, want Classify = %v", used, got, want)
		}
	}
}

// TestClassifyAt_ConfiguredTriggerMovesEscalation: with a trigger set,
// critical lands AT the trigger and warn at 80% of it — so a 350k
// trigger on a 1M window goes red at 350k rather than at 750k.
func TestClassifyAt_ConfiguredTriggerMovesEscalation(t *testing.T) {
	m := "deepseek-flash"
	at := 350_000
	cases := []struct {
		used int
		want Severity
	}{
		{0, SeverityOK},
		{279_999, SeverityOK},
		{280_000, SeverityWarn}, // 0.8 × 350k
		{349_999, SeverityWarn},
		{350_000, SeverityCritical},
		{900_000, SeverityCritical},
	}
	for _, c := range cases {
		if got := ClassifyAt(m, c.used, at); got != c.want {
			t.Errorf("ClassifyAt(%d, %d) = %v, want %v", c.used, at, got, c.want)
		}
	}
}
