### Child process steals /dev/tty, Esc can't interrupt bash
- **Saw**: a third-party interactive prompt via seek's bash tool (sudo, ssh, git credentials, npm init, etc.) froze the whole TUI — Esc unresponsive, session appeared hung
- **Why**: Go's `exec.Cmd` leaves the child connected to the controlling terminal. Commands that open `/dev/tty` directly (bypassing stdin) steal keystrokes — Esc goes to the child, not seek. The context-cancel path (Esc → cancelStream → ctx.Done) fires, but the child never gets the signal: it's busy reading /dev/tty
- **Fix**: `setsid()` via `cmd.SysProcAttr.Setsid = true` before start: a new session, detached from the controlling terminal — `/dev/tty` open returns ENXIO instead of blocking the UI. Added `detachStdin()` (Unix build tag), no-op on Windows
- **Lesson**: child processes inherit the parent's TTY by default; spawning tools must opt out explicitly (setsid, setctty=false, or equivalent), else interactive prompts steal input where context cancellation can't reach
- **Refs**: `internal/tools/bash/bash_unix.go`, `internal/tools/bash/bash.go` (line 113)
