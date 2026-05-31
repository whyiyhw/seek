// Package bash implements the `bash` tool: run a shell command via
// /bin/sh -c (or %COMSPEC on Windows) with a timeout. Gated by the
// permission Policy — without --yolo every bash call is refused with
// instructions for the model to surface the request to the user.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/bgjob"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/sandbox"
	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "command":    {"type": "string", "description": "Shell command. Runs under /bin/sh -c (POSIX) or cmd.exe /C (Windows)."},
    "timeout_ms": {"type": "integer", "description": "Timeout in milliseconds. Default 120000 (2m), max 600000 (10m).", "minimum": 100, "maximum": 600000},
    "run_in_background": {"type": "boolean", "description": "Run the command detached and return a handle (bg-N) immediately instead of blocking. Track it with the monitor tool (poll/wait/kill). Use for long builds, test suites, and dev servers. timeout_ms is ignored in background mode; the job lives until it exits, you kill it, or the session ends."}
  },
  "required": ["command"],
  "additionalProperties": false
}`)

const description = "Execute a shell command. Prefer dedicated tools (git, grep, read, list_dir, webfetch) for repo inspection — use bash only when no dedicated tool covers the need. By default seek refuses bash; the user must opt in by re-running with --yolo. When allowed, combined stdout/stderr is returned (truncated past 32 KiB). Use timeout_ms to bound long-running commands. Set run_in_background to start a long task (build / test suite / dev server) detached and track it with the monitor tool instead of blocking the turn."

const (
	defaultTimeoutMS = 120_000
	maxTimeoutMS     = 600_000
	maxOutputBytes   = 32 * 1024
	// Grace period after killProcessGroup before abandoning cmd.Wait().
	// Covers slow reaping; prevents indefinite hang when kill fails
	// (e.g. WSL sudo waiting on a Windows credential dialog).
	killWaitGrace = 5 * time.Second
)

var errKillWaitGrace = errors.New("bash: process did not exit after kill")

// lockedBuffer wraps bytes.Buffer for concurrent writes from os/exec pipe
// goroutines and a post-Wait read on the main goroutine. When killWaitGrace
// expires before cmd.Wait() returns (e.g. WSL sudo), we still need a safe
// partial snapshot of captured output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

type Args struct {
	Command         string `json:"command"`
	TimeoutMS       int    `json:"timeout_ms,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

type Tool struct {
	policy  *permission.Policy
	bgMgr   *bgjob.Manager
	deny    func(command string) (bool, string)
	sandbox *sandbox.Options
}

// WithSandbox runs commands inside an OS sandbox (v7 柱 O) — on macOS,
// seatbelt confining writes to opt.WritableDirs. Used by autopilot to jail
// unattended subagents to the project tree. No-op off macOS until landlock
// lands. Composes with WithDeny / WithBackground; nil = no sandbox.
func (t Tool) WithSandbox(opt sandbox.Options) Tool {
	t.sandbox = &opt
	return t
}

