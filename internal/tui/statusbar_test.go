package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

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
// indicator (PRD §4.3). Non-zero count shows "agents N";
// zero count suppresses entirely. Label-first ASCII form —
// no ⤴ glyph — so the bar stays free of ambiguous-width
// characters (the soft-wrap/banner-ghost pitfall class).
func TestStatusBar_SubagentBadge(t *testing.T) {
	// Zero — no badge.
	zero := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-flash",
		Width: 120,
		// SubagentsActive: 0 by default
	}))
	if strings.Contains(zero, "agent") {
		t.Errorf("agent label leaked with zero active: %q", zero)
	}

	// One.
	one := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-flash",
		Width:           120,
		SubagentsActive: 1,
	}))
	if !strings.Contains(one, "agents 1") {
		t.Errorf("expected 'agents 1' badge, got: %q", one)
	}
	if strings.Contains(one, "⤴") {
		t.Errorf("ambiguous-width glyph leaked into the bar: %q", one)
	}

	// Three.
	three := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-flash",
		Width:           120,
		SubagentsActive: 3,
	}))
	if !strings.Contains(three, "agents 3") {
		t.Errorf("expected 'agents 3' badge, got: %q", three)
	}
}

// TestStatusBar_CronBadge pins the v5 柱 H status-bar
// indicator: "cron N" when N > 0; suppressed at zero.
// Label-first ASCII form — no ⏰ glyph — same rationale as
// the agent badge.
func TestStatusBar_CronBadge(t *testing.T) {
	// Zero → no badge.
	zero := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-flash",
		Width: 120,
	}))
	if strings.Contains(zero, "cron") {
		t.Errorf("cron label leaked at count=0: %q", zero)
	}

	// One.
	one := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-flash",
		Width:           120,
		CronsRegistered: 1,
	}))
	if !strings.Contains(one, "cron 1") {
		t.Errorf("expected 'cron 1' badge, got: %q", one)
	}
	if strings.Contains(one, "⏰") {
		t.Errorf("ambiguous-width glyph leaked into the bar: %q", one)
	}

	// Many.
	many := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:           "deepseek-flash",
		Width:           120,
		CronsRegistered: 7,
	}))
	if !strings.Contains(many, "cron 7") {
		t.Errorf("expected 'cron 7' badge, got: %q", many)
	}
	if strings.Contains(many, "crons") {
		t.Errorf("cron should not take plural -s: %q", many)
	}
}

// TestStatusBar_UpgradeAvailable verifies the "new <tag>" segment lands
// in the bar when a newer release was detected at startup. Empty
// UpgradeAvailable must produce no upgrade segment — otherwise we'd
// leave a stray "new" with no version. (The old "↑ " prefix was
// retired: U+2191 is East-Asian-ambiguous, the same width-oracle class
// as the ⤴ glyph this bar already bans.)
func TestStatusBar_UpgradeAvailable(t *testing.T) {
	with := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:            "deepseek-flash",
		UpgradeAvailable: "v0.2.0",
		Width:            120,
	}))
	if !strings.Contains(with, "new v0.2.0") {
		t.Errorf("expected upgrade hint, bar = %q", with)
	}

	without := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: "deepseek-flash",
		Width: 120,
	}))
	if strings.Contains(without, "new ") {
		t.Errorf("upgrade segment leaked when UpgradeAvailable is empty: %q", without)
	}
}

func TestStatusBar_Idle_Standard(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai) // peak window
	nt, na := pricing.NextTransition(at)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:    deepseek.ModelV41Flash,
		Tier:     pricing.CurrentTier(at),
		NextTier: nt,
		NextAt:   na,
		// Zero Usage — no turns yet → cache shows "n/a".
		Now: at,
	}))
	for _, frag := range []string{"seek ", "deepseek-flash", "cache n/a", "cost $0", "peak", "off-peak in"} {
		if !strings.Contains(bar, frag) {
			t.Errorf("missing %q in: %q", frag, bar)
		}
	}
	// "○ idle" is retired: idle is the resting state, and the segment's
	// absence is the signal — the ● streaming label appearing IS the
	// event.
	if strings.Contains(bar, "idle") {
		t.Errorf("idle segment retired — absence is the signal; got %q", bar)
	}
}

