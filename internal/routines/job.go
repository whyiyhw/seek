package routines

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// Job is the in-memory + on-disk shape of one cron job. See
// feature-routines.md §3.2 for field semantics.
//
// JSON layout deliberately uses snake_case keys to match every
// other persistent state seek writes (session JSONL, subagents
// jsonl). Renaming a key is a breaking schema change — existing
// jobs.jsonl entries would silently lose values on Load.
type Job struct {
	Name        string    `json:"name"`
	Schedule    Schedule  `json:"schedule"`
	Prompt      string    `json:"prompt"`
	ProjectRoot string    `json:"project_root,omitempty"`
	Created     time.Time `json:"created_at"`
	NextRun     time.Time `json:"next_run_at,omitzero"`
	LastRun     time.Time `json:"last_run_at,omitzero"`
	LastRunID   string    `json:"last_run_id,omitempty"`
	LastStatus  string    `json:"last_status,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	MaxRuns     int       `json:"max_runs,omitempty"`
	RunCount    int       `json:"run_count,omitempty"`
	Yolo        bool      `json:"yolo,omitempty"`
	Notify      string    `json:"notify,omitempty"`
}

// MarshalJSON encodes the Schedule field by its Raw form rather
// than as a nested object — matches what a user would type into
// `seek cron create --at`. Decoding goes through UnmarshalJSON
// below to round-trip cleanly.
type jobWire struct {
	Name        string    `json:"name"`
	Schedule    string    `json:"schedule"`
	Prompt      string    `json:"prompt"`
	ProjectRoot string    `json:"project_root,omitempty"`
	Created     time.Time `json:"created_at"`
	NextRun     time.Time `json:"next_run_at,omitzero"`
	LastRun     time.Time `json:"last_run_at,omitzero"`
	LastRunID   string    `json:"last_run_id,omitempty"`
	LastStatus  string    `json:"last_status,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	MaxRuns     int       `json:"max_runs,omitempty"`
	RunCount    int       `json:"run_count,omitempty"`
	Yolo        bool      `json:"yolo,omitempty"`
	Notify      string    `json:"notify,omitempty"`
}

// MarshalJSON serialises Job with Schedule flattened to its
// Raw string. Round-trips via UnmarshalJSON.
func (j Job) MarshalJSON() ([]byte, error) {
	w := jobWire{
		Name:        j.Name,
		Schedule:    j.Schedule.Raw,
		Prompt:      j.Prompt,
		ProjectRoot: j.ProjectRoot,
		Created:     j.Created,
		NextRun:     j.NextRun,
		LastRun:     j.LastRun,
		LastRunID:   j.LastRunID,
		LastStatus:  j.LastStatus,
		LastError:   j.LastError,
		MaxRuns:     j.MaxRuns,
		RunCount:    j.RunCount,
		Yolo:        j.Yolo,
		Notify:      j.Notify,
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes the Schedule by re-parsing its Raw form
// at load time. Any schedule that was valid on `create` remains
// valid on `Load`; corrupted entries surface a clear error
// pointing at the bad field so the user can fix jobs.jsonl by
// hand if disaster strikes.
func (j *Job) UnmarshalJSON(data []byte) error {
	var w jobWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	s, err := ParseSchedule(w.Schedule)
	if err != nil {
		return fmt.Errorf("routines: job %q has invalid schedule: %w", w.Name, err)
	}
	*j = Job{
		Name:        w.Name,
		Schedule:    s,
		Prompt:      w.Prompt,
		ProjectRoot: w.ProjectRoot,
		Created:     w.Created,
		NextRun:     w.NextRun,
		LastRun:     w.LastRun,
		LastRunID:   w.LastRunID,
		LastStatus:  w.LastStatus,
		LastError:   w.LastError,
		MaxRuns:     w.MaxRuns,
		RunCount:    w.RunCount,
		Yolo:        w.Yolo,
		Notify:      w.Notify,
	}
	return nil
}

// validNameRe permits alnum + dash + underscore, must start
// with alnum, max 64 chars. The 64-char cap is generous — it
// fits "schedule_wakeup-20260601-103412-abcdef" (the
// auto-generated wakeup name pattern) plus room for descriptive
// user names. Filesystem-safe by construction (these names
// appear in runs/<name>.lock paths).
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ValidateName returns nil iff name passes the canonical job-
// name shape. Surfaced as a public function (rather than
// inline) so the CLI / wakeup tool can pre-flight names before
// reaching Store.Create.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("routines: job name required")
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("routines: job name %q invalid: must match ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$ (alnum + dash + underscore, starts alnum, max 64 chars)", name)
	}
	return nil
}

// Status constants for LastStatus + run record events. Wire
// format (read by routinescli list / TUI panel): renaming would
// break existing jobs.jsonl entries.
const (
	StatusScheduled = "scheduled"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusKilled    = "killed"
)

// Notify constants for the Notify field.
const (
	NotifyAlways    = "always"
	NotifyOnFailure = "on_failure"
	NotifyNever     = "never"
)

// ValidateNotify returns nil iff s is one of the three known
// values OR empty (defaults to always). Public so CLI can
// pre-flight.
func ValidateNotify(s string) error {
	switch s {
	case "", NotifyAlways, NotifyOnFailure, NotifyNever:
		return nil
	}
	return fmt.Errorf("routines: notify %q invalid: must be always | on_failure | never", s)
}
