### A frozen baseline keeps asserting a shell-parameter shape that has since been changed
- **Saw**: the 2026-11-19 baseline's headline `+50, 991 次` (offset step = the 50-line read page) was read downstream as "read still paginates at 50", but `2f0f1df` (2026-08-14) had already raised `limit` 50 → 200 and added whole-file reads. All 991 `+50` steps come from sessions created before 2026-08-14; the 20 later sessions contribute 0.
- **Why**: a baseline is immutable by design (the "before" half of an A/B), so it can't be regenerated when the shell changes — every reader gets a snapshot of a world that no longer exists, and nothing in the numbers says so.
- **Fix**: `eval/transcript-baseline/2026-11-19.md` now carries an explicit "historical shape, not current behaviour" note at both quote sites (key-numbers block + next-card checklist), naming commit, date, and old/new values.
- **Lesson**: any frozen measurement whose subject is a tunable shell parameter must be stamped "as measured under <value>, changed at <commit/date>" at every place a reader meets the number, not just in the header.
- **Refs**: `eval/transcript-baseline/2026-11-19.md` § "四个关键数字" / "下一张卡（预告）", commit `2f0f1df`
