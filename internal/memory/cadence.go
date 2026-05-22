package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// DreamState is the persisted cadence tracker for M5.8's auto-dream.
// One file at ~/.seek/dream-state.json — shared across projects
// because dreaming itself is cross-project by design.
//
// SessionsSinceDream counts non-trivial seek sessions that have ended
// since the last dream completed (incremented at SessionStart by the
// auto-dream gate). LastDreamAt is updated when an auto-dream
// successfully completes; manual `seek -dream` invocations do NOT
// reset it — manual runs are exploratory and shouldn't pause the
// automatic cadence.
type DreamState struct {
	SchemaVersion      int       `json:"schema_version"`
	LastDreamAt        time.Time `json:"last_dream_at,omitzero"`
	SessionsSinceDream int       `json:"sessions_since_dream"`
}

// dreamStateFile is the path under SEEK_HOME holding DreamState. JSON
// (not JSONL) because it's a single object, not a stream.
const dreamStateFile = "dream-state.json"

// dreamStateSchemaVersion bumps if we ever change the on-disk shape.
const dreamStateSchemaVersion = 1

// Cadence defaults — sized for "dreaming once every couple of weeks at
// most" so the reasoner cost is bounded and the user has time to
// review L-pending entries between runs.
const (
	defaultDreamEverySessions = 20
	defaultDreamEveryDays     = 14
	envDreamSessions          = "SEEK_AUTO_DREAM_SESSIONS"
	envDreamDays              = "SEEK_AUTO_DREAM_DAYS"
	envAutoDream              = "SEEK_AUTO_DREAM"
)

// dreamStatePath returns the absolute path to dream-state.json.
// Exposed for tests that need to inspect or pre-plant state; production
// callers go through LoadDreamState / SaveDreamState.
func dreamStatePath() (string, error) {
	root, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, dreamStateFile), nil
}

// LoadDreamState reads the cadence file. Missing file returns a fresh
// zero-value state (no error) — first-run users should pass through
// without an error path.
func LoadDreamState() (*DreamState, error) {
	path, err := dreamStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DreamState{SchemaVersion: dreamStateSchemaVersion}, nil
		}
		return nil, err
	}
	var s DreamState
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt state file shouldn't break startup — same philosophy
		// as project manifest parse failures. Reset to a fresh state
		// and overwrite on the next save.
		return &DreamState{SchemaVersion: dreamStateSchemaVersion}, nil
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = dreamStateSchemaVersion
	}
	return &s, nil
}

// SaveDreamState writes atomically (atomicWrite tmpfile + rename).
func (s *DreamState) Save() error {
	path, err := dreamStatePath()
	if err != nil {
		return err
	}
	s.SchemaVersion = dreamStateSchemaVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data)
}

// DueResult is the structured verdict from IsDreamDue — exposed so
// callers can log WHY the cadence fired (or didn't) for telemetry.
type DueResult struct {
	Due             bool
	SessionsTrigger bool // SessionsSinceDream >= cap
	DaysTrigger     bool // now - LastDreamAt >= cap
	SessionCap      int
	DayCap          int
	SessionsSeen    int
	SinceLastDream  time.Duration // for telemetry; zero if never dreamed
}

// IsDreamDue evaluates the cadence rule against the current state +
// current time. Either trigger fires Due=true; both are reported so
// callers can show "auto-dream: every 20 sessions or 14 days, current
// 17 / 12 days" hints.
//
// First-run semantics: a zero LastDreamAt is treated as "the user
// just installed seek a moment ago" — the days trigger uses
// SessionsSinceDream as a fallback proxy to avoid auto-dreaming
// immediately on day 1.
func (s *DreamState) IsDreamDue(now time.Time) DueResult {
	sessionCap := readIntEnv(envDreamSessions, defaultDreamEverySessions)
	dayCap := readIntEnv(envDreamDays, defaultDreamEveryDays)

	r := DueResult{
		SessionCap:   sessionCap,
		DayCap:       dayCap,
		SessionsSeen: s.SessionsSinceDream,
	}
	if !s.LastDreamAt.IsZero() {
		r.SinceLastDream = now.Sub(s.LastDreamAt)
	}

	if s.SessionsSinceDream >= sessionCap {
		r.SessionsTrigger = true
	}
	// Days trigger only after LastDreamAt is set — otherwise a fresh
	// install would meet the days condition on session 1.
	if !s.LastDreamAt.IsZero() && now.Sub(s.LastDreamAt) >= time.Duration(dayCap)*24*time.Hour {
		r.DaysTrigger = true
	}
	r.Due = r.SessionsTrigger || r.DaysTrigger
	return r
}

// readIntEnv reads an int from the named env var, falling back to the
// default on missing / non-positive / unparseable. Silent fallback
// matches halfLifeFromEnv's "broken env var degrades gracefully"
// philosophy.
func readIntEnv(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// autoDreamEnabled mirrors autoDistillEnabled — gate via env var so
// the safety net (manual `seek -dream`) stays the default.
func autoDreamEnabled() bool {
	v := os.Getenv(envAutoDream)
	switch v {
	case "1", "true", "yes", "on", "TRUE", "Yes", "ON", "True":
		return true
	}
	return false
}
