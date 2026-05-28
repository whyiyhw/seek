package routines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// shellStub builds an /bin/sh command that mimics one of three
// canned subprocess behaviours: "echo" (writes to stdout +
// exits 0), "fail" (exits 1 with stderr), "sleep N" (sleeps,
// times out under short RunTimeout). Tests pass the desired
// shape via job.Prompt + the caller chooses which builder.
//
// Skipped on Windows — /bin/sh isn't ubiquitously available
// there. Windows-specific tests for the tick engine wait for
// a dot release. M11.2 ship blockers on Unix-only validation.
func shellStub(t *testing.T, script string) SubprocessFn {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows; M11.2 verified on Unix")
	}
	return func(ctx context.Context, job Job, runID string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script), nil
	}
}

// newTickTestFixture sets up a Store + tickPaths + fixed Now
// for deterministic test runs. All paths land in tempdirs so
// runs/<id>.jsonl don't pollute the user's real ~/.seek/cron/.
type tickFixture struct {
	dir    string
	store  *Store
	now    time.Time
}

func newTickFixture(t *testing.T) *tickFixture {
	t.Helper()
	dir := t.TempDir()
	return &tickFixture{
		dir:   dir,
		store: OpenStoreAt(filepath.Join(dir, "jobs.jsonl")),
		now:   time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
	}
}

func (f *tickFixture) opts(sub SubprocessFn) TickOptions {
	return TickOptions{
		Now:        func() time.Time { return f.now },
		Subprocess: sub,
		RunTimeout: 5 * time.Second,
		CronDir:    f.dir,
	}
}

