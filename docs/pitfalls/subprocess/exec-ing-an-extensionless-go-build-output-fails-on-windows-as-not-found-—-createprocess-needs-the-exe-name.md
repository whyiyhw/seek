### exec'ing an extensionless go build output fails on Windows as "not found" — CreateProcess needs the .exe name
- **Saw**: 2026-11-17/18 nightlies, Windows lane only: every `TestToolsFlagE2E_*` died with `exec: "C:\...\seek-e2e-bin-…\seek-e2e": executable file not found in %PATH%` — though `go build -o` had just written that exact path.
- **Why**: Windows CreateProcess (under os/exec) only executes files carrying a known extension; for an extensionless path it searches for `<path>.exe` and reports ENOENT. `go build -o <name>` writes the file under exactly `<name>` (no suffix added), so the artifact exists but is unexecutable by that name.
- **Fix**: `buildSeekBin` (cmd/seek/tools_flag_test.go) appends `.exe` when `runtime.GOOS == "windows"`; card c_45npoi.
- **Lesson**: any test/helper that builds-and-execs a helper binary must derive BOTH the `-o` target and the exec path from one `runtime.GOOS`-aware name. Green on macOS/Linux says nothing about this class — it only ever fires on the Windows CI lane.
- **Refs**: `cmd/seek/tools_flag_test.go` `buildSeekBin`
