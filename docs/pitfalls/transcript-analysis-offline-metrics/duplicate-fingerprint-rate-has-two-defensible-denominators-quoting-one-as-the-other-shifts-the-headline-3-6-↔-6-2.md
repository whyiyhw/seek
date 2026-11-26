### Duplicate-fingerprint rate has two defensible denominators; quoting one as the other shifts the headline 3.6% ↔ 6.2%
- **Saw**: the same corpus yields "3.6% of tool calls are duplicates" or "6.2%" depending on whether you sum `count-1` per fingerprint (extra call = wasted) or `count` for every fingerprint seen more than once. The research note quoted the first for the total and the second per tool, producing "bash worst at 9%" vs an honest per-tool extra of 8.5%.
- **Why**: both correctly answer different questions ("how many calls were wasted" vs "how much of the stream is repeat activity"); the gap is non-trivial whenever repeat clusters are large (a fingerprint appearing 5 times contributes 4 vs 5).
- **Fix**: `scripts/transcript-stats` reports both (`dup_extra`, `dup_repeated`) side by side, naming the headline; the baseline report flags the mixing in the source note.
- **Lesson**: a ratio over "repeated things" must name its denominator in the same breath as the number, and both variants belong in the output — the value alone doesn't reveal which was used.
- **Refs**: `scripts/transcript-stats` `analyse()`, `eval/transcript-baseline/2026-11-19.md` § "工具名分布 + 重复指纹"
