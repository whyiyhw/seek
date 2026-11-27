package taskproc

// Spawn and kill: the two places the registry touches a real process.
// Both take their syscall surface from the Runner's injected fields so
// the CLI's table-driven tests can present fake processes, graft
// zombies, and force timeouts without depending on the machine's
// process table. Production fills them from probe_*.go and spawn_*.go.
//
// The lifetime contract (docs/prd/archive/feature-task-lifetime.md §1.3): the
// WRAPPER owns the exit fact, not the spawning seek process. A spawner
// that dies immediately after forking therefore loses nothing — the
// record is still completed by the process it started, which is the
// whole point of detaching.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/whyiyhw/seek/internal/childenv"
)

// DefaultKillGrace is how long `seek kill` waits after SIGTERM before
// escalating to SIGKILL. Short on purpose: a task that ignores SIGTERM
// is usually a wedged one, and the operator asked for it to die.
const DefaultKillGrace = 3 * time.Second

// killPollInterval is the SIGTERM→SIGKILL probe cadence.
const killPollInterval = 50 * time.Millisecond

// Runner binds the registry to a spawn/kill surface.
type Runner struct {
	Store *Store
	// Exe is the seek binary the wrapper re-execs itself from. Empty
	// means os.Executable() — the normal case; tests point it at a
	// stub.
	Exe string
	// Probe is the liveness oracle (defaults to the platform's).
	Probe ProbeFunc
	// Signal delivers a signal to a process group (defaults to the
	// platform's).
	Signal func(pid int, sig syscall.Signal) error
	// Sleep is the kill-escalation wait (defaults to time.Sleep).
	Sleep func(time.Duration)
	// Now is the clock (defaults to Store.Now).
	Now func() time.Time
}

// SpawnOptions is what `seek task run` collected.
type SpawnOptions struct {
	Command string
	CWD     string
	Session string
	Project string
}

// Spawn starts the detached wrapper and returns the recorded task. The
// returned task's Status is whatever the record folds to right now —
// usually `running`, but a command that exits instantly may already be
// `exited` by the time this returns, which is why the pid fact is
// appended by the wrapper rather than raced in from here.
func (r *Runner) Spawn(o SpawnOptions) (Task, error) {
	if r.Store == nil {
		return Task{}, errors.New("taskproc: Spawn needs a Store")
	}
	if o.Command == "" {
		return Task{}, errors.New("taskproc: Spawn needs a command")
	}
	exe := r.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return Task{}, fmt.Errorf("taskproc: locate seek binary: %w", err)
		}
	}
	id, err := r.Store.NewID()
	if err != nil {
		return Task{}, err
	}
	cwd := o.CWD
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return Task{}, fmt.Errorf("taskproc: getwd: %w", err)
		}
	}

	// Metadata first, PID second: the wrapper appends its own pid, so a
	// task that exits before Spawn returns still has an ordered record.
	if err := r.Store.Append(id, Event{
		Kind:      KindStart,
		ID:        id,
		Command:   o.Command,
		CWD:       cwd,
		Session:   o.Session,
		Project:   o.Project,
		StartedAt: r.now(),
	}); err != nil {
		return Task{}, err
	}

	if err := os.MkdirAll(r.Store.Dir(), 0o755); err != nil {
		return Task{}, fmt.Errorf("taskproc: mkdir tasks: %w", err)
	}
	logf, err := os.OpenFile(r.Store.LogPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.fail(id, fmt.Sprintf("open log: %v", err))
		return Task{}, fmt.Errorf("taskproc: open log: %w", err)
	}
	defer logf.Close()

	shell := shellArgv(o.Command)
	wrapperArgv := append([]string{"task", "exec", "--id", id, "--"}, shell...)
	cmd := exec.Command(exe, wrapperArgv...)
	cmd.Dir = cwd
	// The wrapper is seek itself and must find the same registry, but
	// nothing else of the parent's SEEK_* or credentials may leak into
	// it (childenv's documented last-wins append pattern).
	cmd.Env = append(childenv.Sanitized(), "SEEK_HOME="+r.Store.HomeDir())
	cmd.Stdout = logf
	cmd.Stderr = logf
	detach(cmd)

	if err := cmd.Start(); err != nil {
		r.fail(id, fmt.Sprintf("start: %v", err))
		return Task{}, fmt.Errorf("taskproc: start wrapper: %w", err)
	}
	// Reap the wrapper so a live parent leaves no zombie. Deliberately
	// the ONLY thing this goroutine does: the exit fact comes from the
	// wrapper, so losing this goroutine (parent dies) costs nothing.
	go func() { _ = cmd.Wait() }()

	return r.Store.Read(id, r.probe())
}

