# references-trigger

**Source**: the 2026-11-21 40-session baseline. The `references` tool
(semantic find-references via gopls / pyright / typescript-language-server,
`internal/tools/references`) was called **zero times in 6,474 tool calls**,
in the same 40 sessions that ran grep 2,100 times from bash. `gopls` was
on PATH the whole time. The tool works; nothing in its description gives
the model a *moment to reach for it* — it reads like an optional extra
rather than the right first move before changing an exported signature.

## What this probes

Whether the model picks the semantic tool when the task is exactly its
use case: "I am about to change this signature — who calls it?".

Deliberately hardened against grep: the target symbol `Compose` is
matched by a naive text search in four distinct ways —

- the real call sites (`sysprompt.Compose(` in two packages)
- `ComposeSubagent(` — a different function whose name contains the needle
- doc comments that quote `Compose(h, "subagent")` verbatim
- the test file's own calls, which are real but not what a caller audit
  of *production* code wants mixed in unlabelled

A name grep answers all of those with no distinction; `references`
resolves the symbol and returns only real references. That difference is
the point of the case, so the prompt states the hazard explicitly rather
than letting the model stumble into it.

## Expected behaviour

- `references_calls >= 1` — the semantic tool is reached for
- `bash_grep_calls <= 1` — one fallback grep is tolerated; three is the
  measured habit and scores red
- ≤6 tool calls, ≤10 turns

## Baseline (recorded 2026-11-22, `--rounds 3`, deepseek lane, `b7c1bb5`)

**3/3 green, `references_calls: 1` in every round.** Real output from
one round — 17 semantic references, including the definition line and
the one intra-package caller (`sysprompt.go:203`) that a
`grep -n "Compose("` run buries among `ComposeSubagent` hits:

```
17 reference(s) to Compose:
internal/sysprompt/sysprompt.go:146:6  | func Compose(h Header, modeLabel string) string {
cmd/seek/main.go:1101:28  | systemPrompt := sysprompt.Compose(sysprompt.Header{
cmd/seek/main.go:2050:20  | sp := sysprompt.Compose(sysprompt.Header{
internal/sysprompt/sysprompt.go:203:12  | header := Compose(h, "subagent")
...
```

**This falsifies the attribution that motivated the metric, and the
correction matters more than the case.** The 40-session baseline showed
`references` at 0 of 6,474 calls, which I read as "the tool is
undiscoverable". It is not: given a task that *frames itself* as a
caller audit ("I am about to change this signature"), the model reaches
for it every time, first or second tool call, and uses it correctly.
The 0 was a **prompt-framing** fact, not a discovery fact — organic
work rarely announces itself as a caller audit. So the lever is the
task framing the caller (drive cards, butler `next_step`, a human's
phrasing), not a description rewrite in `internal/tools/references`.
Do **not** ship a description change on the strength of the 0 alone.

## Why `record_only: true` (still)

3/3 on one prompt is a single cell of a matrix that has never been
sampled. The open question this case now exists to answer is the
*organic* rate — how often a real task frames itself this way — and
that is measured by `bash_grep_calls` vs `references_calls` in real
sessions over time, not by this fixture. Keep `record_only` until there
are two version-to-version records; then either gate it (if the fixture
stays 3/3, meaning the capability is stable and worth protecting) or
retire it (if the organic rate stays at 0, meaning the case guards a
path nobody takes).

## The skip path (runner.sh, not the harness)

`references` needs a language server binary. The default invocation
cannot express "skip when absent", and a missing gopls on a CI box would
manufacture a red that says nothing about the model. `runner.sh` probes
for `gopls`/`pyright`/`typescript-language-server` and, when none is
found, emits a single **non-failing** fact and no assertions:

```
{"type":"runner_fact","assert":"runner_precondition","failed":false,"detail":"no language server on PATH …"}
```

`runner_assert_failures` therefore stays 0 and the row is an honest
record. This is the same doctrine as every other runner early-out —
except the direction is inverted on purpose: a crashed harness must turn
the row red (`plan-e2e`), while an absent *optional dependency* must not.
The distinguishing rule: an early-out that hides evidence is red; an
early-out that reports "this machine cannot answer the question" is not
pretending to be green either — it emits no verdict at all.

## Not in a lane

Absent from both lane files until it has an observation record — see
[`grep-affordance`](../grep-affordance/README.md) for the same rule.
