# subagent-bg-resume

## What this probes

Load-bearing axis #2 of eval v2
([`docs/prd/feature-eval-v2.md`](../../../docs/prd/feature-eval-v2.md) §2.2,
E4 at :108): the **background-subagent lifecycle end to end** — one of
the six zero-coverage areas named in §1.2. Three PRD assertions, each
bound to a metric:

| PRD assertion | Metric(s) | Surface |
|---|---|---|
| 完成通知进主会话流 (completion notice enters the main session stream) | `bg_notified_events` ≥ 1, `bg_notified_transcript` ≥ 1 | the `notified` consumption marker in `projects/*/subagents.jsonl` (Notifier delivery-side write, feature-orchestration §4.1 D3/D5) **and** the `<context>` user message in the saved parent session transcript (Notifier `OnPrePrompt` Prepend, M-O1.x D2) |
| 续话 run 带 `resumed_from` 血统 (continuation carries lineage) | `bg_resumed_lineage` ≥ 1, `agent_continue_calls` ≥ 1 | a `started` event whose `resumed_from` is non-empty AND names a sub_sid present in the index (`Manager.Continue` via the agent tool's `session_id` param, P1-b) |
| 子代理产物落在预期路径 (artifacts at the expected path) | `bg_artifacts_missing` = 0 | every `completed` sub_sid has `projects/<pid>/sessions/<parent-sid>/subagents/<sub-sid>/transcript.jsonl` (`paths.SubagentSessionDir` layout) |

Model-side shapes are asserted from the stream: `agent_bg_calls` ≥ 1
(the spawn carried `run_in_background: true`) and `agent_continue_calls`
≥ 1 (a follow-up carried a non-empty `session_id`).

## How it runs (runner.sh)

The default harness invocation is unusable here: it passes `-no-save`,
and seek skips subagent wiring entirely in that mode (no `agent` tool in
the registry). The runner replaces it with a two-invocation chain under
an isolated per-round `SEEK_HOME` (`eval/tmp/subagent-bg-resume/home`,
wiped each round):

1. **Run 1** (fresh, session saved): the model spawns ONE
   `run_in_background` subagent with a trivial no-tool task, then keeps
   working (reads `go.mod`). The child must finish **before the parent
   process exits** — `Manager.Shutdown` cancels still-active children
   (`failed reason=canceled`), which folds to `bg_completed` 0 and a red
   round. This is the case's **known race** (see Flake source).
2. **Run 2** (`-resume <sid>`): startup `RecoverNotices` (M-O1.3 D3)
   rehydrates the completed-but-un-notified child; the first Prompt
   drains it → `<context>` message + `notified` marker. The prompt then
   asks the model to continue the child (`session_id=<sub-sid>`,
   interpolated by the runner from the index) with a trivial follow-up
   → new `started` event carrying `resumed_from`.

The runner then folds `subagents.jsonl` + the saved session transcript
and reports counts as
`{"type":"runner_fact","check":<name>,"value":N}` lines — the
file-level sibling of `nonzero_exits`, consumed by run.sh's `bg_*`
metric family. **The facts are emitted unconditionally (value ≥ 0)** so
the gated metric keys exist even on failing rounds — a red always names
its metric, never "unknown metric".

## Anti-false-red doctrine (bounds assert shapes and fields, never prose)

Every disk-side assertion is on **event kind + field identity**:

- joins are on `sub_sid` values across `started` / `completed` /
  `notified` events — never on descriptions or summaries;
- the transcript check matches `role=="user"` + `content` starting with
  `<context>` + containing the wire prefix
  `[agent: background done sub-sid=<full sid>]` — the closed
  wire-format token (`internal/subagent/wireformat.go`), not the
  summary prose inside it (which legitimately differs between live D2
  delivery and D3 resume-backfill: "delivered on session resume" is
  prose we deliberately do NOT assert);
- `resumed_from` lineage checks field non-emptiness AND referential
  validity (names an existing sub_sid), not formatting.

What would false-red if prose were asserted: the child's summary text
("marlowe"), the notification's trailing prose, the model's final
reply. None are bound.

## Flake source (calibration input): the child-outlives-parent race

There is no agent-side "wait for background subagent" primitive (the
`monitor` tool covers bash bgjobs only), so run 1's prompt engineers
the window: spawn FIRST, then do one read, then reply. A trivial
one-turn child (no tools) normally finishes in a few seconds while the
parent still has a read round-trip + final turn ahead of it — but a
slow lane can still cancel the child at process exit. Rounds +
`min_pass_rate` absorb exactly this: the gate starts at **0.6 @ 5
rounds** (PRD §六.3 doctrine: a load-bearing case measuring < 0.6
means the product's prompts need strengthening, not the threshold
loosened). Real-key baseline rows are **not yet measured** — see the
calibration slot in `eval/README.md` § Load-bearing axes.

## Cost

Two parent invocations + two child invocations per round, all trivial
(≤ 3 turns each): ≈ $0.02–0.05 per round on the default subscription
lane → ≈ $0.1–0.25 per 5-round release-lane run (PRD §五's B-项
$0.1–0.3 estimate). Release lane only — never nightly.

## Baseline status (2026-11-21, card c_vollxv)

Mechanism landed and pinned by `scripts/eval-e4-case-test.sh`
(deterministic fake-binary selftest, no key): all-pass / each
assertion-failure flavor / record_only / `--rounds` aggregate
compatibility. **Real-key baseline: NOT measured** — unattended lanes
run fake-key by rule (`docs/prd/v8.md` §9.1); the 5-round baseline and
any gate tightening are a human/main-session action on a release lane.

## Mechanism budget (一进一出)

Zero new resident machinery: the disk-side channel rides the existing
`runner_fact` seam and run.sh's existing `extract_metric` table (one
multi-pattern case arm); the nine agent/bg metric keys are DATA-GATED
into the result row (merged only when the round's stream has an agent
surface) precisely so the no-env byte-equivalence pin
(`scripts/eval-ablate-test.sh`) stays green — same discipline as E1's
conditional `round` key. The selftest is one deterministic script in
the auto-discovered `scripts/` lane. Nothing to retire: no state
files, no daemons, no CI wiring (release-lane membership is E6's card).

## Architecture page: not applicable

This case adds no component, state, or flow — it READS existing
surfaces (`subagents.jsonl`, session JSONL, the subagent transcript
tree) that `docs/guide-architecture.md` already covers. Recorded here
per the card's acceptance ⑥.
