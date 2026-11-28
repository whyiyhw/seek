### `os.Process.Signal(syscall.Signal(0))` is invalid on Windows — a signal-0 liveness probe calls every live process dead

- **Saw**: the `p, _ := os.FindProcess(pid); p.Signal(syscall.Signal(0))` fallback would have reported every running task as gone on Windows — `seek ps` showing `lost` for live tasks, `seek kill` believing nothing to terminate.
- **Why**: `os.Process.signal` on Windows accepts only `os.Kill`/`os.Interrupt`, `EINVAL` otherwise; the identical source compiles on both platforms, so wrong semantics survive review and cross-build.
- **Fix**: `internal/taskproc/probe_windows.go` probes with `syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` + `GetExitCodeProcess` (`STILL_ACTIVE == 259` ⇒ alive) and terminates with `taskkill /T /F`; the signal-0 fallback is behind `!darwin && !linux && !windows` (BSDs only). Card `c_wvd7a7`.
- **Lesson**: "compiles everywhere" ≠ "means the same everywhere" — signal-based liveness needs an explicit Windows branch (build-tagged file + `GOOS=windows go build ./...`); CI cross-build catches missing symbols, never wrong semantics.
- **Refs**: `internal/taskproc/probe_windows.go`, `internal/taskproc/probe_other.go`; `$GOROOT/src/os/exec_windows.go` `(*Process).signal`; `internal/tools/bash/bash_windows.go`.
