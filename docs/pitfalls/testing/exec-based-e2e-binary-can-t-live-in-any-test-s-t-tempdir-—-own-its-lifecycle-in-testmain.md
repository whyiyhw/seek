### Exec-based E2E binary can't live in any test's `t.TempDir()` — own its lifecycle in `TestMain`
- **Saw**: the first `-tools` E2E test passed; every later one failed with `fork/exec …/seek-e2e: no such file or directory` even though the build succeeded.
- **Why**: the shared `go build` artifact was written into the FIRST consumer's `t.TempDir()`, which is removed when that test ends — the `sync.Once`-cached path then points at a deleted file for the rest of the test-binary run.
- **Fix**: build into a package-level `os.MkdirTemp` dir, removed by a `TestMain` after `m.Run()`; `sync.Once` still dedupes the build. Commit (this one).
- **Lesson**: anything shared across tests (built binaries, downloaded fixtures) must have package-lifetime storage, never per-test `t.TempDir()`. Per-test dirs are for per-test inputs only.
- **Refs**: `cmd/seek/tools_flag_test.go:TestMain`, `buildSeekBin`
