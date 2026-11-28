### SSE test hangs in `srv.Close()` when the response body only closes via `t.Cleanup`
- **Saw**: an `/events` (SSE) topic test passed all assertions, then wedged 30s until `go test -timeout` — blocked inside `httptest.Server.Close`, "waiting for connections", the open body registered only as `t.Cleanup(func(){resp.Body.Close()})`.
- **Why**: `t.Cleanup` runs after the test's `defer`s, so `defer srv.Close()` (registered first) fires while the body is open, and `httptest.Server.Close` blocks indefinitely on live connections. Same LIFO family as the chdir/TempDir entry, inverted: the cleanup-only close is too LATE.
- **Fix**: the caller must `defer resp.Body.Close()` immediately after the open — LIFO closes the body before the server; the helper's `t.Cleanup` stays as belt-and-suspenders for Fatal-before-return paths (double `Body.Close` is a safe no-op).
- **Lesson**: for never-ending bodies (SSE, long-poll), a helper returning `resp` is a contract: the caller owes `defer resp.Body.Close()` before anything that closes the server.
- **Refs**: `internal/controlplane/webfrontend_topic_test.go:openEventsRaw` callers; W1.3's `openSSE` in `webfrontend_conv_test.go`.
