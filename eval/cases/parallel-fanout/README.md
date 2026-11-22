# parallel-fanout

**Source**: the 2026-11-21 40-session baseline. Turn fan-out distribution
over 4,797 tool-bearing assistant messages:

| calls per turn | messages | share |
|---|---|---|
| 1 | 3,070 | 64.0% |
| 2 | 1,715 | 35.8% |
| 3 | 7 | 0.1% |
| 4 | 5 | 0.1% |

`pkg/agent/agent.go` dispatches `ReadOnlyTool` calls as a concurrent
batch, so three reads in one turn cost one round trip and one wall-clock
wait — the same work serialised costs three of each. The model almost
never takes that option above two.

## What this probes

Whether the model batches three *independent* reads. The fixture is
three tiny files with one distinctive sentinel line each; the prompt
asks for all three values in one question, so there is no informational
reason to stagger them.

- **Healthy**: one turn, three `read` calls, one answer
- **Drifting**: `read alpha` → answer → `read bravo` → answer → … or a
  single `bash("cat all three")`
- `max_total_tool_calls: 6` leaves room for a `grep` to locate the files
  first plus the three reads; `min_parallel_turns: 1` is the whole point

## Fixture contract (setup.sh, sanity-checked)

`eval/tmp/parallel-fanout/{alpha,bravo,charlie}.txt`, each 81 lines with
exactly one `NAME-SENTINEL: <number>` line. Gold:

- `ALPHA-SENTINEL: 7412`
- `BRAVO-SENTINEL: 9025`
- `CHARLIE-SENTINEL: 3167`

`setup.sh` asserts one sentinel per file; `teardown.sh` removes the
scratch dir. Nothing is scored from disk — the answer is read out of the
row's `final_text`.

## Baseline (recorded 2026-11-22, `--rounds 3`, deepseek lane, `b7c1bb5`)

**3/3 green.** Every round issued all three reads in ONE turn
(`parallel_turns: 1`, `total_tool_calls: 3`, `read_calls: 3`) in 4–6s,
with zero `grep` and zero `bash`.

That result is worth stating plainly because it **falsifies the
prediction this case was built on**: the 0.2% ≥3-call figure in the
aggregate baseline is a property of *organic* work, where each next
probe depends on the previous answer — not of a prompt that names three
independent files. Told exactly what to fetch, the model batches. So
this case is a **regression guard**, not a red-baseline probe: it pins
that the fan-out habit survives future prompt/tooling changes, and it
does **not** justify a system-prompt nudge of its own.

If a later change makes it red, that is a real regression in turn-shape —
investigate before touching the bound.

## Why `min_pass_rate: 0.6`

3/3 on the first record is a small sample, and fan-out is a stylistic
preference with a modest payoff (two saved round trips). 0.6 = 2/3 lets
the case gate while tolerating one serialised round out of three — the
shape E2 calibrated for "depends on model choice, not fixture
arithmetic". Tighten to 1.0 after a second 3/3 record if the habit looks
stable.

## Not in a lane

Absent from both lane files until green, same discipline as
[`grep-affordance`](../grep-affordance/README.md).
