### Go's `time.ParseDuration` rejects "30d" / "1w" — only ns…h
- **Saw**: a `flag.Duration` with default `30*24*time.Hour` accepted `--since=720h` but choked on `--since=30d` with "parse error" — the natural unit a CLI user would write
- **Why**: `time.ParseDuration` (since 1.0) only supports `ns`, `us`/`µs`, `ms`, `s`, `m`, `h`. There is no day or week unit because they're not exactly 24h / 7d (DST, leap seconds), and the stdlib won't fake it
- **Fix**: document the limitation in the flag help text (`--since DURATION (e.g. 24h, 168h, 720h)`), and have tests use hour-based values. Don't reinvent parsing on top — too many edge cases. M8.4b commit
- **Lesson**: when exposing a duration flag, either accept Go's surface and tell the user, or write a custom parser that handles `d`/`w` explicitly. Half-measures (silently approximating "30d" as 720h) confuse users who write `31d` and don't see what they expected
- **Refs**: `cmd/seek/skill_query.go:cmdSkillStats`
