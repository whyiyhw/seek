### Shell-hook SkipReason needs per-event handling, not a single funnel
- **Saw**: a hook marked `SkipReason="syntax: bad"` (failed `bash -n` at startup) produced `Deny: 'hook "broken" exited with code -1'` and blocked the tool, though the PRD says skipped hooks must be silently bypassed.
- **Why**: the natural short-circuit inside `runHook` (return `code=-1`, `Reason="skipped: …"`) fails because `OnPreToolUse` treats `code != 0` as deny by design — no neutral sentinel exit code for "skipped" exists, and pre_tool needs skip-and-continue while observers need skip-and-noop.
- **Fix**: each event entry point checks `h.SkipReason` itself BEFORE calling `runHook` and decides what skip means for that event — `OnPreToolUse` `continue`s the loop, observers no-op; `runHook` still defends-in-depth by returning `code=0` if reached with SkipReason set.
- **Lesson**: when a flag has different semantics per call site, push the handling to the call site rather than synthesising a magic value in the shared helper — shared helpers for shared invariants, per-caller decisions with the caller.
- **Refs**: `internal/hooks/shell_runner.go:OnPreToolUse`, `internal/hooks/shell_runner.go:runHook`
