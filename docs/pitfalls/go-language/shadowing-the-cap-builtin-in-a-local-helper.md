### Shadowing the `cap` builtin in a local helper
- **Saw**: a local helper named `cap(s string, limit int) string` worked fine but felt wrong; reviewers would flag it
- **Why**: `cap` is a Go builtin (slice/channel capacity). Shadowing it inside a package is legal but confusing — and would silently break code in the same file that wanted `cap(slice)`
- **Fix**: renamed to `clip`. Commit `8264f52`
- **Lesson**: check `go doc builtin` before naming a function. Builtins that are also common English words (`cap`, `len`, `new`, `make`, `copy`, `close`) are easy to step on
