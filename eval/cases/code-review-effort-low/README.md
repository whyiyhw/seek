# code-review-effort-low

**Probes**: does DeepSeek actually *differentiate* the `code-review` skill's effort
levels, or are `low`…`max` just prompt-framing theater? (PRD
[`feature-code-review.md`](../../../docs/prd/feature-code-review.md) §D4 flagged this as the
load-bearing unknown.)

This case is one half of a controlled A/B. It shares a **byte-identical fixture** with
[`code-review-effort-max`](../code-review-effort-max/); the *only* difference between the two
prompts is the effort sentence (`low — precision-first` vs `max — exhaustive recall`). The
prompts reproduce verbatim what `internal/tui/commands.go:codeReviewPrompt` injects, so this
exercises the real shipped path (skill manifest → `Skill` tool → effort framing → review).

## The fixture (graded planted bugs)

A tiny `sizecache` package with a deliberate severity gradient:

| # | Severity | Bug | Who should catch it |
|---|----------|-----|---------------------|
| 1 | Critical | `New()` returns `&Cache{}` with a **nil `sizes` map** → `Record` writes a nil map → panic | low **and** max |
| 2 | Medium | `os.Open` / `f.Stat()` errors ignored (`_`) | low (likely) + max |
| 3 | High | on `os.Open` failure `f` is nil → `defer f.Close()` / `f.Stat()` **nil-deref panic** | mostly max |
| 4 | High | `int(fi.Size())` truncates int64→int on 32-bit / huge files | mostly max |
| 5 | High | `mu` declared but **never locked**; `Record` writes & `Total` reads the map → **data race** | mostly max |
| 6 | Nit | unused `mu` field would trip `staticcheck`; naming | max only |

If the effort knob works: `low` reports ~#1 (+ maybe #2) tersely; `max` additionally surfaces
#3–#5 with confidence tags. If `low` and `max` produce ~the same findings and token counts,
the knob is theater — collapse to 2 levels or give high/max real teeth (reasoner / subagent
fan-out), per the PRD §D4 discussion.

## Metrics & calibration

The differentiation signal is **the delta between this case and `-max`**, on two metrics
(both added to `eval/run.sh` for these cases):

- `completion_tokens` — robust verbosity/thoroughness proxy.
- `review_line_refs` — count of `line N` / `LN` references = rough "number of findings".

`expect.json` ships with only `max_turns` until the first run calibrates real numbers.
**Workflow**: run both cases (`eval/run.sh code-review-effort-low` + `…-max`), read the two
result rows, record the observed numbers below, then set this case's `max_completion_tokens` /
`max_review_line_refs` ceilings and the `-max` case's `min_…` floors so they straddle — turning
the A/B into a standing regression guard.

### Observed (first calibration, 2026-05-29, v0.4.1 · 548aaa0, n=3 each)

| metric | low (3 runs) | max (3 runs) | clean per-run separation? |
|--------|--------------|--------------|----------------------------|
| `completion_tokens` | **1386, 2065, 3514** | 2389, 3174, 2705 | **NO** — ranges overlap; low's max (3514) > max's max (3174) |
| `review_line_refs`  | 0, 5, 0 | 4, 13, 6 | weak — max trends higher (mean ~7.7 vs ~1.7) but low's 5 > max's 4 |

**The single-pair trap.** The very first A/B looked clean — low 1386 vs max 2389 tokens
(1.7×). With n=3 that gap evaporates: **within-arm variance swamps the between-arm signal.** A
`low` run ballooned to 3514 tokens — more verbose than any `max` run. So on token volume the
effort knob does **not** reliably differentiate per invocation.

**Qualitative read of one pair (the gold standard — metrics are only proxies)** still shows a
real difference *in that pair*: `low` reported ~5 findings (the unambiguous bugs — nil-map
panic, nil-deref on `f.Close()`, nil `Stat`, unguarded-map race, `int64`→`int` truncation);
`max` reported ~12 (the same five **plus** unplanted items — unbounded map growth, TOCTOU
stale-read, no dir/regular-file check, missing `Len()`, zero-value unsafety — each with a fix
snippet, a severity legend, and a summary table).

**Verdict (n=3, stochastic — a data point, not proof):** the effort knob has a **real but weak
and noisy** effect. In aggregate / qualitatively `max` is more exhaustive (long-tail design &
speculative findings); but the effect is **not robust per-run** — any single `/code-review low`
can be as verbose as `max`, and both levels catch every blocking bug. As pure prompt framing,
4 crisp levels aren't supported by the data. Two follow-ups were on the table:

1. **✅ DONE — collapsed to 2 levels** (`quick` precision-first / `thorough` exhaustive;
   default `quick`). Legacy `low`/`medium`→`quick`, `high`/`max`→`thorough` map as soft
   aliases. Shipped in `parseCodeReviewArgs` + `codeReviewPrompt` + the `code-review` skill
   body. The framing text in *these* prompts is unchanged, so `low` here ≈ the `quick` framing
   and `max` ≈ the `thorough` framing — the cases remain a valid 2-level A/B.
2. **Not done — give `thorough` mechanical teeth** so the gradation isn't fakeable by
   verbosity: force the reasoner on (`/effort`/think), or fan out a subagent per review
   dimension (v5 柱 G). Only worth it if 2 levels also prove too noisy. Re-run this A/B after
   and expect real separation.

That's why these cases are **record-only** (no separation bound): a tight bound would be a
~⅓-flaky eval. The value is the trend accumulating in `eval/results/`.

## Cost

Two real DeepSeek calls per A/B (~$0.04–0.10). This calibration spent ~$0.40 across captures +
re-runs. See `eval/README.md` § Cost.
