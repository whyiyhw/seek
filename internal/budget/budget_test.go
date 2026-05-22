package budget

import "testing"

// pct returns floor(limit * fraction). Helper because Go's constant
// folding refuses to truncate a constant float to int at compile time,
// even when both operands are known.
func pct(limit int, frac float64) int {
	return int(float64(limit) * frac)
}

func TestLimit_KnownModelAndFallback(t *testing.T) {
	// V4 launched with a 1M window for both flash and pro; the legacy
	// chat/reasoner names route to V4 server-side.
	if got := Limit("deepseek-chat"); got != 1_000_000 {
		t.Errorf("deepseek-chat = %d, want 1M", got)
	}
	if got := Limit("deepseek-v4-flash"); got != 1_000_000 {
		t.Errorf("deepseek-v4-flash = %d, want 1M", got)
	}
	if got := Limit("unknown-model-x"); got != Default {
		t.Errorf("fallback = %d, want %d", got, Default)
	}
}

func TestClassify_Boundaries(t *testing.T) {
	m := "deepseek-chat"
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
	limit := Limit("deepseek-chat")
	if got := Fraction("deepseek-chat", limit); got != 1.0 {
		t.Errorf("at limit = %v, want 1.0", got)
	}
	if got := Fraction("unknown", 0); got != 0 {
		t.Errorf("zero usage = %v", got)
	}
}
