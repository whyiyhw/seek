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

## Recalibration (2026-11-18)

The 6000 `max_completion_tokens` ceiling was set from the n=3 first calibration
(`{2389, 3174, 2705}`) and had already stopped bounding runaways by 2026-11-13, the day
v0.11.0 was tagged. Ten recorded runs across 11-13…11-18: `3587, 4540, 7172, 8243, 9368,
10527, 11851, 13701, 14122, 14757` — a bimodal shape (terse ~3.5k vs exhaustive 7–15k) with
`review_line_refs` 16–30 against the calibration's 4–13. The ceiling was gating *thoroughness*,
which is the one thing this record-only case is not supposed to gate. Raised to **20000** —
~35 % above the observed maximum, so it fires only on a true runaway. First run under the new
ceiling: 12186 tokens, 30 line refs, PASS. The case remains record-only; the trend, not the
bound, is the evidence.
