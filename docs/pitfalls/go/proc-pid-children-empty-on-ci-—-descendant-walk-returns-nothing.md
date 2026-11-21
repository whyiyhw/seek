### /proc/PID/children empty on CI — descendant walk returns nothing
- **Saw**: Linux CI failed `TestDescendantPIDs_FindsNestedChildren` (got `[]`) and `TestBash_Timeout_KillsEscapedProcessGroup` (elapsed ≈ `killWaitGrace` 5s — setsid sleep survived)
- **Why**: `descendantPIDs` relied solely on `/proc/[pid]/children`, which is missing or empty on some GitHub Actions kernels/containers even when `/proc/[pid]/stat` PPID links are present
- **Fix**: fall back to scanning `/proc/*/stat` for `PPID == parent` when the `children` file is absent or empty; keep snapshot-before-kill ordering in `killProcessGroup`
- **Lesson**: treat `/proc/PID/children` as an optimisation, not the only source of truth — PPID scan is slower but works wherever `/proc` is mounted
- **Refs**: `internal/tools/bash/proc_linux.go`, `internal/tools/bash/proc_linux_test.go`
