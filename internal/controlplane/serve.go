package controlplane

// M6a serve lifecycle: the supervisor that keeps N frontends' Run
// loops fanning into ONE inbound channel and owns the shutdown
// choreography. Design + decisions: docs/prd/feature-remote-control-
// plane.md §5 M6, D-C1 (fan-in), D-C4 (queueing posture); ops contract
// (drain budgets, exit codes, unit files): docs/serve/README.md.
//
// NOTE: like executor.go, this file deliberately does not import the
// M1a type layer (Frontend / Inbound on drive/c_xcy7ff, in review).
// ServeSource is generic and MINIMAL on purpose: once M1a merges,
// controlplane.Frontend satisfies ServeSource[Inbound] structurally
// (Name + Run(ctx, chan<- Inbound) error) and Serve[Inbound] compiles
// against it unchanged — no adapter, no rework. Send stays out of the
// lifecycle's business: replies are the M2 router's concern.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ServeSource is the slice of the Frontend port the serve lifecycle
// needs: a stable name and a blocking receive loop that pushes
// normalised messages. Run must return promptly once ctx is cancelled
// (the larkws Start(ctx) shape, PRD §6 verified ③); a Run that wedges
// past the drain budget forfeits graceful exit for its messages.
type ServeSource[M any] interface {
	// Name is the channel identifier used in logs and as the ConvID
	// prefix. Non-empty and unique among the sources of one Serve call
	// — Serve rejects duplicates, because two frontends sharing a
	// ConvID prefix IS crosstalk.
	Name() string

	// Run blocks receiving for the channel and pushes each normalised
	// event into in. It owns transport + reconnects; returning a
	// non-nil error while the serve context is still live marks the
	// frontend down.
	Run(ctx context.Context, in chan<- M) error
}

// The M1a Frontend port satisfies ServeSource[Inbound] STRUCTURALLY —
// Name + Run(ctx, chan<- Inbound) error, no adapter. This assertion is
// the load-bearing seam of the whole design: it lets Serve drive real
// frontends while staying generic, so the lifecycle never learns about
// Send and the router never learns about drain budgets. If a future
// Frontend change breaks it, the compiler stops the build here rather
// than at the serve CLI.
var _ ServeSource[Inbound] = Frontend(nil)

// ServeConfig tunes Serve. Zero fields fill from the defaults below;
// the M6b CLI wiring maps plane config onto this struct.
type ServeConfig struct {
	// QueueDepth is the buffer of the shared inbound channel — the
	// plane's queue. Full buffers block frontend Run loops (natural
	// backpressure); nothing is silently dropped in steady state.
	QueueDepth int
	// DrainWait bounds the shutdown drain: time from the shutdown
	// trigger until in-flight work is declared dropped. Covers both
	// late frontend sends and queued-message processing.
	DrainWait time.Duration
	// HandlerGrace bounds each wait for handler goroutines: first to
	// finish their work after the queue is provably drained, then —
	// after their ctx is cancelled at the deadline — to observe the
	// cancellation. Worst-case shutdown ≈ DrainWait + 2×HandlerGrace;
	// supervisor unit timeouts must exceed that sum (docs/serve/).
	HandlerGrace time.Duration
}

const (
	defaultServeQueueDepth  = 64
	defaultServeDrainWait   = 10 * time.Second
	defaultServeHandlerWait = 5 * time.Second
)

func (c ServeConfig) withDefaults() ServeConfig {
	if c.QueueDepth <= 0 {
		c.QueueDepth = defaultServeQueueDepth
	}
	if c.DrainWait <= 0 {
		c.DrainWait = defaultServeDrainWait
	}
	if c.HandlerGrace <= 0 {
		c.HandlerGrace = defaultServeHandlerWait
	}
	return c
}

// Exit codes of the serve process, the contract supervisor units
// (launchd/systemd) and operators rely on. ExitCode maps errors onto
// them; nothing else in the plane chooses exit codes.
const (
	ExitClean           = 0 // shutdown drained fully; no frontend failed
	ExitDrainIncomplete = 1 // drain deadline exceeded: in-flight work dropped
	ExitFrontendsDown   = 2 // frontend-layer failure: none left / bad config
)

var (
	ErrDrainIncomplete = errors.New("controlplane: serve: drain deadline exceeded, in-flight work dropped")
	ErrFrontendsDown   = errors.New("controlplane: serve: all frontends stopped on errors")
	ErrNoFrontends     = errors.New("controlplane: serve: no usable frontends")
)

