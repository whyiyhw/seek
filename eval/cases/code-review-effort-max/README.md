# code-review-effort-max

The `max` half of the effort-differentiation A/B. See
[`code-review-effort-low`](../code-review-effort-low/) for the full experiment design, the
shared fixture's graded planted bugs, the metric definitions, and the calibration table.

**Only difference from the `-low` prompt**: the effort sentence reads
`Effort level: max — exhaustive recall: edge cases, concurrency, error paths, and test gaps;
mark unconfirmed items explicitly.` Everything else (the fixture, the skill instruction, the
read-only framing) is byte-identical, so any delta in `completion_tokens` / `review_line_refs`
is attributable to the effort level alone.

**Finding (n=3, first calibration)**: `max` *does* trend more exhaustive — qualitatively ~12
findings vs `low`'s ~5, surfacing long-tail items (unbounded growth, TOCTOU, no file-type
check) a `low` review skips. **But the effect is not robust per-run**: `completion_tokens`
distributions overlap (a `low` run hit 3514, above every `max` run), so no `min_…` floor holds
without making this a flaky eval. Both cases are therefore **record-only**. See the
[`-low` README](../code-review-effort-low/README.md) for the full A/B data, the single-pair
trap it exposed, and the two recommended follow-ups (collapse to 2 levels, or give high/max
mechanical teeth).
