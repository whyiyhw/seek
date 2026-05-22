package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// stripANSI is a quick & lossy escape stripper so tests can assert on
// the textual content of styled lipgloss output.
func stripANSI(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// Skip to next 'm'.
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// TestStatusBar_UpgradeAvailable verifies the "↑ <tag>" segment lands
// in the bar when a newer release was detected at startup. Empty
// UpgradeAvailable must produce no upgrade segment — otherwise we'd
// leave a stray "↑" with no version.
func TestStatusBar_UpgradeAvailable(t *testing.T) {
	with := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:            "deepseek-chat",
		UpgradeAvailable: "v0.2.0",
		Width:            120,
	}))
	if !strings.Contains(with, "↑ v0.2.0") {
		t.Errorf("expected upgrade hint, bar = %q", with)
	}

	without := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-chat",
		Width: 120,
	}))
	if strings.Contains(without, "↑") {
		t.Errorf("upgrade segment leaked when UpgradeAvailable is empty: %q", without)
	}
}

func TestStatusBar_Idle_Standard(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai) // standard
	nt, na := pricing.NextTransition(at)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:    deepseek.ModelChat,
		Tier:     pricing.CurrentTier(at),
		NextTier: nt,
		NextAt:   na,
		// Zero Usage — no turns yet → cache shows "n/a".
		Now: at,
	}))
	for _, frag := range []string{"seek", "deepseek-chat", "idle", "cache n/a", "cost $0.0000", "standard", "next 🌙 in"} {
		if !strings.Contains(bar, frag) {
			t.Errorf("missing %q in: %q", frag, bar)
		}
	}
}

func TestStatusBar_Streaming_Yolo(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:     deepseek.ModelChat,
		Yolo:      true,
		Streaming: true,
		Tier:      pricing.CurrentTier(at),
		Now:       at,
	}))
	for _, frag := range []string{"YOLO", "streaming"} {
		if !strings.Contains(bar, frag) {
			t.Errorf("missing %q in: %q", frag, bar)
		}
	}
}

func TestStatusBar_OffPeak(t *testing.T) {
	at := time.Date(2026, time.January, 15, 3, 0, 0, 0, pricing.Shanghai) // off-peak
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: deepseek.ModelChat,
		Tier:  pricing.CurrentTier(at),
		Now:   at,
	}))
	if !strings.Contains(bar, "off-peak") {
		t.Errorf("missing off-peak label: %q", bar)
	}
	// No "next 🌙 in" countdown during off-peak.
	if strings.Contains(bar, "next 🌙") {
		t.Errorf("countdown should not show during off-peak: %q", bar)
	}
}

func TestStatusBar_CountsAndCost(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:     deepseek.ModelChat,
		Turns:     5,
		ToolCalls: 3,
		Tier:      pricing.TierStandard,
		Usage: deepseek.Usage{
			PromptCacheMissTokens: 1_000_000,
			CompletionTokens:      1_000_000,
		},
		Now: at,
	}))
	if !strings.Contains(bar, "turns:5") || !strings.Contains(bar, "tools:3") {
		t.Errorf("counters missing: %q", bar)
	}
	// V4-Flash rates: 1M miss * $0.14 + 1M completion * $0.28 = $0.42.
	// (deepseek-chat aliases to V4-Flash post the 2026-01 V4 launch.)
	if !strings.Contains(bar, "$0.4200") {
		t.Errorf("cost missing: %q", bar)
	}
}

func TestStatusBar_StreamingElapsed(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	// Under 1s: should show plain "● streaming"
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:         deepseek.ModelChat,
		Streaming:     true,
		StreamElapsed: 500 * time.Millisecond,
		Tier:          pricing.TierStandard,
		Now:           at,
	}))
	if !strings.Contains(bar, "● streaming") {
		t.Errorf("under 1s should show '● streaming': %q", bar)
	}
	if strings.Contains(bar, "↓~") {
		t.Errorf("should not show token estimate under 1s: %q", bar)
	}

	// Over 1s, no bytes yet: show elapsed only
	bar = stripANSI(RenderStatusBar(StatusSnapshot{
		Model:         deepseek.ModelChat,
		Streaming:     true,
		StreamElapsed: 7 * time.Second,
		Tier:          pricing.TierStandard,
		Now:           at,
	}))
	if !strings.Contains(bar, "● 7s") {
		t.Errorf("should show elapsed '● 7s': %q", bar)
	}

	// Over 1s with bytes: show elapsed + token estimate
	bar = stripANSI(RenderStatusBar(StatusSnapshot{
		Model:            deepseek.ModelChat,
		Streaming:        true,
		StreamElapsed:    54 * time.Second,
		StreamDeltaBytes: 12000, // 12000/4 = 3000 → "3.0ktok"
		Tier:             pricing.TierStandard,
		Now:              at,
	}))
	if !strings.Contains(bar, "● 54s") {
		t.Errorf("should show '● 54s': %q", bar)
	}
	if !strings.Contains(bar, "↓~3.0ktok") {
		t.Errorf("should show token estimate '↓~3.0ktok': %q", bar)
	}
}

func TestFormatStreamElapsed(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{90 * time.Second, "1m30s"},
		{125 * time.Second, "2m5s"},
	}
	for _, c := range cases {
		if got := formatStreamElapsed(c.in); got != c.want {
			t.Errorf("formatStreamElapsed(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatTokenEst(t *testing.T) {
	cases := []struct {
		bytes int
		want  string
	}{
		{0, "0tok"},
		{400, "100tok"},     // 400/4 = 100
		{3999, "999tok"},    // 3999/4 = 999
		{4000, "1.0ktok"},   // 4000/4 = 1000
		{12000, "3.0ktok"},  // 12000/4 = 3000
		{40000, "10.0ktok"}, // 40000/4 = 10000
	}
	for _, c := range cases {
		if got := formatTokenEst(c.bytes); got != c.want {
			t.Errorf("formatTokenEst(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "now"},
		{30 * time.Second, "<1m"},
		{5 * time.Minute, "5m"},
		{125 * time.Minute, "2h5m"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
