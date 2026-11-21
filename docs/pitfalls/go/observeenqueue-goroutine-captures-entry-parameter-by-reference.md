### ObserveEnqueue goroutine captures entry parameter by reference
- **Saw**: `go test -race ./internal/memory` failed in `TestObserveEnqueue_RespectsSessionCap` at `hook.go:713` (read/write on the same address from two filter goroutines) even after `observeAcceptCt` was converted to `atomic.Int32`
- **Why**: the async goroutine closed over the outer `entry` parameter without copying it. The closure returns immediately after a non-blocking `ResultChan` send; the test (and production TUI) enqueues the next entry while the prior goroutine may still be running. Go reuses the parameter stack slot, so the second call overwrites `entry` while the first goroutine still reads it
- **Fix**: launch with `go func(ctx context.Context, entry Entry) { … }(ctx, entry)` so each goroutine owns a value copy
- **Lesson**: any `go func()` inside a callback that returns before the goroutine finishes must copy loop/parameter variables into the closure literal — atomics on unrelated counters do not fix captured-variable races
- **Refs**: `internal/memory/hook.go:ObserveEnqueue`, `internal/memory/auto_distill_test.go:TestObserveEnqueue_RespectsSessionCap`
