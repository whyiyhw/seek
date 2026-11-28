### `t.Setenv` inside a `t.Parallel` test panics — and the panic surfaces from a helper, not the test
- **Saw**: `go test ./internal/tui/` blew up with `panic: testing: t.Setenv() called after t.Parallel()` and a stack pointing at `persistSessionTestFixture` — a shared helper, not the new test (whose body was clean; `t.Parallel()` sat two frames up).
- **Why**: Go forbids `t.Setenv` after `t.Parallel()` (the process env is global), and the guard sits on the `*testing.T` receiver — ANY helper receiving the parallel test's `t` inherits it, so `t.Setenv` helpers (`persistSessionTestFixture`, `withSeekHome`) are unusable from parallel tests.
- **Fix**: drop `t.Parallel()` from tests reaching a `t.Setenv` helper. Don't hoist the `Setenv` into `TestMain` — that makes the sandbox dir global and defeats per-test isolation.
- **Lesson**: a helper's hidden requirements travel with `t`: when a new test panics inside a helper you didn't touch, check whether it calls `t.Setenv` / `t.Chdir` / `os.Setenv` and whether the test declared `t.Parallel()`.
- **Refs**: `internal/tui/persist_test.go`, `internal/config/config_test.go`