// Serve runs the plane's ingress: every source's Run pushes into one
// shared buffered channel; each received message is handed to handle
// in its own goroutine (the executor's global cap, not Serve, bounds
// real concurrency — D-C4). Serve blocks until the serve context is
// cancelled (a main that maps SIGINT/SIGTERM onto cancel), all
// frontends stop on their own errors, or every frontend has returned.
//
// Shutdown choreography:
//
//  1. stop ingress — cancel the frontend context; Runs still get their
//     in-flight sends consumed by the drain below;
//  2. drain (≤ DrainWait) — keep dispatching whatever is queued until
//     the channel closes (all Runs returned ⇒ provably empty) or the
//     deadline; handlers run on a ctx that is STILL live, so in-flight
//     work may complete during the drain;
//  3. settle (≤ HandlerGrace) — wait for handler goroutines; if the
//     grace expires their ctx is cancelled and they get one more
//     HandlerGrace to observe it.
//
// Handlers MUST respect their ctx: Serve cannot kill goroutines, so a
// handler ignoring cancellation makes the worst-case shutdown
// DrainWait + 2×HandlerGrace, after which Serve returns anyway and the
// wedged goroutine leaks (contract violation; the process exits and
// the supervisor restarts it).
//
// Return: nil on a fully drained shutdown; ErrDrainIncomplete when
// work was dropped; ErrFrontendsDown when every frontend died on its
// own error while serving; ErrNoFrontends (or wrapped) for config
// mistakes. Both failure kinds can be joined; ExitCode untangles them.
func Serve[M any](ctx context.Context, sources []ServeSource[M], handle func(context.Context, M), cfg ServeConfig) error {
	if len(sources) == 0 {
		return ErrNoFrontends
	}
	if handle == nil {
		return errors.New("controlplane: serve: nil handler")
	}
	seen := make(map[string]bool, len(sources))
	for _, s := range sources {
		if s == nil {
			return fmt.Errorf("%w: nil frontend", ErrNoFrontends)
		}
		n := s.Name()
		if n == "" {
			return fmt.Errorf("%w: frontend with empty name", ErrNoFrontends)
		}
		if seen[n] {
			// Two frontends sharing a ConvID prefix is crosstalk by
			// construction (D-C1) — refuse to serve, predictably.
			return fmt.Errorf("%w: duplicate frontend name %q", ErrNoFrontends, n)
		}
		seen[n] = true
	}
	cfg = cfg.withDefaults()

	// serveCtx stops ingress; handlerCtx deliberately does NOT inherit
	// its cancellation (context.WithoutCancel keeps values only) — the
	// whole point of the drain is letting in-flight handlers FINISH.
	serveCtx, stopIngress := context.WithCancel(ctx)
	defer stopIngress()
	handlerCtx, cancelHandlers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelHandlers()

	in := make(chan M, cfg.QueueDepth)

	var runWG sync.WaitGroup  // frontend Run goroutines = the only writers of in
	var workWG sync.WaitGroup // handler goroutines
	var alive atomic.Int32
	alive.Store(int32(len(sources)))
	var cause atomic.Pointer[error]

	for _, s := range sources {
		runWG.Add(1)
		go func(s ServeSource[M]) {
			defer runWG.Done()
			err := s.Run(serveCtx, in)
			n := alive.Add(-1)
			// A Run error during shutdown is the cancellation itself,
			// not a crash — only errors while serveCtx is live count.
			if err != nil && serveCtx.Err() == nil {
				fmt.Fprintf(os.Stderr, "controlplane: serve: frontend %s down: %v\n", s.Name(), err)
				if n == 0 {
					e := ErrFrontendsDown
					cause.CompareAndSwap(nil, &e)
					stopIngress() // nothing left to serve: drain and exit(2)
				}
			}
		}(s)
	}

	// The single writer-closer: once every Run has returned, no send
	// can happen and close(in) is safe. A wedged Run never returns, so
	// the channel never closes and the drain deadline bounds shutdown
	// (a closed channel with a live writer would panic the writer).
	go func() {
		runWG.Wait()
		close(in)
	}()

	dispatch := func(m M) {
		workWG.Go(func() {
			handle(handlerCtx, m)
		})
	}

	// Steady state: pump messages until shutdown starts — by ctx
	// cancellation or by the channel closing under us (all frontends
	// already returned, e.g. every one of them crashed).
	for serving := true; serving; {
		select {
		case m, ok := <-in:
			if !ok {
				serving = false
			} else {
				dispatch(m)
			}
		case <-serveCtx.Done():
			serving = false
		}
	}

	// Drain: dispatch everything already queued (and anything late
	// senders push during their own wind-down) until the queue is
	// provably empty — signalled by the channel closing — or budget.
	deadline := time.NewTimer(cfg.DrainWait)
	defer deadline.Stop()
drain:
	for {
		select {
		case m, ok := <-in:
			if !ok {
				break drain
			}
			dispatch(m)
		case <-deadline.C:
			cancelHandlers()
			waitWork(&workWG, cfg.HandlerGrace)
			return withCause(&cause, ErrDrainIncomplete)
		}
	}

	// Queue provably drained; in-flight handlers may still be working.
	// Bounded settle: let them finish, else cancel and give them the
	// observation grace.
	done := make(chan struct{})
	go func() { workWG.Wait(); close(done) }()
	grace := time.NewTimer(cfg.HandlerGrace)
	defer grace.Stop()
	select {
	case <-done:
		if c := cause.Load(); c != nil {
			return *c
		}
		return nil
	case <-grace.C:
		cancelHandlers()
		waitWork(&workWG, cfg.HandlerGrace)
		return withCause(&cause, ErrDrainIncomplete)
	}
}

// waitWork waits for handler goroutines at most bound. A handler that
// ignores its cancelled ctx past the bound wedges here no longer: the
// waiter goroutine leaks with it, Serve returns, the process exits.
func waitWork(wg *sync.WaitGroup, bound time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(bound):
	}
}

// withCause joins a shutdown-phase failure with the earlier cause, if
// any (e.g. all frontends crashed AND the drain dropped work — report
// both; ExitCode gives the drop precedence as the loudest fact).
func withCause(cause *atomic.Pointer[error], e error) error {
	if c := cause.Load(); c != nil {
		return errors.Join(*c, e)
	}
	return e
}

// ExitCode maps a Serve error onto the process exit code. Deterministic
// and total: nil → 0; ErrDrainIncomplete (even joined) → 1, work was
// dropped; ErrFrontendsDown / ErrNoFrontends → 2, frontend layer;
// anything else → 1. Supervisor units key off these (docs/serve/).
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitClean
	case errors.Is(err, ErrDrainIncomplete):
		return ExitDrainIncomplete
	case errors.Is(err, ErrFrontendsDown), errors.Is(err, ErrNoFrontends):
		return ExitFrontendsDown
	default:
		return ExitDrainIncomplete
	}
}
