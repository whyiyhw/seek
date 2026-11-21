package taskproc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newStore builds a store rooted in a temp SEEK_HOME. The env var is set
// as well (c_rzsvk5 lesson) so any code path that falls back to
// paths.Home() inside the test still lands in the sandbox.
func newStore(t *testing.T) *Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	return OpenAtHome(home)
}

// appendStart writes the start fact the way the spawner does (metadata
// first, pid second) and returns the folded task.
func appendStart(t *testing.T, s *Store, ev Event) (Task, error) {
	t.Helper()
	if ev.Kind == "" {
		ev.Kind = KindStart
	}
	if ev.StartedAt.IsZero() {
		ev.StartedAt = s.Now()
	}
	if err := s.Append(ev.ID, ev); err != nil {
		return Task{}, err
	}
	return s.Read(ev.ID, ProbeGoneFunc)
}

func TestStore_OpenUsesSeekHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if want := filepath.Join(home, "tasks"); s.Dir() != want {
		t.Errorf("Dir() = %q, want %q", s.Dir(), want)
	}
	if s.HomeDir() != home {
		t.Errorf("HomeDir() = %q, want %q", s.HomeDir(), home)
	}
}

func TestStore_NewIDIsSortableAndUnique(t *testing.T) {
	s := newStore(t)
	s.Now = func() time.Time { return time.Unix(1700000000, 0) }
	a, err := s.NewID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "t-1700000000-") {
		t.Errorf("id %q not prefixed with the fake clock's unix time", a)
	}
	if a == b {
		t.Errorf("ids collided: %q", a)
	}
}

// TestFold_StatusTable is the heart of "facts on disk, status computed":
// every fact combination maps to exactly one status, and a live probe
// never overrides a recorded outcome.
func TestFold_StatusTable(t *testing.T) {
	start := time.Unix(1700000000, 0)
	code := func(c int) *int { return &c }
	exitAt := start.Add(5 * time.Second)

	cases := []struct {
		name   string
		evs    []Event
		probe  Probe
		want   Status
		wantEx *int
	}{
		{
			name:  "no pid yet reads as starting, never lost",
			evs:   []Event{{Kind: KindStart, StartedAt: start}},
			probe: ProbeGone,
			want:  StatusStarting,
		},
		{
			name:  "running while the pid answers alive",
			evs:   []Event{{Kind: KindStart, PID: 42, StartedAt: start}},
			probe: ProbeAlive,
			want:  StatusRunning,
		},
		{
			name:  "zombie is reported as zombie, not running",
			evs:   []Event{{Kind: KindStart, PID: 42, StartedAt: start}},
			probe: ProbeZombie,
			want:  StatusZombie,
		},
		{
			name:  "gone with no exit fact is lost",
			evs:   []Event{{Kind: KindStart, PID: 42, StartedAt: start}},
			probe: ProbeGone,
			want:  StatusLost,
		},
		{
			name: "exit 0 is exited",
			evs: []Event{{Kind: KindStart, PID: 42, StartedAt: start},
				{Kind: KindExit, ExitCode: code(0), FinishedAt: &exitAt}},
			probe:  ProbeAlive, // a recycled pid must not resurrect it
			want:   StatusExited,
			wantEx: code(0),
		},
		{
			name: "non-zero exit is failed",
			evs: []Event{{Kind: KindStart, PID: 42, StartedAt: start},
				{Kind: KindExit, ExitCode: code(3), FinishedAt: &exitAt}},
			probe:  ProbeGone,
			want:   StatusFailed,
			wantEx: code(3),
		},
		{
			name: "killed after the process is gone",
			evs: []Event{{Kind: KindStart, PID: 42, StartedAt: start},
				{Kind: KindKilled, Signal: "SIGTERM", FinishedAt: &exitAt}},
			probe: ProbeGone,
			want:  StatusKilled,
		},
		{
			name: "a real exit outranks a later kill fact",
			evs: []Event{{Kind: KindStart, PID: 42, StartedAt: start},
				{Kind: KindExit, ExitCode: code(0), FinishedAt: &exitAt},
				{Kind: KindKilled, Signal: "SIGTERM", FinishedAt: &exitAt}},
			probe:  ProbeGone,
			want:   StatusExited,
			wantEx: code(0),
		},
		{
			name: "pid written by the wrapper is folded in",
			evs: []Event{{Kind: KindStart, StartedAt: start},
				{Kind: KindStart, PID: 99}},
			probe: ProbeAlive,
			want:  StatusRunning,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold("t-1", "/tmp/log", tc.evs, func(int) Probe { return tc.probe }, start.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
			if tc.wantEx == nil {
				if got.ExitCode != nil {
					t.Errorf("exit code = %d, want none", *got.ExitCode)
				}
			} else if got.ExitCode == nil || *got.ExitCode != *tc.wantEx {
				t.Errorf("exit code = %v, want %d", got.ExitCode, *tc.wantEx)
			}
		})
	}
}

func TestFold_NoStartFactIsNotFound(t *testing.T) {
	_, err := fold("t-1", "", []Event{{Kind: KindExit}}, ProbeGoneFunc, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no start fact") {
		t.Fatalf("want a no-start-fact error, got %v", err)
	}
}