// shellArgv builds the argv for command, wrapped in the OS sandbox when
// one is configured. Off-sandbox it's the plain `/bin/sh -c` (or cmd.exe)
// argv, so the existing exec machinery (Setsid + process-group kill) is
// unchanged.
func (t Tool) shellArgv(command string) (string, []string) {
	name, args := "/bin/sh", []string{"-c", command}
	if runtime.GOOS == "windows" {
		name, args = "cmd.exe", []string{"/C", command}
	}
	if t.sandbox != nil {
		name, args = sandbox.Argv(*t.sandbox, name, args...)
	}
	return name, args
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

// WithDeny installs a command guard checked before permission/exec. When
// the guard returns true, the command is refused with a model-visible
// result (not a fatal error) carrying the reason. Used by autopilot to
// deny remote ops (push / PR) in unattended subagents (v7 柱 N D2);
// composes with WithBackground. nil guard = no restriction.
func (t Tool) WithDeny(fn func(command string) (deny bool, reason string)) Tool {
	t.deny = fn
	return t
}

// WithBackground wires the session's background-job manager, enabling
// run_in_background. Optional: without it, background launches are
// refused while foreground bash keeps working — keeps 柱 K independently
// rollback-able (v6 §2.2). Mirrors write/edit's WithSnapshotter builder.
func (t Tool) WithBackground(mgr *bgjob.Manager) Tool {
	t.bgMgr = mgr
	return t
}

func (Tool) Name() string            { return "bash" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("bash", raw, &a, "command", "timeout_ms", "run_in_background"); err != nil {
		return "", err
	}
	if a.Command == "" {
		return "", tools.MissingField("bash", "command", raw, "command", "timeout_ms", "run_in_background")
	}

	// Command guard (autopilot no-remote, etc.) — refused as a
	// model-visible result, not a fatal error, so the model adapts.
	if t.deny != nil {
		if blocked, reason := t.deny(a.Command); blocked {
			return fmt.Sprintf("[bash: blocked — %s]\n$ %s", reason, a.Command), nil
		}
	}

	if err := t.policy.Check(permission.Action{
		Kind:     permission.KindBash,
		Command:  a.Command,
		ReadOnly: isReadOnlySafe(a.Command),
	}); err != nil {
		// Plan-analyze denial — append a command-specific hint so the
		// model gets pointed at the right alternative (use the git
		// tool, drop the cd prefix, use go vet instead of go test,
		// etc.) at the exact moment it's about to retry. Other workflow
		// / pref deny paths get the vanilla message — their hints
		// ("--yolo", "user declined") already point the model the
		// right way.
		if errors.Is(err, permission.ErrDenied) && t.policy.Workflow() == permission.WorkflowPlanAnalyze {
			if hint := planAnalyzeBashHint(a.Command); hint != "" {
				return "", fmt.Errorf("%w. Hint: %s", err, hint)
			}
		}
		return "", err
	}

	// Background launch: permission already cleared above (same KindBash
	// gate). Detach and return a handle immediately — never block the turn,
	// never bind the process to the turn ctx (PRD §4 D5).
	if a.RunInBackground {
		return t.runBackground(a.Command)
	}

	timeout := a.TimeoutMS
	if timeout <= 0 {
		timeout = defaultTimeoutMS
	}
	if timeout > maxTimeoutMS {
		timeout = maxTimeoutMS
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	shell, shArgs := t.shellArgv(a.Command)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cctx, shell, shArgs...)
	} else {
		// We use exec.Command (NOT CommandContext) on Unix because
		// exec.CommandContext only kills the direct child PID when the
		// context is cancelled. With detachStdin creating a new session
		// (Setsid), the child (sh) forks grandchildren (e.g. `sleep`)
		// that inherit the stdout/stderr pipe fds. Killing sh alone
		// orphans them but the pipes stay open → cmd.Wait() deadlocks.
		// Instead we kill the whole process group (negative PID) via a
		// dedicated goroutine.
		cmd = exec.Command(shell, shArgs...)
	}
	// Pin the working directory to the project root the policy was
	// configured with, NOT whatever the process happens to be in.
	// Without this we'd inherit os.Getwd() at exec time — fragile if
	// anything inside the program (a tool, a test, a future feature)
	// ever calls os.Chdir. Pinning here also means relative paths in
	// model-issued commands resolve to the right project root, so the
	// model doesn't need `cd /abs/path && …` prefixes (the system
	// prompt promises this; here is where the promise becomes load-
	// bearing).
	if cwd := t.policy.CWD(); cwd != "" {
		cmd.Dir = cwd
	}

	detachStdin(cmd)

	var buf lockedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	start := time.Now()
	err := func() error {
		if runtime.GOOS == "windows" {
			return cmd.Run()
		}
		// Unix: start manually so we can kill the process group on cancel.
		// With Setsid, the child (sh) forks grandchildren that inherit pipe
		// fds; killing only sh orphans them and the pipes stay open, causing
		// cmd.Wait() to deadlock (test: TestBash_ContextCancel_KillsProcess).
		if e := cmd.Start(); e != nil {
			return e
		}
		waitDone := make(chan error, 1)
		go func() {
			waitDone <- cmd.Wait()
		}()
		select {
		case err := <-waitDone:
			return err
		case <-cctx.Done():
			killProcessGroup(cmd)
			select {
			case err := <-waitDone:
				return err
			case <-time.After(killWaitGrace):
				return errKillWaitGrace
			}
		}
	}()
	dur := time.Since(start)

	output := buf.snapshot()
	truncated := false
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes]
		truncated = true
	}

	exitCode := 0
	timedOut := false
	if err != nil {
		forcedExit := errors.Is(err, errKillWaitGrace)
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			timedOut = true
			forcedExit = true
		}
		if errors.Is(cctx.Err(), context.Canceled) {
			forcedExit = true
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else if !forcedExit {
			// Non-exit error (e.g. shell not found). Surface verbatim so
			// the model can adjust.
			return "", fmt.Errorf("bash: %w (output: %s)", err, string(output))
		}
	}

	header := fmt.Sprintf("$ %s\n(exit=%d, elapsed=%s", a.Command, exitCode, dur.Round(time.Millisecond))
	if timedOut {
		header += ", TIMED OUT"
	}
	if truncated {
		header += fmt.Sprintf(", output truncated to %d bytes", maxOutputBytes)
	}
	header += ")\n"
	result := header + string(output)
	// Success-path advisory: if the command had a clearly-better
	// dedicated-tool alternative (ls → list_dir, git → git tool,
	// cd-prefix waste, etc.), append a [hint: …] line so the model
	// learns the preferred shape on the next turn. Doesn't block
	// execution and doesn't affect non-matching commands.
	if advisory := bashAdvisory(a.Command); advisory != "" {
		result += "\n[hint: " + advisory + "]\n"
	}
	return result, nil
}

