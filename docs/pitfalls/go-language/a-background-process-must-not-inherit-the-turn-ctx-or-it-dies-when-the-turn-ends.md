### A background process must NOT inherit the turn ctx, or it dies when the turn ends
- **Saw**: `bash run_in_background` (v6 柱 K) mirroring the foreground path — `exec.CommandContext(turnCtx, …)` — would kill every background job the moment its launching turn returned or Esc fired.
- **Why**: seek's tool `ctx` is the **turn** ctx, cancelled when the turn completes or is interrupted; foreground bash wants that, a background job must outlive its turn.
- **Fix**: `runBackground` uses plain `exec.Command` (no ctx) and registers a kill closure with session-scoped `bgjob.Manager`; cleanup via `monitor(action=kill)` or `Manager.Shutdown()`, never turn cancellation; observer (`monitor wait`) is turn-bound — Esc stops watching, job survives.
- **Lesson**: separate turn-scoped lifetimes (foreground exec, wait/observe) from session-scoped (the process); tested with a pre-cancelled ctx in `TestBash_Background_IgnoresTurnCtx`.
- **Refs**: `internal/tools/bash/bash.go:runBackground`, `internal/bgjob/bgjob.go`, `internal/tools/monitor/monitor.go` (wait propagates ctx.Err), `docs/prd/archive/feature-bash-monitor.md` §4 D5
