package routines

// SpeakerDispatcher sibling tests (feature-deepseek-app-api §4.3 V1a):
// terminal events of cron jobs AND triggers reach the optional speech
// sink with the same (event, title, body) payload webhooks get, firing
// INDEPENDENT of Job.Notify (the §D4 orthogonality, extended to the
// third sibling). nil sibling changes nothing — the rest of the suite
// runs that path with every Tick.

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// speakerRecorder is a thread-safe capture of sibling invocations
// (per-job goroutines may fire in parallel).
type speakerRecorder struct {
	mu    sync.Mutex
	calls []struct {
		event, title, body string
	}
}

func (r *speakerRecorder) dispatch(_ context.Context, event, title, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct{ event, title, body string }{event, title, body})
}

func (r *speakerRecorder) snapshot() []struct{ event, title, body string } {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]struct{ event, title, body string }{}, r.calls...)
}

func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh stub not available on Windows")
	}
}

// A completed cron run fires the sibling once with the terminal event,
// the job-attributed title and the run's summary line as the body.
func TestSpeakerSibling_FiresOnCronTerminalEvent(t *testing.T) {
	skipIfWindows(t)
	f := newTickFixture(t)
	f.createDueJob(t, "hello", "irrelevant")

	rec := &speakerRecorder{}
	opts := f.opts(shellStub(t, "echo hi from cron; exit 0"))
	opts.Speaker = rec.dispatch
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("speaker calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.event != "cron.completed" {
		t.Errorf("event = %q, want cron.completed", c.event)
	}
	if !strings.Contains(c.title, "hello") || !strings.Contains(c.title, "completed") {
		t.Errorf("title = %q, want job name + status", c.title)
	}
	if !strings.Contains(c.body, "hi from cron") {
		t.Errorf("body = %q, want the run summary as spoken text", c.body)
	}
}

// The sibling fires INDEPENDENT of Job.Notify (same §D4 posture as
// webhooks): a notify:"never" job still speaks — the popup and the
// audio are orthogonal channels.
func TestSpeakerSibling_IndependentOfJobNotify(t *testing.T) {
	skipIfWindows(t)
	f := newTickFixture(t)
	// Same shape as createDueJob, but with Notify: never — the store
	// has no Update; create the job pre-configured.
	sched, _ := ParseSchedule("@every 5m")
	if err := f.store.Create(Job{
		Name:     "quiet",
		Schedule: sched,
		Prompt:   "irrelevant",
		Notify:   NotifyNever,
		NextRun:  f.now.Add(-1 * time.Minute),
	}, CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	rec := &speakerRecorder{}
	opts := f.opts(shellStub(t, "echo done; exit 0"))
	opts.Speaker = rec.dispatch
	opts.Notifier = func(title, body string) error {
		t.Error("Notifier must not fire for notify:never — orthogonality sanity")
		return nil
	}
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}
	if calls := rec.snapshot(); len(calls) != 1 {
		t.Fatalf("speaker calls = %d, want 1 (independent of Job.Notify)", len(calls))
	}
}

// A failing cron run speaks the failure event.
func TestSpeakerSibling_FiresOnFailure(t *testing.T) {
	skipIfWindows(t)
	f := newTickFixture(t)
	f.createDueJob(t, "boom", "fail")

	rec := &speakerRecorder{}
	opts := f.opts(shellStub(t, "echo oops >&2; exit 1"))
	opts.Speaker = rec.dispatch
	if _, err := Tick(context.Background(), f.store, opts); err != nil {
		t.Fatal(err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0].event != "cron.failed" {
		t.Fatalf("speaker calls = %+v, want one cron.failed", calls)
	}
}

// Triggers speak too: a dispatched trigger file fires the sibling with
// the "trigger.<status>" event.
func TestSpeakerSibling_FiresOnTriggerTerminalEvent(t *testing.T) {
	skipIfWindows(t)
	root := t.TempDir()
	triggersDir := root + "/triggers"
	runsDir := root + "/runs"

	now := time.Now().UTC()
	writeTrigger(t, triggersDir, now, Trigger{TriggerID: "ci-build-42", Prompt: "summarise CI #42"})

	rec := &speakerRecorder{}
	sub := SubprocessFn(func(ctx context.Context, j Job, runID string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "echo handled CI #42"), nil
	})
	if _, err := processTriggers(context.Background(), triggersDir, runsDir, now, sub, noopNotifier, nil, rec.dispatch); err != nil {
		t.Fatal(err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("speaker calls = %d, want 1", len(calls))
	}
	if calls[0].event != "trigger.completed" {
		t.Errorf("event = %q, want trigger.completed", calls[0].event)
	}
	if !strings.Contains(calls[0].title, "ci-build-42") {
		t.Errorf("title = %q, want the trigger id", calls[0].title)
	}
}