// runBackground launches the command detached and returns immediately
// with a handle. Crucially it binds the process to NOTHING ctx-wise (no
// turn ctx, no timeout ctx): a background job must outlive the turn that
// started it (PRD §4 D5). Cleanup is via the monitor tool's kill or the
// manager's Shutdown at session end — never via turn cancellation.
func (t Tool) runBackground(command string) (string, error) {
	if t.bgMgr == nil {
		return "", errors.New("bash: background execution is unavailable in this session (run_in_background not supported here)")
	}
	job, err := t.bgMgr.Launch(command)
	if err != nil {
		return "", err // concurrency cap reached — message is already actionable
	}

	shell, shArgs := t.shellArgv(command)
	cmd := exec.Command(shell, shArgs...)
	if cwd := t.policy.CWD(); cwd != "" {
		cmd.Dir = cwd
	}
	detachStdin(cmd)
	// Stdout == Stderr (same *Job): os/exec shares a single pipe + copy
	// goroutine, so combined output streams into the ring buffer with no
	// interleave race.
	cmd.Stdout = job
	cmd.Stderr = job

	if err := cmd.Start(); err != nil {
		job.Finish(-1)
		return "", fmt.Errorf("bash: failed to start background job: %w", err)
	}
	job.SetKiller(func() error {
		killProcessGroup(cmd)
		return nil
	})
	go func() {
		job.Finish(exitCodeOf(cmd.Wait()))
	}()

	return fmt.Sprintf("[bg: started %s] $ %s\nTrack with the monitor tool: monitor(job=%q, action=poll|wait|kill).", job.ID, command, job.ID), nil
}

// exitCodeOf extracts a process exit code from cmd.Wait's error: 0 on
// success, the real code on ExitError, -1 for anything else (killed,
// shell-not-found, …).
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
