### `--idle` must read last_used from the UNfiltered aggregate — the window-filtered one reports every idle skill as "never"
- **Saw**: implementing `seek skill stats --idle` (bridge M-B4): the one-aggregate approach (reuse the window-filtered `aggregate`) yields `LastUsed == ""` for every listed skill — their only calls predate the window and were filtered out, making "not called recently" indistinguishable from "never called".
- **Why**: `skillcli.aggregate(entries, since, now)` answers two questions in one fold (call count AND last-used); windowing it empties both. Idleness (window) and recency (all-time) are different questions over the same rows.
- **Fix**: `idleRows` runs two aggregates — `inWindow` decides inclusion, `ever` supplies the true last_used; commit with card `c_lz5wxa`.
- **Lesson**: when one fold answers two questions, windowing it silently degrades the second to the window's edge case. Split the folds, not the fields.
- **Refs**: `internal/skillcli/query.go` `idleRows`, `docs/prd/feature-memory-skill-bridge.md` §6.
