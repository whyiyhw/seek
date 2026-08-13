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

// TestStatusBar_SubagentBadge pins the v5 柱 G status-bar
// indicator (PRD §4.3). Non-zero count shows "⤴ N agents";
// zero count suppresses entirely. Singular/plural matter
// because users WILL nitpick "⤴ 1 agents" reading.
func TestStatusBar_SubagentBadge(t *testing.T) {
	// Zero — no badge.
	zero := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-v4-flash",
		Width: 120,
		// SubagentsActive: 0 by default
	}))
	if strings.Contains(zero, "⤴") {
		t.Errorf("agent badge leaked with zero active: %q", zero)
	}
	if strings.Contains(zero, "agent") {
		t.Errorf("agent label leaked with zero active: %q", zero)
	}

	// One — singular.
	one := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-v4-flash",
		Width:           120,
		SubagentsActive: 1,
	}))
	if !strings.Contains(one, "⤴ 1 agent") {
		t.Errorf("expected '⤴ 1 agent' badge, got: %q", one)
	}
	if strings.Contains(one, "1 agents") {
		t.Errorf("singular pluralisation wrong: %q", one)
	}

	// Three — plural.
	three := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-v4-flash",
		Width:           120,
		SubagentsActive: 3,
	}))
	if !strings.Contains(three, "⤴ 3 agents") {
		t.Errorf("expected '⤴ 3 agents' badge, got: %q", three)
	}
}

// TestStatusBar_CronBadge pins the v5 柱 H status-bar
// indicator: "⏰ N cron" when N > 0; suppressed at zero.
// Unlike the agent badge, the unit ("cron") doesn't take a
// plural -s — "5 cron" reads more naturally than "5 crons"
// when "cron" refers to scheduled jobs rather than the
// scheduler itself. Test verifies both forms.
func TestStatusBar_CronBadge(t *testing.T) {
	// Zero → no badge.
	zero := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-v4-flash",
		Width: 120,
	}))
	if strings.Contains(zero, "⏰") {
		t.Errorf("cron badge leaked at count=0: %q", zero)
	}
	if strings.Contains(zero, " cron") {
		t.Errorf("cron label leaked at count=0: %q", zero)
	}

	// One.
	one := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-v4-flash",
		Width:           120,
		CronsRegistered: 1,
	}))
	if !strings.Contains(one, "⏰ 1 cron") {
		t.Errorf("expected '⏰ 1 cron' badge, got: %q", one)
	}

	// Many — same unit, no -s. The agent badge takes -s for
	// the noun "agent"; cron is treated as already-plural
	// (like "sheep").
	many := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-v4-flash",
		Width:           120,
		CronsRegistered: 7,
	}))
	if !strings.Contains(many, "⏰ 7 cron") {
		t.Errorf("expected '⏰ 7 cron' badge, got: %q", many)
	}
	if strings.Contains(many, "crons") {
		t.Errorf("cron should not take plural -s: %q", many)
	}
}

// TestStatusBar_UpgradeAvailable verifies the "↑ <tag>" segment lands
// in the bar when a newer release was detected at startup. Empty
// UpgradeAvailable must produce no upgrade segment — otherwise we'd
// leave a stray "↑" with no version.
func TestStatusBar_UpgradeAvailable(t *testing.T) {
	with := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:            "deepseek-v4-flash",
		UpgradeAvailable: "v0.2.0",
		Width:            120,
	}))
	if !strings.Contains(with, "↑ v0.2.0") {
		t.Errorf("expected upgrade hint, bar = %q", with)
	}

	without := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-v4-flash",
		Width: 120,
	}))
	if strings.Contains(without, "↑") {
		t.Errorf("upgrade segment leaked when UpgradeAvailable is empty: %q", without)
	}
}

func TestStatusBar_Idle_Standard(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai) // peak window
	nt, na := pricing.NextTransition(at)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:    deepseek.ModelV4Flash,
		Tier:     pricing.CurrentTier(at),
		NextTier: nt,
		NextAt:   na,
		// Zero Usage — no turns yet → cache shows "n/a".
		Now: at,
	}))
	for _, frag := range []string{"seek", "deepseek-v4-flash", "idle", "cache n/a", "cost $0.0000", "peak", "next 🌙 in"} {
		if !strings.Contains(bar, frag) {
			t.Errorf("missing %q in: %q", frag, bar)
		}
	}
}

func TestStatusBar_Streaming_Yolo(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:     deepseek.ModelV4Flash,
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

func TestStatusBar_Idle_Plan(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: deepseek.ModelV4Flash,
		Plan:  true,
		Tier:  pricing.CurrentTier(at),
		Now:   at,
	}))
	for _, frag := range []string{"PLAN", "idle"} {
		if !strings.Contains(bar, frag) {
			t.Errorf("missing %q in: %q", frag, bar)
		}
	}
	// PLAN and YOLO are mutually exclusive — PLAN badge means no YOLO badge.
	if strings.Contains(bar, "YOLO") {
		t.Error("PLAN mode should not show YOLO badge")
	}
	// No substate = legacy callers / pre-substate test fixtures should
	// see the plain "PLAN" badge, not "PLAN:ANALYZE" or "PLAN:EXEC".
	if strings.Contains(bar, "ANALYZE") || strings.Contains(bar, "EXEC") {
		t.Errorf("empty substate must render plain PLAN badge, got: %q", bar)
	}
}

