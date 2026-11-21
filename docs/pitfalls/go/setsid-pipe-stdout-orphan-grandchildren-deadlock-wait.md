### Setsid + pipe stdout = orphan grandchildren deadlock Wait()
- **Saw**: `TestBash_ContextCancel_KillsProcess` timed out at 10s. Context cancellation (Esc) didn't kill `sleep 600` even though `exec.CommandContext` sent SIGKILL to the child PID.
- **Why**: `detachStdin` sets `Setsid = true` to detach the child from the controlling terminal. But Setsid creates a new session where `sh` can fork `sleep` as a grandchild. When SIGKILL kills `sh` alone, `sleep` survives as an orphan **inheriting the stdout/stderr pipe fds** → the write-ends stay open → `cmd.Wait()` blocks forever waiting for EOF on the pipes.
- **Fix**: switch from `exec.CommandContext` to `exec.Command` + manual `Start()`/`Wait()` on Unix. A dedicated goroutine waits for `cctx.Done()` and kills the **entire process group** via `syscall.Kill(-pid, SIGKILL)`. This closes all pipe fds and lets `Wait()` return.
- **Lesson**: Setsid orphans can keep pipe fds alive. When using Setsid + pipe I/O, always cancel by process group (`-pid`), not by PID.
- **Refs**: `internal/tools/bash/bash.go`, `internal/tools/bash/bash_unix.go`
