package tui

import (
	"testing"
	"time"
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