// Kill terminates the task if it is still running and records the kill
// fact. A task that already reached a terminal state is returned
// unchanged with a nil error — "it is not running any more" is the
// caller's goal, not a failure.
func (r *Runner) Kill(id string, grace time.Duration) (Task, error) {
	if r.Store == nil {
		return Task{}, errors.New("taskproc: Kill needs a Store")
	}
	probe := r.probe()
	t, err := r.Store.Read(id, probe)
	if err != nil {
		return Task{}, err
	}
	if t.Status.Terminal() || t.Status == StatusZombie {
		return t, nil
	}
	// Between the spawner's start fact and the wrapper's pid fact there is
	// a window (microseconds, but reachable from a script that backgrounds
	// `seek task run` and kills immediately). Signalling pid 0 is
	// meaningless, and recording `killed` there would be a lie the operator
	// cannot recover from: the task would keep running while `ps` reports
	// it killed. Refuse instead — the caller retries a moment later. The
	// hint names the escape hatch for the wedged case (a wrapper that died
	// before it could report a pid leaves no process to signal and no
	// terminal fact — GC is out of scope, PRD §5).
	if t.PID == 0 {
		return t, fmt.Errorf("taskproc: task %s is still starting (no pid recorded yet); retry in a moment — "+
			"if it stays that way the wrapper never ran, and %s can be removed", id, r.Store.RecordPath(id))
	}
	if grace <= 0 {
		grace = DefaultKillGrace
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	sig := r.Signal
	if sig == nil {
		sig = SignalGroup
	}

	if err := sig(t.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return t, fmt.Errorf("taskproc: SIGTERM %s (pid %d): %w", id, t.PID, err)
	}
	escalated := false
	deadline := r.now().Add(grace)
	for r.now().Before(deadline) {
		if probe(t.PID) == ProbeGone {
			break
		}
		sleep(killPollInterval)
	}
	if probe(t.PID) != ProbeGone {
		escalated = true
		_ = sig(t.PID, syscall.SIGKILL)
	}

	// Report the LAST signal sent, tracked as a flag: probing after the
	// fact cannot tell us what killed it (the process is gone either
	// way), and mislabelling an escalation as a polite SIGTERM would
	// hide exactly the case an operator needs to see.
	signal := "SIGTERM"
	if escalated {
		signal = "SIGKILL"
	}
	now := r.now()
	if err := r.Store.Append(id, Event{
		Kind:       KindKilled,
		ID:         id,
		Signal:     signal,
		FinishedAt: &now,
	}); err != nil {
		return t, err
	}
	return r.Store.Read(id, probe)
}

func (r *Runner) fail(id, reason string) {
	now := r.now()
	ev := Event{Kind: KindExit, ID: id, FinishedAt: &now, Reason: reason}
	code := -1
	ev.ExitCode = &code
	_ = r.Store.Append(id, ev)
}

func (r *Runner) probe() ProbeFunc {
	if r.Probe != nil {
		return r.Probe
	}
	return ProbePID
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	if r.Store != nil && r.Store.Now != nil {
		return r.Store.Now()
	}
	return time.Now()
}