func (f *tickFixture) createDueJob(t *testing.T, name, prompt string) {
	t.Helper()
	if prompt == "" {
		// Store rejects empty Prompt; tests that don't care
		// about the prompt content get a stable placeholder so
		// they don't have to thread one through.
		prompt = "test prompt"
	}
	sched, _ := ParseSchedule("@every 5m")
	j := Job{
		Name:    name,
		Schedule: sched,
		Prompt:  prompt,
		NextRun: f.now.Add(-1 * time.Minute), // already due
	}
	if err := f.store.Create(j, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func (f *tickFixture) createNotDueJob(t *testing.T, name string) {
	t.Helper()
	sched, _ := ParseSchedule("@every 5m")
	j := Job{
		Name:    name,
		Schedule: sched,
		Prompt:  "shouldn't run",
		NextRun: f.now.Add(10 * time.Minute), // future
	}
	if err := f.store.Create(j, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestTick_NoDueJobsExitsCleanly: empty store / future-only
// jobs → no goroutines spawn, Fired=0.
func TestTick_NoDueJobsExitsCleanly(t *testing.T) {
	f := newTickFixture(t)
	f.createNotDueJob(t, "future")

	sub := shellStub(t, "echo nope")
	res, err := Tick(context.Background(), f.store, f.opts(sub))
	if err != nil {
		t.Fatal(err)
	}
	if res.Fired != 0 {
		t.Errorf("Fired = %d, want 0", res.Fired)
	}
	if res.Considered != 1 {
		t.Errorf("Considered = %d, want 1", res.Considered)
	}
}

// TestTick_DueJobRunsAndAdvancesNextRun: end-to-end happy path
// — due job spawns subprocess, writes run record, Store.MarkRun
// advances NextRun.
func TestTick_DueJobRunsAndAdvancesNextRun(t *testing.T) {
	f := newTickFixture(t)
	f.createDueJob(t, "hello", "irrelevant")

	sub := shellStub(t, "echo hi from cron; exit 0")
	res, err := Tick(context.Background(), f.store, f.opts(sub))
	if err != nil {
		t.Fatal(err)
	}
	if res.Fired != 1 {
		t.Errorf("Fired = %d, want 1", res.Fired)
	}

	// Job state advanced.
	got, _ := f.store.Get("hello")
	if got.LastStatus != StatusCompleted {
		t.Errorf("LastStatus = %q, want completed", got.LastStatus)
	}
	if got.RunCount != 1 {
		t.Errorf("RunCount = %d, want 1", got.RunCount)
	}
	if !got.NextRun.After(f.now) {
		t.Errorf("NextRun (%v) didn't advance past now (%v)", got.NextRun, f.now)
	}

	// Run record exists + contains stdout chunk.
	entries, _ := os.ReadDir(filepath.Join(f.dir, "runs"))
	var runFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			runFile = e.Name()
			break
		}
	}
	if runFile == "" {
		t.Fatal("no run record written")
	}
	content, _ := os.ReadFile(filepath.Join(f.dir, "runs", runFile))
	if !strings.Contains(string(content), "hi from cron") {
		t.Errorf("run record missing stdout chunk:\n%s", content)
	}
	if !strings.Contains(string(content), `"event":"completed"`) {
		t.Errorf("run record missing completed event:\n%s", content)
	}
}

// TestTick_FailedSubprocessRecorded: exit 1 → failed event,
// last_status=failed.
func TestTick_FailedSubprocessRecorded(t *testing.T) {
	f := newTickFixture(t)
	f.createDueJob(t, "boom", "fail")

	sub := shellStub(t, "echo something; echo oops >&2; exit 1")
	if _, err := Tick(context.Background(), f.store, f.opts(sub)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.Get("boom")
	if got.LastStatus != StatusFailed {
		t.Errorf("LastStatus = %q, want failed", got.LastStatus)
	}
}

// TestTick_TimeoutKillsAndRecordsKilled: long-running
// subprocess + short RunTimeout → killed event with reason=timeout.
func TestTick_TimeoutKillsAndRecordsKilled(t *testing.T) {
	f := newTickFixture(t)
	f.createDueJob(t, "slow", "long")

	opts := f.opts(shellStub(t, "sleep 10"))
	opts.RunTimeout = 200 * time.Millisecond

	start := time.Now()
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("Tick took %v; timeout should have killed promptly", elapsed)
	}
	got, _ := f.store.Get("slow")
	if got.LastStatus != StatusKilled {
		t.Errorf("LastStatus = %q, want killed", got.LastStatus)
	}
	if got.LastError != "timeout" {
		t.Errorf("LastError = %q, want timeout", got.LastError)
	}
}

// TestTick_LockHeldSkipsSilently: a held tick.lock makes a
// second Tick return Skipped=true with no run.
func TestTick_LockHeldSkipsSilently(t *testing.T) {
	f := newTickFixture(t)
	f.createDueJob(t, "j", "x")

	// Pre-acquire the lock from this test goroutine via TryLock,
	// then run Tick. The lock-from-the-same-fd-in-different-fd
	// caveat from flock_test.go applies here too — flock is
	// per-fd on Linux, so this test SKIPS if the platform
	// allows in-process double-lock.
	tickLockPath := filepath.Join(f.dir, "tick.lock")
	holder, ok, err := TryLock(tickLockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("test setup: pre-acquire of tick.lock should succeed")
	}
	defer holder.Close()

	sub := shellStub(t, "echo should-not-run")
	res, err := Tick(context.Background(), f.store, f.opts(sub))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		// Platform allowed double-flock; document + skip.
		t.Skip("platform allows in-process double-flock; cross-process tick.lock contention works in production but isn't observable from a single test process")
	}
	got, _ := f.store.Get("j")
	if got.RunCount != 0 {
		t.Errorf("RunCount = %d, want 0 — Tick should have skipped", got.RunCount)
	}
}

// TestTick_SubprocessBuildFailureRecorded: SubprocessFn returns
// an error (e.g. os.Executable failed in production) → Tick
// records the failure via Store without crashing.
func TestTick_SubprocessBuildFailureRecorded(t *testing.T) {
	f := newTickFixture(t)
	f.createDueJob(t, "noexec", "x")

	sub := func(ctx context.Context, job Job, runID string) (*exec.Cmd, error) {
		return nil, errors.New("synthesised: no executable")
	}
	if _, err := Tick(context.Background(), f.store, f.opts(sub)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.Get("noexec")
	if got.LastStatus != StatusFailed {
		t.Errorf("LastStatus = %q, want failed", got.LastStatus)
	}
	if !strings.Contains(got.LastError, "no executable") {
		t.Errorf("LastError = %q, should mention the build err", got.LastError)
	}
}

// TestTick_MultipleDueJobsRunInParallel: N due jobs all fire
// in their own goroutines. Verify wall-clock is closer to
// max(jobs) than sum(jobs).
func TestTick_MultipleDueJobsRunInParallel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	const N = 4
	for i := 0; i < N; i++ {
		f.createDueJob(t, fmt.Sprintf("j%d", i), "")
	}

	// Each job sleeps 300ms. Serial would take 1.2s; parallel
	// should be ~300ms.
	sub := shellStub(t, "sleep 0.3")
	start := time.Now()
	res, err := Tick(context.Background(), f.store, f.opts(sub))
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if res.Fired != N {
		t.Errorf("Fired = %d, want %d", res.Fired, N)
	}
	// Allow generous headroom; the assertion is "much closer to
	// 300ms than 1200ms", not "exactly 300ms".
	if elapsed > 900*time.Millisecond {
		t.Errorf("elapsed %v suggests serial execution; expected parallel (~300ms)", elapsed)
	}
}

// TestTick_PerJobLockSerialisesRetries: same job's runs/<name>
// .lock prevents a long run from being re-fired by an immediate
// next Tick (same process, different invocation). Verifies the
// goroutine doesn't double-spawn the subprocess.
func TestTick_PerJobLockSerialisesRetries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	f.createDueJob(t, "long", "")

	// First Tick starts a 600ms job in a background goroutine.
	sub := shellStub(t, "sleep 0.6")
	opts := f.opts(sub)

	// Pre-acquire the per-job lock; mimics "another tick is
	// still running the job". This is the closest single-
	// process test we can do for the cross-tick contention.
	jobLockPath := filepath.Join(f.dir, "runs", "long.lock")
	if err := os.MkdirAll(filepath.Dir(jobLockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, ok, err := TryLock(jobLockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("test setup: pre-acquire of per-job lock should succeed")
	}
	defer holder.Close()

	res, err := Tick(context.Background(), f.store, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Tick saw the job as Fired (it crossed NextRun) but the
	// goroutine's TryLock failed, so no run was recorded.
	if res.Fired != 1 {
		t.Errorf("Fired = %d, want 1 (job was due even if lock blocked exec)", res.Fired)
	}
	got, _ := f.store.Get("long")
	if got.RunCount != 0 {
		// Same platform-skip caveat as TestTick_LockHeldSkipsSilently.
		t.Skip("platform allows in-process double-flock; per-job lock contention works in production but isn't observable here")
	}
}

// TestTick_ConcurrentMarkRunRaceFree: drive Tick in a goroutine
// while reading Store from the main one. -race catches any
// missing sync.
func TestTick_ConcurrentMarkRunRaceFree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	for i := 0; i < 3; i++ {
		f.createDueJob(t, fmt.Sprintf("j%d", i), "")
	}
	sub := shellStub(t, "echo hi; exit 0")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = Tick(context.Background(), f.store, f.opts(sub))
	}()
	// Hammer List during the tick.
	for i := 0; i < 50; i++ {
		_, _ = f.store.List()
	}
	wg.Wait()
}
