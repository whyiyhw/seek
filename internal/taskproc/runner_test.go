package taskproc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeProc is a scripted process table: it answers "alive?" from a set
// the test flips, and records every signal it receives.
type fakeProc struct {
	mu      sync.Mutex
	alive   map[int]Probe
	signals []struct {
		pid int
		sig syscall.Signal
	}
	// onSignal lets a test model a task that dies on SIGTERM.
	onSignal func(pid int, sig syscall.Signal)
}

func newFakeProc() *fakeProc { return &fakeProc{alive: map[int]Probe{}} }

func (f *fakeProc) probe(pid int) Probe {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.alive[pid]; ok {
		return p
	}
	return ProbeGone
}

func (f *fakeProc) signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	f.signals = append(f.signals, struct {
		pid int
		sig syscall.Signal
	}{pid, sig})
	cb := f.onSignal
	f.mu.Unlock()
	if cb != nil {
		cb(pid, sig)
	}
	return nil
}

func (f *fakeProc) sent() []syscall.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]syscall.Signal, 0, len(f.signals))
	for _, s := range f.signals {
		out = append(out, s.sig)
	}
	return out
}

// TestRunner_Kill covers the three real outcomes: a cooperative task
// dies on SIGTERM, a wedged one escalates to SIGKILL, and a finished or
// zombie task is left alone.
func TestRunner_Kill(t *testing.T) {
	start := time.Unix(1700000000, 0)
	cases := []struct {
		name        string
		probe       Probe
		dieOnTerm   bool
		wantSignals []syscall.Signal
		wantStatus  Status
		wantSignal  string
	}{
		{
			name:        "cooperative task dies on SIGTERM",
			probe:       ProbeAlive,
			dieOnTerm:   true,
			wantSignals: []syscall.Signal{syscall.SIGTERM},
			wantStatus:  StatusKilled,
			wantSignal:  "SIGTERM",
		},
		{
			name:        "wedged task escalates to SIGKILL",
			probe:       ProbeAlive,
			dieOnTerm:   false,
			wantSignals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL},
			wantStatus:  StatusKilled,
			wantSignal:  "SIGKILL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			fp := newFakeProc()
			if _, err := appendStart(t, s, Event{ID: "t-1", Command: "sleep 100", PID: 4242, StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			fp.alive[4242] = tc.probe
			if tc.dieOnTerm {
				fp.onSignal = func(pid int, sig syscall.Signal) {
					if sig == syscall.SIGTERM {
						fp.mu.Lock()
						delete(fp.alive, pid)
						fp.mu.Unlock()
					}
				}
			}
			// Fake clock: each Sleep advances it past the grace window so
			// the escalation path is exercised without real waiting.
			clock := start
			r := &Runner{
				Store:  s,
				Probe:  fp.probe,
				Signal: fp.signal,
				Now:    func() time.Time { return clock },
				Sleep:  func(d time.Duration) { clock = clock.Add(d) },
			}
			got, err := r.Kill("t-1", 3*time.Second)
			if err != nil {
				t.Fatalf("Kill: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Signal != tc.wantSignal {
				t.Errorf("signal = %q, want %q", got.Signal, tc.wantSignal)
			}
			if got.FinishedAt == nil {
				t.Error("FinishedAt must be set by kill")
			}
			gotSigs := fp.sent()
			if len(gotSigs) != len(tc.wantSignals) {
				t.Fatalf("signals = %v, want %v", gotSigs, tc.wantSignals)
			}
			for i := range gotSigs {
				if gotSigs[i] != tc.wantSignals[i] {
					t.Errorf("signal[%d] = %v, want %v", i, gotSigs[i], tc.wantSignals[i])
				}
			}
		})
	}
}

func TestRunner_KillFinishedTaskIsNoOp(t *testing.T) {
	s := newStore(t)
	fp := newFakeProc()
	code := 0
	if _, err := appendStart(t, s, Event{ID: "t-1", Command: "echo hi", PID: 11}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("t-1", Event{Kind: KindExit, ID: "t-1", ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Store: s, Probe: fp.probe, Signal: fp.signal, Now: time.Now, Sleep: func(time.Duration) {}}
	got, err := r.Kill("t-1", time.Second)
	if err != nil {
		t.Fatalf("Kill on finished task: %v", err)
	}
	if got.Status != StatusExited {
		t.Errorf("status = %q, want exited", got.Status)
	}
	if sigs := fp.sent(); len(sigs) != 0 {
		t.Errorf("signalled a finished task: %v", sigs)
	}
}

func TestRunner_KillZombieIsNoOp(t *testing.T) {
	s := newStore(t)
	fp := newFakeProc()
	if _, err := appendStart(t, s, Event{ID: "t-1", Command: "x", PID: 11}); err != nil {
		t.Fatal(err)
	}
	fp.alive[11] = ProbeZombie
	r := &Runner{Store: s, Probe: fp.probe, Signal: fp.signal, Now: time.Now, Sleep: func(time.Duration) {}}
	got, err := r.Kill("t-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusZombie {
		t.Errorf("status = %q, want zombie", got.Status)
	}
	if sigs := fp.sent(); len(sigs) != 0 {
		t.Errorf("signalled a zombie: %v", sigs)
	}
}

// TestRunner_KillDuringStartWindowRefuses: between the start fact and the
// wrapper's pid fact there is nothing to signal. Recording `killed` there
// would leave the real task running while `ps` claimed it was dead — the
// one lie an operator cannot recover from — so Kill must refuse and leave
// the record untouched for a retry.
func TestRunner_KillDuringStartWindowRefuses(t *testing.T) {
	s := newStore(t)
	fp := newFakeProc()
	if _, err := appendStart(t, s, Event{ID: "t-1", Command: "x"}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Store: s, Probe: fp.probe, Signal: fp.signal, Now: time.Now, Sleep: func(time.Duration) {}}
	if _, err := r.Kill("t-1", time.Second); err == nil || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("want a still-starting error, got %v", err)
	}
	if sigs := fp.sent(); len(sigs) != 0 {
		t.Errorf("signalled pid 0: %v", sigs)
	}
	got, err := s.Read("t-1", ProbeGoneFunc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusStarting {
		t.Errorf("status = %q, want starting (no killed fact written)", got.Status)
	}
}

func TestRunner_KillUnknownTask(t *testing.T) {
	s := newStore(t)
	r := &Runner{Store: s, Probe: ProbeGoneFunc, Now: time.Now, Sleep: func(time.Duration) {}}
	if _, err := r.Kill("nope", time.Second); err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestRunner_SpawnShape pins the two things the detach contract depends
// on and that a refactor could silently break: the wrapper argv
// (`task exec --id <id> -- <shell> -c <command>`) and SEEK_HOME being
// handed to the wrapper while the parent's other SEEK_* do not leak.
// The "seek binary" is a stub script, so nothing is really detached.
func TestRunner_SpawnShape(t *testing.T) {
	s := newStore(t)
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	envFile := filepath.Join(dir, "env.txt")
	stub := filepath.Join(dir, "seek-stub")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\nenv > " + envFile + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEEK_SHOULD_NOT_LEAK", "1")
	t.Setenv("DEEPSEEK_API_KEY", "sk-secret")

	r := &Runner{Store: s, Exe: stub, Probe: ProbeGoneFunc, Now: time.Now}
	task, err := r.Spawn(SpawnOptions{Command: "echo hi", CWD: dir})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Wait for the stub to finish writing (it is detached, so poll).
	var argv []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if argv, err = os.ReadFile(argvFile); err == nil && len(argv) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(argv) == 0 {
		t.Fatalf("stub never ran (record: %+v)", task)
	}
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{"task", "exec", "--id", task.ID, "--"}
	if len(lines) < len(want)+3 {
		t.Fatalf("argv too short: %q", lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, lines[i], w)
		}
	}
	if lines[len(want)] != "/bin/sh" || lines[len(want)+1] != "-c" || lines[len(want)+2] != "echo hi" {
		t.Errorf("shell tail = %q, want /bin/sh -c 'echo hi'", lines[len(want):])
	}

	// The stub writes argv then env; poll for the second file rather than
	// assuming both landed in the same instant.
	var envBytes []byte
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if envBytes, err = os.ReadFile(envFile); err == nil && len(envBytes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(envBytes) == 0 {
		t.Fatalf("stub env file never appeared: %v", err)
	}
	env := string(envBytes)
	if !strings.Contains(env, "SEEK_HOME="+s.HomeDir()) {
		t.Errorf("wrapper did not receive SEEK_HOME=%s; env:\n%s", s.HomeDir(), env)
	}
	if strings.Contains(env, "SEEK_SHOULD_NOT_LEAK") {
		t.Errorf("parent SEEK_* leaked into the wrapper:\n%s", env)
	}
	if strings.Contains(env, "sk-secret") {
		t.Errorf("a credential leaked into the wrapper:\n%s", env)
	}
}

func TestRunner_SpawnBadExeRecordsFailure(t *testing.T) {
	s := newStore(t)
	r := &Runner{Store: s, Exe: filepath.Join(t.TempDir(), "does-not-exist"), Probe: ProbeGoneFunc, Now: time.Now}
	if _, err := r.Spawn(SpawnOptions{Command: "echo hi", CWD: t.TempDir()}); err == nil {
		t.Fatal("want an error from a missing exe")
	}
	// A failure fact must exist even though spawn failed, so `seek ps`
	// can explain the id instead of showing a record stuck on "running".
	tasks, err := s.List(ProbeGoneFunc)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d records, want 1", len(tasks))
	}
	if tasks[0].Status != StatusFailed {
		t.Errorf("status = %q, want failed", tasks[0].Status)
	}
}

func TestRunner_SpawnRequiresCommand(t *testing.T) {
	s := newStore(t)
	r := &Runner{Store: s, Exe: "/bin/true", Now: time.Now}
	if _, err := r.Spawn(SpawnOptions{}); err == nil {
		t.Fatal("want an error for an empty command")
	}
}
