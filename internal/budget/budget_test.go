package budget

import "testing"

// pct returns floor(limit * fraction). Helper because Go's constant
// folding refuses to truncate a constant float to int at compile time,
// even when both operands are known.
func pct(limit int, frac float64) int {
	return int(float64(limit) * frac)
}

func TestLimit_KnownModelAndFallback(t *testing.T) {
	if got := Limit("deepseek-chat"); got != 65_536 {
		t.Errorf("deepseek-chat = %d", got)
	}
	if got := Limit("unknown-model-x"); got != Default {
		t.Errorf("fallback = %d, want %d", got, Default)
	}
}

func TestClassify_Boundaries(t *testing.T) {
	m := "deepseek-chat" // limit 65536
	cases := []struct {
		used int
		want Severity
	}{
		{0, SeverityOK},
		// Using values strictly above the threshold so int truncation
		// in pct() doesn't put us a fraction-of-a-token below the
		// boundary and flip a tier.
		{pct(65536, 0.79), SeverityOK},                   // just below warn
		{pct(65536, 0.81), SeverityWarn},                 // just into warn
		{pct(65536, 0.90), SeverityWarn},                 // mid-warn band
		{pct(65536, 0.96), SeverityCritical},             // just into critical
		{99_999, SeverityCritical},                       // over the limit
	}
	for _, c := range cases {
		if got := Classify(m, c.used); got != c.want {
			t.Errorf("Classify(%d/%d) = %v, want %v", c.used, Limit(m), got, c.want)
		}
	}
}

func TestFraction(t *testing.T) {
	if got := Fraction("deepseek-chat", 65_536); got != 1.0 {
		t.Errorf("at limit = %v, want 1.0", got)
	}
	if got := Fraction("unknown", 0); got != 0 {
		t.Errorf("zero usage = %v", got)
	}
}
