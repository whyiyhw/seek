package routines

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// NewRunID returns a fresh run ID in the canonical seek shape:
// `YYYYMMDD-HHMMSS-<6 hex>`. Same scheme as session.generateID
// / subagent.newSubSid; independent ID spaces but identical
// format so visual inspection of paths immediately tells the
// reader what kind of artifact they're looking at.
//
// On entropy exhaustion falls back to nanosecond-precision hex
// so IDs never collide from a zero-valued random buffer.
func NewRunID(now time.Time) string {
	var rnd [3]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("%s-%06x",
			now.Format("20060102-150405"),
			now.Nanosecond()/1000)
	}
	return fmt.Sprintf("%s-%s",
		now.Format("20060102-150405"),
		hex.EncodeToString(rnd[:]))
}

// RunRecorder writes the JSONL stream for one cron run:
//
//	line 1   : header (job name + command + start time + ...)
//	lines 2..: events — stdout / stderr chunks + the single
//	           terminal event (completed / failed / killed)
//
// All writes go through a single mu-guarded encoder so concurrent
// stdout + stderr streamer goroutines can't interleave bytes
// mid-line. JSONL's line-terminated format guarantees
// per-event integrity AS LONG AS each event is written via one
// Encode call — which is what we do.
//
// Lifecycle:
//
//	rr, err := NewRecorder(runID, jobName, ...)
//	defer rr.Close()
//	rr.WriteStdout(data) / WriteStderr(data) — from streamers
//	rr.WriteTerminal(...)                    — exactly once at end
//
// Close is idempotent and safe to defer alongside an early
// return from setup failure.
type RunRecorder struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// runHeaderRecord is the first line of every run file.
type runHeaderRecord struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	JobName       string    `json:"job_name"`
	StartedAt     time.Time `json:"started_at"`
	ProjectRoot   string    `json:"project_root,omitempty"`
	Command       []string  `json:"command"`
	Yolo          bool      `json:"yolo,omitempty"`
}

// runEventRecord is the shape of subsequent lines. The Event
// field is the discriminator. Different event kinds populate
// different fields — fine because empty fields omit-empty out.
type runEventRecord struct {
	Event      string    `json:"event"`
	TS         time.Time `json:"ts"`
	Data       string    `json:"data,omitempty"`
	ExitCode   int       `json:"exit_code,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Error      string    `json:"error,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

// NewRecorder creates the run file under runsDir and writes the
// header line. ProjectRoot / Yolo / Command go into the header
// verbatim so post-run analysis (jq grep / TUI render) can
// reconstruct exactly what subprocess was spawned.
//
// runsDir is created lazily (MkdirAll) — first run on a fresh
// install hits this path.
func NewRecorder(runsDir, runID, jobName, projectRoot string, command []string, yolo bool, startedAt time.Time) (*RunRecorder, error) {
	if runID == "" {
		return nil, fmt.Errorf("routines: NewRecorder: empty run id")
	}
	if jobName == "" {
		return nil, fmt.Errorf("routines: NewRecorder: empty job name")
	}
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return nil, fmt.Errorf("routines: mkdir runs: %w", err)
	}
	path := filepath.Join(runsDir, runID+".jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("routines: open run file: %w", err)
	}
	rr := &RunRecorder{f: f, enc: json.NewEncoder(f)}
	rr.enc.SetEscapeHTML(false)

	hdr := runHeaderRecord{
		SchemaVersion: 1,
		RunID:         runID,
		JobName:       jobName,
		StartedAt:     startedAt.UTC(),
		ProjectRoot:   projectRoot,
		Command:       command,
		Yolo:          yolo,
	}
	if err := rr.enc.Encode(hdr); err != nil {
		_ = rr.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("routines: write header: %w", err)
	}
	return rr, nil
}

// WriteStdout records one chunk of subprocess stdout. data may
// be of any size; one Encode call = one JSONL line. Streamer
// goroutines call this without external sync — the mu inside
// RunRecorder serialises writes between stdout + stderr +
// terminal events.
func (r *RunRecorder) WriteStdout(data string) error {
	return r.writeEvent("stdout", runEventRecord{Data: data})
}

// WriteStderr records one chunk of subprocess stderr. Same
// semantics as WriteStdout.
func (r *RunRecorder) WriteStderr(data string) error {
	return r.writeEvent("stderr", runEventRecord{Data: data})
}

// WriteCompleted records the successful terminal event.
// exitCode SHOULD be 0; summary is a short head-of-stdout
// preview for /agents-style table rendering. Call exactly once
// per run; callers should not also call WriteFailed or
// WriteKilled.
func (r *RunRecorder) WriteCompleted(exitCode int, dur time.Duration, summary string) error {
	return r.writeEvent("completed", runEventRecord{
		ExitCode:   exitCode,
		DurationMs: dur.Milliseconds(),
		Summary:    summary,
	})
}

// WriteFailed records a failure terminal event. exitCode
// reflects whatever the subprocess returned (non-zero) OR a
// synthetic value if the failure was at the start path (e.g.
// fork failed). errMsg should include enough context to debug
// without re-running.
func (r *RunRecorder) WriteFailed(exitCode int, dur time.Duration, errMsg string) error {
	return r.writeEvent("failed", runEventRecord{
		ExitCode:   exitCode,
		DurationMs: dur.Milliseconds(),
		Error:      errMsg,
	})
}

// WriteKilled records a killed terminal event. reason names the
// why ("timeout" / "user" / future causes); Duration captures
// total wall time including the grace period after SIGTERM.
func (r *RunRecorder) WriteKilled(reason string, dur time.Duration) error {
	return r.writeEvent("killed", runEventRecord{
		Reason:     reason,
		DurationMs: dur.Milliseconds(),
	})
}

// writeEvent is the single mu-guarded write path. Every public
// Write* method routes through here. Empty f signals a
// post-Close call — silently swallow.
func (r *RunRecorder) writeEvent(kind string, ev runEventRecord) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil // closed
	}
	ev.Event = kind
	ev.TS = time.Now().UTC()
	return r.enc.Encode(ev)
}

// Close finalises the run file. Idempotent — second call is a
// no-op so callers can defer Close alongside early returns
// during setup. Returns the first close error encountered.
func (r *RunRecorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
