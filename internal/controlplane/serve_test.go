package controlplane

// M6a serve lifecycle tests. The fakes are ServeSource implementations
// pushing prefixed-Conv messages — Conv discipline ("<name>:<native>")
// is the SOURCE's job (D-C1), so the crosstalk assertions check that
// the fan-in never garbles it: every recorded message keeps the prefix
// of the frontend that injected it, with two frontends sharing the
// SAME native chat id on purpose.
//
// Failure-path bar (AGENTS.md): every shutdown shape below is a
// mid-loop interruption — cancel with queued work, deadline expiry,
// wedged writer, crashed frontends — asserted with -race in CI.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testMsg mirrors the Inbound shape the plane standardises on (M1a):
// Conv is "<frontend>:<native>", built by the source itself. Src tags
// the injecting fake so crosstalk assertions don't depend on parsing
// the very prefix they are checking.
type testMsg struct {
	Conv string
	Text string
	Src  string
}

// fakeSource is a ServeSource[testMsg]: pushes its staged messages
// (ctx-aware sends), closes pushed once the last one landed in the
// plane, then parks until ctx ends. Wedged mode deliberately ignores
// ctx (the Run contract violation) to exercise the bounded shutdown.
type fakeSource struct {
	name string
	msgs []testMsg

	err            error // Run's terminal return; non-nil = "crashed"
	crashAfterPush bool  // return err right after pushing, without ctx ending
	wedged         bool  // block on never, ignoring ctx

	pushed chan struct{}
	never  chan struct{}
}

func newFakeSource(name string, msgs []testMsg) *fakeSource {
	return &fakeSource{
		name:   name,
		msgs:   msgs,
		pushed: make(chan struct{}),
		never:  make(chan struct{}),
	}
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Run(ctx context.Context, in chan<- testMsg) error {
	if f.wedged {
		<-f.never
		return nil
	}
	for _, m := range f.msgs {
		select {
		case in <- m:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	close(f.pushed)
	if f.crashAfterPush {
		return f.err
	}
	<-ctx.Done()
	return f.err
}

// convMsgs stages n messages all on ONE native conversation id, with
// per-message texts — same native id across frontends is the collision
// the ConvID prefix exists to prevent.
func convMsgs(src, native string, n int) []testMsg {
	msgs := make([]testMsg, n)
	for i := range msgs {
		msgs[i] = testMsg{
			Conv: src + ":" + native,
			Text: fmt.Sprintf("%s%03d", src, i),
			Src:  src,
		}
	}
	return msgs
}

// recorder is the handler stand-in for the M2 router.
type recorder struct {
	mu   sync.Mutex
	msgs []testMsg
}

func (r *recorder) handle(_ context.Context, m testMsg) {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
}

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *recorder) snapshot() []testMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]testMsg(nil), r.msgs...)
}

// runServe starts Serve on its own goroutine and returns its result
// channel plus the cancel for the serve context.
func runServe(t *testing.T, cfg ServeConfig, sources ...*fakeSource) (results <-chan error, rec *recorder, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srcs := make([]ServeSource[testMsg], len(sources))
	for i, s := range sources {
		srcs[i] = s
	}
	rec = &recorder{}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srcs, rec.handle, cfg) }()
	return done, rec, cancel
}

// TestServe_FanInNoCrossTalk is the card's first acceptance: two fake
// frontends inject concurrently into one fan-in, BOTH using the same
// native conversation id, and no message may end up attributed to the
// other frontend. QueueDepth < total forces backpressure interleaving.
func TestServe_FanInNoCrossTalk(t *testing.T) {
	const per = 100
	fa := newFakeSource("fa", convMsgs("fa", "chat-1", per))
	fb := newFakeSource("fb", convMsgs("fb", "chat-1", per))

	done, rec, cancel := runServe(t, ServeConfig{QueueDepth: 8}, fa, fb)
	<-fa.pushed
	<-fb.pushed
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown expected after full push, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if got := rec.len(); got != 2*per {
		t.Fatalf("message loss/dup in fan-in: got %d of %d", got, 2*per)
	}

	// The isolation assertion: same native id, two frontends — every
	// message must still carry ITS OWN frontend's full ConvID.
	perSrc := map[string]int{}
	for _, m := range rec.snapshot() {
		perSrc[m.Src]++
		wantConv := m.Src + ":chat-1"
		if m.Conv != wantConv {
			t.Errorf("crosstalk: msg from %q has Conv %q, want %q", m.Src, m.Conv, wantConv)
		}
	}
	if perSrc["fa"] != per || perSrc["fb"] != per {
		t.Errorf("per-frontend counts drifted: %+v", perSrc)
	}
}

