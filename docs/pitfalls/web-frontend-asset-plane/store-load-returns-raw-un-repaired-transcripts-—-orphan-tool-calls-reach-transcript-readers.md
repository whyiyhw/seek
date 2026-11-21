### Store.Load returns raw un-repaired transcripts — orphan tool_calls reach transcript readers
- **Saw**: assuming `session.Store.Load` hands back a playable message history, stats/friction (card `c_2i6sm6`) nearly scored orphan tool_calls as "already repaired away" — they aren't: a mid-tail orphan survives Load verbatim.
- **Why**: repair lives in `Session.Repair()` (backfill + truncate-at-last-orphan), which is a separate RESUME-time action callers opt into; `Load`/`decodeJSONL` just decode the JSONL as written.
- **Fix**: `friction.Fold` handles orphans as first-class input (counted as Calls, no Errors/Denied attributable); commit with card `c_2i6sm6`.
- **Lesson**: transcript readers on the Load path see the disk truth — interrupted-run tails included. Anything assuming API-replayable shape must call `Repair` itself or tolerate orphans; document which at the read site.
- **Refs**: `internal/session/session.go` `Repair` / `Load`; `internal/friction/friction.go` `Fold`.
