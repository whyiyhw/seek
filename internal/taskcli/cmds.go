package taskcli

// The five verbs. Read-side verbs (ps / attach) never write; kill writes
// one terminal fact; run writes the start fact and forks the wrapper;
// exec IS the wrapper, i.e. the process whose lifetime is the task's.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/childenv"
	"github.com/whyiyhw/seek/internal/taskproc"
)

// splitPositionals separates the positional arguments from the flags so
// a flag may appear on EITHER side of the id (`seek attach t-1 --timeout
// 30s` is what people type; `flag.Parse` stops at the first positional and
// would silently ignore everything after it — a follow that never times
// out looks like a hang, not a usage error).
//
// valueFlags names the flags that consume the NEXT argv element, so a
// value like "30s" isn't mistaken for a positional. `--flag=value` and
// `-no-follow` (boolean) need no special casing beyond that.
func splitPositionals(args []string, valueFlags map[string]bool) (flags, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			return flags, pos
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				continue // value is attached
			}
			if valueFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			pos = append(pos, a)
		}
	}
	return flags, pos
}

// cmdPS lists every recorded task. The default view is ALL of them (the
// question "what is running" is answered by the STATUS column, and
// hiding finished tasks makes a just-killed task look like it vanished);
// --running narrows it.
func cmdPS(e *Env, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	fs.SetOutput(stderr)
	onlyRunning := fs.Bool("running", false, "only tasks still running")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tasks, err := e.Store.List(e.Probe)
	if err != nil {
		return err
	}
	if *onlyRunning {
		kept := tasks[:0]
		for _, t := range tasks {
			if !t.Status.Terminal() {
				kept = append(kept, t)
			}
		}
		tasks = kept
	}
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "no tasks")
		return nil
	}
	now := e.Now()
	fmt.Fprintf(stdout, "%-24s  %-8s  %-5s  %-9s  %-7s  %s\n", "ID", "STATUS", "EXIT", "ELAPSED", "PID", "COMMAND")
	for _, t := range tasks {
		exit := "-"
		if t.ExitCode != nil {
			exit = fmt.Sprint(*t.ExitCode)
		} else if t.Signal != "" {
			exit = t.Signal
		}
		fmt.Fprintf(stdout, "%-24s  %-8s  %-5s  %-9s  %-7d  %s\n",
			t.ID, t.Status, exit, t.Elapsed(now).Round(time.Second), t.PID, oneLine(t.Command))
	}
	return nil
}

// cmdAttach prints the task's output and follows it until the task
// reaches a terminal state. Two exits are deliberate and non-error: the
// task finishing (normal) and --timeout expiring (the caller asked to
// stop watching; the task keeps running — same contract as the monitor
// tool's wait, where observation never kills).
func cmdAttach(e *Env, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noFollow := fs.Bool("no-follow", false, "print what is there now and return")
	timeout := fs.Duration("timeout", attachDefaultTimeout, "stop following after this long (0 = until the task ends)")
	flags, pos := splitPositionals(args, map[string]bool{"timeout": true})
	if err := fs.Parse(flags); err != nil {
		return err
	}
	id := firstArg(pos)
	if id == "" {
		return errors.New("attach: task id required (see `seek ps`)")
	}
	t, err := e.Store.Read(id, e.Probe)
	if err != nil {
		return describeReadErr("attach", id, err)
	}
	fmt.Fprintf(stdout, "[task %s] %s\n[status %s, pid %d, cwd %s]\n", t.ID, oneLine(t.Command), t.Status, t.PID, t.CWD)

	var offset int64
	var deadline time.Time
	if *timeout > 0 {
		deadline = e.Now().Add(*timeout)
	}
	for {
		offset, err = e.Store.CopyLog(stdout, id, offset)
		if err != nil {
			return err
		}
		if *noFollow || t.Status.Terminal() {
			fmt.Fprintf(stdout, "\n[task %s %s]\n", t.ID, terminalLine(t))
			return nil
		}
		if !deadline.IsZero() && !e.Now().Before(deadline) {
			fmt.Fprintf(stdout, "\n[task %s still %s; stopped following after %s — `seek attach %s` resumes]\n",
				t.ID, t.Status, *timeout, t.ID)
			return nil
		}
		e.Sleep(attachPoll)
		if t, err = e.Store.Read(id, e.Probe); err != nil {
			return describeReadErr("attach", id, err)
		}
	}
}

// cmdKill terminates the task and records the fact.
func cmdKill(e *Env, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	grace := fs.Duration("grace", taskproc.DefaultKillGrace, "wait this long after SIGTERM before SIGKILL")
	flags, pos := splitPositionals(args, map[string]bool{"grace": true})
	if err := fs.Parse(flags); err != nil {
		return err
	}
	id := firstArg(pos)
	if id == "" {
		return errors.New("kill: task id required (see `seek ps`)")
	}
	r := &taskproc.Runner{Store: e.Store, Exe: e.Exe, Probe: e.Probe, Signal: e.Signal, Sleep: e.Sleep, Now: e.Now}
	before, err := e.Store.Read(id, e.Probe)
	if err != nil {
		return describeReadErr("kill", id, err)
	}
	if before.Status.Terminal() {
		fmt.Fprintf(stdout, "[task %s already %s — nothing to kill]\n", id, before.Status)
		return nil
	}
	t, err := r.Kill(id, *grace)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[task %s %s]\n", t.ID, terminalLine(t))
	return nil
}

