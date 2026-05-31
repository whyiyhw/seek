// Package bgjob is the session-scoped registry for background shell
// jobs (v6 柱 K). It owns nothing OS-specific: the bash tool launches a
// process, wires the job's io.Writer as the command's stdout/stderr,
// registers a kill closure, and reports the exit code; the monitor tool
// reads output and waits. Keeping this package process-agnostic means it
// imports neither os/exec nor syscall, so it is fully testable without
// spawning real processes and can be imported by both bash and monitor
// without coupling them to each other (PRD §4 D2).
//
// Lifecycle contract (PRD §3 "零常驻 daemon"): jobs live and die with the
// seek session. There is no persistence, nothing crosses a restart, and
// Shutdown kills every survivor so nothing is orphaned. The crucial ctx
// rule (PRD §4 D5) lives in the callers, not here: a background process
// must NOT be bound to a turn's context, only to this Manager's
// Shutdown. Wait, by contrast, IS bound to the turn ctx — cancelling a
// Wait (Esc) must never kill the job, only stop observing it.
package bgjob

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"
)

const (
	defaultRingCap     = 64 * 1024
	defaultMaxJobs     = 8
	defaultWaitTimeout = 120 * time.Second
	maxWaitTimeout     = 600 * time.Second
	waitPollInterval   = 100 * time.Millisecond
)

// Status is a background job's lifecycle state.
type Status int

const (
	StatusRunning Status = iota
	StatusExited
	StatusKilled
)

func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusExited:
		return "exited"
	case StatusKilled:
		return "killed"
	default:
		return "unknown"
	}
}

// WaitReason explains why a Wait returned.
type WaitReason int

const (
	ReasonExited    WaitReason = iota // the job's process exited
	ReasonMatched                     // until_regex matched the output
	ReasonTimeout                     // the wait timeout elapsed, job still running
	ReasonCancelled                   // the turn ctx was cancelled (Esc); job still running
)

