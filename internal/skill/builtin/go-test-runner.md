---
name: go-test-runner
description: Use when the user asks to run, debug, or analyze Go tests. Triggers on phrases like "run tests", "go test", "fix failing test", or when a tool result reports a test failure.
---

# Running Go tests in seek

This codebase is Go, with a CI policy of `go test -race ./...` on three OSes. When the user asks about tests, follow this workflow:

1. **Locate the package** — if the user named a path, use it; otherwise list candidates with `go list ./...` and pick the obvious one based on what they said.
2. **Run with race detector and verbose output** for the chosen package:
   ```
   go test -race -v ./path/...
   ```
3. **On failure**: read the failure message, identify the test name + assertion, then `read` the source file. Propose the smallest diff that satisfies the assertion. Do NOT relax the assertion to make the test pass — that's almost always wrong.
4. **Re-run before claiming success** — don't say "fixed" until you've seen a green run.

Common traps:
- `_test.go` files in the same package can see unexported symbols. Don't move tests to a `_test` external package unless that's specifically what the user wants.
- `t.Parallel()` + shared state often hides flakes that only race-detector catches. If a test is flaky, run it with `-count=10 -race` to surface ordering bugs.
- `httptest` servers must be closed via `defer srv.Close()` — leaking them in a test loop will exhaust ephemeral ports on CI.
