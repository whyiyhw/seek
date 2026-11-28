### `flag.Parse` stops at the first positional — `seek attach <id> --timeout 30s` silently ignored the flag
- **Saw**: `seek attach t-1 --timeout 30s` never returned: `flag.Parse` stopped at `t-1`, the timeout flag was never read — no error, just an apparent hang.
- **Why**: Go's `flag` package stops at the first non-flag argument, so every verb with a positional id (`attach <id>`, `kill <id>`) had a silent second calling convention: flags after the id were dead — and flags-last is how users type it.
- **Fix**: `taskcli.splitPositionals(args, valueFlags)` splits flags from positionals before `fs.Parse`, so a flag works either side of the id; `valueFlags` names flags consuming the next argv element so `30s` isn't read as a positional; `--` still terminates parsing. Card `c_wvd7a7`.
- **Lesson**: for `verb <positional> [flags]` shapes, assume flags-last and test both spellings; prefer an explicit splitter over `flag.Parse`'s stop-at-first-positional — a silently ignored flag is worse than an error.
- **Refs**: `internal/taskcli/cmds.go` `splitPositionals`; `internal/taskcli/taskcli_test.go` (`TestAttach_TimeoutLeavesTaskRunning`, `TestKill_GRACE_ESCALATION`).
