### Approval callback that blocks on a channel needs ctx-aware select on BOTH ends
- **Saw**: implementing per-call approval (M4.5.5), the agent goroutine occasionally hung when Ctrl+C fired during a tool's permission.Check — the askFn was already past its send and blocked on `<-resp`, with no way to escape
- **Why**: a naïve `askFn := func(a) { ch <- a; return <-resp }` has TWO blocking ops. SIGINT cancels the outer ctx, but the goroutine sitting on either send or receive doesn't notice unless we explicitly select on `ctx.Done()` at each step
- **Fix**: wrap both the send and the receive in `select { case ... : ; case <-ctx.Done(): return false }`. The "deny if cancelled" semantics also matches user expectation — Ctrl+C means stop, not "block forever waiting for me to decide". Commit `7c96bd7`
- **Lesson**: any blocking channel op in a host-supplied callback needs ctx-aware select on every step. The convenience of `ch <- x; <-resp` syntax hides two deadlock points
- **Refs**: `cmd/seek/main.go` askFn closure, `internal/permission/permission.go` ApprovalRequest
