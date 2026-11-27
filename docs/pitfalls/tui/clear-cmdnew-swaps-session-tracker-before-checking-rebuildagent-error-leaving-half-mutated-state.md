### /clear (cmdNew) swaps session/tracker before checking RebuildAgent error, leaving half-mutated state
- **Saw**: on a transient RebuildAgent timeout, `/new` had already swapped `m.opts.Session` to a fresh empty session, saved it to disk, and reset `m.opts.Tracker` (losing cumulative cost) — subsequent prompts sent the OLD agent's messages under the NEW session ID with zero cost tracked.
- **Why**: the order was persist old session → create new session + save → reset tracker → RebuildAgent → check error, so both mutations were committed before the failure-prone call.
- **Fix**: reordered to persist old session → RebuildAgent → check error → only on success, swap session + reset tracker.
- **Lesson**: when a function has multiple state-mutating steps and the last can fail, run the failure-prone one first and mutate only after it succeeds — "validate then mutate" applies at function level too.
- **Refs**: `internal/tui/commands.go:cmdNew`
