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
- ≤8 tool calls, ≤12 turns

## Baseline (recorded 2026-11-22, `--rounds 3`, deepseek lane, `b7c1bb5`)

Measured on the tree that has only `pattern` / `path` / `context_lines` /
`fixed` / `ignore_case` / `max_matches` — exactly the "before" side of
the A/B this case exists for. **3/3 red**, and red for the intended
reason:

| round | total calls | grep calls | bash calls | `bash_grep_calls` | failure |
|---|---|---|---|---|---|
| r1 | 8 | 2 | 2 | 2 | `bash_grep_calls` >1 |
| r2 | 9 | 2 | 6 | 6 | `total_tool_calls`, `bash_calls`, `bash_grep_calls` |
| r3 | 9 | 4 | 4 | 4 | `total_tool_calls`, `bash_calls`, `bash_grep_calls` |

Every round reached for `bash`—`grep` for the exact-count question
(r1 also paged `read` three times trying to make the arithmetic work by
hand; r3 got there with four `grep` calls and still needed the shell).
`grep_calls` was 2–4 in every round: the tool was used, just not for the
part of the question it could not express. **Do not delete these red
rows as failing tests**: they are the baseline, and they are why
`record_only` is true until the grep extension lands.

## Why `record_only: true`

The case is red by construction on today's tree. Gating a known-red case
would make `eval/run.sh` exit 1 for work that has not shipped yet
(E2 doctrine: gate what a record has calibrated, not what it has
observed). Flip to `min_pass_rate: 0.6` in the change that adds
`include` / `invert` / `count_only` — and delete this paragraph when you
do, so the next reader is not told a landed case is unlanded.

## Not in a lane

Deliberately absent from `eval/lane-core.txt` and `eval/lane-release.txt`
until it goes green. A red case in a release lane would gate every
release on work that has not landed yet.
