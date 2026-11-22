# grep-affordance

**Source**: the 2026-11-21 40-session baseline (`~/.seek/sessions/202611*.jsonl`,
6,474 tool calls). `bash` was 76.9% of all calls; the `grep` **tool** was
1.2%. Of the 2,104 bash commands that ran grep/rg/ag, only 350 (16.6%)
could have been a single richer grep call — but the reason the model
reached for bash was concrete: 11.3% needed `--include`, 9.6% needed
`-v`, 9.7% needed `-c`, and 50.1% piped to `head`/`tail` for a limit the
tool already had under a different name. The schema took that trade away
from the model, and the model responded rationally.

## What this probes

Whether the model can answer three *restricted* searches without
shelling out. The fixture is five files in one directory with mixed
extensions, so `path: <dir>` alone cannot answer any of the three
questions:

| file | `MarkerALPHA` | `MarkerBETA` | `MarkerGAMMA` |
|---|---|---|---|
| `core.go` | yes | yes | ×2 |
| `util.go` | yes | — | ×3 |
| `extra.go` | yes | — | ×1 |
| `README.md` | yes | yes | ×5 |
| `notes.txt` | yes | yes | ×4 |
| `gen/g00..g05.go` | — | — | ×940 |

- Q1 needs an extension filter (`--include=*.go` ≡ a `path` glob)
- Q2 needs negation or files-only (`-L`)
- Q3/Q4 need an exact occurrence count over a 940-match population, which
  `max_matches: 200` cannot express at all

The anti-pattern is `bash("grep -rn … | head")`: it works, which is
exactly why the measured sessions did it 2,095 times. `max_bash_grep_calls: 1`
leaves room for one legitimate shell fallback while scoring the third
and beyond as overreach.

Gold (deterministic — computed by `setup.sh` from the arithmetic above,
not from a snapshot of the repo):

- **(1) 3** — `core.go`, `util.go`, `extra.go`
- **(2) `util.go` and `extra.go`** — the two hand-written `.go` files without `MarkerBETA`
- **(3) 946** — 6 hand-written + 940 generated
- **(4) 940**

The `src/gen/` half is what makes Q3/Q4 unanswerable by paging `grep`:
`max_matches` caps at 200 and the tool's capped notice reports the
**capped** count, not the true total (the same arithmetic
[`grep-refine-not-flood`](../grep-refine-not-flood/README.md) documents).
940 matches over ~100-char lines is also ~94 KiB, so paging `read` is not
a cheap side door.

## Fixture arithmetic (setup.sh, sanity-checked)

`setup.sh` emits the tree and then asserts all four golds (3 / 2 / 946 /
940), failing the hook loudly on drift rather than silently moving the
answer. `teardown.sh` removes the scratch dir; nothing is scored from
disk (the answers are read out of the row's `final_text`).

## Expected behaviour

- ≥2 `grep` tool calls, ≤1 bash call that runs grep
- `bash_before_dedicated = 0` — never shell first
- ≤12 tool calls, ≤12 turns

## A/B (2026-11-22, `--rounds 5` each side, deepseek lane)

Both sides ran the SAME `expect.json` against two binaries: `b7c1bb5`
(before the grep extension) and the extension build.

| side | passes | `bash_grep_calls` (sum over 5) | `grep_calls` (sum) | answers correct |
|---|---|---|---|---|
| before (`b7c1bb5`) | **1/5** | 12 | 11 | 5/5 |
| after (extension) | **5/5** | 2 | 18 | 5/5 |

Read the middle two columns together, not separately:

- `bash_grep_calls` fell 12 → 2, and the two survivors are one round's
  single fallback — exactly the budget the bound allows.
- `grep_calls` rose 10 → 18 because the extension spreads the work over
  more, narrower tool calls (one `include`-filtered `files_only`, one
  `invert`, one `count_only`) instead of one shell pipeline. That is the
  intended shape, and it is why `max_total_tool_calls` is 12 rather than
  the pre-extension 8: **a call-count bound calibrated before the
  extension punished the very shape the case asks for.** The first
  post-extension run (3/3 red, all on `total_tool_calls`) was the case
  mis-specified, not the model regressing — the model was correct in
  all six rounds.
- Answers were correct on both sides in every round. This case scores
  the ROUTE, not the answer; the answer gate is
  [`grep-refine-not-flood`](../grep-refine-not-flood/README.md).

The before-side detail (3 rounds at the earlier bound) is preserved in
`eval/results/2026-11-22-v0.12.2-*.jsonl`; the A/B rows are in
`eval/results/2026-11-22-v0.12.4-*.jsonl`. **Do not delete the red
before-side rows**: they are the measurement that justifies the
extension.

## Why `min_pass_rate: 0.6`

5/5 on the after side is a clean record, but Q3 is answerable two ways
(one `count_only`, or a filtered `read`) and the prompt allows either.
0.6 = 3/5 keeps the gate live while tolerating two rounds of legitimate
route variance. Tighten to 1.0 after a second 5/5 record if the route
stays this stable.

## Not in a lane

Deliberately absent from `eval/lane-core.txt` and `eval/lane-release.txt`
for one more version: promote it after a second record, so the lane
entry is backed by two observations rather than one.