func TestTask_ElapsedFreezesAtFinish(t *testing.T) {
	start := time.Unix(1000, 0)
	fin := start.Add(30 * time.Second)
	running := Task{StartedAt: start}
	if got := running.Elapsed(start.Add(90 * time.Second)); got != 90*time.Second {
		t.Errorf("running elapsed = %v, want 90s", got)
	}
	done := Task{StartedAt: start, FinishedAt: &fin}
	if got := done.Elapsed(start.Add(10 * time.Minute)); got != 30*time.Second {
		t.Errorf("finished elapsed = %v, want frozen 30s", got)
	}
	// A clock that moved backwards must not produce a negative duration.
	if got := (Task{StartedAt: start}).Elapsed(start.Add(-time.Hour)); got != 0 {
		t.Errorf("elapsed with a backwards clock = %v, want 0", got)
	}
}

func TestStore_AppendReadRoundTrip(t *testing.T) {
	s := newStore(t)
	s.Now = func() time.Time { return time.Unix(1700000000, 0) }
	if _, err := appendStart(t, s, Event{ID: "t-1", Command: "sleep 5", CWD: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("t-1", Event{Kind: KindStart, ID: "t-1", PID: 1234}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("t-1", ProbeGoneFunc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "sleep 5" || got.CWD != "/tmp" || got.PID != 1234 {
		t.Errorf("folded task = %+v", got)
	}
	if got.LogPath != s.LogPath("t-1") {
		t.Errorf("LogPath = %q, want %q", got.LogPath, s.LogPath("t-1"))
	}
}

func TestStore_ReadMissingIsNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.Read("nope", ProbeGoneFunc)
	if err == nil || !strings.Contains(err.Error(), "no such task") {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestStore_ReadToleratesPartialAndCorruptLines: the record is written
// by three processes, so a reader can always catch a half-written
// trailing line. It must be ignored, not fail the read.
func TestStore_ReadToleratesPartialAndCorruptLines(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"start","id":"t-1","command":"echo hi","started_at":"2026-01-01T00:00:00Z"}
not json at all
{"kind":"start","id":"t-1","pid":7}
{"kind":"exit","id":"t-1","exit_code":0,"finished_at":"2026-01-01T00:00:02Z"}
{"kind":"exit","partial`
	if err := os.WriteFile(s.RecordPath("t-1"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("t-1", ProbeGoneFunc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusExited || got.PID != 7 || got.Command != "echo hi" {
		t.Errorf("folded = %+v", got)
	}
}

// TestStore_ListSkipsCorruptRecord: `seek ps` is a diagnostic surface —
// one bad file must not take the whole listing down.
func TestStore_ListSkipsCorruptRecord(t *testing.T) {
	s := newStore(t)
	if _, err := appendStart(t, s, Event{ID: "t-1", Command: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := appendStart(t, s, Event{ID: "t-2", Command: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.RecordPath("t-3"), []byte("{garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ProbeGoneFunc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d tasks, want 2 (corrupt skipped)", len(got))
	}
	// Newest-first ordering (ids embed a monotonic counter in tests only
	// via the random suffix, so assert by set rather than by order).
	seen := map[string]bool{}
	for _, tk := range got {
		seen[tk.ID] = true
	}
	if !seen["t-1"] || !seen["t-2"] {
		t.Errorf("missing tasks in %+v", got)
	}
}

func TestStore_ListMissingDirIsEmpty(t *testing.T) {
	s := newStore(t)
	got, err := s.List(ProbeGoneFunc)
	if err != nil || len(got) != 0 {
		t.Fatalf("List on empty store = %+v, %v", got, err)
	}
}

func TestStore_CopyLogIncremental(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing log is not an error.
	var buf bytes.Buffer
	off, err := s.CopyLog(&buf, "t-1", 0)
	if err != nil || off != 0 || buf.Len() != 0 {
		t.Fatalf("missing log: off=%d err=%v", off, err)
	}
	if err := os.WriteFile(s.LogPath("t-1"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off, err = s.CopyLog(&buf, "t-1", off)
	if err != nil || buf.String() != "hello\n" || off != 6 {
		t.Fatalf("first copy: %q off=%d err=%v", buf.String(), off, err)
	}
	// Nothing new: no output, offset unchanged.
	buf.Reset()
	off, err = s.CopyLog(&buf, "t-1", off)
	if err != nil || buf.Len() != 0 || off != 6 {
		t.Fatalf("idle copy: %q off=%d err=%v", buf.String(), off, err)
	}
	if _, err := os.OpenFile(s.LogPath("t-1"), os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(s.LogPath("t-1"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("world\n")
	_ = f.Close()
	off, err = s.CopyLog(&buf, "t-1", off)
	if err != nil || buf.String() != "world\n" {
		t.Fatalf("append copy: %q off=%d err=%v", buf.String(), off, err)
	}
	// A truncated log re-reads from the top rather than seeking past EOF.
	if err := os.WriteFile(s.LogPath("t-1"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if _, err := s.CopyLog(&buf, "t-1", off); err != nil || buf.String() != "x" {
		t.Fatalf("truncated copy: %q err=%v", buf.String(), err)
	}
}

func TestStatus_Terminal(t *testing.T) {
	for st, want := range map[Status]bool{
		StatusStarting: false,
		StatusRunning:  false,
		StatusZombie:   false,
		StatusExited:   true,
		StatusFailed:   true,
		StatusKilled:   true,
		StatusLost:     true,
	} {
		if got := st.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", st, got, want)
		}
	}
}
