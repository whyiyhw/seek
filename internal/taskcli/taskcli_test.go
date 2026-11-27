package taskcli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/taskproc"
)

// testEnv builds an Env backed by a temp SEEK_HOME with a fake process
// table and a fake clock. Every write goes to the sandbox (c_rzsvk5).
type testEnv struct {
	*Env
	t        *testing.T
	probes   map[int]taskproc.Probe
	signals  []syscall.Signal
	clock    time.Time
	sleeps   int
	followed int
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	te := &testEnv{
		t:      t,
		probes: map[int]taskproc.Probe{},
		clock:  time.Unix(1700000000, 0),
	}
	store := taskproc.OpenAtHome(home)
	store.Now = func() time.Time { return te.clock }
	te.Env = &Env{
		Store: store,
		Probe: func(pid int) taskproc.Probe {
			if p, ok := te.probes[pid]; ok {
				return p
			}
			return taskproc.ProbeGone
		},
		Signal: func(pid int, sig syscall.Signal) error {
			te.signals = append(te.signals, sig)
			if sig == syscall.SIGKILL {
				te.probes[pid] = taskproc.ProbeGone
			}
			return nil
		},
		Sleep: func(d time.Duration) { te.sleeps++; te.clock = te.clock.Add(d) },
		Now:   func() time.Time { return te.clock },
	}
	return te
}

func (te *testEnv) run(args ...string) (string, string, error) {
	var out, errb bytes.Buffer
	err := RunEnv(te.Env, args, &out, &errb)
	return out.String(), errb.String(), err
}

// seed creates a task record with the given facts.
func (te *testEnv) seed(id string, pid int, evs ...taskproc.Event) {
	te.t.Helper()
	start := taskproc.Event{Kind: taskproc.KindStart, ID: id, Command: "go test ./...", CWD: "/w", PID: pid, StartedAt: te.clock}
	if err := te.Store.Append(id, start); err != nil {
		te.t.Fatal(err)
	}
	for _, ev := range evs {
		if err := te.Store.Append(id, ev); err != nil {
			te.t.Fatal(err)
		}
	}
}

func (te *testEnv) writeLog(id, body string) {
	te.t.Helper()
	if err := os.MkdirAll(te.Store.Dir(), 0o755); err != nil {
		te.t.Fatal(err)
	}
	if err := os.WriteFile(te.Store.LogPath(id), []byte(body), 0o644); err != nil {
		te.t.Fatal(err)
	}
}

// ---------- ps ----------

func TestPS_Table(t *testing.T) {
	code := func(c int) *int { return &c }
	fin := time.Unix(1700000005, 0)

	cases := []struct {
		name   string
		pid    int
		probe  taskproc.Probe
		evs    []taskproc.Event
		want   []string
		absent []string
	}{
		{
			name:  "running",
			pid:   101,
			probe: taskproc.ProbeAlive,
			want:  []string{"running", "101"},
		},
		{
			name:  "zombie is surfaced honestly",
			pid:   102,
			probe: taskproc.ProbeZombie,
			want:  []string{"zombie"},
		},
		{
			name: "exited with code 0",
			pid:  103,
			evs:  []taskproc.Event{{Kind: taskproc.KindExit, ExitCode: code(0), FinishedAt: &fin}},
			want: []string{"exited", " 0 "},
		},
		{
			name: "failed with a non-zero code",
			pid:  104,
			evs:  []taskproc.Event{{Kind: taskproc.KindExit, ExitCode: code(7), FinishedAt: &fin}},
			want: []string{"failed", "7"},
		},
		{
			name: "killed",
			pid:  105,
			evs:  []taskproc.Event{{Kind: taskproc.KindKilled, Signal: "SIGKILL", FinishedAt: &fin}},
			want: []string{"killed", "SIGKILL"},
		},
		{
			name:  "lost (process vanished, no exit fact)",
			pid:   106,
			probe: taskproc.ProbeGone,
			want:  []string{"lost"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t)
			te.probes[tc.pid] = tc.probe
			te.seed("t-1", tc.pid, tc.evs...)
			out, _, err := te.run("ps")
			if err != nil {
				t.Fatalf("ps: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("ps output missing %q:\n%s", w, out)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(out, a) {
					t.Errorf("ps output unexpectedly contains %q:\n%s", a, out)
				}
			}
		})
	}
}

