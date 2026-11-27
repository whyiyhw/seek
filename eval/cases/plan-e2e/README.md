# plan-e2e — eval v2 E3, 承重轴首例（c_c5wwey）

`docs/prd/feature-eval-v2.md` §2.2 B 的第一条承重轴：真实走 plan 工具链全链——
**propose（带 done_when 断言列表 + reviewed_by）→ 用户批准 → plan(start) 执行期回显 →
write → plan(complete) → /exit → `-resume` 状态折叠**。判分全部机械化
（`eval/tools/plan-e2e-judge.py`），无 LLM-as-judge、无散文匹配。

## 为什么这样跑（三条设计约束）

1. **审批是交互的，所以是 PTY。** propose 走 `askuser.Policy`，其回调只在 TUI
   路径接线（`cmd/seek/main.go` 在 `-p`/`--json` 分支 return 之后才
   `SetAskFn`）——print 模式下 propose 必然 `ErrNoCallback`。无人值守要真实
   走完审批门，只能给 seek 一个终端：`eval/tools/plan-e2e-drive.py` 在 pty 上
   打 prompt、在 picker 上按 ↓+Enter（选第二行 "approve — auto-approve per
   step"，让 `plan(start)` 武装 per-step 预批、write 不再弹 y/N）、等 done
   marker、`/exit` 落盘退出。
2. **模型是本地脚本，所以测的是工具链。** unattended 车道按 README § Isolation
   用假 key——假 key 之下唯一可用的"模型"是本仓库脚本化的
   `eval/tools/fake_plan_endpoint.py`（OpenAI-compatible SSE，状态机按
   transcript 内容决定下一步，重试幂等）。**本 case 度量的是工具链**：
   wire-format marker、审批门、transcript 记录、resume 折叠——这些在固定
   tool-call 序列下全部确定性。*模型选择*（真模型会不会按序调 plan()）不在
   本 case 范围内，也绝不能从本 case 推出——那是 release lane 真模型跑的事。
3. **`-plan` 不带 `-yolo`。** 两者互斥（seek 启动直接拒绝）。`-plan` 让
   WorkflowPlanAnalyze 为 A3 提供真实防线（analyze 态 write 被 workflow 终决
   拒绝，与 pref 无关）；批准经 approve_batch 进 execute 后 `plan(start)` 武
   装预批，write 畅通。

## 判分口径（A1–A10 + resume_same_session，全部可复算）

| 断言 | 事实来源 | 说明 |
|---|---|---|
| A1 propose_done_when | transcript args | done_when ≥2 条、reviewed_by 非空、steps ≥2 |
| A2 approval_marker | tool result 前缀 | `[plan: approved]`（含 approve_batch 变体——变体只能在 token 后加词，AGENTS.md wire-format 契约） |
| A3 no_write_before_approval | 消息序 | 批准行之前零 write/edit |
| A4 plan_echo_shapes | tool result 前缀 | `[plan: step 1 started]`、`[plan: step 1 complete, 1/2 done]` |
| A5/A6 done_when 复算 | 文件系统 | ws/notes.md 存在 / 含 FIXTURE-NOTES-OK——即 propose 的 done_when 列表逐条对照 |
| A7 resume_fold | tool result 前缀 | resume 段 `plan(complete,2)` 答 `[plan: step 2 complete, 2/2 done]`；折叠丢失则答 `no active plan`（形状不同，非散文） |
| A8 resume_no_rewrite | 消息序 | resume 段零 write/edit（已完成步骤不重跑） |
| A9/A10 drive 状态 | drive status JSON | 两臂 prompt_sent/done/exited；fresh 另有 approved |
| resume_same_session | sessions 目录 | `-resume` 复用同一 session 文件（目录里只有 1 个 .jsonl） |

**防假红**：断言只认 wire-format 前缀（marker 是 `plan.go`/`propose.go` 的契约）
与消息序/文件系统事实；marker 之后的散文措辞一概不断言。**防假绿**：runner 的
每个提前退出分支都发 `failed:true` 的 runner_fact（空流全零 metrics 不可能
碰巧过闸——本卡实测中真踩到一次：endpoint 没起来时空流让 max-bound 空过）。

## 三态与 E1 口径（2026-11-21 实测，二进制 2163358+，全本地 fixture 零 API 费）

- **全过**：`PASS plan-e2e (18s, …runner_assert_failures:0)` + `RECORD …
  record-only — not gated`；直跑 runner 时 judge 报 `10/10 assertions passed`。
