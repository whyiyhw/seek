### WSL sudo escapes process-group kill, bash Wait() hangs past timeout
- **Saw**: `bash -c 'sudo cp …'` in WSL2 hung 55+ minutes despite `timeout_ms` max (10m); Esc/context cancel fired but the TUI stayed blocked until manual kill.
- **Why**: `sudo` (and `setsid`) create a new session/PGID so `SIGKILL` to `-sh.PGID` only kills the shell wrapper, TTY-less WSL `sudo` blocks on a Windows credential dialog (not killable via Linux signals), and orphaned descendants keep the stdout/stderr pipe fds open so `cmd.Wait()` never returns.
- **Fix**: on Linux, walk `/proc/[pid]/children` recursively and SIGKILL every descendant **before** the PGID kill (killing sh first reparents escapees and empties `/proc/<sh>/children`), cap `cmd.Wait()` with a 5s post-kill grace, and snapshot stdout via a mutex-protected buffer.
- **Lesson**: Setsid + PGID kill is necessary but not sufficient when children escape the group (sudo, setsid) — snapshot the descendant tree before reaping the root and pair group kill with descendant cleanup AND a Wait() deadline.
- **Refs**: `internal/tools/bash/proc_linux.go`, `internal/tools/bash/bash_unix.go`, `internal/tools/bash/bash.go`, issue #9, docs/book/chapter-24.md
