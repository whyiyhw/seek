package modelcap

import "testing"

// TestVision_Static pins the compiled table. The retired DeepSeek ids —
// including the old -Exp vision build — are plain unknowns, and no id
// is ever matched by substring: a pattern would silently drift into a
// 400 the next time a vendor rotates a suffix.
func TestVision_Static(t *testing.T) {
	cases := map[string]bool{
		"deepseek-flash":               true, // deepseek.ModelV41Flash
		"deepseek-v4.1-flash":          true, // pricing.ModelOpenCodeV41Flash — the same model on the Go gateway
		"claude-sonnet-4-20250514":     true,
		"claude-3-5-sonnet-20241022":   true,
		"gpt-4o":                       true,
		"gpt-4o-mini":                  true,
		"gemini-2.0-flash":             true,
		"gemini-1.5-pro":               true,
		"deepseek-v4-flash-vision-exp": false, // retired, server-routed
		"deepseek-v4-flash":            false,
		"deepseek-v4-pro":              false,
		"deepseek-v4-flash-vision":     false, // never-shipped GA name — NOT matched
		"vision":                       false, // substring must never match
		"gpt-4o-audio":                 false, // prefix must never match
		"":                             false,
	}
	for model, want := range cases {
		if got := Vision(model); got != want {
			t.Errorf("Vision(%q) = %v, want %v", model, got, want)
		}
	}
}

// TestVision_GrantPromotes: a catalogue grant is how a model the
// compiled table has never heard of (every other OpenCode Go row today)
// becomes vision-capable without a release.
func TestVision_GrantPromotes(t *testing.T) {
	t.Cleanup(func() { SetVision(nil) })
	if Vision("kimi-k3") {
		t.Fatal("kimi-k3 must not be vision before the catalogue says so")
	}
	SetVision(map[string]bool{"kimi-k3": true})
	if !Vision("kimi-k3") {
		t.Error("grant did not promote kimi-k3")
	}
	// Grants never revoke: a false/missing entry leaves the static table
	// authoritative (see the package comment — an older cache has no
	// vision key at all, so absence cannot mean "no").
	SetVision(map[string]bool{"deepseek-flash": false})
	if !Vision("deepseek-flash") {
		t.Error("a false grant must not demote a static-table entry")
	}
	// Empty / blank ids are dropped, not stored as a "" grant.
	SetVision(map[string]bool{"": true})
	if Vision("") {
		t.Error("empty id must never be vision")
	}
	// SetVision(nil) clears the grants entirely.
	SetVision(nil)
	if Vision("kimi-k3") {
		t.Error("cleared grants must not survive")
	}
}

// TestVision_ConcurrentReadsDuringInstall: the catalogue refresh
// goroutine installs grants while turns read them; -race must stay
// clean and no read may observe a torn map.
func TestVision_ConcurrentReadsDuringInstall(t *testing.T) {
	t.Cleanup(func() { SetVision(nil) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			SetVision(map[string]bool{"kimi-k3": i%2 == 0})
		}
	}()
	for i := 0; i < 200; i++ {
		_ = Vision("kimi-k3")
		_ = Vision("deepseek-flash")
	}
	<-done
}
