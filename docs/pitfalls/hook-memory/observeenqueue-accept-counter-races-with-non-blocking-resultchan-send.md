### ObserveEnqueue accept counter races with non-blocking ResultChan send
- **Saw**: `go test -race ./internal/memory` failed in `TestObserveEnqueue_RespectsSessionCap` on `h.observeAcceptCt++` — two filter goroutines could increment concurrently
- **Why**: `ObserveEnqueue` is non-blocking; the TUI/test receives from `ResultChan` as soon as the goroutine sends, which can overlap with the goroutine still bumping stats. `observeAcceptCt++` ran after the channel send, so a second enqueue could start before the first increment finished
- **Fix**: store `observeCount` / `observeAcceptCt` as `atomic.Int32` (Add/Load)
- **Lesson**: non-blocking handoff to a consumer doesn't mean the producer goroutine is done — any post-send bookkeeping in that goroutine needs the same synchronization as the shared counter itself
- **Refs**: `internal/memory/hook.go:ObserveEnqueue`, `internal/memory/auto_distill_test.go:TestObserveEnqueue_RespectsSessionCap`
