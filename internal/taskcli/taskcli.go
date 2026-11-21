// Package taskcli implements `seek ps` / `seek attach` / `seek kill` and
// the `seek task run|exec` verbs behind them (v8 柱 V1,
// docs/prd/feature-task-lifetime.md).
//
// Structure mirrors the other *cli packages (worktreecli, checkpointcli):
// a Run(args, stdout, stderr) entry point that dispatches verbs. The one
// addition is Env: every ambient dependency (clock, process probe,
// signal delivery, sleep, child exec) arrives as a field, so the three
// management subcommands are table-testable against fake processes,
// forced timeouts and grafted zombie states — no reliance on the test
// machine's process table.
//
// Why not a daemon: D2's refined reading of "zero daemon" (v8 §3 I2)
// allows a task's LIFETIME to escape the seek process; it does not allow
// a resident supervisor. Nothing here polls, listens, or stays alive
// between invocations — each subcommand runs once and exits, and the
// task's own wrapper process is what records the exit.
package taskcli

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"github.com/whyiyhw/seek/internal/taskproc"
)

// attachPoll is how often `seek attach` re-reads the log.
const attachPoll = 100 * time.Millisecond

// attachDefaultTimeout is 0 = follow until the task ends, which is what a
// human at a terminal wants. Unattended callers pass --timeout so a stuck
// task cannot hang a CI step forever (observation never kills the task).
const attachDefaultTimeout = 0

// Env is the injected ambient surface. Only Store is required; the rest
// default to the real platform behaviour.
type Env struct {
	Store  *taskproc.Store
	Probe  taskproc.ProbeFunc
	Signal func(pid int, sig syscall.Signal) error
	Sleep  func(time.Duration)
	Now    func() time.Time
	// Exec runs the wrapper's child argv and returns its exit code.
	// Production leaves it nil (cmdExec uses os/exec); tests inject a
	// stub so the wrapper's fact-writing can be exercised without
	// forking.
	Exec func(argv []string) int
	// Exe overrides the seek binary the wrapper is spawned from.
	Exe string
}

// Run is the public entry for `seek task ...` and for the top-level
// aliases ps / attach / kill.
func Run(args []string, stdout, stderr io.Writer) error {
	return RunEnv(nil, args, stdout, stderr)
}

// RunEnv is Run with an explicit Env (nil = production wiring).
func RunEnv(env *Env, args []string, stdout, stderr io.Writer) error {
	e, err := resolve(env)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "ps", "list", "ls":
		return cmdPS(e, rest, stdout, stderr)
	case "attach":
		return cmdAttach(e, rest, stdout, stderr)
	case "kill":
		return cmdKill(e, rest, stdout, stderr)
	case "run":
		return cmdRun(e, rest, stdout, stderr)
	case "exec":
		return cmdExec(e, rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	}
	return fmt.Errorf("unknown task subcommand %q (try `seek task help`)", verb)
}

// RunAlias dispatches a top-level alias (`seek ps`, `seek attach`,
// `seek kill`) into the same verb handlers, so there is exactly one
// implementation of each.
func RunAlias(verb string, args []string, stdout, stderr io.Writer) error {
	return Run(append([]string{verb}, args...), stdout, stderr)
}

func resolve(env *Env) (*Env, error) {
	if env == nil {
		st, err := taskproc.Open()
		if err != nil {
			return nil, err
		}
		env = &Env{Store: st}
	}
	if env.Store == nil {
		return nil, errors.New("taskcli: Env.Store is required")
	}
	if env.Probe == nil {
		env.Probe = taskproc.ProbePID
	}
	if env.Signal == nil {
		env.Signal = func(pid int, sig syscall.Signal) error { return taskproc.SignalGroup(pid, sig) }
	}
	if env.Sleep == nil {
		env.Sleep = time.Sleep
	}
	if env.Now == nil {
		now := env.Store.Now
		if now == nil {
			now = time.Now
		}
		env.Now = now
	}
	return env, nil
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `seek task — tasks whose lifetime outlives the seek process

Usage:
  seek task run [--cwd DIR] "<command>"   start a detached task, print its id
  seek ps                                 list every task with status
  seek attach <id> [--no-follow] [--timeout D]
                                          print the task's output and follow it
  seek kill <id> [--grace D]              SIGTERM the task's process group,
                                          escalate to SIGKILL after D (default 3s)

Aliases: seek ps / seek attach / seek kill are the same handlers.

A task keeps running after seek exits, but nothing is resident: ps / attach /
kill each read ~/.seek/tasks/ once and exit. Track a session-scoped background
job with the monitor tool instead — those die with the session.`)
}