// TestServe_DrainLetsInFlightHandlersFinish pins the drain semantics:
// after the shutdown trigger, handlers run on a STILL-LIVE ctx and get
// the full budget to finish — cancellation only at deadline expiry.
func TestServe_DrainLetsInFlightHandlersFinish(t *testing.T) {
	const n = 6
	fa := newFakeSource("fa", convMsgs("fa", "c", n))

	var finished atomic.Int32
	var cancelled atomic.Int32
	handler := func(ctx context.Context, m testMsg) {
		select {
		case <-time.After(80 * time.Millisecond): // finite work spanning the shutdown
			finished.Add(1)
		case <-ctx.Done():
			cancelled.Add(1)
		}
	}

	ctx, cancelRoot := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, []ServeSource[testMsg]{fa}, handler, ServeConfig{DrainWait: 5 * time.Second, HandlerGrace: 2 * time.Second})
	}()
	<-fa.pushed
	cancelRoot()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("work was finite and budget ample: want nil, got %v (exit %d)", err, ExitCode(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	if got := finished.Load(); got != n {
		t.Fatalf("handlers should finish their work during drain: %d/%d finished, %d cancelled", got, n, cancelled.Load())
	}
	if cancelled.Load() != 0 {
		t.Fatalf("no cancellation may reach handlers before the deadline, %d saw ctx.Done", cancelled.Load())
	}
}

// TestServe_DrainDeadlineDropsAndCancels is the bounded half of the
// contract: work that cannot finish within DrainWait is dropped, its
// handlers get cancelled, and Serve reports ErrDrainIncomplete (exit
// 1) within the predicted worst-case bound.
func TestServe_DrainDeadlineDropsAndCancels(t *testing.T) {
	const n = 4
	fa := newFakeSource("fa", convMsgs("fa", "c", n))

	var aborted atomic.Int32
	var finished atomic.Int32
	handler := func(ctx context.Context, m testMsg) {
		select {
		case <-time.After(5 * time.Second): // cannot finish in budget
			finished.Add(1)
		case <-ctx.Done():
			aborted.Add(1)
		}
	}

	ctx, cancelRoot := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- Serve(ctx, []ServeSource[testMsg]{fa}, handler, ServeConfig{DrainWait: 250 * time.Millisecond, HandlerGrace: 250 * time.Millisecond})
	}()
	<-fa.pushed
	cancelRoot()

	select {
	case err := <-done:
		if !errors.Is(err, ErrDrainIncomplete) {
			t.Fatalf("deadline exceeded: want ErrDrainIncomplete, got %v", err)
		}
		if code := ExitCode(err); code != ExitDrainIncomplete {
			t.Fatalf("exit code: got %d, want %d", code, ExitDrainIncomplete)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve overran its drain bound")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("shutdown took %v, well past DrainWait+2×HandlerGrace", d)
	}
	if finished.Load() != 0 {
		t.Fatalf("dropped work must not also report finished: %d", finished.Load())
	}
	if aborted.Load() != n {
		t.Fatalf("all %d handlers must observe cancellation, %d did", n, aborted.Load())
	}
}

// TestServe_WedgedWriterBoundedReturn: a Run that ignores ctx (the
// documented contract violation) cannot wedge shutdown — the channel
// never closes, so the queue is never PROVABLY drained and Serve exits
// ErrDrainIncomplete at the deadline instead of hanging.
func TestServe_WedgedWriterBoundedReturn(t *testing.T) {
	fa := newFakeSource("fa", convMsgs("fa", "c", 1))
	fb := newFakeSource("fb", nil)
	fb.wedged = true
	defer close(fb.never) // release the wedged goroutine after the test

	done, rec, cancel := runServe(t, ServeConfig{DrainWait: 300 * time.Millisecond, HandlerGrace: 300 * time.Millisecond}, fa, fb)
	<-fa.pushed
	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrDrainIncomplete) {
			t.Fatalf("wedged writer must bound out as incomplete, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve hung behind a wedged writer")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown took %v, want ≈ DrainWait", d)
	}
	if rec.len() != 1 {
		t.Fatalf("already-dispatched work should still be processed, got %d msgs", rec.len())
	}
}

// TestServe_FrontendDownOthersKeepServing: one frontend crashing on its
// own error does NOT take the plane down — the survivors keep serving
// and the shutdown is still clean (exit 0).
func TestServe_FrontendDownOthersKeepServing(t *testing.T) {
	fa := newFakeSource("fa", nil)
	fa.err = errors.New("boom: transport gone")
	fa.crashAfterPush = true
	fb := newFakeSource("fb", convMsgs("fb", "chat-1", 3))

	done, rec, cancel := runServe(t, ServeConfig{}, fa, fb)
	<-fa.pushed
	<-fb.pushed
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fa crashed but fb served on: want nil exit, got %v (exit %d)", err, ExitCode(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	if got := rec.len(); got != 3 {
		t.Fatalf("fb's messages must all be served, got %d of 3", got)
	}
	for _, m := range rec.snapshot() {
		if !strings.HasPrefix(m.Conv, "fb:") {
			t.Errorf("message from crashed fa leaked into results: %+v", m)
		}
	}
}

