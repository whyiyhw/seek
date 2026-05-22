package upgrade

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckCache_RoundTrip exercises the read-after-write happy path
// using SEEK_HOME so we don't touch the real user home. The point is
// to verify that the JSON layout survives a round trip — if a future
// field rename silently nukes existing caches, this catches it.
func TestCheckCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)

	want := CheckCache{
		CheckedAt: time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
		LatestTag: "v0.2.0",
	}
	if err := SaveCheckCache(want); err != nil {
		t.Fatalf("SaveCheckCache: %v", err)
	}

	// File should land exactly where we expect.
	if _, err := filepath.Abs(filepath.Join(dir, "upgrade-check.json")); err != nil {
		t.Fatal(err)
	}

	got := LoadCheckCache()
	if !got.CheckedAt.Equal(want.CheckedAt) || got.LatestTag != want.LatestTag {
		t.Errorf("LoadCheckCache = %+v, want %+v", got, want)
	}
}

func TestCheckCache_MissingReturnsZero(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	got := LoadCheckCache()
	if !got.CheckedAt.IsZero() || got.LatestTag != "" {
		t.Errorf("expected zero value, got %+v", got)
	}
}

func TestCheckCache_CorruptReturnsZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)
	// Write garbage so json.Unmarshal fails.
	if err := os.WriteFile(filepath.Join(dir, "upgrade-check.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadCheckCache()
	if !got.CheckedAt.IsZero() {
		t.Errorf("corrupt cache should yield zero value, got %+v", got)
	}
}

func TestCheckCache_Fresh(t *testing.T) {
	cases := []struct {
		name string
		c    CheckCache
		want bool
	}{
		{"zero", CheckCache{}, false},
		{"just now", CheckCache{CheckedAt: time.Now()}, true},
		{"23h ago", CheckCache{CheckedAt: time.Now().Add(-23 * time.Hour)}, true},
		{"25h ago", CheckCache{CheckedAt: time.Now().Add(-25 * time.Hour)}, false},
		{"future (clock skew)", CheckCache{CheckedAt: time.Now().Add(time.Hour)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.c.Fresh(); got != c.want {
				t.Errorf("Fresh() = %v, want %v", got, c.want)
			}
		})
	}
}

