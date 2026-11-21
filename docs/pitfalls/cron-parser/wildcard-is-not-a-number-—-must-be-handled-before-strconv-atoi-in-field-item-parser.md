### `*` wildcard is not a number — must be handled before `strconv.Atoi` in field-item parser
- **Saw**: after adding 5-field cron parsing, all valid expressions with `*` fields (e.g. `* * * * *`) failed with `bad value "*"` because `parseCronItem` fell through to `strconv.Atoi("*")`. Same for any `@hourly`-disguised test that happened to pass `*` to the cron path.
- **Why**: the parse pipeline for a single field item checked name aliases → `*/N` → range/step → range → `strconv.Atoi`. A bare `*` matched none of those patterns and hit the number parser, which rejected it.
- **Fix**: add an early `if item == "*"` check before any other item-type detection, returning `cronStep(min, max, 1)` (all values). Commit `[current commit]`.
- **Lesson**: any cron parser that decomposes field items into "special forms" must treat `*` as a first-class form, not a fallback default. It's the most common cron symbol and the easiest to forget.
- **Refs**: `internal/routines/schedule.go:parseCronItem`, `internal/routines/schedule.go:parseCronField`