// TestServe_AllFrontendsDownStillDrains: when the LAST frontend dies
// on its own error the plane stops serving — but already-queued work
// still drains first; the exit distinguishes the failure (2, not 1).
func TestServe_AllFrontendsDownStillDrains(t *testing.T) {
	fa := newFakeSource("fa", convMsgs("fa", "chat-1", 3))
	fa.err = errors.New("boom a")
	fa.crashAfterPush = true
	fb := newFakeSource("fb", convMsgs("fb", "chat-1", 3))
	fb.err = errors.New("boom b")
	fb.crashAfterPush = true

	done, rec, cancel := runServe(t, ServeConfig{HandlerGrace: 2 * time.Second}, fa, fb)
	<-fa.pushed
	<-fb.pushed
	cancel() // no signal needed for the stop; release the ctx regardless

	select {
	case err := <-done:
		if !errors.Is(err, ErrFrontendsDown) {
			t.Fatalf("all frontends crashed: want ErrFrontendsDown, got %v", err)
		}
		if errors.Is(err, ErrDrainIncomplete) {
			t.Fatalf("queue drained within budget, must not double-report: %v", err)
		}
		if code := ExitCode(err); code != ExitFrontendsDown {
			t.Fatalf("exit code: got %d, want %d", code, ExitFrontendsDown)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after all frontends died")
	}
	if got := rec.len(); got != 6 {
		t.Fatalf("queued work must drain before exit: got %d of 6", got)
	}
}

// TestServe_RejectsBadConfigs: misconfiguration fails fast and
// predictably (exit 2) — before any goroutine starts.
func TestServe_RejectsBadConfigs(t *testing.T) {
	good := newFakeSource("fa", nil)
	cases := []struct {
		name    string
		sources []ServeSource[testMsg]
		handle  func(context.Context, testMsg)
		wantMsg string
	}{
		{"no sources", nil, func(context.Context, testMsg) {}, "no usable frontends"},
		{"nil source", []ServeSource[testMsg]{good, nil}, func(context.Context, testMsg) {}, "nil frontend"},
		{
			"duplicate names",
			[]ServeSource[testMsg]{newFakeSource("fa", nil), newFakeSource("fa", nil)},
			func(context.Context, testMsg) {},
			"duplicate frontend name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := Serve(ctx, tc.sources, tc.handle, ServeConfig{})
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("want error containing %q, got %v", tc.wantMsg, err)
			}
			if !errors.Is(err, ErrNoFrontends) {
				t.Fatalf("config failures must wrap ErrNoFrontends, got %v", err)
			}
			if code := ExitCode(err); code != ExitFrontendsDown {
				t.Fatalf("exit code: got %d, want %d", code, ExitFrontendsDown)
			}
		})
	}

	t.Run("nil handler", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := Serve(ctx, []ServeSource[testMsg]{good}, nil, ServeConfig{})
		if err == nil || !strings.Contains(err.Error(), "nil handler") {
			t.Fatalf("want nil-handler error, got %v", err)
		}
		if code := ExitCode(err); code != ExitDrainIncomplete {
			t.Fatalf("generic failure maps to exit 1, got %d", code)
		}
	})
}

// TestServe_AlreadyCancelledCtx: a serve context that is dead on
// arrival must shut down cleanly and promptly (writers see the cancel
// as shutdown, not as crashes — no exit-2 poisoning).
func TestServe_AlreadyCancelledCtx(t *testing.T) {
	fa := newFakeSource("fa", convMsgs("fa", "c", 2))
	fb := newFakeSource("fb", convMsgs("fb", "c", 2))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	rec := &recorder{}
	start := time.Now()
	go func() {
		done <- Serve(ctx, []ServeSource[testMsg]{fa, fb}, rec.handle, ServeConfig{DrainWait: time.Second, HandlerGrace: time.Second})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled-before-start must exit clean, got %v (exit %d)", err, ExitCode(err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve hung on a dead context")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("dead-context shutdown took %v", d)
	}
	// How many of the staged sends lose the race with cancellation is
	// inherently timing-dependent; only the clean exit is contracted.
}

// TestExitCode_Mapping pins the whole error→exit table, joins included.
func TestExitCode_Mapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, ExitClean},
		{ErrDrainIncomplete, ExitDrainIncomplete},
		{ErrFrontendsDown, ExitFrontendsDown},
		{ErrNoFrontends, ExitFrontendsDown},
		{fmt.Errorf("%w: duplicate frontend name %q", ErrNoFrontends, "x"), ExitFrontendsDown},
		{errors.Join(ErrFrontendsDown, ErrDrainIncomplete), ExitDrainIncomplete}, // dropped work is the loudest fact
		{errors.New("anything else"), ExitDrainIncomplete},                       // generic nonzero
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