func TestStatusBar_Streaming_Yolo(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:     deepseek.ModelV41Flash,
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
		Model: deepseek.ModelV41Flash,
		Plan:  true,
		Tier:  pricing.CurrentTier(at),
		Now:   at,
	}))
	if !strings.Contains(bar, "PLAN") {
		t.Errorf("missing PLAN badge in: %q", bar)
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
		Model:        deepseek.ModelV41Flash,
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
		Model:        deepseek.ModelV41Flash,
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
		Model:        deepseek.ModelV41Flash,
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
		Model: deepseek.ModelV41Flash,
		Tier:  pricing.CurrentTier(at),
		Now:   at,
	}))
	if !strings.Contains(bar, "off-peak") {
		t.Errorf("missing off-peak label: %q", bar)
	}
	// No "off-peak in" countdown during off-peak — the badge alone.
	if strings.Contains(bar, "off-peak in ") {
		t.Errorf("countdown should not show during off-peak: %q", bar)
	}
}

func TestStatusBar_Cost(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	// CumulativeCost is a pre-computed input into the bar, not
	// re-derived from Usage × current rates at render time. The bar
	// just formats whatever number the tracker locked in. Tests for
	// the locked-in math live in internal/cache/cache_test.go
	// (TestTracker_CumulativeCostLockedInAtRecord).
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model: deepseek.ModelV41Flash,
		Tier:  pricing.TierStandard,
		Usage: deepseek.Usage{
			PromptCacheMissTokens: 1_000_000,
			CompletionTokens:      1_000_000,
		},
		CumulativeCost: 0.42,
		Now:            at,
	}))
	// Sub-dollar costs read in thousandths: $0.420.
	if !strings.Contains(bar, "cost $0.420") {
		t.Errorf("cost missing: %q", bar)
	}
	// Turn/tool counters are retired from the bar — they live in
	// /diagnose and the exit summary.
	if strings.Contains(bar, "turns") || strings.Contains(bar, "tools") {
		t.Errorf("retired counters leaked back: %q", bar)
	}
}

// TestCacheTinted pins the ≥80% green floor: a low-but-nonzero hit
// ratio (12%) must not read as healthy, and a fresh session (no
// cache-accounted tokens at all, HitRatio 0) must stay uncoloured
// rather than triggering a warning tint on every session start.
func TestCacheTinted(t *testing.T) {
	cases := []struct {
		name      string
		hit, miss int
		want      bool
	}{
		{"no data", 0, 0, false},
		{"all miss", 0, 100, false},
		{"12%", 12, 88, false},
		{"79%", 79, 21, false},
		{"80% floor", 80, 20, true},
		{"warm", 99, 1, true},
		{"all hit", 100, 0, true},
	}
	for _, c := range cases {
		u := deepseek.Usage{PromptCacheHitTokens: c.hit, PromptCacheMissTokens: c.miss}
		if got := cacheTinted(u); got != c.want {
			t.Errorf("%s: cacheTinted(%d hit / %d accounted) = %v, want %v",
				c.name, c.hit, c.hit+c.miss, got, c.want)
		}
	}
}

