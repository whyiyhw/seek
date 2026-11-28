### ObserveEnqueue goroutine captures entry parameter by reference
- **Saw**: `go test -race ./internal/memory` failed in `TestObserveEnqueue_RespectsSessionCap` at `hook.go:713` (read/write on the same address, two filter goroutines), even after `observeAcceptCt` became `atomic.Int32`
- **Why**: the async goroutine closed over the `entry` parameter without copying it. The closure returns right after a non-blocking `ResultChan` send; the test (and production TUI) enqueues the next entry while the prior goroutine may still run. Go reuses the parameter stack slot, so the second call overwrites `entry` while the first still reads it
- **Fix**: launch with `go func(ctx context.Context, entry Entry) { … }(ctx, entry)` so each goroutine owns a value copy
- **Lesson**: any `go func()` inside a callback that returns before the goroutine finishes must copy loop/parameter variables into the closure literal — atomics on unrelated counters don't fix captured-variable races
- **Refs**: `internal/memory/hook.go:ObserveEnqueue`, `internal/memory/auto_distill_test.go:TestObserveEnqueue_RespectsSessionCap`