func TestPS_EmptyAndRunningFilter(t *testing.T) {
	te := newTestEnv(t)
	out, _, err := te.run("ps")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no tasks") {
		t.Errorf("empty store should say so:\n%s", out)
	}

	te.seed("t-live", 201)
	te.probes[201] = taskproc.ProbeAlive
	code := 0
	te.seed("t-done", 202, taskproc.Event{Kind: taskproc.KindExit, ExitCode: &code})

	out, _, err = te.run("ps", "--running")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "t-live") {
		t.Errorf("--running dropped a live task:\n%s", out)
	}
	if strings.Contains(out, "t-done") {
		t.Errorf("--running listed a finished task:\n%s", out)
	}
}

// ---------- attach ----------

// TestAttach_FollowsUntilExit: the normal path — print what exists, then
// keep following as the log grows, and stop when the task records its
// exit.
func TestAttach_FollowsUntilExit(t *testing.T) {
	te := newTestEnv(t)
	te.probes[301] = taskproc.ProbeAlive
	te.seed("t-1", 301)
	te.writeLog("t-1", "line one\n")

	// Each Sleep appends the next chunk and, on the second tick, records
	// the exit fact — so the follow loop has new data every round.
	step := 0
	var logf *os.File
	var err error
	logf, err = os.OpenFile(te.Store.LogPath("t-1"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	te.Sleep = func(d time.Duration) {
		te.sleeps++
		te.clock = te.clock.Add(d)
		step++
		switch step {
		case 1:
			_, _ = logf.WriteString("line two\n")
		case 2:
			_, _ = logf.WriteString("line three\n")
			code := 0
			fin := te.clock
			_ = te.Store.Append("t-1", taskproc.Event{Kind: taskproc.KindExit, ExitCode: &code, FinishedAt: &fin})
			te.probes[301] = taskproc.ProbeGone
		}
	}

	out, _, err := te.run("attach", "t-1")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	for _, w := range []string{"line one", "line two", "line three", "exited (0)"} {
		if !strings.Contains(out, w) {
			t.Errorf("attach output missing %q:\n%s", w, out)
		}
	}
	if te.sleeps == 0 {
		t.Error("attach returned without ever following")
	}
}

// TestAttach_TimeoutLeavesTaskRunning: --timeout is observation-only.
// It must return with a hint, and must NOT signal the task.
func TestAttach_TimeoutLeavesTaskRunning(t *testing.T) {
	te := newTestEnv(t)
	te.probes[302] = taskproc.ProbeAlive
	te.seed("t-1", 302)
	te.writeLog("t-1", "still going\n")

	out, _, err := te.run("attach", "t-1", "--timeout", "250ms")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !strings.Contains(out, "still going") || !strings.Contains(out, "stopped following") {
		t.Errorf("timeout output = %q", out)
	}
	if len(te.signals) != 0 {
		t.Errorf("attach signalled the task: %v", te.signals)
	}
	if te.probes[302] != taskproc.ProbeAlive {
		t.Error("attach must leave the task running")
	}
}

func TestAttach_NoFollow(t *testing.T) {
	te := newTestEnv(t)
	te.probes[303] = taskproc.ProbeAlive
	te.seed("t-1", 303)
	te.writeLog("t-1", "snapshot only\n")

	out, _, err := te.run("attach", "t-1", "--no-follow")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "snapshot only") {
		t.Errorf("output = %q", out)
	}
	if te.sleeps != 0 {
		t.Errorf("--no-follow slept %d times", te.sleeps)
	}
}

func TestAttach_MissingLogIsNotAnError(t *testing.T) {
	te := newTestEnv(t)
	te.seed("t-1", 304)
	out, _, err := te.run("attach", "t-1", "--no-follow")
	if err != nil {
		t.Fatalf("attach on a task with no output yet: %v", err)
	}
	if !strings.Contains(out, "t-1") {
		t.Errorf("output = %q", out)
	}
}

func TestAttach_UnknownTask(t *testing.T) {
	te := newTestEnv(t)
	_, _, err := te.run("attach", "t-nope")
	if err == nil || !strings.Contains(err.Error(), "no task") {
		t.Fatalf("want a no-such-task error, got %v", err)
	}
}

func TestAttach_RequiresID(t *testing.T) {
	te := newTestEnv(t)
	if _, _, err := te.run("attach"); err == nil {
		t.Fatal("want an error without an id")
	}
}

// ---------- kill ----------

func TestKill_GRACE_ESCALATION(t *testing.T) {
	te := newTestEnv(t)
	te.probes[401] = taskproc.ProbeAlive
	te.seed("t-1", 401)

	out, _, err := te.run("kill", "t-1", "--grace", "100ms")
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !strings.Contains(out, "killed") || !strings.Contains(out, "SIGKILL") {
		t.Errorf("output = %q", out)
	}
	if len(te.signals) != 2 || te.signals[0] != syscall.SIGTERM || te.signals[1] != syscall.SIGKILL {
		t.Errorf("signals = %v, want [SIGTERM SIGKILL]", te.signals)
	}
}

func TestKill_Cooperative(t *testing.T) {
	te := newTestEnv(t)
	te.probes[402] = taskproc.ProbeAlive
	te.seed("t-1", 402)
	// Model a task that dies on SIGTERM.
	te.Signal = func(pid int, sig syscall.Signal) error {
		te.signals = append(te.signals, sig)
		te.probes[pid] = taskproc.ProbeGone
		return nil
	}
	out, _, err := te.run("kill", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SIGTERM") || strings.Contains(out, "SIGKILL") {
		t.Errorf("output = %q", out)
	}
	if len(te.signals) != 1 {
		t.Errorf("signals = %v, want just SIGTERM", te.signals)
	}
}

func TestKill_AlreadyFinished(t *testing.T) {
	te := newTestEnv(t)
	code := 0
	te.seed("t-1", 403, taskproc.Event{Kind: taskproc.KindExit, ExitCode: &code})
	out, _, err := te.run("kill", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already exited") {
		t.Errorf("output = %q", out)
	}
	if len(te.signals) != 0 {
		t.Errorf("signalled a finished task: %v", te.signals)
	}
}

func TestKill_ZombieIsNotSignalled(t *testing.T) {
	te := newTestEnv(t)
	te.probes[404] = taskproc.ProbeZombie
	te.seed("t-1", 404)
	out, _, err := te.run("kill", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "zombie") {
		t.Errorf("output = %q", out)
	}
	if len(te.signals) != 0 {
		t.Errorf("signalled a zombie: %v", te.signals)
	}
}

func TestKill_UnknownTask(t *testing.T) {
	te := newTestEnv(t)
	_, _, err := te.run("kill", "t-nope")
	if err == nil || !strings.Contains(err.Error(), "no task") {
		t.Fatalf("want no-such-task, got %v", err)
	}
}

// ---------- exec (the wrapper) ----------

// TestExec_RecordsPIDAndExit: the wrapper's two facts are what make an
// orphaned task observable, so assert them directly.
func TestExec_RecordsPIDAndExit(t *testing.T) {
	te := newTestEnv(t)
	te.Exec = func(argv []string) int { return 4 }
	if err := os.MkdirAll(te.Store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := te.Store.Append("t-1", taskproc.Event{Kind: taskproc.KindStart, ID: "t-1", Command: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := te.run("exec", "--id", "t-1", "--", "/bin/sh", "-c", "exit 4"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	got, err := te.Store.Read("t-1", taskproc.ProbeGoneFunc)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != os.Getpid() {
		t.Errorf("pid = %d, want the wrapper's own pid %d", got.PID, os.Getpid())
	}
	if got.Status != taskproc.StatusFailed || got.ExitCode == nil || *got.ExitCode != 4 {
		t.Errorf("status=%q exit=%v, want failed/4", got.Status, got.ExitCode)
	}
}

// TestExec_TaskEnvIsScrubbedOfSeekHome: the wrapper needs SEEK_HOME to
// find the registry, but the TASK must not inherit it — a nested `seek`
// would otherwise write sessions into a home the user never named
// (docs/prd/archive/feature-task-lifetime.md §1.2). This is the real-exec path
// (no Exec stub), so it verifies the actual child environment.
func TestExec_TaskEnvIsScrubbedOfSeekHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/env and a POSIX child argv")
	}
	te := newTestEnv(t)
	te.t.Setenv("SEEK_SHOULD_NOT_LEAK", "1")
	if err := os.MkdirAll(te.Store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := te.Store.Append("t-1", taskproc.Event{Kind: taskproc.KindStart, ID: "t-1", Command: "env"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := te.run("exec", "--id", "t-1", "--", "env"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	log, err := os.ReadFile(te.Store.LogPath("t-1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "SEEK_HOME=") {
		t.Errorf("task child inherited SEEK_HOME:\n%s", log)
	}
	if strings.Contains(string(log), "SEEK_SHOULD_NOT_LEAK") {
		t.Errorf("parent SEEK_* leaked into the task child:\n%s", log)
	}
}

func TestExec_RequiresIDAndArgv(t *testing.T) {
	te := newTestEnv(t)
	if _, _, err := te.run("exec", "--", "/bin/sh", "-c", "true"); err == nil {
		t.Fatal("want an error without --id")
	}
	if _, _, err := te.run("exec", "--id", "t-1"); err == nil {
		t.Fatal("want an error without a child argv")
	}
}

// ---------- run ----------

func TestRun_SpawnsAndPrintsHandle(t *testing.T) {
	te := newTestEnv(t)
	dir := t.TempDir()
	stub := filepath.Join(dir, "seek-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	te.Exe = stub
	out, _, err := te.run("run", "--cwd", dir, "echo hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "seek attach") || !strings.Contains(out, "seek kill") {
		t.Errorf("run output should name the follow-up commands:\n%s", out)
	}
	tasks, err := te.Store.List(taskproc.ProbeGoneFunc)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %+v, err = %v", tasks, err)
	}
	if tasks[0].Command != "echo hi" || tasks[0].CWD != dir {
		t.Errorf("recorded task = %+v", tasks[0])
	}
}

func TestRun_RequiresCommand(t *testing.T) {
	te := newTestEnv(t)
	if _, _, err := te.run("run"); err == nil {
		t.Fatal("want an error with no command")
	}
}

// ---------- dispatch ----------

func TestRun_HelpAndUnknownVerb(t *testing.T) {
	te := newTestEnv(t)
	out, _, err := te.run()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"seek task", "ps", "attach", "kill", "run"} {
		if !strings.Contains(out, w) {
			t.Errorf("help missing %q:\n%s", w, out)
		}
	}
	if _, _, err := te.run("bogus"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown-verb error, got %v", err)
	}
}

// TestRunAlias: `seek ps` / `seek attach` / `seek kill` must route to the
// same handlers as the `seek task` verbs — one implementation, two
// spellings.
func TestRunAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	store := taskproc.OpenAtHome(home)
	code := 0
	if err := store.Append("t-1", taskproc.Event{Kind: taskproc.KindStart, ID: "t-1", Command: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("t-1", taskproc.Event{Kind: taskproc.KindExit, ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunAlias("ps", nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "t-1") || !strings.Contains(out.String(), "exited") {
		t.Errorf("alias ps output = %q", out.String())
	}
}
