package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// checkCacheTTL throttles how often the startup check actually hits
// GitHub. 24h is the right scale for a hobby CLI: not so short that
// we hammer the API, not so long that a fresh release stays invisible
// for a week.
const checkCacheTTL = 24 * time.Hour

// CheckCache is the on-disk record of the last upgrade probe. Lives at
// ~/.seek/upgrade-check.json (or $SEEK_HOME/upgrade-check.json). Tiny
// stable schema: callers add fields, never remove.
type CheckCache struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

func cachePath() (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "upgrade-check.json"), nil
}

// LoadCheckCache returns the persisted cache, or a zero value when the
// file is missing or malformed. Errors are intentionally swallowed:
// a corrupt cache should never block startup, only force a re-check.
func LoadCheckCache() CheckCache {
	p, err := cachePath()
	if err != nil {
		return CheckCache{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return CheckCache{}
	}
	var c CheckCache
	if err := json.Unmarshal(b, &c); err != nil {
		return CheckCache{}
	}
	return c
}

// SaveCheckCache writes the cache, creating the home dir on first use.
// Returns the persistence error — callers usually log + carry on.
func SaveCheckCache(c CheckCache) error {
	p, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Fresh reports whether the cache was written less than checkCacheTTL
// ago. A future timestamp (clock skew) counts as fresh.
func (c CheckCache) Fresh() bool {
	if c.CheckedAt.IsZero() {
		return false
	}
	return time.Since(c.CheckedAt) < checkCacheTTL
}
