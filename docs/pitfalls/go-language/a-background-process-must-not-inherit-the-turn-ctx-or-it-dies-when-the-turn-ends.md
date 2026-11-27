### A background process must NOT inherit the turn ctx, or it dies when the turn ends
- **Saw**: `bash run_in_background` (v6 柱 K) mirroring the foreground path — `exec.CommandContext(turnCtx, …)` — would kill every background job the instant its launching turn returned or the user pressed Esc.
- **Why**: seek's tool `ctx` is the **turn** ctx, cancelled when the turn completes or is interrupted; foreground bash wants that, but a background job must outlive its turn.
- **Fix**: `runBackground` uses plain `exec.Command` (no ctx) and registers a kill closure with session-scoped `bgjob.Manager`; cleanup via `monitor(action=kill)` or `Manager.Shutdown()`, never turn cancellation; the observer (`monitor wait`) is turn-bound so Esc stops watching without killing the job.
- **Lesson**: separate turn-scoped lifetimes (foreground exec, wait/observe) from session-scoped ones (the process); tested with an already-cancelled ctx in `TestBash_Background_IgnoresTurnCtx`.
- **Refs**: `internal/tools/bash/bash.go:runBackground`, `internal/bgjob/bgjob.go` (Shutdown), `internal/tools/monitor/monitor.go` (wait propagates ctx.Err), `docs/prd/archive/feature-bash-monitor.md` §4 D5