func TestStatusBar_StreamingElapsed(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	// Under 1s: should show plain "● streaming"
	bar := stripANSI(RenderStatusBar(StatusSnapshot{
		Model:         deepseek.ModelV41Flash,
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
		Model:         deepseek.ModelV41Flash,
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
		Model:            deepseek.ModelV41Flash,
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
// the bar sheds low/medium-priority metrics, keeps the pinned identity
// (the model id) + mode badge, and never wraps to a second line.
func TestRenderStatusBar_FoldsLowPriorityWhenNarrow(t *testing.T) {
	t.Parallel()
	rich := StatusSnapshot{
		Model:            "deepseek-flash",
		Yolo:             true,
		CumulativeCost:   0.42,
		CronsRegistered:  2,
		UpgradeAvailable: "v0.9.0",
	}

	wide := rich
	wide.Width = 200
	w := stripANSI(RenderStatusBar(wide))
	for _, frag := range []string{"seek ", "YOLO", "cron 2", "new v0.9.0", "cost"} {
		if !strings.Contains(w, frag) {
			t.Fatalf("wide bar should contain %q; got %q", frag, w)
		}
	}
	// Turn/tool counters are retired — the widest bar shows no trace.
	if strings.Contains(w, "turns") || strings.Contains(w, "tools") {
		t.Errorf("retired counters leaked into the wide bar: %q", w)
	}

	narrow := rich
	narrow.Width = 28
	n := RenderStatusBar(narrow)
	plain := stripANSI(n)
	if !strings.Contains(plain, "deepseek-flash") || !strings.Contains(plain, "YOLO") {
		t.Errorf("narrow bar must keep the pinned model id + mode badge; got %q", plain)
	}
	for _, frag := range []string{"seek ", "cache", "cost", "cron", "new v0.9.0"} {
		if strings.Contains(plain, frag) {
			t.Errorf("narrow bar should have folded away %q; got %q", frag, plain)
		}
	}
	// Exactly the pinned pair survives — nothing else. (Field-join
	// normalises badge padding; any extra surviving segment would add
	// a token and fail the comparison.)
	if got := strings.Join(strings.Fields(plain), " "); got != "deepseek-flash YOLO" {
		t.Errorf("narrow bar = %q, want exactly \"deepseek-flash YOLO\"", got)
	}
	if strings.Contains(n, "\n") {
		t.Errorf("status bar must stay single-line; got %q", n)
	}
}

// TestRenderStatusBar_NeverExceedsTerminalWidth pins the ghost-banner
// fix: whatever the width oracle disagreement on ambiguous glyphs (☀️/○
// render wider on CJK-locale terminals than lipgloss measures), the
// bar's physically-rendered width must stay within the frame width — a
// soft-wrapped status line desyncs bubbletea's frame line-count and
// ghosts the previous frame's top block (the duplicated welcome banner
// after a /model switch to the longer vision-exp id).
func TestRenderStatusBar_NeverExceedsTerminalWidth(t *testing.T) {
	for _, w := range []int{40, 60, 80, 90, 100, 110, 120, 140, 180} {
		// Critical ctx is pinned, so at the small widths the pin stack
		// alone overflows the budget: the contract is that the bar
		// TRUNCATES (MaxWidth backstop) and stays one line — a wrapped
		// bar would desync bubbletea's frame accounting.
		s := StatusSnapshot{
			Model: "deepseek-flash", Effort: "max",
			Tier: pricing.TierStandard, NextTier: pricing.TierOffPeak,
			LastUsage: deepseek.Usage{PromptTokens: 10_000_000}, // force ctx critical
			Width:     w,
		}
		out := stripANSI(RenderStatusBar(s))
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 1 {
			t.Errorf("width=%d: bar must stay single-line, got %d lines", w, len(lines))
		}
		// runewidth is the strictest oracle in play (counts ○ as 2);
		// staying within it guarantees no wrap on any mainstream terminal.
		if got := runewidth.StringWidth(lines[0]); got > w {
			t.Errorf("width=%d: bar physically %d cols: %q", w, got, lines[0])
		}
	}
}

// TestStatusBar_NoRetiredEmojiGlyphs pins the ASCII-only bar contract:
// the glyphs retired for width-oracle reasons (☀️ VS16 measures 1
// renders 2; ⤴/⚠/↑ East-Asian-ambiguous; 🌙/⏰/🎯 font-dependent Wide)
// must never reappear in ANY bar state. Renders a maximal snapshot —
// every badge lit, ctx at critical, standard tier with countdown — so
// a regression in any segment trips the test.
func TestStatusBar_NoRetiredEmojiGlyphs(t *testing.T) {
	at := time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai)
	nt, na := pricing.NextTransition(at)
	for _, tier := range []pricing.Tier{pricing.TierStandard, pricing.TierOffPeak} {
		bar := stripANSI(RenderStatusBar(StatusSnapshot{
			Model: deepseek.ModelV41Flash, Effort: "max",
			Tier: tier, NextTier: nt, NextAt: na, Now: at,
			Streaming: true,
			StreamElapsed: 5 * time.Second, StreamDeltaBytes: 4096,
			LastUsage:        deepseek.Usage{PromptTokens: 10_000_000}, // force ctx critical
			SubagentsActive:  2,
			CronsRegistered:  3,
			GoalActive:       true,
			GoalTurns:        2,
			GoalMaxTurns:     8,
			UpgradeAvailable: "v9.9.9",
			Width:            0, // width 0 = every segment rendered, none folded
		}))
		for _, glyph := range []string{"☀", "🌙", "⤴", "⏰", "🎯", "⚠", "↑"} {
			if strings.Contains(bar, glyph) {
				t.Errorf("tier=%v: retired glyph %q back in the bar: %q", tier, glyph, bar)
			}
		}
	}
}

// TestRenderModelPicker_RowsFitNarrowTerminal: every picker row —
// including the "(current)" suffix on the longest id — must fit a
// 100-col terminal. An overlong row wraps in the popup and desyncs the
// frame (same failure class as the status bar).
func TestRenderModelPicker_RowsFitNarrowTerminal(t *testing.T) {
	m := testModel().Build()
	m.opts.Model = "deepseek-flash"
	m.modelPickerFiltered = knownModelsForProvider("")
	m.pickerPurpose = "model"
	for i := range m.modelPickerFiltered {
		m.modelPickerSelected = i
		out := m.renderModelPicker()
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := lipgloss.Width(stripANSI(line)); w > 100 {
				t.Errorf("picker row %d physically %d cols (>100): %q", i, w, stripANSI(line))
			}
		}
	}
}