func TestStatusBar_Idle_PlanAnalyze(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:        deepseek.ModelV4Flash,
		Plan:         true,
		PlanSubstate: "analyze",
		Tier:         pricing.CurrentTier(at),
		Now:          at,
	}))
	if !strings.Contains(bar, "PLAN:ANALYZE") {
		t.Errorf("missing PLAN:ANALYZE badge in: %q", bar)
	}
	if strings.Contains(bar, "EXEC") {
		t.Errorf("analyze substate must not show EXEC: %q", bar)
	}
}

func TestStatusBar_Idle_PlanExecute(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:        deepseek.ModelV4Flash,
		Plan:         true,
		PlanSubstate: "execute",
		Tier:         pricing.CurrentTier(at),
		Now:          at,
	}))
	if !strings.Contains(bar, "PLAN:EXEC") {
		t.Errorf("missing PLAN:EXEC badge in: %q", bar)
	}
	if strings.Contains(bar, "ANALYZE") {
		t.Errorf("execute substate must not show ANALYZE: %q", bar)
	}
}

func TestStatusBar_PlanSubstateIgnoredWhenPlanOff(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	// Plan=false should suppress any substate badge — defensive against
	// a stale substate value lingering after /plan off.
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:        deepseek.ModelV4Flash,
		Plan:         false,
		PlanSubstate: "execute",
		Tier:         pricing.CurrentTier(at),
		Now:          at,
	}))
	if strings.Contains(bar, "PLAN") {
		t.Errorf("Plan=false must not show any PLAN badge: %q", bar)
	}
}

func TestStatusBar_OffPeak(t *testing.T) {
	at := time.Date(2026, time.January, 15, 3, 0, 0, 0, pricing.Shanghai) // off-peak
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: deepseek.ModelV4Flash,
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
	// CumulativeCost is now a pre-computed input into the bar, not
	// re-derived from Usage × current rates at render time. The bar
	// just formats whatever number the tracker locked in. Tests for
	// the locked-in math live in internal/cache/cache_test.go
	// (TestTracker_CumulativeCostLockedInAtRecord).
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:     deepseek.ModelV4Flash,
		Turns:     5,
		ToolCalls: 3,
		Tier:      pricing.TierStandard,
		Usage: deepseek.Usage{
			PromptCacheMissTokens: 1_000_000,
			CompletionTokens:      1_000_000,
		},
		CumulativeCost: 0.42,
		Now:            at,
	}))
	if !strings.Contains(bar, "turns:5") || !strings.Contains(bar, "tools:3") {
		t.Errorf("counters missing: %q", bar)
	}
	if !strings.Contains(bar, "$0.4200") {
		t.Errorf("cost missing: %q", bar)
	}
}

func TestStatusBar_StreamingElapsed(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	// Under 1s: should show plain "● streaming"
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:         deepseek.ModelV4Flash,
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
		Model:         deepseek.ModelV4Flash,
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
		Model:            deepseek.ModelV4Flash,
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

// TestRenderStatusBar_FoldsLowPriorityWhenNarrow pins the adaptive-fold
// contract: at a wide width every segment is present; at a narrow width
// the bar sheds low/medium-priority metrics, keeps the pinned identity +
// mode badge, and never wraps to a second line.
func TestRenderStatusBar_FoldsLowPriorityWhenNarrow(t *testing.T) {
	t.Parallel()
	rich := StatusSnapshot{
		Model:            "deepseek-v4-pro",
		Yolo:             true,
		Turns:            5,
		ToolCalls:        3,
		CumulativeCost:   0.42,
		CronsRegistered:  2,
		UpgradeAvailable: "v0.9.0",
	}

	wide := rich
	wide.Width = 200
	w := stripANSI(RenderStatusBar(wide))
	for _, frag := range []string{"seek", "YOLO", "turns:5", "⏰ 2 cron", "↑ v0.9.0", "cost"} {
		if !strings.Contains(w, frag) {
			t.Fatalf("wide bar should contain %q; got %q", frag, w)
		}
	}

	narrow := rich
	narrow.Width = 28
	n := RenderStatusBar(narrow)
	plain := stripANSI(n)
	if !strings.Contains(plain, "seek") || !strings.Contains(plain, "YOLO") {
		t.Errorf("narrow bar must keep pinned identity + mode badge; got %q", plain)
	}
	for _, frag := range []string{"turns:", "⏰", "↑ v0.9.0", "cost"} {
		if strings.Contains(plain, frag) {
			t.Errorf("narrow bar should have folded away %q; got %q", frag, plain)
		}
	}
	if strings.Contains(n, "\n") {
		t.Errorf("status bar must stay single-line; got %q", n)
	}
}