// cmdRun spawns a detached task.
func cmdRun(e *Env, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("task run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cwd := fs.String("cwd", "", "working directory for the command (default: current)")
	session := fs.String("session", "", "session id to attribute this task to (informational)")
	project := fs.String("project", "", "project path to attribute this task to (informational)")
	flags, pos := splitPositionals(args, map[string]bool{"cwd": true, "session": true, "project": true})
	if err := fs.Parse(flags); err != nil {
		return err
	}
	command := strings.TrimSpace(strings.Join(pos, " "))
	if command == "" {
		return errors.New(`task run: command required, e.g. seek task run "go test ./..."`)
	}
	r := &taskproc.Runner{Store: e.Store, Exe: e.Exe, Probe: e.Probe, Signal: e.Signal, Sleep: e.Sleep, Now: e.Now}
	t, err := r.Spawn(taskproc.SpawnOptions{Command: command, CWD: *cwd, Session: *session, Project: *project})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[task %s] %s\nStarted detached; it keeps running if seek exits.\nFollow: seek attach %s    Stop: seek kill %s\n",
		t.ID, command, t.ID, t.ID)
	return nil
}

// cmdExec is the wrapper: the process the task's lifetime belongs to.
// It records its own pid (so `ps`/`kill` have a target even if the
// spawner died first), runs the child with output appended to the log,
// and records the exit fact. Its argv after "--" is the child argv.
func cmdExec(e *Env, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("task exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "task id (set by `seek task run`)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// flag.Parse consumes the "--" terminator itself, so Args() is
	// already the child argv (possibly empty).
	argv := fs.Args()
	if *id == "" {
		return errors.New("task exec: --id required")
	}
	if len(argv) == 0 {
		return errors.New("task exec: child argv after -- required")
	}

	// Fact order matters: pid before the child can exit, so a task that
	// finishes instantly still leaves a complete record.
	if err := e.Store.Append(*id, taskproc.Event{Kind: taskproc.KindStart, ID: *id, PID: os.Getpid()}); err != nil {
		return err
	}

	logf, err := os.OpenFile(e.Store.LogPath(*id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("task exec: open log: %w", err)
	}
	defer logf.Close()

	code := 0
	if e.Exec != nil {
		code = e.Exec(argv)
	} else {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout = logf
		cmd.Stderr = logf
		cmd.Env = taskChildEnv()
		// No cmd.Dir: the wrapper was spawned with the task's cwd, so
		// the child inherits it (§1.2 CWD row).
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			code = 0
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			fmt.Fprintf(logf, "\ntask exec: %v\n", err)
			code = -1
		}
	}

	now := e.Now()
	if err := e.Store.Append(*id, taskproc.Event{
		Kind: taskproc.KindExit, ID: *id, ExitCode: &code, FinishedAt: &now,
	}); err != nil {
		return err
	}
	return nil
}

// taskChildEnv is the environment the TASK command runs under: fully
// scrubbed, with nothing appended. The wrapper was handed
// SEEK_HOME=<home> only so it could find this registry; handing that on
// would let a nested `seek` write sessions into a home the user never
// named (docs/prd/feature-task-lifetime.md §1.2, second segment).
func taskChildEnv() []string { return childenv.Sanitized() }

// terminalLine renders a finished task's summary for attach/kill.
func terminalLine(t taskproc.Task) string {
	switch t.Status {
	case taskproc.StatusExited:
		return "exited (0)"
	case taskproc.StatusFailed:
		if t.Reason != "" {
			return "failed — " + t.Reason
		}
		if t.ExitCode != nil {
			return fmt.Sprintf("failed (exit=%d)", *t.ExitCode)
		}
		return "failed"
	case taskproc.StatusKilled:
		if t.Signal != "" {
			return fmt.Sprintf("killed (%s)", t.Signal)
		}
		return "killed"
	case taskproc.StatusLost:
		return "lost — the process disappeared without recording an exit code"
	}
	return string(t.Status)
}

func describeReadErr(verb, id string, err error) error {
	if errors.Is(err, taskproc.ErrNotFound) {
		return fmt.Errorf("%s: no task %q — run `seek ps` to list tasks", verb, id)
	}
	return fmt.Errorf("%s: %w", verb, err)
}

// firstArg returns the first positional, or "".
func firstArg(pos []string) string {
	if len(pos) == 0 {
		return ""
	}
	return pos[0]
}

// oneLine collapses a command to a single display line — the table and
// the attach header are line-oriented, and a multi-line command would
// shear the columns.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
