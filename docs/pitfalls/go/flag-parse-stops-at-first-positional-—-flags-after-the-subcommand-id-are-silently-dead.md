### `flag.Parse` stops at the first positional — `seek attach <id> --timeout 30s` silently ignored the flag

- **Saw**: `seek attach t-1 --timeout 30s` never returned: `flag.Parse` had stopped at `t-1`, so the timeout flag was never read — no error, just an apparent hang.
- **Why**: Go's `flag` package stops parsing just before the first non-flag argument, so every verb taking a positional id (`attach <id>`, `kill <id>`) had a silent second calling convention where flags after the id were dead — and flags-last is exactly how users type it.
- **Fix**: `taskcli.splitPositionals(args, valueFlags)` splits flags from positionals before `fs.Parse`, so a flag works on either side of the id; `valueFlags` names the flags that consume the next argv element so `30s` is not mistaken for a positional; `--` still terminates flag parsing. Card `c_wvd7a7`.
- **Lesson**: for verbs of the shape `verb <positional> [flags]`, assume flags-last and test both spellings; prefer an explicit splitter over `flag.Parse`'s stop-at-first-positional behaviour — a silently ignored flag is worse than an error.
- **Refs**: `internal/taskcli/cmds.go` `splitPositionals`; `internal/taskcli/taskcli_test.go` (`TestAttach_TimeoutLeavesTaskRunning`, `TestKill_GRACE_ESCALATION`).
