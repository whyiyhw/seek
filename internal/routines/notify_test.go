package routines

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestShouldNotify pins the (policy, status) gating matrix.
// Pure function with no dependencies; the assertion is just
// "the table matches PRD §3.2 Notify semantics".
func TestShouldNotify(t *testing.T) {
	cases := []struct {
		policy string
		status string
		want   bool
	}{
		// always → notify on all terminal statuses
		{NotifyAlways, StatusCompleted, true},
		{NotifyAlways, StatusFailed, true},
		{NotifyAlways, StatusKilled, true},
		// on_failure → skip completed; notify everything else
		{NotifyOnFailure, StatusCompleted, false},
		{NotifyOnFailure, StatusFailed, true},
		{NotifyOnFailure, StatusKilled, true},
		// never → never
		{NotifyNever, StatusCompleted, false},
		{NotifyNever, StatusFailed, false},
		{NotifyNever, StatusKilled, false},
		// empty policy defaults to always (PRD §3.2 default;
		// Store.Create fills it on disk-write, but in-memory
		// Jobs from other paths might leave it empty)
		{"", StatusCompleted, true},
		// unknown policy: defensive — treat as never so a
		// schema bump introducing a new policy without code
		// support doesn't accidentally spam notifications
		{"weird", StatusCompleted, false},
	}
	for _, c := range cases {
		got := shouldNotify(Job{Notify: c.policy}, c.status)
		if got != c.want {
			t.Errorf("shouldNotify(policy=%q, status=%q) = %v, want %v",
				c.policy, c.status, got, c.want)
		}
	}
}

// TestTick_NotifierCalledAfterCompletion: end-to-end — a due
// job with notify=always fires the injected notifier exactly
// once with the expected title shape.
func TestTick_NotifierCalledAfterCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	f.createDueJob(t, "morning-report", "summarise PRs")

	var (
		mu          sync.Mutex
		gotTitle    string
		gotBody     string
		callCount   int
	)
	stub := Notifier(func(title, body string) error {
		mu.Lock()
		defer mu.Unlock()
		gotTitle, gotBody = title, body
		callCount++
		return nil
	})

	opts := f.opts(shellStub(t, "echo done"))
	opts.Notifier = stub
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Errorf("notifier called %d times, want 1", callCount)
	}
	if gotTitle == "" || !contains(gotTitle, "morning-report") {
		t.Errorf("title should mention job name; got %q", gotTitle)
	}
	if !contains(gotTitle, string(StatusCompleted)) {
		t.Errorf("title should mention status; got %q", gotTitle)
	}
	if gotBody == "" {
		t.Errorf("body empty; expected summary head or fallback message")
	}
}

// TestTick_NotifierRespectsNotifyNever: notify=never jobs
// don't call the notifier even on terminal events.
func TestTick_NotifierRespectsNotifyNever(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	// Create with Notify=never explicitly. createDueJob fills
	// defaults (always); we need to override.
	sched, _ := ParseSchedule("@every 5m")
	j := Job{
		Name:    "quiet",
		Schedule: sched,
		Prompt:  "p",
		NextRun: f.now.Add(-1 * time.Minute),
		Notify:  NotifyNever,
	}
	if err := f.store.Create(j, CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	stub := Notifier(func(title, body string) error {
		calls++
		return nil
	})
	opts := f.opts(shellStub(t, "echo done"))
	opts.Notifier = stub
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("notifier called %d times despite notify=never", calls)
	}
}

// TestTick_NotifierRespectsOnFailure: notify=on_failure skips
// successful completions but fires on failure.
func TestTick_NotifierRespectsOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	sched, _ := ParseSchedule("@every 5m")

	// One job completes (no notification expected).
	if err := f.store.Create(Job{
		Name: "happy", Schedule: sched, Prompt: "p",
		NextRun: f.now.Add(-1 * time.Minute),
		Notify:  NotifyOnFailure,
	}, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// One job fails (notification expected).
	if err := f.store.Create(Job{
		Name: "sad", Schedule: sched, Prompt: "p",
		NextRun: f.now.Add(-1 * time.Minute),
		Notify:  NotifyOnFailure,
	}, CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// Subprocess that succeeds for "happy", fails for "sad".
	// Distinguish via the cmd.Dir or job.Prompt — simpler to
	// branch on job name accessible via stack capture. Easiest:
	// build a custom SubprocessFn that introspects job.Name.
	custom := SubprocessFn(func(ctx context.Context, job Job, runID string) (*exec.Cmd, error) {
		if job.Name == "happy" {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0"), nil
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1"), nil
	})

	var (
		mu      sync.Mutex
		titles  []string
	)
	stub := Notifier(func(title, body string) error {
		mu.Lock()
		defer mu.Unlock()
		titles = append(titles, title)
		return nil
	})

	opts := TickOptions{
		Now:        func() time.Time { return f.now },
		Subprocess: custom,
		RunTimeout: 5 * time.Second,
		CronDir:    f.dir,
		Notifier:   stub,
	}
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(titles) != 1 {
		t.Fatalf("notifier called %d times, want 1 (only sad should notify); titles=%v", len(titles), titles)
	}
	if !contains(titles[0], "sad") {
		t.Errorf("notification title %q should mention the failing job 'sad'", titles[0])
	}
}

// TestTick_NotifierFailureDoesNotAbortRun: if the notifier
// returns an error (osascript / notify-send missing), the cron
// run still completes successfully and Store.MarkRun fires
// normally. Per PRD §3.8 "user loses popup, not data".
func TestTick_NotifierFailureDoesNotAbortRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh tick stub not available on Windows")
	}
	f := newTickFixture(t)
	f.createDueJob(t, "j", "p")

	stub := Notifier(func(title, body string) error {
		return errors.New("simulated: notify binary missing")
	})
	opts := f.opts(shellStub(t, "echo ran"))
	opts.Notifier = stub
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.Get("j")
	if got.LastStatus != StatusCompleted {
		t.Errorf("LastStatus = %q; notify failure should not affect run outcome", got.LastStatus)
	}
	if got.RunCount != 1 {
		t.Errorf("RunCount = %d; notify failure shouldn't suppress MarkRun", got.RunCount)
	}
}

// TestTick_DefaultNotifierIsSetPerPlatform: DefaultNotifier is
// never nil — every platform has either a real adapter or the
// noopNotifier fallback. Catches a future build-tag refactor
// that accidentally leaves a platform without a default.
func TestTick_DefaultNotifierIsSetPerPlatform(t *testing.T) {
	if DefaultNotifier == nil {
		t.Errorf("DefaultNotifier is nil on %s/%s; per-platform build-tag wiring missing",
			runtime.GOOS, runtime.GOARCH)
	}
}

// contains is a small substring check shared across tests.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
