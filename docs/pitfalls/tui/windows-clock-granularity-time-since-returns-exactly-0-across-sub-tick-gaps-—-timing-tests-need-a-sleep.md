### Windows clock granularity: `time.Since` returns exactly 0 across sub-tick gaps — timing tests need a sleep
- **Saw**: new TUI timing tests failed deterministically standalone with `llmTime = 0s, want > 0` — the span between `applyAgentEvent(TurnStart)` and `applyAgentEvent(MessageEnd)` is a few hundred nanoseconds and `time.Since` returned exactly 0. Under load they passed intermittently (tick crossing is a coin flip).
- **Why**: `time.Now()`/`time.Since` on Windows has tick granularity coarser than a µs-scale gap; two readings in one tick are identical, so `Sub` is exactly 0. The code under test is fine.
- **Fix**: each test asserting a positive duration sleeps ≥2ms inside the measured span before the closing event (empirical: 2ms reliable, sub-ms not).
- **Lesson**: on Windows, never assert `duration > 0` on a span created and measured microseconds apart in one goroutine — sleep across a tick or assert structure (zeroed state, accumulation), not magnitude.
- **Refs**: `internal/tui/summary_test.go` (the `time.Sleep(2 * time.Millisecond)` comments); related entry "t.Cleanup(chdir) after t.TempDir() breaks Windows TempDir removal"
