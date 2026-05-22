package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestFormatCommittedDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		// Sub-100ms is suppressed — bytes/exit-code is enough info for
		// near-instant operations and the noise hurts readability.
		{0, ""},
		{50 * time.Millisecond, ""},
		{99 * time.Millisecond, ""},

		// 100ms..999ms with one decimal — readable, stable.
		{100 * time.Millisecond, "0.1s"},
		{800 * time.Millisecond, "0.8s"},

		// Whole seconds.
		{time.Second, "1s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},

		// Minutes + seconds.
		{time.Minute, "1m0s"},
		{125 * time.Second, "2m5s"},
		{10 * time.Minute, "10m0s"},
	}
	for _, c := range cases {
		if got := formatCommittedDuration(c.in); got != c.want {
			t.Errorf("formatCommittedDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatToolElapsed_SuppressesSubSecond(t *testing.T) {
	if got := formatToolElapsed(500 * time.Millisecond); got != "" {
		t.Errorf("sub-1s should be empty, got %q", got)
	}
	if got := formatToolElapsed(2 * time.Second); got != "2s" {
		t.Errorf("got %q, want 2s", got)
	}
}

func TestDurationTail(t *testing.T) {
	if got := durationTail(50 * time.Millisecond); got != "" {
		t.Errorf("sub-100ms should be empty: %q", got)
	}
	if got := durationTail(2 * time.Second); got != " · 2s" {
		t.Errorf("got %q", got)
	}
}

func TestFormatTokensK(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{99600, "99.6k"},
		{670000, "670.0k"},
	}
	for _, c := range cases {
		if got := formatTokensK(c.n); got != c.want {
			t.Errorf("formatTokensK(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestRenderTurnFooter_Format(t *testing.T) {
	tracker := cache.New()
	tracker.Record(deepseek.Usage{
		PromptTokens:          99600,
		PromptCacheHitTokens:  82000,
		PromptCacheMissTokens: 17600,
		CompletionTokens:      1700,
	}, deepseek.ModelV4Flash, pricing.TierStandard)
	m := Model{
		opts: Options{
			Model:   deepseek.ModelChat,
			Tracker: tracker,
		},
		turns:     3,
		toolCalls: 7,
		now:       time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai),
	}
	footer := stripANSI(m.renderTurnFooter())
	for _, frag := range []string{
		"turn 3",
		"7 tools",
		"↑99.6k prompt",
		"(82% cache)",
		"↓1.7k tok",
	} {
		if !strings.Contains(footer, frag) {
			t.Errorf("missing %q in footer: %q", frag, footer)
		}
	}
}

func TestRenderTurnFooter_NoCacheNote_WhenNoHits(t *testing.T) {
	tracker := cache.New()
	tracker.Record(deepseek.Usage{
		PromptTokens:          5000,
		PromptCacheHitTokens:  0,
		PromptCacheMissTokens: 5000,
		CompletionTokens:      200,
	}, deepseek.ModelV4Flash, pricing.TierStandard)
	m := Model{
		opts: Options{
			Model:   deepseek.ModelChat,
			Tracker: tracker,
		},
		turns:     1,
		toolCalls: 0,
		now:       time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai),
	}
	footer := stripANSI(m.renderTurnFooter())
	if strings.Contains(footer, "cache)") {
		t.Errorf("should not show cache note when no cache hits: %q", footer)
	}
	if !strings.Contains(footer, "↑5.0k prompt") {
		t.Errorf("missing prompt tokens: %q", footer)
	}
}

func TestStreamingLabel_SubSecond(t *testing.T) {
	m := Model{streamStartTime: time.Now()}
	if got := m.streamingLabel(); got != "thinking…" {
		t.Errorf("sub-1s should return 'thinking…', got %q", got)
	}
}

func TestStreamingLabel_OverSecond(t *testing.T) {
	m := Model{streamStartTime: time.Now().Add(-10 * time.Second)}
	got := m.streamingLabel()
	if !strings.HasPrefix(got, "thinking… ") {
		t.Errorf("should have 'thinking… ' prefix, got %q", got)
	}
	if !strings.Contains(got, "s") {
		t.Errorf("should contain elapsed seconds, got %q", got)
	}
}
