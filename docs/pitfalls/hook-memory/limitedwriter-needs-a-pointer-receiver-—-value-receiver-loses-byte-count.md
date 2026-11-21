### limitedWriter needs a pointer receiver — value receiver loses byte count
- **Saw**: cap-output write helper looked OK in isolation but in tests the captured stdout would exceed the documented 64KiB cap because `l.n` never seemed to grow
- **Why**: `func (l limitedWriter) Write` (value receiver) gets a fresh copy of the struct on every call. `l.n += n` modifies the copy, which is discarded when Write returns. The caller (exec.Cmd) sees `Write` return successfully and keeps streaming, blowing past the cap silently
- **Fix**: switch to pointer receiver `func (l *limitedWriter) Write` and pass `&limitedWriter{…}` when assigning to `cmd.Stdout` / `cmd.Stderr`. The `io.Writer` interface is satisfied by both value and pointer receivers, but ONLY the pointer receiver mutates state across calls
- **Lesson**: any "tracking" struct (counter, accumulator, ring buffer) that satisfies an interface MUST use pointer receivers. The compiler doesn't warn — it'll happily build and silently drop your mutations
- **Refs**: `internal/hooks/shell_runner.go:limitedWriter`