- **断言失败**：gate 面——expect.json 去掉 record_only + 注入不可能 bound
  （`exact_bash_calls: 99`）→ `FAIL plan-e2e` + `eval: 0 passed, 1 failed` +
  run.sh exit 1；判据面——对 sabotage 输入（无 plan echo/无 notes.md/无折叠）
  重放 judge → 4 断言红、judge exit 4。
- **record_only**：坏 bound 保留 + `record_only: true` → FAIL 行照打（判分红
  可见）、汇总 `0 passed, 0 failed, 1 recorded`、run.sh exit 0（不闸门）。
- **--rounds 2**：两轮各 PASS（r1/2、r2/2 标签），行带 `round` 字段，RECORD
  按 2 轮聚合——E1 的 `--rounds N` / `min_pass_rate` / `record_only` 全兼容
  （本 case 全部 bound 是 max_ 形，min_pass_rate 语义由 E1 框架保证，
  `scripts/eval-rounds-test.sh` 已钉）。

**record_only 现状（转正路径定案 c_3xyt7w, 2026-11-25）**：仍是
`record_only: true`。转正 = 两步，缺一不可：① **主会话/人**用真 key 在发版道
跑 `eval/run.sh --rounds 5 plan-e2e` 取 baseline（无人值守道按
`docs/prd/v8.md` §9.1 一律假 key——本卡纪律，不是疏漏）；② E2 标定表把 gate
记进 `eval/README.md` 的承重轴表，同时 `expect.json` 改
`record_only: false` + `lane-release.txt` 取消本行注释。**在此之前不进
release lane**：名单每一行都当闸门读，把 record-only 案例写进闸门只会让闸门
永久绿。本表 2026-11-25 的假 key 5 轮复跑（5/5 绿，`RECORD … record-only`，
证据 `docs/evidence/c_3xyt7w/plan-e2e-5round-run.log`）证明 fixture 侧确定性，
**不是**真 key baseline。

## 首跑即抓到真 bug（baseline 的意义）

首跑 resume 臂全红：`plan(complete,2)` 恒答 `no active plan`（scripted model
死循环重试 204 turn——endpoint 已加 3 次探针上限防失控）。根因：
`plan.ReconstructFromTranscript` 用 `m.Name == "propose"` 匹配，而落盘的 tool
消息**没有 name 字段**（`Message.Name` 是 `omitempty`，wire 上名字只在
assistant tool_call 里；单测 fixture 手工填了 Name 所以常年绿）。修复 =
`toolNameAt` 配对恢复（`internal/tools/plan/reconstruct.go`）+ 现实形状回归
测试 `TestReconstruct_PersistedWireShapeNoToolName`。pitfall 已记
`docs/pitfalls.md`。这正是 PRD §1.2 说的「v0.11–0.12 新面积零 eval 覆盖」
的实际代价——该路径自 v0.11 上线起就在生产里坏着。

## 成本

单轮全链 = 2 lane × ~5 turn，模型是本地 fixture → **API $0**（实测 18s/轮，
其中大半是 TUI 渲染与打字节奏）。release lane 换真模型后预估 $0.1–0.3/例
（PRD §2.2 B 口径），属 E6 的门禁预算，不在日常 core 子集。

## 机制预算答问

- 常驻机制增量 = `eval/run.sh` 一个通用 metric `runner_assert_failures`
  （~12 行：case 分支 + argjson + 输出行）。它不属 plan-e2e：任何 runner.sh
  发 `{"failed":true}` fact 都会被计入——E4 subagent-bg-resume 的三断言判分
  直接复用同一模式，无需再动机制。
- 本 case 的三个 python 脚本（endpoint 343 行 / drive ~230 / judge ~210）是
  **测试资产不是机制**：只在 case 跑时存在，不退役（同 E4 卡面口径：机制账
  本只记常驻检查项）。
- 返工 C 案（2026-11-21）补的 `scripts/eval-e3-case-test.sh`（~340 行，含内
  嵌假二进制）同属测试资产：selftest 发现制零登记、无 cron/CI/常驻状态，
  产物全落 mktemp 沙盒 + `SEEK_EVAL_RESULTS_DIR` 重定向；其唯一机制性影响
  是 `eval-lane-test` norm 白名单补一行（吸收平行分支 metric 键的既有纪律，
  见 docs/pitfalls/eval/byte-equivalence-selftests-…）。

## 架构页

不适用：本 case 不新增架构红线。两条相关既有契约原样适用——wire-format
marker 变体只能加在 token 之后（AGENTS.md "Wire-format strings are
contracts"）；propose 审批只在 TUI 路径可交互（本卡记入 pitfalls，属行为事
实而非新约束）。
