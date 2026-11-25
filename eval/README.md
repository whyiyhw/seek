# seek eval harness

Behavior comparison tests for the agent loop. Each case fixes a
**prompt** + **expected metrics**; the runner pipes the prompt
through `seek -json` and checks the resulting tool-call stream
against the case's `expect.json`.

This is **not** a unit-test suite — it's a black-box probe of LLM
behaviour against a real model API (default lane since 2026-11-20:
the OpenCode Go subscription's DeepSeek — see § "Provider lane"),
with all the stochasticity that implies. A single failing run is
signal, not proof; trends across versions are what matter.

## Layout

```
eval/
├── run.sh          # eval/run.sh [case] [binary]   (defaults: all cases, ./seek)
├── ablate.sh       # eval/ablate.sh [case] [binary] — bare-core vs full dual run (below)
├── lane-release.txt  # release lane case list (eval/run.sh --lane release; "发版前跑什么")
├── lane-core.txt     # nightly core lane case list (eval/run.sh --lane core; same section)
├── lane-anchor.txt   # 外部锚任务集车道（eval/run.sh --lane anchor; eval v3 A, below）
├── cases/
│   └── <name>/
│       ├── README.md   # what the case probes + the bug it came from
│       ├── prompt.txt  # exact text passed via `seek -p`
│       └── expect.json # PASS/FAIL bounds; see "Metric vocabulary" below
├── results/        # checked-in JSONL of each pass; one row per case
├── tools/          # shared case engines/scorers (mcp-arm-*, mcp-semble-score, mcp-first-gold-read, judge.py, anchor-runner.sh, …)
├── judge-calibration/  # J-line calibration fixtures + agreement reports (below)
├── anchor/         # cross-harness comparison artifacts (eval v3 A3; NEVER in results/, #2575 private)
├── ablation/       # dual-run lanes + comparison tables (NOT trend data)
├── transcript-baseline/  # offline transcript snapshots (see "transcript-stats 与口径")
└── fixtures/       # synthetic transcripts for scripts/transcript-stats --selftest
```

## Running

```bash
# Run every case against ./seek
eval/run.sh

# One specific case
eval/run.sh tool-args-hallucination

# Against a different binary (e.g. an older release for A/B comparison)
eval/run.sh tool-args-hallucination /tmp/seek-baseline

# N-round distribution sampling (see "Judgment" below)
eval/run.sh --rounds 5 tool-args-hallucination

# A curated case list instead of all cases (see "发版前跑什么" below)
eval/run.sh --lane release
```

The runner appends one row to `results/<UTC-date>-<binary-version>.jsonl`
per case it executed, e.g.:

```json
{"case":"tool-args-hallucination","pass":true,"metrics":{"unknown_field_errors":0,"turns":4},"prompt_tokens":12834,"completion_tokens":612,"elapsed_s":21}
```

> Rows recorded before 2026-11-22 also carry `think_calls` — the metric
> was retired with the `think` tool (card c_cs4dw6); historical results
> files keep it, new rows don't.

## Judgment: single sample vs N rounds (E1)

Historically every case ran **once** and that single sample was the
PASS/FAIL verdict — noise could flip either way (the v0.12.0 pre-release
run had 3 reds of which 2 were false alarms; see
[`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md) §1).
`--rounds N` (default 1 = historical behaviour, byte for byte) changes
the judgement to a **distribution**:

- Each round is a full case lifecycle (`setup.sh` → seek → `teardown.sh`)
  appending its own result row, carrying `round: k` (the field appears
  **only** when `--rounds > 1`, so single-round rows keep their exact
  historical shape for the trend readers).
- After the rounds, a **gate** case passes iff `passed_rounds / rounds >=
  min_pass_rate` — **inclusive**: 3/5 against 0.6 passes. The comparison
  runs on IEEE doubles, and division and decimal parsing round to the
  same double, so a written threshold that is exactly k/n (0.6, 0.75,
  0.8, 0.5) behaves as intended — but `0.67` is **not** 2/3 and will
  gate one round stricter than a naive "2 of 3" reading suggests.
  Calibration (E2) should pick k/n-representable thresholds.
- A `record_only: true` case is **never gated**: per-round rows and the
  summary line record it (`RECORD <case> (k/N rounds, record-only — not
  gated)`), it counts in a separate "recorded" column, and it cannot
  make the run red or green. This is the machine-enforced home for
  cases whose bounds only bound runaways or whose failures are not
  behavioral — four cases are marked (see the calibration table).
- **Crashed rounds** (seek died mid-flight, no `agent_end`) are simply
  failing rounds — their metrics come out near-zero and any bound like
  `exact_agent_ends` fails; the denominator always counts them. No
  separate error class yet (`max_error_rate` is an open question in the
  PRD §6, deliberately not invented here).
- Interruption safety is structural: each round's row is appended only
  after that round completes (one `echo`, one line), so Ctrl-C mid-run
  loses nothing already written and cannot produce half-line JSONL.

With `--rounds > 1` an **aggregate row** follows the round rows:

```json
{"case":"bash-overreach","type":"aggregate","rounds":5,"passed":4,"pass_rate":0.8,"min_pass_rate":0.67,"record_only":false,"verdict":"pass","ran_at":"…"}
```

It deliberately carries **no `pass` key**: `scripts/eval-trend.sh`
selects sample rows on `pass` being a boolean, so aggregate rows can
never double-count as samples — the trend keeps seeing one sample per
round, which is exactly the distribution data it wants.

`min_pass_rate` / `record_only` live in `expect.json` (not a separate
calibration table): expect.json is already the per-case judgement
authority — a second file would be a parallel source of truth for the
same case, plus a name-join to maintain (the drift risk the repo's
transcript-vs-state discipline exists to avoid). Both keys are
validated at case start; anything malformed (non-number, ≤ 0, > 1,
non-boolean, unparseable JSON) exits 2 before any round runs — never
silently coerced to 1.0/false.

The values are calibrated — see ["Calibration table (E2)"](#calibration-table-e2-2026-11-21)
below ([`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md) §三 E2;
landed 2026-11-21, card c_347nnh).

### Judgment self-test (deterministic, no key)

`scripts/eval-rounds-test.sh` (auto-discovered by the
[`scripts/selftest.sh`](../scripts/selftest.sh) lane, name
`eval-rounds-test`) pins the judgement mechanics with an
invocation-counting fake binary: explicit `--rounds 1` is byte-identical
to the no-flag run (the no-flag-vs-pre-E1 equivalence stays pinned by
`scripts/eval-ablate-test.sh`'s git-history blob compare); a 5-round run
writes 5 round-tagged rows whose crashed rounds judge red **and stay in
the denominator**; the inclusive boundary is pinned from both sides
(3/5 vs 0.6 → green, 3/5 vs 0.7 → red) and re-pinned on the `--rounds=3`
form (2/3 vs 0.66 → green, vs 0.67 → red — the "0.67 is not 2/3"
lesson, testable); record-only never colours the exit code at either
round count; malformed `--rounds` / `min_pass_rate` / `record_only` /
broken JSON exit 2 with zero rows written; and a round-bearing results
dir fed through `eval-trend.sh` counts the round rows only (aggregate
rows skip, proving no double-count).

## Calibration table (E2, 2026-11-21)

One row per existing case under `eval/cases/` — **30 cases** at E2
time. The PRD's "21 例" predates the nine `mcp-semble-routing-*` arms
added 2026-11-17/18 by the MCP D-line (21 + 9 = 30); E5's
`serve-web-stream` (2026-11-21) makes it 31 — its row lives in the
load-bearing table below, not in this history table (it has no
pre-landing history by construction). Three cases added 2026-11-22
alongside the shell-as-search metrics (`grep-affordance`,
`parallel-fanout`, `references-trigger`) make it 34; all three are
rows in this table from their first record, with the baseline noted in
the history column rather than reconstructed. `compact-ab`
(2026-11-25, card c_qjb5mq) makes it 35 — record_only from day one,
because the fake-key lane cannot carry an LLM judge for a case whose
"model" is this repo's own scripted endpoint. `grep-affordance` is the
one that has already moved: 1/5 before the grep-tool extension, 5/5
after, same bounds both sides (case README). History columns are
computed from
the 372 committed sample rows in `results/` — all-time, plus the window
since each case's last bound/prompt repair (all-time is contaminated
wherever a repair landed mid-history; the window is the honest basis).

**Rounds policy** ([PRD §2.5](../docs/prd/feature-eval-v2.md), pinned by
card c_347nnh): **5 rounds only for known-flaky cases and the
load-bearing axes; every other case 3 rounds.** (PRD §五's "cheap read
cases may get 5, axes 3" contradicts §2.5; the card resolved to §2.5.)
These counts are **lane policy** for the release/nightly lanes (E6) —
the runner itself takes one global `--rounds`.

**Threshold rule**: gate values are decimals that are exactly k/n at
the chosen round count (the 0.5 / 0.6 / 0.75 / 0.8 menu — E1's
inclusive `>=` on doubles); an absent `min_pass_rate` is the default
1.0 (zero tolerance). **Tolerance principle**: a case tolerates only
the failure mass its history attributes to *non-regression noise*
(environment blocks, one-off exploration variance); a clean recent
window keeps the strict gate — a red there is new signal, which is the
point of having a gate.

| case | history (all-time → window) | expected | rounds | gate | record-only |
|---|---|---|---|---|---|
| bash-overreach | 4/15 → post-recal study: 5/5 replay + 1/6 live true-overreach | ~0.83 | 5 | **0.8** | no |
| code-review-effort-low | 11/12 → 9/9 | ~1.0 | 5 | — | **yes** |
| code-review-effort-max | 6/14 → 3/11 under the old 6000 ceiling | ~0.9 | 5 | — | **yes** |
| compact-ab | 首个记录 2026-11-25（record-only，机械断言 3 条） | ~1.0 | 3 | — | **yes** |
| edit-unicode-read-first | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| git-multi-query-no-bash | 23/31 → 10/10 | 1.0 | 3 | 1.0 | no |
| git-subcommand-midtask | 23/23 → 9/9 | 1.0 | 3 | 1.0 | no |
| git-subcommand-shape | 24/25 → 9/9 (lone red 2026-08-18) | 1.0 | 3 | 1.0 | no |
| glm-lane-contract | 1/7 → env-bimodal | env | 5 | — | **yes** |
| grep-affordance | 1/5 pre-extension → 5/5 post (A/B 2026-11-22, case README) | ~0.2 → 1.0 | 5 | **0.6** | no |
| grep-refine-not-flood | 8/11 → 7/10 | ~0.7 | 5 | **0.6** | no |
| instruction-following | 11/11 → 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a1-mixed | 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a1-nl | 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a1-symbol | 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a2-mixed | 5/6 (one transient API crash) | ~0.9 | 3 | **0.6** | no |
| mcp-semble-routing-a2-nl | 5/5 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a2-symbol | 5/5 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a3-mixed | 5/5 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a3-nl | 0/5 — by design (see below) | ~0 | 3 | — | **yes** |
| mcp-semble-routing-a3-symbol | 5/5 | 1.0 | 3 | 1.0 | no |
| parallel-fanout | 3/3 (first record 2026-11-22) | 1.0 | 3 | **0.6** | no |
| print-deny-semantics | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| read-eof-blind | 14/19 → 9/9 (red mass pre 2026-11-13) | 1.0 | 3 | 1.0 | no |
| read-tail-fact | 17/19 → 9/9 | 1.0 | 3 | 1.0 | no |
| read-whole-file | 14/19 → 9/9 | 1.0 | 3 | 1.0 | no |
| references-trigger | 3/3 real calls — first observation 2026-11-22 | unmeasured (baseline: 0/6,474) | 3 | — | **yes** |
| redundant-tool-calls | 12/12 → 10/10 | 1.0 | 3 | 1.0 | no |
| session-id-contract | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| tool-args-hallucination | 10/10 → 9/9, then **rewritten 2026-11-22** (subject moved off the retired `think` tool to a multi-tool audit run; binding `min_total_tool_calls: 5` so it can't pass vacuously) | 1.0 | 3 | 1.0 | no |
| tool-selection | 10/14 → 8/8 since the 11-13 repair | 1.0 | 3 | 1.0 | no |
| write-elided-edit-fallback | 9/10 (one unexplained red 11-16) | ~0.9 | 3 | **0.6** | no |
| write-guard-fallback | 19/19 → 9/9 | 1.0 | 3 | 1.0 | no |
| yolo-allows | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |

Gate semantics at the chosen round counts: `1.0` = 3/3 (or 5/5), zero
tolerance; `0.6 @ 3` = 2/3 (tolerate 1 red); `0.6 @ 5` = 3/5 (tolerate
2); `0.8 @ 5` = 4/5 (tolerate 1). The `expect.json` files carry exactly
these values (`min_pass_rate` written only where ≠ 1.0; `record_only:
true` on the five marked rows); expect.json remains the per-case
judgement authority — this table is the calibration record, not a
second source the runner reads.

### Known-flaky dispositions

The three named in the PRD's triggering incident, plus two the history
forced:

1. **`bash-overreach` — boundary set, stays gated** (`min_pass_rate
   0.8` @ 5 rounds). Post-recalibration studies (case README,
   2026-11-18): all 5 saved streams pass the ordering gate; true
   overreach ("opens with bash, never calls `list_dir`") occurs
   ~1-in-6 — both 2026-11-19 committed rows are exactly that shape and
   judge red (`bash_before_dedicated` 1 and 2 vs the 0 bound). 0.8 =
   4/5 tolerates precisely that noise; ≥2 red rounds (persistent
   overreach) fail the gate. Constructed-row verification (2026-11-21,
   fake-binary sandbox — one-off, not a checked-in mechanism): 1
   overreach round in 5 → `PASS bash-overreach aggregate (4/5 rounds >=
   min_pass_rate 0.8)`; 2 in 5 → `FAIL … (3/5 rounds < min_pass_rate
   0.8)`; a single no-flag overreach round → red row, exit 1.
2. **`code-review-effort-low` / `-max` — record_only** (the PRD §2.1-3
   assignment, acceptance ③). Their bounds are runaway ceilings only —
   the effort signal is swamped by run-to-run variance (case READMEs);
   the recorded distribution is the product, so they run 5 rounds but
   never colour the exit code.
3. **`glm-lane-contract` — record_only (environment)**. Failures are
   lane-environment, not behaviour: the GLM plan's 5-hour quota 429s
   block a whole window at once (2026-11-19: 6/7 committed rows red,
   `nonzero_exits=5`, zero `agent_ends`) — a blocked window is 100%
   red, no threshold separates that from regression. Re-gating is an
   E6 decision (run it inside a quota window with a real key; PRD
   §六.2).
4. **`mcp-semble-routing-a3-nl` — record_only (deterministic
   measurement red)**: 0/5 by design — `min_mcp_calls: 1` IS the
   §5.4① judgment instrument (does `mcp_prompt` guidance route nl
   probes to MCP? today: no — three-arm report 2026-11-18). Gating
   would be permanently red until the guidance changes; the go/no-go
   belongs to the MCP D-line (D5), which reads recorded rows either
   way.
5. **`compact-ab` — record_only (new case, 2026-11-25)**. Its judgement
   is mechanical (`runner_fact` assertions: does each arm's answer carry
   the golden facts, and is arm B really the summary pair), so there is
   nothing for a judge to add on the fake-key lane; gating waits for
   the E2 calibration of a real-key lane or an explicit E6 decision.
   The negative leg is checked in as `scripts/eval-compact-ab-test.sh`,
   not as a gate.
6. **`grep-refine-not-flood` — promoted to the flaky class**
   (`min_pass_rate 0.6` @ 5): 7/10 in the current window and every red
   is the same shape (7–12 total tool calls vs the 6 bound —
   exploration variance), not drift. Bounds stay first-pass; a
   tightening pass may re-strict the gate.

Two gated cases carry tolerate-1 for observed one-off noise:
`mcp-semble-routing-a2-mixed` (`0.6 @ 3`; its one red is a documented
transient API EOF crash — crash rounds are red rounds by doctrine, but
a single transient should not colour a 3-round gate) and
`write-elided-edit-fallback` (`0.6 @ 3`; one unexplained red 2026-11-16
in a 9/10 window).

### Load-bearing axes (E3 placeholder; E4/E5 backfilled 2026-11-21)

| planned case | rounds | gate / baseline |
|---|---|---|
| `plan-e2e` (E3) | 5 | **case 已落地**（c_c5wwey，2026-11-21）：`record_only: true` 挂账至 E2 标定 / E6 release lane 接管（真二进制 real-key baseline 未测——无人值守车道假 key 纪律，且三态实跑入口 `eval/run.sh` 不在验证车道白名单、默认写正式 `eval/results`）；机制面由 `scripts/eval-e3-case-test.sh` 假二进制钉死（真 runner/drive/endpoint/judge 全参与、只换二进制：全绿行 + 四种具名红 + record-only + rounds 聚合兼容）。首跑已抓出 resume 折叠真 bug（落盘 tool 消息无 name 字段） |
| `subagent-bg-resume` (E4) | 5 | **case 已落地**（c_vollxv，2026-11-21）：gate 先取 **0.6**（PRD §六.3 的 doctrine 下限——承重轴实测 < 0.6 时修产品提示而非放界）；**real-key baseline 未测**（无人值守车道假 key 纪律），机制面由 `scripts/eval-e4-case-test.sh` 假二进制钉死（全绿行 + 五种具名红 + record-only + rounds 聚合兼容）。已知噪声源：run 1 的「子代须先于父进程退出跑完」竞态（无 agent 侧 wait 原语），标定后若实测通过率显著低于预期，先核该形态再动界 |
| `serve-web-stream` (E5) | 5 | **已回填**（c_lbtdqw，2026-11-21，见下） |

Rounds reserved per §2.5 (承重轴); the gate values are those cards' to
calibrate against their own baselines (PRD §六.3: if a load-bearing
case measures < 0.6, the fix belongs in the product's prompts, not the
threshold). E2 neither ran nor pre-gated them; E4's 0.6 is the doctrine
floor pending its own real-key baseline (a release-lane,
human/main-session action per the provider-lane rule).

**`serve-web-stream` baseline (E5, c_lbtdqw)**: landed with the case —
[`eval/cases/serve-web-stream/`](cases/serve-web-stream/)（README 记
判分口径与代码锚点）。Fake-key lane（卡面硬规定：无人值守假 key，本地
`eval/tools/fake_openai_compat.py` 两段 delta）实测 3/3 绿（1 单轮 +
2 rounds 聚合，`eval/results/2026-11-21-v0.12.2-…jsonl`）+ 六轴构造行
负验证（换序/帧序倒置/sid 未命中/收口缺 session_id/崩溃形各自单独翻红）。
**Gate `1.0`**：假 key 车道全链确定性（端点固定两段 delta、serve 排干契约
0），红即真回归——W5 组再动 `webfrontend_stream/conv/rpc` 任何一处即翻红，
这正是把 serve/web 平面纳入保护网的目的（PRD §1.2 零覆盖清单）。**真 key
口径留待 E6 发版道**：同 runner 不清空车道凭证、不设 GLM 假端点即走真车道
（env-in/env-out 同一接缝），届时按 §2.5 在 5 轮上重标定（真流多 delta 多
turn，`min_sse_delta_frames: 2` 照常成立；PRD §六.3 的「< 0.6 修产品提示
不修阈值」规则届时适用）。轮数随 E6 发版道名单定（本卡单轮/多轮均已验通）。

**Live re-measurement**: the expected-pass column is history-derived;
real-key rounds under this policy (5-round re-runs of the flaky set,
3-round lanes) are a human/main-session action per the provider-lane
rule below — this calibration used committed rows plus constructed-row
verification only.

## 不由 eval 保证（E7，2026-11-24，c_okmkfl）

`docs/prd/feature-eval-v2.md` E7 的交付面：eval 的判分面只有一件事——
**模型在给定 schema/prompt 下的选择倾向**（v2 §1.1 第三层）。下列五类行为面
**不在判分覆盖内**，每条检查在这里有且只有一个归属，三态闭集：

- **已有测试** — 写明测试名与所在车道（`go test -race` CI 三 OS /
  `e2e.yml` 每日真 key / `bash scripts/selftest.sh <name>` 验证车道 /
  人工无头脚本）。
- **需补** — 真缺口，收在文末「待开卡清单」；**E7 卡内不顺手补**
  （v2 §三 E7 验收原文）。
- **接受不保证** — 写明为什么：测不了 / 测了必 flaky / 已有替代观测面。

判据（v2 §2.4）：一条检查若能在**没有模型参与**时断言，就不该出现在 eval 里。

### C1 确定性契约（无模型参与可断言）

v2 §2.4 点名的五项「移回 Go 单测」清单，逐项归属：

| 检查 | 归属 | 证据 / 理由 |
|---|---|---|
| `-tools` 白名单冻结 + 未知名 exit 2 | 已有测试 | `cmd/seek/tools_flag_test.go`：`TestToolsFlagE2E_WhitelistFiveToolsDeterministic`、`TestToolsFlagE2E_UnknownNameExits2`、`TestApplyToolsWhitelist_UnknownNameErrToolsUsage`、`TestApplyToolsWhitelist_ResumeWarnsOnChangedSet`（`go test -race`，CI 三 OS） |
| ACP `tool_call` 帧形态 + thinking chunk | 已有测试 | `cmd/seek/acp_test.go`：`TestACPUpdate_Mapping`（reasoning / streaming CoT / streaming answer delta 三者都落 `agent_thought_chunk`；tool_call kind/locations/content）、`TestACPUpdate_ToolKind`、`TestACPUpdate_Locations`、`TestACPUpdate_Content`；全链在 `cmd/seek/acp_*_e2e_test.go`（审批/模式/多会话） |
| MCP `server__tool` 前缀 + 撞名回退 | 已有测试 | `internal/tools/mcptool/bridge_test.go`：`TestLoadServers_PrefixPolicy`（默认 `srv__tool`；`NoPrefix` 撞内置名回退成 `<server>__<tool>`）、`TestLoadServers_NamePrefix`、`TestLoadServers_DefaultPrefixIsUnfolded`、`TestSafeToolName`、`TestLoadServers_DeterministicRoster` |
| session JSONL 头 / `schema_version` / repair | 已有测试 | `internal/session/session_test.go`：`TestSaveLoad_Roundtrip`、`TestRepair_DropsTrailingOrphanToolCalls`、`TestRepair_PartialMultiCallStillCountsAsOrphan`、`TestRepair_BackfillsEmptyToolContent`、`TestRepair_LeavesValidHistoryAlone`；读路径 `internal/session/readpath_test.go`：`TestReadMessagesPageTornTail`、`TestReadMessagesPageCorruptLine`。**注**：legacy `.json`（schema_version ≤ 1）迁移已随 `65a31d50` 删除（`Load`/`LoadFrom` 只开 `.jsonl`），故无迁移测试可写；AGENTS.md 同一句仍称「migrated on next Save」且写 `schema_version=2`（代码 `CurrentSchemaVersion = 3`），两处漂移见待开卡 #2 |
| 工具输出 write-time 上限（schema 参数不得绕过） | 已有测试 | `read`：`TestRead_LimitParamValidation` / `TestRead_DefaultLimitTruncatesLargeFile` / `TestRead_ByteCapElidesLongLines`；`grep`：`TestGrep_DefaultMaxMatchesCapsAt20` / `TestGrep_HardBytesCap`；`bash`：`TestClampOutput_UnderBudgetIsUntouched` / `TestClampOutput_ExactlyAtBudget`；`git`：`TestExecute_DefaultLineCapIs100`；`webfetch`：`TestExecute_RejectsMaxBytesAboveCap`；`references`：`TestRefs_OutputCap` |

### C2 真 API / 外部服务

| 检查 | 归属 | 证据 / 理由 |
|---|---|---|
| prefix cache 真命中（字节稳定性是约定，只有真端点能证） | 已有测试 | `pkg/agent/cache_e2e_test.go` `TestPrefixCache_RealAPI`；`.github/workflows/e2e.yml` 每日 00:17Z 真 key 跑 `go test ./... -run RealAPI`，且 preflight 必须见到 `--- PASS: TestPrefixCache_RealAPI`——缺 key 判红，不静默跳过 |
| Files 上传/引用/resume/cache；ASR/TTS | 已有测试 | `pkg/deepseek/files_smoke_test.go` `TestFiles_RealAPI_UploadReferenceResumeCache`、`TestASR_RealAPIRoundtrip`、`TestTTS_RealAPIStella100`（同 e2e 车道，key-gated，`-short` 跳过） |
| 第二梯队 provider 的 wire 形状（Anthropic / OpenAI / Gemini / GLM-compatible） | 已有测试 | `pkg/llm/provider/{anthropic,openai,gemini}/client_test.go`（httptest 假端点）+ `scripts/eval-provider-test.sh`（车道选择/凭证清洗机制，假端点） |
| 第二梯队车道上的**模型行为** | 接受不保证 | eval 只测当前配置车道的选择倾向；跨车道取样成本与额度不可控（`glm-lane-contract` 仅 record-only 观测，E2 处置）。车道 wire 契约由上一行负责 |
| 环境类失败（429、额度窗、登录过期） | 接受不保证 | 环境不是被测对象：`glm-lane-contract` 整窗 429 时阈值无解，record-only（E2 known-flaky 处置）；判红只会训练人忽略红。token 规则见 §Provider lane（harness 永不读/刷 `opencode-auth.json`） |
| 答案质量（`final_text` 对不对） | 已有测试（v3 J，分工明确的有限面） | `scripts/eval-judge-test.sh` 钉接线（record 不染色 / gate 翻红 / doubt 两模式不染色 / 畸形 exit 2 / kill-switch）；judge 与人判 agreement 0.88、false-pass 0（`eval/judge-calibration/report.md`）。余量照 v3 §2.1：judge 只对校准过的分布负责，rubric 外仍是盲区；gate 已于 2026-11-24 开启（用户拍板，卡 c_xhbqae——两例 `judge.mode=gate`，`SEEK_EVAL_JUDGE=0` 一行关回） |

### C3 并发与竞态

| 检查 | 归属 | 证据 / 理由 |
|---|---|---|
| 数据竞争 / 并发正确性 | 已有测试 | CI 三 OS `go test -race ./...`；点例：`TestACPBackend_MultiSession_ConcurrentPromptsNoCrossTalk`（acp）、`TestPolicy_ConcurrentAskAndSetMode`（askuser）、`TestConcurrentExecute`（plan）、`TestTracker_ConcurrentSafe` / `TestTracker_AdoptChild_ConcurrentCumulativeDoesNotDeadlock`（cache）、`TestConcurrentWrites`（sessmeta）、`TestSessionLookupConcurrent`（servecli）、`TestWriteGuarded_ExclusiveCreateRefusesRaceWinner`（write） |
| eval 自身多进程同写 `results/*.jsonl` | **需补** → 待开卡 #1 | v2 §四「并发」明列「两个进程各写 100 行验证」；`scripts/eval-rounds-test.sh` 未实现该断言。`run.sh` 目前只靠单行 `echo "$row" >> "$results_file"`（1078 / 1249 行）的 O_APPEND 约定，无钉死测试 |
| Ctrl-C 中途中断不产生半行 JSONL | 接受不保证（结构替代信号注入） | 已完成轮次的落盘由结构保证：每轮完成后单行 `echo` 追加。`scripts/eval-rounds-test.sh` 头注释 34–36 行显式记录「信号注入测试故意不做；若未来改行写入方式，先回来补」——这是计划过的取舍，不是缺口 |
| 崩溃轮（无 `agent_end`）计入分母 | 已有测试 | `scripts/eval-rounds-test.sh` ②（前 3 轮好、后 2 轮崩溃 → 3/5 对 0.6 绿、对 0.7 红）与 @rounds=3 边界；口径见 §Judgment |

### C4 平台与前端差异

| 检查 | 归属 | 证据 / 理由 |
|---|---|---|
| OS / 架构差异（路径、换行、子进程） | 已有测试 | `ci.yml` matrix 三 OS 跑 `go vet` + `go test -race` + `go build`；`cross-build` 六目标（linux/darwin/windows × amd64/arm64） |
| eval 结果行的平台/车道归属 | 接受不保证 | 行上没有 OS / model / lane 字段（字段集见 `run.sh` 行构造），eval 单机采样。被测面是模型选择倾向，与 OS 无关；OS 敏感层归 C1 与 CI 三平台。跨车道成本只能读比值（§Provider lane 已记） |
| TUI 视图 / 键位 / 状态机 | 已有测试 | `internal/tui/*_test.go`（含 `approval_test.go`、`mode_test.go`、`plan_render_test.go`、`render_test.go`） |
| TUI 其余真终端交互（ANSI / 宽度 / 粘贴 / resize） | 接受不保证 | Go 侧无 pty 依赖（stdlib-first），自动化 PTY 仅 E3 `eval/cases/plan-e2e` 的审批链一处（Python `eval/tools/plan-e2e-drive.py`）；其余靠人工/合并处验收 |
| Web 面板交互（会话、流、审批、移动、ctxmeter…） | 已有测试（人工无头车道） | `scripts/web-*-manual.sh` + `*-headless.mjs`（真实 CDP 驱动 headless Chrome，断言走真实 UI 路径）；不进 CI 是零构建链 doctrine（`scripts/web-gate.sh` 只钉「仓根无 node 构建链」+ 二进制体积） |
| Web 记忆面板视觉呈现 | 接受不保证 | 无可脚本化载体（无对应 `*-headless.mjs`），`scripts/selftest.sh` 的 `web-memory-manual` 条目已记结构性（c_gqo32q / c_kwiajf 判据） |
| serve / RPC 平面 | 已有测试 | `internal/rpc/registry_test.go`、`internal/servecli/*_test.go`、`scripts/serve-smoke-test.sh`；**唯一进了 eval 的 web 面**是 `serve-web-stream`（E5，假 key 全链 + 六轴构造行负验证） |

### C5 性能、延迟与成本

| 检查 | 归属 | 证据 / 理由 |
|---|---|---|
| 运行延迟 / 吞吐 | 接受不保证 | 全仓无延迟断言：`elapsed_s` 只落行观测（eval 行 / ablate 表），`scripts/weight.sh` 量的是代码与文档体量，不是运行时。理由：wall-clock 由 provider 排队与本机负载决定，历史行同例跨度可达数倍（`code-review-effort-max` 25–96s、`code-review-effort-low` 14–70s、`git-subcommand-shape` 4–43s），闸门必 flaky；趋势面归 `scripts/eval-trend.sh` |
| 成本 / token 效率 | 接受不保证（观测面齐） | eval 行自带 `prompt_tokens` / `completion_tokens` / `cache_hit_tokens`；ablate 表出 F/B 比；`seek -benchmark self-hosting` 有 cache 命中率阈值报告，但真跑是人工真 key 动作，阈值逻辑由 `cmd/seek/benchmark_test.go` 钉死。无闸门 = 有意（同一原因：阈值随模型/车道/机器漂移） |
| 二进制体积 | 已有测试 | `scripts/web-gate.sh`（+200KB 对 pin，合并三闸之一）+ CI `web-gate` job |
| 代码 / 文档体量（减法尺度） | 已有测试 | `scripts/verify-doc-budgets.sh`（AGENTS.md 字面硬闸，CI `doc-budgets` job）+ `scripts/weight.sh`（软建议，`docs/budget.json` 只读、永不进 CI gate） |

### 待开卡清单（E7 发现的真缺口；不在本卡补）

| # | 建议标题 | 为什么是真缺口 | 证据 |
|---|---|---|---|
| 1 | eval 并发写结果行自测：PRD §四 计划项未实现 | v2 §四把「两个进程各写 100 行」写成测试计划，仓库里没有对应断言；当前原子性只靠单行 `echo >>` 的 O_APPEND 约定，改写入方式即无保护 | `docs/prd/feature-eval-v2.md` §四「并发」；`scripts/eval-rounds-test.sh` 无并发场景；`eval/run.sh:1078,1249` |
| 2 | AGENTS.md 会话存储句校正：legacy `.json` 迁移已删 + `schema_version` 写的是 2 | 同一句两处与代码不符：①「Legacy `.json` files (schema_version ≤ 1) are migrated on next `Save`」——`65a31d50` 之后 `Load`/`LoadFrom` 只开 `.jsonl`，无迁移路径；②写 `schema_version=2`，代码是 `CurrentSchemaVersion = 3`。照文档写测试会测到不存在的路径 | `git show 65a31d50`（"seek hasn't shipped, so there are no schema-v1 single-file JSON sessions in the wild"）；`internal/session/session.go` 的 `Load` / `LoadFrom` / `CurrentSchemaVersion`；`AGENTS.md:94` |
| 3 | E3 / E4 承重轴的 release lane 成员资格定案 | 两例 2026-11-21 均已落地（`plan-e2e` record_only、`subagent-bg-resume` gate 0.6），`lane-release.txt` 仍是注释占位、注释里的卡号还是旧的，README lane 表仍写「待 E3/E4 落地后取消注释」——E6 验收「release lane 只含承重轴 3 例」实际只有 1 例在册，归属无人 | `eval/lane-release.txt:19-21`；`eval/cases/plan-e2e/expect.json`（注「E6 release lane 接管后转正」）；`eval/cases/subagent-bg-resume/expect.json`；本文 §发版前跑什么 的 lane 表 |

## 发版前跑什么（lanes, E6）

E6（[`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md)
§2.2/§2.3、card c_rkoeiy）把两条观测道落成 **lane 名单**：`eval/run.sh
--lane <name>` 跑 `eval/lane-<name>.txt` 里的用例集（一行一名，`#` 注释
与空行忽略；名单出现不存在的用例、`--lane` 与显式 `<case-name>` 同给、
名单无有效条目——都启动 exit 2 零行写入）。名单文件就是车道策略：E2
标定表的轮数/闸门值是 expect.json 的事实，**谁进哪条道**是名单文件的
事实，两者都受审进 git。机制由 `scripts/eval-lane-test.sh` 钉死（无
`--lane` 输出与 E6 前 run.sh 字节等价——git 历史 blob 对照；过滤/顺序/
畸形 exit 2）。

| lane | 名单 | 用途 |
|---|---|---|
| `release` | 承重轴（`serve-web-stream` 已在；`plan-e2e`/`subagent-bg-resume` 注释占位待 E3/E4 落地后取消注释）+ E2 标定表判稳定例（gate 1.0 的 22 例；已知 flaky 与 record-only 刻意不进） | 发版前人工闸门 |
| `core` | 低成本子集 8 例（便宜单发用例 + 零成本假 key 的 `serve-web-stream`；无 MCP fixture、无整仓 review） | 夜间观测（CI schedule，`--rounds 2`） |

**发版命令**（在 main、干净树）：

```bash
go build -o seek ./cmd/seek
eval/run.sh --lane release --rounds 5
```

- 稳定例按 §2.5 标定在 3 轮（gate 1.0 = 3/3）；发版道统一 5 轮是
  PRD §五成本表的形状（$1.5–4/次），且是**收紧**不是放松——同一个
  1.0 闸门在 5 轮上要求 5/5。
- **判读口径**：任何 FAIL 行先看其 failures 列的 metric 与该例
  README；稳定例单轮红按 E2 容差原则是**新信号**而非噪声——单发复跑
  该例 `eval/run.sh --rounds 3 <case>` 区分偶发与持续，持续红 = 阻塞
  发版。record-only 与已知 flaky 例不在 release 名单：它们的观测归
  日常 trend（`scripts/eval-trend.sh`），不归发版闸门。
- **真 key 口径**（`serve-web-stream` 的 E5 留白在此收口）：发版道同
  runner 不清空车道凭证、不设假端点即走真车道；届时按 §2.5 在 5 轮
  上重标定（PRD §六.3「< 0.6 修产品提示不修阈值」适用）。

**夜间入口**（只观测、不阻塞 PR）：`.github/workflows/ci.yml` 的
`eval-nightly` job 仅在 `schedule` 触发时运行（PR / workflow_dispatch
结构性地跑不到它——ci.yml 的 keyless PR 面不动），执行 `--lane core
--rounds 2`，行落 actions cache 持久化的独立目录（不进 `eval/results/`、
不回写 git；cache 逐出会重置计数，方向保守）。失败策略**复用**
`scripts/eval-trend.sh` 的既有 streak 实现（`streak_failures` 字段 =
从最新行往回数的连续红行数，本卡零新计数代码）：core 名单内任一用例
streak ≥ 3 才把 job 判红——夜间每例每晚写 2 行，即第二个连续整红夜过
阈；中间任一绿行即重置。key 走 `secrets.OPENCODE_API_KEY`（OpenCode
订阅车道：无人值守不烧计量 key，[`docs/prd/v8.md`](../docs/prd/v8.md)
§9.1；secret 未配则告警并跳过当晚——观察者不是安全网，缺 key 静默绿
无害，但跑出全红行是纯噪声）。trend 报告按 PRD §2.3 以 artifact 上传
（留 30 天）。

## Isolation: the SEEK_HOME sandbox

Every seek invocation run.sh launches — default lane and `runner.sh`
cases alike — inherits a **pinned `SEEK_HOME`**:

- caller sets a non-empty `SEEK_HOME` → respected verbatim (an outer
  sandbox redirecting the redirect);
- otherwise → forced to `eval/tmp/seek-home` (gitignored), and an
  inherited `SEEK_SESSIONS_DIR` is stripped — it ranks ABOVE
  `SEEK_HOME` in `session.NewStore`'s resolution ladder and would
  silently bypass the sandbox for sessions.

seek resolves all user-data writes (sessions, `projects/`,
dream-state, cron, skill stats, upgrade-check) through
`paths.Home()` = `$SEEK_HOME`-or-`~/.seek`, so an eval run
**cannot** write the real `~/.seek` — not even a run whose API call
fails instantly (a fake-key probe still wrote four paths there before
this pin landed; card c_clip4i). Deliberate side effect: evals no
longer inherit the running machine's user skills / memory /
mcp.json — measurements are hermetic and comparable across machines.

The API key does NOT live in the sandbox: `internal/config` resolves
env var first (`DEEPSEEK_API_KEY` > `$SEEK_HOME/config.json`), so a
key that used to sit in `~/.seek/config.json` must be exported for
eval runs once SEEK_HOME is pinned.

Who may burn a real key at all: unattended lanes (batch cards,
resident loops, steward sweeps) run **fake-key by rule** — a real key
is only for a human-named `--only` run or a card that explicitly
authorizes it ([`docs/prd/v8.md`](../docs/prd/v8.md) §9.1). The
pinning mechanism AND the zero-real-HOME guarantee are held by
`scripts/eval-home-isolation-test.sh` (selftest lane
`eval-home-isolation-test`: mechanism asserts via a fake binary +
a real-binary canary-HOME A/B whose control arm proves the canary
catches writes in the first place). That real-binary arm pins the
provider lane to `SEEK_EVAL_PROVIDER=deepseek` on purpose (card
c_f734of): the default lane scrubs `DEEPSEEK_API_KEY` from the child,
so a runner that carries no lane credentials dies at the setup wizard
and writes **nothing** — indistinguishable, at the assertion, from
"the sandbox redirect broke". The `SEEK_HOME` pin itself sits above
the lane block and is lane-independent.

## Metric vocabulary (extracted by `run.sh` from the JSONL stream)

| Metric | What it counts |
|---|---|
| `unknown_field_errors` | `tool_end` events whose `error` contains `unknown field` |
| `total_tool_calls` | every `tool_start` event |
| `read_calls` | `tool_start` events with `name=read` |
| `grep_calls` | `tool_start` events with `name=grep` |
| `references_calls` | `tool_start` events with `name=references` — the semantic LSP lane (`internal/tools/references`, gopls / pyright / typescript-language-server). Landed 2026-11-21 with the 40-session baseline that motivated it: 0 calls across 6,474 events while `bash_grep_calls` stood at ~2,100 — the tool was capable and simply never got a trigger |
| `mcp_calls` | `tool_start` events whose tool name contains the `__` infix — MCP bridge tools register as `<server>__<tool>` (D1 default prefix, `internal/tools/mcptool/bridge.go`); no built-in name contains `__`. Reads 0 before D1 landed (the bridge prefixed only on conflicts then), even when MCP tools are used — the metric assumes D1's naming (`docs/prd/feature-mcp-client.md` §5.2) |
| `list_dir_calls` | `tool_start` events with `name=list_dir` |
| `bash_chains` | `tool_start` events for `bash` whose raw args contain `;` or `&&` — the multi-command-chaining anti-pattern. `;` isn't even a separator on cmd.exe (Windows), so a POSIX-style chain fails wholesale; `&&` is legal on cmd but still the anti-pattern (target shape: one command per call, or parallel dedicated-tool calls). `|` deliberately NOT counted — pipelines and grep alternations are legitimate |
| `bash_git_calls` | `tool_start` events for `bash` whose raw args contain a `git` invocation (`\bgit\b`) — read-only git belongs in the dedicated git tool (whitelist + auto-fix); mutating git (commit/push) legitimately lives in bash, so bind this metric only in read-only prompts |
| `bash_grep_calls` | `tool_start` events for `bash` that run `grep`/`rg`/`ag` in COMMAND POSITION (command start, or after `;`/`&&`/`\|`/newline/`(`, optionally behind env assignments and `command`/`exec`) — shell used as the search tool. Deliberately not the looser whitespace-delimited form `bash_git_calls` uses: on the 40-session baseline (5,015 bash calls) the loose form adds 19 hits and all 19 are false positives (`git grep`, heredoc bodies in `git commit -F -`, Python inside a heredoc), while command position misses 7 (`do grep` / `case` bodies). Baseline value 2,095 (41.8% of bash). Read it against `grep_calls`, not alone: 51% of those commands were real multi-command chains the grep tool cannot express, so this is a floor on shell-as-search, not a count of avoidable calls |
| `bash_cd_prefix_calls` | `tool_start` events for `bash` whose command STARTS with `cd ` — the state-free-shell tax. The bash tool already runs from the project root, so a leading `cd <root>` is noise and a leading `cd /elsewhere` is the model rebuilding "where am I" every call. Baseline 926/4,980 (18.6%), of which 322 (6.4%) cd'd into the session's own cwd. Middle-of-chain `cd /tmp && …` does not count |
| `parallel_turns` | `turn_end` events whose response issued ≥2 tool calls in one turn — the parallel read-only batch (`pkg/agent` dispatches `ReadOnlyTool` calls concurrently). Baseline: 36.0% of tool-bearing turns, but 98% of those were exactly 2 and only 0.2% were ≥3. Counts the ≥2 population so a case can bind `min_parallel_turns` |
| `bash_before_dedicated` | `bash` tool_starts occurring BEFORE the first `grep`/`read`/`list_dir` tool_start — bash as FIRST resort. Ordering is the honest anti-pattern signal when a case's task legitimately forces shell recovery later (e.g. `read` elided an over-1024-byte line so verbatim text needs awk): a total `bash_calls` bound cannot tell recovery from overreach, this can. A run with no dedicated call at all counts every bash call (`list_dir` joined the set 2026-11-18 — adding a tool can only lower the metric, so no existing bound tightens) |
| `git_calls` | `tool_start` events with `name=git` |
| `git_subcommand_dupes` | `tool_end` events for `git` whose result contains `repeated from args` — the model repeated the subcommand as `args[0]` and the tool auto-fixed it; counts the construction mistake at zero behavioral cost (see `eval/cases/git-subcommand-shape/README.md`) |
| `probe_reads` | `tool_end` events for `read` whose result contains `0 lines emitted` **and** `from line` — a read past EOF that returned nothing, i.e. the model was probing because it couldn't tell whether more pages existed (see `docs/test-plan-read-tool.md` §3.2) |
| `write_refusals` | `tool_end` events for `write` whose error contains `write refused` — the fsobserve blind-overwrite guard refusing (see `internal/fsobserve/fsobserve.go` `Explain`) |
| `write_ok` | `tool_end` events for `write` with no error — the in-CWD write default-allow path (writes inside CWD aren't "dangerous", so even print-mode `PrefDeny` lets them through) |
| `denied_tool_ends` | `tool_end` events whose `error` contains `permission denied` — `internal/permission.ErrDenied` surfaced as a tool *result*, never a fatal error (the run continues and exits 0) |
| `agent_ends` | `agent_end` events — one per seek invocation; bind `exact_` in runner-chained cases to catch a run that died mid-flight (no `agent_end`) |
| `session_id_missing` | `agent_end` events without `session_id` (`cmd/seek/main.go` `jsonLine.SessionID`, stable contract; `--no-save` legitimately omits it — only bind this in cases that save) |
| `session_id_distinct` | distinct `session_id` values across `agent_end` events — a fresh run plus `-resume` of that session must report the same id → 1 |
| `nonzero_exits` | `runner_fact` lines with `exit_code != 0` — a runner.sh's channel for process-level results (exit codes aren't in the JSONL stream); 0 for default-invocation cases |
| `runner_assert_failures` | `runner_fact` lines with `failed == true` — a runner.sh's channel for mechanical judgement assertions (task-level scoring; first user: plan-e2e's A1–A10, see "Load-bearing axis cases" below). Generic: any runner may emit failed-facts |
| `turns` | `turn_end` events |
| `completion_tokens` | cumulative generated tokens (`agent_end.completion_tokens`) — verbosity/thoroughness proxy |
| `cache_hit_tokens` | MAX `agent_end.cache_hit_tokens` across the stream (not the first event) — in a runner-chained fresh+resume case the hit lands on the resume run, whose re-sent prefix finds the provider's server cache warm; 0 means the provider's cache detail never reached `agent_end` at all (see `eval/cases/glm-lane-contract/`) |
| `review_line_refs` | count of `line N` / `LN` references in the assistant's text — rough "how many findings" proxy (deliberately ignores severity words so it doesn't echo the effort vocabulary) |
| `agent_calls` | `tool_start` events with `name=agent` — subagent spawns + continuations (`internal/tools/agent`) |
| `agent_bg_calls` | `agent` tool_starts whose parsed args carry `run_in_background=true` — the D1 detached-spawn shape; the E4 model-side assertion that the spawn was actually background |
| `agent_continue_calls` | `agent` tool_starts whose parsed args carry a non-empty `session_id` — the P1-b continuation shape; the E4 model-side assertion that the follow-up quoted the lineage |
| `bg_started_events` | E4 disk-side family: `runner_fact` lines with `check=bg_started_events` (max `value`) — background `started` events in the isolated SEEK_HOME's `projects/*/subagents.jsonl`, reported by subagent-bg-resume's runner after its invocations |
| `bg_completed_events` | `completed` terminals joined to those background sub_sids — the child finished before parent-process teardown cancelled it (a cancelled child folds to `failed reason=canceled` and is excluded) |
| `bg_notified_events` | `notified` consumption markers joined to the **completed** background set — the completion notice specifically (`internal/subagent` Notifier write-side, feature-orchestration §4.1 D3/D5); a failure-notice marker does not count |
| `bg_notified_transcript` | parent-session transcript rows that are user-role `<context>` messages carrying the `[agent: background done sub-sid=<full>]` wire prefix for a background child — the "notice entered the main session stream" surface itself; prefix + sub_sid only, never the summary prose |
| `bg_resumed_lineage` | `started` events whose `resumed_from` is non-empty AND names a sub_sid present in the index — the continuation run's lineage link |
| `bg_artifacts_missing` | completed runs whose `transcript.jsonl` is absent from `projects/<pid>/sessions/<parent-sid>/subagents/<sub-sid>/` (`paths.SubagentSessionDir`) — 0 is the pass state |
| `arm_a_keeps_facts` / `arm_b_keeps_facts` / `arm_b_negative_loses_facts` / `arm_b_is_summary_pair` | compact-ab: `runner_fact` assertions (generic `runner_assert_failures` metric counts them). Arms A/B answer the same prompt over the same scripted endpoint; the only difference is the history (full vs `/compact` summary pair). The negative variant inverts the arm-B assertion, so a summary that dropped the facts turns RED when it should. See the case README |
| `sse_delta_frames` | serve-web-stream only: `delta` frames on the W1.4 SSE wire — the case's runner.sh pairs captured `event:`/`data:` lines into `sse_frame` rows first; **content-conditional** (merged into the metrics object only when the stream carries `sse_frame` rows, so non-serve rows keep their historical byte shape — see the ablate no-env guard) |
| `sse_usage_frames` | serve-web-stream only: `usage` frames (turn_end translated server-side, `internal/controlplane/webfrontend_stream.go`) |
| `sse_frame_order_ok` | serve-web-stream only, 1/0: every `delta`/`usage`/`tool` frame of the close-out's run PRECEDES the close-out default frame — the per-conv `events…reply` wire invariant |
| `sse_delta_accum_ok` | serve-web-stream only, 1/0: the delta frames' `text` concatenated in wire order equals the close-out frame's `text` (the authoritative replacement) — the fake endpoint ships two deltas so a swap or a drop breaks it |
| `sse_reply_closeout_ok` | serve-web-stream only, 1/0: the close-out frame carries a non-empty `run` (equal to every delta frame's run) and a non-empty `session_id` |
| `session_list_hits` | serve-web-stream only: `/rpc session/list` `sessions[].sid` entries matching the close-out frame's `session_id` — the persistence axis (child wrote it under the same `SEEK_HOME` ladder the rpc plane reads through) |

The nine agent/bg keys are **data-gated** in the result row: they appear
only when the round's stream carries an agent surface (an `agent`
tool_start or a check-bearing `runner_fact`), so rows of non-agent cases
keep their exact historical bytes — the same discipline as the
`round`-field gate, and load-bearing for
[`scripts/eval-ablate-test.sh`](../scripts/eval-ablate-test.sh)'s no-env
byte-equivalence pin. Readers must treat an absent agent key as 0; a
case binding these bounds must guarantee the surface (subagent-bg-resume's
runner emits its fact channel unconditionally, so even a failed round
has the keys — a red always names its metric, never "unknown metric").

Bounds in `expect.json` use the prefix to declare direction:

| Key shape | Meaning |
|---|---|
| `max_<metric>` | metric must be `≤ value` |
| `min_<metric>` | metric must be `≥ value` |
| `exact_<metric>` | metric must equal value |

`expect.json` also carries scalar config:

| Key | Meaning |
|---|---|
| `description` | human-readable summary |
| `max_turns` | passed to `seek -max-turns` (NB it also rides the `max_` prefix scan, so it doubles as a turns ceiling — pre-existing behaviour) |
| `min_pass_rate` | gate threshold over the round set, `0 < m <= 1`, default 1.0, INCLUSIVE `>=` — see "Judgment" above (E2 calibrates; validated, exit 2 on malformed) |
| `record_only` | boolean, default false — record the case, never gate it (see "Judgment" above) |
| `judge` | optional object `{"rubric": str, "mode": "record"\|"gate" (default record), "model": str (optional)}` — the LLM-as-judge opt-in (eval v3 J, next section); validated in `read_gate_config`, malformed → exit 2 |

## Adding a case

1. `mkdir eval/cases/<name>` (kebab-case)
2. `README.md` explaining what behaviour the case probes — link to the original failing session, bug, or PRD claim if any
3. `prompt.txt` — the exact `-p` input; multi-line is fine
4. `expect.json` — start with `{"max_turns": 10}` and add bounds as you learn what good vs. bad looks like

Seeding from the pitfalls log: `scripts/pitfall-to-eval.sh --list` ranks
`docs/pitfalls.md` entries by model-behaviour likelihood (a triage
heuristic, not a decision), and `--make <line> <slug>` scaffolds a draft —
README.md + prompt.txt with the pitfall excerpt embedded, **no
expect.json**. The runner skips a case without expect.json, so a
scaffolded draft cannot run until a human writes the bounds; that skip is
the review gate.

Optional per-case hooks (run from the repo root, same cwd as the seek invocation):

- `setup.sh` — prepare fixtures before the run, e.g. copy `fixtures/` into `eval/tmp/<case>/` so the model can mutate them without dirtying the repo.
- `teardown.sh` — clean up after the run; runs even when the invocation failed.
- `runner.sh` — **replace the default seek invocation entirely** (`-yolo -no-save … -p`). For cases whose contract is about the CLI shape itself rather than the model's tool-call stream: running without `-yolo` (print-mode permission defaults), chaining multiple invocations (fresh run + `-resume` same session id), isolating `SEEK_HOME`, … Contract: executed with `SEEK_EVAL_BINARY`, `SEEK_EVAL_REPO_ROOT`, `SEEK_EVAL_PROMPT_FILE`, `SEEK_EVAL_MAX_TURNS` in the environment; writes the JSONL event stream to stdout, optionally followed by `{"type":"runner_fact","exit_code":N}` lines per invocation (consumed by the `nonzero_exits` metric). Cases without a `runner.sh` keep the default invocation and its historical behaviour.

## Tool-description tuning (relocated from AGENTS.md 2026-11-14)

The `const description` inside each `internal/tools/<name>/` is the highest-leverage behavioural lever: it rides every tool schema to the API, so the model MUST read it to build a valid `tool_call` — unlike AGENTS.md, which weaker/faster models may skim.

Pattern (proven in `eval/cases/tool-selection/`):

1. **Eval case first** — `prompt.txt` + `expect.json` defining the desired behaviour in measurable terms.
2. **Baseline** — run and record to `eval/results/` before changing anything.
3. **Edit the description** — 10–20 words of targeted guidance; zero runtime cost, the string is already sent.
4. **Compare** — re-run; success is the tool-call sequence changing, not just binary PASS/FAIL.

This repo's own tuning history:

| Tool | Guidance added | Observed effect |
|---|---|---|
| `bash` | "Prefer dedicated tools for repo inspection" | Model stops reaching for `bash ls` / `bash cat` |
| `edit` | "Read target lines first — don't guess whitespace" | Model reads before constructing `old_string` |
| `git` | "Prefer grep+read over git show/cat-file" | Model uses `grep`+`read` instead of `git show HEAD:file` |
| `list_dir` + `bash` (2026-11-13) | "One call answers count-/size-questions about a tree" / "counting files or comparing sizes is list_dir work, not find/wc/stat" | Model stops reaching for `bash find \| wc -l` / `find -exec stat \| sort` (`eval/cases/bash-overreach` drift repair) |

## transcript-stats 与口径（离线，不调模型）

`scripts/transcript-stats` 是**离线**分析器：不调模型、不发请求、不需要 key，
只回放已经落盘的 `~/.seek/sessions/*.jsonl`。它和 `eval/cases` 的分工是：

| | `eval/cases` + `run.sh` | `scripts/transcript-stats` |
|---|---|---|
| 判什么 | **边界纪律**：这一步有没有越界（二值 PASS/FAIL） | **真实任务分布上的行为形状**：打了多少轮、多少调用是白打的、read 是不是在翻页循环 |
| 数据来自 | 现跑一次（真 API，随机性，单次判定） | 历史 transcript（确定性、可复算、零成本） |
| 回答 | 「这个 bug 修好了吗」 | 「这一类任务平均长什么样」 |
| 粒度 | 单例 | 全部会话 / 按 model×月份分组 |
| 用在 | 回归闸门、发版前 | 连续效率趋势、参数调优的改前/改后对照 |

它填的是 [`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md) §1.3
诊断的留白——用例集由 pitfalls 台账孵化，本质是「历史 bug 的回归集」，测不了
「复杂任务上的效率分布」。这个工具补的正是后者：同一批 transcript 上量出
**连续量**，而不是再补一条二值断言。

首个基线报告：[`eval/transcript-baseline/2026-11-19.md`](transcript-baseline/2026-11-19.md)
（含四个关键数字：重复率 6.16% / read 重读率 67.99% / 翻页占比 92.00% /
offset 步长 `+50` 991 次）。

### 口径定义（复算以 `scripts/transcript-stats` 的头注释为唯一真源）

| 指标 | 定义 |
|---|---|
| session | 一个 `*.jsonl`；首行元数据（`schema_version`/`model`/`created_at`/`turns`），其余为 `deepseek.Message`，`tool_calls[]` 挂在 assistant 行 |
| fingerprint | 同一会话内 `(tool name, arguments 原始字节)` 完全相同 |
| `dup_extra` | Σ per-fingerprint (次数 − 1) —— 「白打出去的调用数」，按工具分列 |
| `dup_repeated` | Σ 出现次数 > 1 的 fingerprint 的**全部次数** / 总调用 —— 头条重复率口径（与 6.2% 调研数字同口径） |
| read 文件身份 | 参数里 `path` > `file_path` > `file` 第一个存在的键。解析失败的 read 单列 `unresolved` 计数，**绝不并进同一个桶**（并桶会凭空造出「同文件重读」假信号）。三键兼容是防御性契约：实测 2026-11-19 全量 read 参数里 `path` 是绝对主体、`file_path` 一次未出现、`file` 有 2 次——但键漂移的失败模式（静默丢信号）代价远高于三行兼容代码 |
| `reread_rate` | Σ 同会话同文件 (`read 次数 − 1`) / read 总数 —— 多余调用口径 |
| `pagination_share` | 被重读文件里 offset 出现过 >1 个不同值的比例（offset 缺省视作 1） |
| `offset_steps` | 同会话同文件 offset 集合**去重后升序**相邻差（跳页步长分布，top5） |
| `chains.children` / `parents_found` / `orphans` | 派生会话数（有 `parent_id`）/ 其中父会话也在扫描范围内 / 父缺失的断链数。断链单列，**不当 0 压缩比** |
| `chains.compression_mean` | 仅 compact 链：子会话首条**非 system** 消息的 content 字节（扣掉 76B `SUMMARY_PREFIX` 样板）/ 父历史 Σcontent（**不含 system**）。两处「不含 system」都承重——session JSONL 的 message 区以 system 开头（实测 461/461），算进分母会让比值恒等于 system 占比。分母也不含 `tool_calls.arguments`（那是给工具消费的指令，重复计入会让比值失真） |
| `chains.kind` | `compact`（首条非 system 消息以 `SUMMARY_MARKER` 开头）vs `branch`（其余，历史是父的副本）。branch 子会话没有摘要，**不进**压缩比与召回；混进来会造出 ≈1.0 的假样本 |
| `chains.recall` | 实体召回：父会话 tool_call 里的路径（read/edit/write）、bash 命令、grep pattern 出现在子会话摘要里的比例。**plain 桶是头条**；附件链（父历史含 `[image: `/`[file: `）单列 `attachment` 桶且不进总分——`Summarise` 走 `WithoutImages`/`WithoutFiles`，附件正文从来不进摘要视野 |
| `chains.rescue` | **不做分桶**：`rescueNote` 只追加到 summariser 的请求 prompt（`pkg/agent/compact_rescue.go`），不落 transcript，所以「这条链走过救援」无法从落盘数据机械判定。报告改打印 `parent_bytes` 让人看出溢出量级 |
| `chains.child_head_rate` | 子会话前 `CHAIN_WINDOW_CALLS`(=10) 个 tool_call 里、参数路径命中「父会话读过的文件集合」的 read 数 / 同期 read 数。压缩的代价是信息丢失，最先表现为「刚读过的又读一遍」 |
| `chains.baseline` | 参照系：同 cwd、无 `parent_id`、**且不是任何链的父会话**的聚合 `Σ(次数−1)/read`。父会话不算——它本身就是被压缩掉的那段历史 |
| 分组 | 指标同时按 `model × created_at[:7]` 汇总——5 月 v0.x 时代占 82% 的调用量，不分组会把版本混杂读成趋势 |

读取侧纪律：权限受限文件跳过并计数（`skipped_files`）；解析失败行计数
（`malformed_lines`）而不中断；数据路径可参数化（`--sessions-dir`）供 fixture
使用。`--selftest` 用 `eval/fixtures/transcript-stats/` 的新旧两种 schema 样例
断言四形态（`path` 键 / `file_path` 键 / 完全重复调用 / 翻页读），并带
`--read-key-mode path-only|naive-bucket` 两组反例，证明单键解析会丢信号或造
假信号。裸跑即自测的入口：`bash scripts/selftest.sh transcript-stats-test`。

## When to read results

Results are checked into git so you can `git log eval/results/` and watch how the agent's behaviour drifts as the codebase, prompts, or model versions change. A single PASS does not mean the bug is fixed; a sustained PASS rate across many runs does.

## Trend reports

`scripts/eval-trend.sh` aggregates every `results/*.jsonl` into
`trend-report.md` + `trend-report.json` (both checked in, regenerated on
demand): per-case last-5 pass rate, first-failure time, consecutive-failure
streak, and drift-suspect flags — most notably `suspect:gap-uncovered`
(failing run whose previous coverage was >30 days earlier: the failure date
is only an **upper bound** on when behaviour drifted — the bash-overreach
lesson) and `suspect:no-baseline` (case with ≤5 recorded runs, first run
<30 days old, already failing). `--selftest` drives the whole flag matrix
from a fake results dir. Env knobs (`TREND_WINDOW`, `TREND_GAP_DAYS`,
`TREND_NOW`, …) are documented in the script header.

Note for tooling: the early result files (pre v0.5) are pretty-printed
concatenated JSON objects — one result spans many lines — so readers must
parse with jq, never line-by-line (see `docs/pitfalls.md`).

## Ablation: bare-core vs full dual run (v8 pillar T)

`eval/ablate.sh [case] [binary]` runs every case **twice** — once with
the default (full) tool registry, once with the bare v8 kernel five
(`-tools read,edit,write,bash,grep`) — and prints a per-case F/B
comparison table (pass, turns, tool_calls, prompt/completion tokens,
cost) plus a TOTAL row. The table also lands in
`eval/ablation/<date>-<version>.md`; per-lane JSONL rows stay in
`eval/ablation/{full,bare}/`. Output files are **not** checked in
automatically — commit a `.md` by hand when a run is meant to backfill
[`docs/prd/v8.md`](../docs/prd/v8.md) §9.

**Both lanes write to `eval/ablation/`, never to `eval/results/`** —
that dir is the trend reports' data source, and ablation rows there
would fabricate regressions. The isolation rides on two `run.sh` env
knobs (both unset = historical behaviour, byte for byte). The
provider lane is orthogonal: `ablate.sh` sets no `SEEK_EVAL_PROVIDER*`
knobs, so both ablation lanes ride run.sh's DEFAULT provider lane
(the OpenCode Go subscription since the 2026-11-20 user call —
ablation runs are batch by definition and must not burn the metered
key either; the explicit `SEEK_EVAL_PROVIDER=deepseek` fallback is a
human decision, see § "Provider lane"):

| Env var | Effect |
|---|---|
| `SEEK_EVAL_RESULTS_DIR` | redirect the JSONL sink (default `eval/results`) |
| `SEEK_EVAL_EXTRA_ARGS` | extra seek args appended to the default invocation (space-separated flag list, word-split by design); also passed through to `runner.sh` cases in the env — runners that build their own invocations opt in by forwarding it (none do yet, so their F/B rows are lane-invariant; `ablate.sh` footnotes them) |

Cost in the table uses the deepseek-v4.1-flash card
(`internal/pricing`); rows carry no model id, so on other lanes read
the F/B ratio, not the absolute `$`. Per-case FAILs are ablation data,
not a script failure — `ablate.sh` exits 0 whenever both lanes
measured, 2 on setup errors.

### Self-test (deterministic, no key)

`scripts/eval-ablate-test.sh` (auto-discovered by the
[`scripts/selftest.sh`](../scripts/selftest.sh) lane, name
`eval-ablate-test`) re-verifies the ablation plumbing mechanically —
the original card's sandbox acceptance could not be re-run (the
permission layer refuses ad-hoc fake binaries), so the fake binary now
lives inside a checked-in test. A fake `seek` prints a fixed JSONL
stream and switches streams when it sees `-tools`; the test asserts
the dual run exits 0, the table lands in `eval/ablation/` with
lane-distinct F/B cells (`tool_calls 1/2` proves `SEEK_EVAL_EXTRA_ARGS`
actually reached the invocation), `eval/results/` gains nothing, and —
with both env knobs unset — `run.sh`'s stdout, stderr and result rows
are byte-identical to the last pre-knob `run.sh` blob (extracted
mechanically from git history; only fields that differ between any two
runs of the *same* script — timestamps, elapsed seconds, date stamps,
sandbox paths — are normalised). The test cleans up its own artifacts
under `eval/ablation/` (mtime-guarded: pre-existing ablation data is
untouched) and fails loudly if normalisation ever degrades to empty
output — an early revision shipped exactly that false-green.

## Provider lane: test traffic on the OpenCode Go plan (c_ffzic2)

**口径（v8.md §9.1）：测试默认 OpenCode Go 套餐 DeepSeek（订阅内）；计量真 key 仍仅 `--only` 人工指名或卡面明示授权。** Batch/unattended eval runs (three-arm comparisons, regressions, ablation lanes) must never burn the metered DeepSeek key — since the 2026-11-20 user call (疑点 A option (a)) the harness's **default lane IS the OpenCode Go subscription's `deepseek-v4.1-flash`**: all four knobs unset steers every seek invocation there, and the only way back to the metered key lane is the explicit `SEEK_EVAL_PROVIDER=deepseek` (a human decision — the env form of 人工指名).

Four `run.sh` env knobs (all four unset = **default opencode lane**, not the historical metered lane):

| Env var | Effect |
|---|---|
| `SEEK_EVAL_PROVIDER` | `deepseek` \| `opencode` \| `compatible`. Unset = `opencode` (OpenCode Go plan; seek's own lane, default model `deepseek-v4.1-flash`). `deepseek` = explicit fallback to the metered key lane — the historical invocation shape (no `-provider` flag, env `DEEPSEEK_API_KEY` untouched by the scrub). `compatible` = any OpenAI-compatible endpoint |
| `SEEK_EVAL_MODEL` | model id, passed as `-model`; with `opencode` unset it IS `deepseek-v4.1-flash` (seek's lane default) |
| `SEEK_EVAL_BASE_URL` | endpoint override (full POST target), passed as `-base-url` **and** exported as `OPENCODE_BASE_URL` so a `providers.opencode.base_url` config preset can't out-rank the flag inside seek |
| `SEEK_EVAL_API_KEY` | lane credential, exported as the lane's canonical env var (`DEEPSEEK_API_KEY` / `OPENCODE_API_KEY` / `OPENAI_API_KEY`); never write real keys into card text, logs or expect files |

While the opencode/compatible lane is active, `run.sh` **scrubs `DEEPSEEK_API_KEY` from the child environment** — the metered key has zero reach into a switched lane (seek's `compatible` case would otherwise read it as a fallback credential). The deepseek lane is the metered lane itself, so only there the env key survives. Credential resolution itself happens inside seek: `OPENCODE_API_KEY` env > `providers.opencode.api_key` > the stored browser login.

**Token rule (hard): the harness never reads — and never refreshes — `~/.seek/opencode-auth.json`.** The refresh token is single-use; a side refresh invalidates the copy seek holds. An expired login surfaces as loud API errors and a human re-logs in; it is never self-healed by eval tooling.

**runner.sh forwarding contract.** Cases that build their own invocations (e.g. the mcp-semble-routing arm engine) opt in by forwarding the exported lane args — the same opt-in shape as `SEEK_EVAL_EXTRA_ARGS`:

```bash
"$SEEK_EVAL_BINARY" -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" \
  -json ${SEEK_EVAL_PROVIDER_ARGS-} -p "$prompt"
```

`SEEK_EVAL_PROVIDER_ARGS` is empty only on the explicit `deepseek` fallback lane (`${VAR-}` is the `set -u`-safe reference; the default opencode lane forwards `-provider opencode`), and the lane's exported credential env vars ride along automatically. Wiring the arm runner this way is the follow-up for the mcp-semble line (card c_eyuxqm ② 复核 and later MCP-lane tests default to this provider).

**Hermetic proof.** `eval/tools/fake_openai_compat.py` is a local fake OpenAI-compatible endpoint (one text delta + usage + `[DONE]`, same wire shape as `fake_paas_glm.py`) that records every POST's path / auth / model to a JSONL log. `scripts/eval-provider-test.sh` (auto-discovered by the [`scripts/selftest.sh`](../scripts/selftest.sh) lane, name `eval-provider-test`) drives the REAL seek binary against it with fake keys and asserts **both the default path (all four knobs unset → `-provider opencode`, metered key scrubbed, wire model `deepseek-v4.1-flash`) and the explicit `deepseek` fallback path (no `-provider`, metered key intact)**, plus the four knobs mapping into argv/env, the request target/credential/model actually switching on the wire, a config-preset endpoint hijack being out-ranked, both env-side and config-side metered-key canaries never reaching the endpoint, and the runner contract above holding. Real-plan smoke (≥3 runs + metered-key usage delta check) is a human/main-session action, not a selftest.

Cost note: rows on a provider lane carry no model id, so cross-lane cost comparisons read the ratios, not the absolute `$` (same caution as the ablation table).

## Load-bearing axis cases (eval v2 E3+, `docs/prd/feature-eval-v2.md` §2.2 B)

Task-level cases for the v0.11–0.12 surface that grew fastest with zero eval
coverage (plan state machine, subagent lifecycle, serve/web plane). Judging is
a per-case mechanical scorer (no LLM, no prose matching): wire-format PREFIX
assertions on tool results + message-order facts + filesystem recomputation,
emitted as `runner_fact` lines and counted by `runner_assert_failures`.

- **`plan-e2e`** (E3, c_c5wwey, 2026-11-21) — the full plan chain
  propose(done_when+reviewed_by) → approval → plan(start) echo → write →
  plan(complete) → `/exit` → `-resume` fold probe, driven through a REAL TUI
  on a pty (`eval/tools/plan-e2e-drive.py`; the approval picker is
  TUI-only — print mode has no askuser callback) against a scripted local
  model (`fake_plan_endpoint.py`, fake key, $0 API). Asserts A1–A10 +
  resume_same_session; **measures the toolchain, not model choice**.
  `record_only: true` until E2's calibration lands; joins the release lane
  at E6. Baseline + judging detail + the resume-fold bug its first run
  caught: `eval/cases/plan-e2e/README.md`. The judging/gate mechanics are
  pinned headless by `scripts/eval-e3-case-test.sh` (selftest lane, name
  `eval-e3-case-test`, ~2 min): the REAL runner/drive(pty)/endpoint/judge
  run as-is with only the binary swapped for a scripted fake that speaks
  the same argv + wire contracts — all-green row, four named-red flavors
  (fold / no-write / premature-write / approval-marker), record-only,
  and E1 `--rounds` aggregate compatibility. The real-binary real-TUI
  full-chain run stays with the release lane (its `eval/run.sh` entry is
  outside the verification lane's `selftest.sh`-only whitelist and its
  default sink is the formal `eval/results/`).
- E4 `subagent-bg-resume` and E5 `serve-web-stream` follow on their own cards
  (same judging pattern: `runner_fact{failed}` + this metric).

## Judge 层：LLM-as-judge over final_text（eval v3 J，2026-11-22）

修订 eval-v2 §2.5 的「❌ 不做 LLM-as-judge」（修订依据与边界见
[`docs/prd/feature-eval-v3.md`](../docs/prd/feature-eval-v3.md) 状态头）：
确定性优先不变——judge 只补**复算不可能的面**（答案对不对、是否答非所问），
可复算性落点移到**校准层**（标注集 + agreement 报告入库）。机械指标与判分层
刻意正交：2026-11-13 的 effort-max 行机械绿但只交付过程叙述、a3-nl 机械红但
答案完整——判分层存在的理由正是这些行。

- **opt-in**：`expect.json` 的 `judge` 块（键表见上）。全局 kill-switch
  `SEEK_EVAL_JUDGE=0`；`final_text` 为空跳过判分（键不出现）。
- **调用**：`eval/tools/judge.py` 一次 fresh-context 单发（同 provider 车道、
  自带判分模型——默认**跨家族** `kimi-k3.1-pro`，`SEEK_EVAL_JUDGE_MODEL`
  可换，per-case `judge.model` 再优先；`SEEK_EVAL_JUDGE_TIMEOUT` 默认 120s）。
  final_text 以 DATA 围栏入 prompt（防注入）；`-tools grep` + `-max-turns 6`
  约束工具面（2026-11-23 标定实测：kimi 会先 grep 自证，1 轮预算烧光无文本）。
- **判决契约** `pass | fail | doubt`——一切失败形态（null 判决、非 JSON、
  超时、judge 崩溃）折叠为 doubt，**永不假绿**（`internal/verifier` 的
  no-JSON=never-green 同款）。
- **行上三键 data-gated**：`judge_verdict` / `judge_reason`（截 300 码点）/
  `judge_model`，只在判分实际发生时并入——不判分的行字节不变（与 agent/bg、
  sse 两族同一纪律，字节 pin 自测不需动）。`elapsed_s` 只量被测调用。
- **染色**：`mode=gate` 且 verdict=fail 才进 `failures`（`"judge: <reason>"`）；
  doubt 与 record 模式永不染色。gate 下 judge 基建故障 = fail-open（doubt +
  响亮记录）——v1 口径，PRD §六.1 挂账。
- **校准（开 gate 的资格）**：`eval/judge-calibration/fixtures.jsonl`（34 条
  = 27 真实行按 (case, ran_at) 从 results 重抽 + 7 构造扰动，标签为代标初稿
  待用户抽查）× `eval/tools/judge-calibrate.sh` → `report.md`。门槛：
  **agreement ≥ 0.85 且 false-pass = 0**。标定通过前一切接线 record 模式；
  首批接线 `code-review-effort-low` 与 `read-tail-fact`。
- **gate 状态：已开（2026-11-24，用户拍板，卡 c_xhbqae）**。两例
  `expect.json` 的 `judge.mode` 由 `record` 切到 `gate`——**开关就是这个
  per-case 字段**（`read_gate_config` 校验），没有也不加全局 env 开关；
  `judge_verdict`/`judge_reason`/`judge_model` 三键的行上口径不变。
  开启时两个已知保留理由（照旧成立，非清零）：①**单轮余量薄**——
  30/34 = 0.88，少一条同意行 29/34 = 0.853 已贴 0.85 门槛，再少一条
  0.824 即跌破；②**凭证路径非正式**——标定跑的是登录透传
  （`SEEK_HOME` 里的 opencode 登录态，本机无 `OPENCODE_API_KEY`），
  与官方 key 重跑口径的差异是已知留白。
  **一行关回**：`SEEK_EVAL_JUDGE=0 bash eval/run.sh <case> <binary>`
  （或对整轮 export）——judge 不调用、零 judge 键、不染色，行回到
  judge 层引入前的字节形状（`scripts/eval-judge-test.sh` ⑥ 钉死）。
  注意 `record_only: true` 的 `code-review-effort-low`：gate 下 judge 判红
  会染进该行 `failures`（`pass:false`），但 record-only 语义不变、
  **不染 exit code**——端到端拦截只见于 `read-tail-fact` 这类非
  record-only 用例。
- **机制自测**：`scripts/eval-judge-test.sh`（8 腿：record 不染色 / gate
  翻红 / 非 JSON doubt / 无块零键 / 畸形 exit 2 / kill-switch / 真二进制×
  假端点的线上模型分离——一次 run 两条 POST，SUT 走车道缺省模型、judge 走
  判分模型 / calibrate 聚合）。

## 外部锚：Terminal-Bench-lite 私有任务集（eval v3 A，2026-11-22）

34 条行为用例全答「自己相对自己没退步」；锚点任务集答「这个 harness 在固定
任务上能不能干活」。**#2575 边界：仅私有校准，任何数字永不发布。**

- **8 题**（`eval/cases/anchor-*/`，`--lane anchor`，全 record_only 起步）：
  修失败测试(Go) / CSV 字节精确报表 / 按规格实现 CLI / 约束重构 /
  症状驱动调试 / 多文件文档调和 / 配置迁移 / git 考古。全 hermetic 无网络
  无 Docker（只用 go/python3/bash/jq/git）；难度目标 = 解得出一部分、解不全。
- **机制**：每题 `runner.sh` 是两行 shim，公共隔离住在
  `eval/tools/anchor-runner.sh`——私有 scratch（`eval/tmp/<case>/{home,ws}`，
  独立 SEEK_HOME）、workspace fixture 复制进 ws、agent 以 ws 为 cwd。
  判分纯机械：`check.sh` 从 agent 世界**外**重算（黄金在 `golden/`，ws 里
  没有）；未解出 → `runner_fact{assert:"anchor_solved",failed:true}` →
  既有 `max_runner_assert_failures` 闸门；崩溃 → `nonzero_exits`。rounds /
  车道 / trend / streak 全部白拿。
- **任务自验**：8/8 原始态 check 红、8/8 参考解 check 绿（落地时逐题验证）。
  首轮真 key 基线后按区分度调题——**修题不修阈值**（同 E2 容差原则）。
- **跨 harness 对照**：`eval/anchor/`（与 ablation 同纪律，**永不进
  `eval/results/`**）+ `eval/tools/anchor-crossharness.sh`——同任务同模型
  驱动另一 harness（默认 opencode CLI；缺席时响亮降级为手工协议）。唯一被
  隔离变量是 harness；check.sh 两侧同一份。
- **机制自测**：`scripts/eval-anchor-test.sh`（4 腿：解出绿 / 不解红指名
  anchor_solved / 隔离（pwd=ws、SEEK_HOME=scratch home、fixture 在位）/
  崩溃计入 nonzero_exits）。

## Cost

Each pass is one full `seek` invocation. On the default lane (since 2026-11-20: the OpenCode Go subscription's `deepseek-v4.1-flash`) it bills the subscription, not the metered API. On the explicit `SEEK_EVAL_PROVIDER=deepseek` fallback it is one metered DeepSeek call — budget roughly **$0.02–0.05 per case** depending on prompt length and tool-call fan-out (full 3-case suite well under $0.20).

Rows also carry `cache_hit_tokens` (for cost accounting — see `docs/test-plan-read-tool.md` §3.3) and `final_text` (joined assistant text, capped at 2000 chars, for offline gold-answer scoring).

Load-bearing axis cases are the exception on both lanes: their model is a local scripted fixture (fake key), so a full plan-e2e round costs **$0 API and ~18s wall** (measured 2026-11-21); the $0.1–0.3/example estimate applies only when the release lane re-runs them against a real model (PRD §2.2 B, E6's budget — not the daily core subset).