func (r WaitReason) String() string {
	switch r {
	case ReasonExited:
		return "exited"
	case ReasonMatched:
		return "matched"
	case ReasonTimeout:
		return "timeout"
	case ReasonCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// Job is one background process tracked by the Manager. Callers obtain a
// Job from Launch, use it as the command's stdout/stderr io.Writer,
// register a kill closure with SetKiller, and report exit with Finish.
type Job struct {
	ID      string
	Command string
	Start   time.Time
	ring    *ringBuffer

	mu       sync.Mutex
	status   Status
	exitCode int
	killer   func() error
	done     chan struct{}
	readCur  int64     // server-side poll cursor (advanced by Poll/Wait)
	end      time.Time // set when the job exits/is killed; freezes elapsed
}

// elapsedLocked returns the run duration: live (since Start) while
// running, frozen at the exit/kill instant once finished — so polling a
// long-dead job doesn't report an ever-growing elapsed. Caller holds mu.
func (j *Job) elapsedLocked() time.Duration {
	if j.status == StatusRunning || j.end.IsZero() {
		return time.Since(j.Start)
	}
	return j.end.Sub(j.Start)
}

// Write implements io.Writer; the bash tool sets cmd.Stdout = job and
// cmd.Stderr = job so combined output flows into the ring buffer.
func (j *Job) Write(p []byte) (int, error) { return j.ring.Write(p) }

// SetKiller registers the closure the Manager calls to terminate the
// process group. The bash tool injects a closure over killProcessGroup;
// tests inject a fake. Safe to call once, right after Launch.
func (j *Job) SetKiller(fn func() error) {
	j.mu.Lock()
	j.killer = fn
	j.mu.Unlock()
}

// Finish marks the job exited with the given code. Called by the wait
// goroutine when the process ends. No-op if the job already transitioned
// (e.g. Kill won the race) — the done channel is closed exactly once.
func (j *Job) Finish(code int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != StatusRunning {
		return
	}
	j.status = StatusExited
	j.exitCode = code
	j.end = time.Now()
	close(j.done)
}

func (j *Job) matches(re *regexp.Regexp) bool { return re.Match(j.ring.snapshot()) }

// Manager is the session-scoped registry. Build one per seek session and
// wire Shutdown into the session's teardown path.
type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	counter int
	ringCap int
	maxJobs int
}

// Option configures a Manager.
type Option func(*Manager)

// WithRingCap sets the per-job output buffer cap in bytes (default 64 KiB).
func WithRingCap(n int) Option { return func(m *Manager) { m.ringCap = n } }

// WithMaxJobs sets the concurrent-running-job ceiling (default 8).
func WithMaxJobs(n int) Option { return func(m *Manager) { m.maxJobs = n } }

// New returns an empty Manager.
func New(opts ...Option) *Manager {
	m := &Manager{
		jobs:    map[string]*Job{},
		ringCap: defaultRingCap,
		maxJobs: defaultMaxJobs,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Launch allocates a new running job with a fresh bg-N handle, or errors
// if the concurrency cap is already met. The caller is responsible for
// actually starting the process and wiring job.Write / SetKiller /
// Finish.
func (m *Manager) Launch(command string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	running := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.status == StatusRunning {
			running++
		}
		j.mu.Unlock()
	}
	if running >= m.maxJobs {
		return nil, fmt.Errorf("bgjob: too many background jobs (%d running, max %d); kill or wait for one to finish before starting another", running, m.maxJobs)
	}
	m.counter++
	j := &Job{
		ID:      fmt.Sprintf("bg-%d", m.counter),
		Command: command,
		Start:   time.Now(),
		ring:    newRing(m.ringCap),
		status:  StatusRunning,
		done:    make(chan struct{}),
	}
	m.jobs[j.ID] = j
	return j, nil
}

// Get returns the job with the given ID.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// PollResult is what Poll returns: new output since the last read plus
// the current status.
type PollResult struct {
	Window   []byte        // new bytes since the previous Poll/Wait
	Dropped  int64         // bytes dropped from the ring before this window (0 = none)
	Status   Status        // running / exited / killed
	ExitCode int           // valid when Status == StatusExited
	Elapsed  time.Duration // since launch
}

// Poll returns output produced since the previous Poll/Wait on this job
// and advances the server-side cursor. The model never tracks cursors;
// each Poll yields only what's new.
func (m *Manager) Poll(id string) (PollResult, error) {
	j, ok := m.Get(id)
	if !ok {
		return PollResult{}, m.unknown(id)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	window, newCur, gap := j.ring.readFrom(j.readCur)
	j.readCur = newCur
	return PollResult{
		Window:   window,
		Dropped:  gap,
		Status:   j.status,
		ExitCode: j.exitCode,
		Elapsed:  j.elapsedLocked(),
	}, nil
}

// WaitResult is what Wait returns.
type WaitResult struct {
	Window   []byte
	Dropped  int64
	Status   Status
	ExitCode int
	Elapsed  time.Duration
	Reason   WaitReason
}

// Wait blocks until the job exits, untilRegex matches new output, the
// timeout elapses, or ctx is cancelled — whichever comes first. timeout
// <= 0 uses the 120s default; it is clamped to 600s. A cancelled ctx
// returns ReasonCancelled AND ctx.Err() so callers can propagate the
// interrupt; the job is left running (Esc stops observing, not the job).
// A bad untilRegex returns an error without blocking.
func (m *Manager) Wait(ctx context.Context, id, untilRegex string, timeout time.Duration) (WaitResult, error) {
	j, ok := m.Get(id)
	if !ok {
		return WaitResult{}, m.unknown(id)
	}
	var re *regexp.Regexp
	if untilRegex != "" {
		var err error
		if re, err = regexp.Compile(untilRegex); err != nil {
			return WaitResult{}, fmt.Errorf("bgjob: bad until_regex %q: %w", untilRegex, err)
		}
	}
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}

	// Fast path: regex may already match buffered output before we block.
	if re != nil && j.matches(re) {
		return m.result(j, ReasonMatched), nil
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(waitPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-j.done:
			return m.result(j, ReasonExited), nil
		case <-ctx.Done():
			return m.result(j, ReasonCancelled), ctx.Err()
		case <-deadline.C:
			return m.result(j, ReasonTimeout), nil
		case <-tick.C:
			if re != nil && j.matches(re) {
				return m.result(j, ReasonMatched), nil
			}
		}
	}
}

// Kill terminates the job's process group via its registered killer and
// marks it killed. No-op (nil error) if the job already finished. Safe
// to race against the wait goroutine's Finish — whichever transitions
// first wins and closes done exactly once.
func (m *Manager) Kill(id string) error {
	j, ok := m.Get(id)
	if !ok {
		return m.unknown(id)
	}
	j.mu.Lock()
	if j.status != StatusRunning {
		j.mu.Unlock()
		return nil // already exited/killed
	}
	j.status = StatusKilled
	killer := j.killer
	j.end = time.Now()
	close(j.done)
	j.mu.Unlock()
	if killer != nil {
		return killer()
	}
	return nil
}

// Shutdown kills every still-running job. Wire this into the session's
// teardown so no background process is ever orphaned (PRD §3, §4 D5).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Kill(id)
	}
}

// result snapshots the job's current output window + state under its lock.
func (m *Manager) result(j *Job, reason WaitReason) WaitResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	window, newCur, gap := j.ring.readFrom(j.readCur)
	j.readCur = newCur
	return WaitResult{
		Window:   window,
		Dropped:  gap,
		Status:   j.status,
		ExitCode: j.exitCode,
		Elapsed:  j.elapsedLocked(),
		Reason:   reason,
	}
}

func (m *Manager) unknown(id string) error {
	m.mu.Lock()
	known := make([]string, 0, len(m.jobs))
	for k := range m.jobs {
		known = append(known, k)
	}
	m.mu.Unlock()
	return fmt.Errorf("bgjob: unknown job %q (known: %v)", id, known)
}
