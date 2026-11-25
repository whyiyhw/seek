# c_3xyt7w 证据：E3/E4 承重轴 release lane 成员资格定案

日期 2026-11-25。**全部假 key**（无人值守车道纪律 `docs/prd/v8.md` §9.1）——
本卡不烧计量 key，真 key baseline 属主会话/人（见文末交接）。

## ① E4 入 lane 后，假 key 车道跑 `--lane release` 该例过

模型 = `eval/tools/fake_subagent_endpoint.py`（本仓 scripted endpoint，端口 18086），
二进制 = 本分支 `go build -o seek ./cmd/seek`（v0.13.2-0.…-617cc53）。

```
SEEK_EVAL_PROVIDER=compatible SEEK_EVAL_MODEL=subagent-e4-fake \
SEEK_EVAL_BASE_URL=http://127.0.0.1:18086/v1/chat/completions \
SEEK_EVAL_API_KEY=fake-key-e4 SEEK_EVAL_RESULTS_DIR=/tmp/e4-lane2 \
eval/run.sh --lane release --rounds 1
```

结果（`lane-release-run-2-final.log`）：

```
eval: lane    release (24 cases from lane-release.txt)
PASS  serve-web-stream  (3s, … sse_delta_frames:2 …)
PASS  subagent-bg-resume  (0s, {… "agent_calls":2,"agent_bg_calls":1,
  "agent_continue_calls":1,"bg_started_events":1,"bg_completed_events":1,
  "bg_notified_events":1,"bg_notified_transcript":1,"bg_resumed_lineage":1,
  "bg_artifacts_missing":0})
eval: 15 passed, 9 failed
```

**9 红是端点副作用，不是回归**：scripted endpoint 对名单里其它用例一律答
普通文本 "ok"（它的状态机只识别 E4/E3 简报），那些用例的 `min_read/min_grep/
…` 界自然咬不到。这正是 endpoint docstring 里写的「redirect the results dir
so those rows never enter the history-derived E2 calibration windows」——
本轮 `SEEK_EVAL_RESULTS_DIR=/tmp/e4-lane2`，正式 `eval/results/` 零写入
（`scripts/eval-lane-test.sh` 收尾的 inventory 比对同轮全绿佐证）。

单例对照（`e4-single-round-row.jsonl`）：同一 endpoint，`eval/run.sh
subagent-bg-resume ./seek` → `PASS`，九个 bg/agent 指标逐项正确。

## ② E3 现状：仍 record_only，假 key 5 轮全绿但**不是**真 key baseline

`SEEK_EVAL_RESULTS_DIR=/tmp/e3-verify eval/run.sh --rounds 5 plan-e2e ./seek`
→ `plan-e2e-5round-run.log`：

```
PASS  plan-e2e  r1/5 … r5/5   (18s each, runner_assert_failures:0)
RECORD  plan-e2e  (5/5 rounds, record-only — not gated)
eval: 0 passed, 0 failed, 1 recorded (record-only)
```

说明 fixture 侧（真 TUI PTY + scripted endpoint + judge）确定性成立；
`expect.json` 的 `record_only: true` 与 lane 缺席是**有意的**：record-only
进闸门 = 闸门永久绿。转正路径见 `eval/README.md` § Load-bearing axes 的 E3 行。

## ③ 机制面自测（全绿）

| 脚本 | 结果 |
|---|---|
| `scripts/eval-lane-test.sh` | PASS（含新增 ⑨「release 名单不含 record-only 用例」，正反两腿） |
| ⑨ 反向实测 | 临时把 `plan-e2e` 塞进 `lane-release.txt` → selftest rc=1，报 `⑨ release lane lists record-only case(s): plan-e2e`；已还原 |
| `scripts/eval-e4-case-test.sh` | PASS（all-green + 5 named-red + record-only + rounds） |
| `scripts/eval-e3-case-test.sh` | PASS（`e3-case-selftest.log`：all-green + 4 named-red + record-only + rounds） |
| `scripts/eval-rounds-test.sh` | PASS |
| `go test -race ./...` | rc=0（100 包 ok，无 FAIL） |

## 车道能力外交接（不算卡点，验证侧按 not-in-lane 单列）

1. **E3 真 key 5 轮 baseline** —— 主会话/人。跑 `eval/run.sh --rounds 5 plan-e2e`
   在发版道真 key 环境；拿到 baseline 后按 `eval/README.md` E3 行的两步走翻
   `record_only` + 取消 lane 注释（两步必须同一次改动）。
2. **E4 真 key 5 轮标定** —— 主会话/人（发版道例行）。gate 0.6 已按 doctrine
   定死，标定只决定「是否收紧」，不改成员资格。
3. **视觉过目条款（c_mqz4kg）不适用** —— 本卡未触及
   `internal/controlplane/webfrontend.html` 或 `internal/controlplane/static/`。
