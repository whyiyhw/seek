### Tick's "len(due) == 0 → early return" silently skipped triggers + GC for the entire tick
- **Saw**: G4 housekeeping integration test failed with `GCRemoved = 0, want 2` — GC ran cleanly when invoked directly, never inside Tick; triggers weren't processed on ticks with no due jobs.
- **Why**: `if len(due) == 0 { return res, nil }` sat after step 3 (load + filter jobs) and before step 5 (trigger drain) — correct when Tick only fired cron jobs, wrong once triggers/ (M11.3) and GC (v0.6.1) became steps 5-6 (triggers are an independent input source; GC is activity-independent housekeeping).
- **Fix**: removed — `wg.Wait()` on an empty WaitGroup is a no-op, so idle ticks fall through to steps 5-6; integration test: zero due jobs + seeded fake history → `GCRemoved = 2`, `Fired = 0`.
- **Lesson**: an early exit is a contract with the future — anything added below it silently doesn't run; prefer an empty `wg.Wait` / empty range loop as the fast path.
- **Refs**: `internal/routines/tick.go:Tick` (step 4 fall-through), `internal/routines/tick_test.go:TestTick_GCSweepsOldRunsAndMalformed`
