### `tm.Output()` is a single-use reader — sequential `WaitFor` calls drain it
- **Saw**: a test with two consecutive `teatest.WaitFor(t, tm.Output(), ...)` calls: first asserted string A (passed), second string B (timed out, empty buffer), though both A and B were in the first failure's output dump
- **Why**: `tm.Output()` returns the same underlying pipe/reader every call; `teatest.WaitFor` reads incrementally, stops when the predicate matches, and the bytes it consumed are gone. The second `WaitFor` reads from that same position — nothing new since. When the program is idle (e.g. help overlay rendered once, no spinner re-renders) there are no new bytes
- **Fix**: one `WaitFor` per test; if multiple signals must be checked, compose them into a single predicate (`bytes.Contains(b, a) && bytes.Contains(b, c)`). Commit (this one)
- **Lesson**: treat `tm.Output()` as a stream, not a buffer: sequential reads work only if the model emits between them — never assume earlier bytes are still readable
- **Refs**: `internal/tui/render_test.go` (the `waitFor` helper takes a single needle for this reason)
