# mcp-semble-routing 三臂对照 — 2026-11-18 采集（Phase 1 判定数据）

Card **MCP-D4**（c_eyuxqm）· 设计依据 [`docs/prd/feature-mcp-client.md`](../../docs/prd/feature-mcp-client.md) §5。
本文件是**三臂完整对照**（A1/A2/A3，同 binary、同 prompt、同 repo），取代
[`mcp-semble-routing-baseline-2026-11-17.md`](mcp-semble-routing-baseline-2026-11-17.md) 的 A1-only 部分基线
（后者保留作历史记录）。

## 结论摘要

| §5.4 判定 | 结果 |
|---|---|
| ① A3 vs A2：case nl 的 `mcp_calls` 从 ≈0 → 每 run ≥1 | **FAIL**（A3 0/5，A2 0/5——引导没有让模型找到工具） |
| ② A3 vs A1：case nl 的 `completion_tokens` 降 ≥30% | **FAIL**（1692.8 → 1254.0，−25.9%，未达 30%；且 0 次 MCP 调用，差异不可归因于机制） |
| ③ A3 case symbol 的 `mcp_calls` 仍 = 0（防过度引导） | **PASS**（5/5 run 均为 0） |
| ④ A3 `cache_hit_tokens` 相对 A1 无异常塌方 | **PASS**（命中率 92.3–93.6% vs A1 91.5–93.7%） |

**Phase 1 验收不成立**（①② 未过）。Phase 3 触发条件的双条件只满足其一
（`mcp_calls=0` 占比 100% > 30% ✅；这些 run 的 `gold_hit` 未低于 A1 ❌）——
详见下文「Phase 3 触发条件预读」。go/no-go 判定归 D5（c_erzfzx），本卡只给读数。

## 采集参数

- **Binary**：`v0.11.1-0.20261118172804-bc1fd08f7137`（`bc1fd08`；tree = `main@32e5e15`
  合入本分支 + 本卡 eval 改动）。三臂同一 binary——PRD §5.1 要求。
- **日期**：2026-11-18（UTC）。原始行：`eval/results/2026-11-18-v0.11.1-0.20261118172804-bc1fd08f7137-bc1fd08.jsonl`。
- **样本量**：每臂 × 每 case **5 个有效 run**（46 行中 45 有效 + 1 无效，见下）。
- **Semble**：0.7.3（`uvx --from 'semble[mcp]' semble`），索引预热 12s
  （`eval/tools/mcp-arm-setup.sh`；磁盘索引缓存跨 run 复用）。
- **隔离**：每臂独立 scratch `SEEK_HOME`（`eval/tmp/mcp-semble-routing/<arm>/`），
  只差 `mcp.json`：a1 无、a2 `mcp_prompt:false`（顶层键）、a3 默认开。
  鉴权从 `~/.seek/config.json` 复制进 scratch home（不进输出、不进 git）。
- **无效 run**：`a2-mixed` 第 2 跑遭遇一次瞬时 API 失败
  （`deepseek: Post .../chat/completions: EOF (after 2 attempts)`，无 `agent_end`），
  已排除并补跑一次（表中 n=5 为有效 run）。
- **全程沙盒**：worktree + `eval/tmp/`（gitignored）。隔离探针（2026-11-18 09:41 PST）：
  单跑 a1-symbol（该探针 run 不计入对照表）前后对 `~/.seek` 做全量 stat 快照，**唯一变化是本工作会话自身的
  `checkpoints.jsonl`**（live session 的正常 checkpoint），无任何 eval 进程写入——
  case 组 runner 以 `SEEK_HOME=<scratch>` + `-no-save` 启动 seek，用户态写入全部落
  `eval/tmp/mcp-semble-routing/<arm>/home/`。
  **依赖偏差（交接主会话）**：卡面引用的 c_clip4i（`eval/run.sh` 全局钉死
  `SEEK_HOME` 沙盒）实测**未进 main**（2026-11-18：`git grep SEEK_HOME main --
  eval/run.sh` 为空，提交 `c17de76` 仅在 `drive/c_clip4i` 分支）。本 case 组的隔离
  由 `mcp-arm-runner.sh` 自钉 per-arm scratch SEEK_HOME 完成，故本轮采集仍全程沙盒；
  但 harness 级默认钉死需主会话合并 c_clip4i 后复核（见「D5 交接」）。

## 三臂对照表（每格 = 5 个有效 run 的均值）

| case | PASS | mcp_calls>0 的 run | grep | read | 总工具 | turns | completion_tokens | prompt_tokens | cache_hit | 命中率 | elapsed_s |
|---|---|---|---|---|---|---|---|---|---|---|---|
| a1-nl | 5/5 | 0/5 | 5.4 | 2.6 | 8.0 | 7.4 | **1692.8** | 170210 | 158515 | 93.1% | 15.6 |
| a2-nl | 5/5 | 0/5 | 6.0 | 2.6 | 8.6 | 7.4 | 1743.0 | 179423 | 167962 | 93.6% | 16.2 |
| a3-nl | 0/5¹ | **0/5** | 4.8 | 1.4 | 6.2 | 5.6 | **1254.0** | 123591 | 114048 | 92.3% | 13.2 |
| a1-symbol | 5/5 | 0/5 | 1.6 | 1.0 | 2.6 | 3.4 | 325.2 | 63717 | 59699 | 93.7% | 5.4 |
| a2-symbol | 5/5 | 0/5 | 2.0 | 1.0 | 3.0 | 4.0 | 389.4 | 82253 | 76979 | 93.6% | 6.6 |
| a3-symbol | 5/5 | **0/5** | 2.0 | 1.0 | 3.0 | 4.0 | 397.4 | 83423 | 78052 | 93.6% | 6.6 |
| a1-mixed | 5/5 | 0/5 | 2.0 | 1.4 | 3.6 | 3.6 | 617.2 | 71495 | 65408 | 91.5% | 7.2 |
| a2-mixed | 5/5 | 0/5 | 2.2 | 1.0 | 3.2 | 3.2 | 453.2 | 61780 | 57624 | 93.3% | 6.4 |
| a3-mixed | 5/5 | 0/5 | 2.2 | 1.2 | 3.6 | 3.6 | 644.0 | 72197 | 66918 | 92.7% | 7.6 |

¹ a3-nl 的 `pass=false` 来自 expect.json 的 `min_mcp_calls: 1` 界——这正是 §5.4 ① 的判定
数据，不是运行失败（`agent_ends=1`、`nonzero_exits=0`、答案正确）。

**gold_hit（按 §5.2 口径离线评分，`eval/tools/mcp-semble-score.sh`）**：
a1 5/5 · 5/5 · 5/5；a2 4/5 · 5/5 · 5/5；a3 5/5 · 5/5 · 5/5（nl/symbol/mixed 顺序）。
a2-nl 的唯一 miss 是答案事实正确但只引 `pkg/agent/agent.go:1260`、未引 gold 文件
`internal/permission/permission.go`——评分口径的已知边界，不是错误答案。

**first_gold_read（§5.2 离线指标，`eval/tools/mcp-first-gold-read.sh`）**：
symbol 2.6 / 3.0 / 3.0（a1/a2/a3），mixed 3.0 / 3.0 / 3.0；nl 三臂全部未 read gold 文件
（grep 输出已含行内容，模型不需要 read）——不是缺陷，是该 metric 在 nl probe 上的退化形态。

### nl 每 run completion_tokens（噪声尺度）

- a1：1433, 1540, 1630, 1832, 2029（极差 596）
- a2：1091, 1163, 1397, 2250, 2814（极差 1723）
- a3：756, 1146, 1308, 1501, 1559（极差 803）

A3 相对 A1 的 −25.9% 落在两臂各自的跑间波动范围内，且 A3 的 5 个 run 全部
`mcp_calls=0`——**该差异不是引导机制造成的**，只能当噪声/轮数差异（turns 7.4→5.6）看。

## 成本账（prefix，§5.2 成本口径）

首 turn `prompt_tokens`（trace 的 `turn_end[0]`，每 case 5 run 均值）：

| 臂 | nl | symbol | mixed | 相对 A1 |
|---|---|---|---|---|
| A1（无 semble） | 16076 | 16058 | 16069 | — |
| A2（semble 工具注册 + 无引导段） | 17035 | 17017 | 17028 | **+~960 tok**（两个 `semble__*` 工具 schema） |
| A3（A2 + MCPSection） | 17173 | 17155 | 17166 | **+~1100 tok**（schema + 引导段；引导段本身 ≈138 tok / 572 bytes） |

这是 A2/A3 隔离生效的直接证据：A2 只多了工具 schema（tools 数组），A3 再多出
引导段的 ~138 tok——若 `mcp_prompt:false` 未生效，A2 应与 A3 的首 turn 相同。

缓存命中率三臂均在 91.5–93.7%，**MCPSection 没有破坏前缀缓存纪律**；前缀成本
（约 +1.1k tok/turn）被缓存放大约 92% 抵消，但本轮没有任何 MCP 调用产生的收益来抵消它。

## §5.4 四项逐项判定

### ① A3 vs A2：case nl 的 `mcp_calls` 从 ≈0 → 每 run ≥1 —— **FAIL**

- A3-nl：5/5 run `mcp_calls = 0`（不是"平均约 0"，是一次都没有）。
- A2-nl：5/5 run `mcp_calls = 0`（符合"工具存在但无引导"的预期）。
- 排除"工具没注册/引导没进 prompt"：A2 首 turn +960 tok 证明工具 schema 已注册；
  A3 再 +138 tok 证明 MCPSection 字节确实在 prompt 里（§5.2 成本口径同时是隔离校验）。
- 更强的信号：A3-nl 的 5 个 run 的 **reasoning 文本里 0 次出现 semble / MCP / semantic search**
  （`jq -r 'select(.type=="reasoning_delta")…' traces | grep -i`）——模型不是考虑后放弃，
  是根本没把该工具纳入候选。工具选择路径仍是 grep→read，与 A1 同构。

### ② A3 vs A1：case nl 的 `completion_tokens` 降 ≥30% —— **FAIL**

- A1-nl 均值 **1692.8**（同 binary 重采；2026-11-17 旧基线为 1597，用旧值只会更差）。
- A3-nl 均值 **1254.0** → 降幅 **25.9%**，未达 30% 门槛。
- 且 A3 全部 run `mcp_calls=0`，降幅来自轮数/答复长度波动，不归因于 semble。
  按保守口径这就是"未通过"，不做二次挑数。

### ③ A3 case symbol 的 `mcp_calls` 仍 = 0 —— **PASS**

- A3-symbol：5/5 run `mcp_calls = 0`，`max_mcp_calls: 0` 界全部通过；
  grep 1.6–2.0 次、2.6–3.0 个工具调用即可完成——符号定位仍走 grep，引导没有把
  精确符号查询推向语义搜索。防过度引导成立。

### ④ A3 `cache_hit_tokens` 相对 A1 无异常塌方 —— **PASS**

- 命中率（cache_hit / prompt，按 5 run 汇总）：A1 91.5–93.7%，A3 92.3–93.6%。
- MCPSection 是 per-session 静态段，位于前缀尾部，不影响缓存命中形态；
  无任何 run 出现 cache_hit ≈ 0 的塌方。

## Phase 3 触发条件预读（读数据，不做决策）

§5.4 的 Phase 3 触发需要**同时**成立：

1. **A3 case1 仍有 >30% 的 run `mcp_calls = 0`** → 实测 100%（5/5）✅ 满足；
2. **且这些 run 的 `gold_hit` 低于 A1** → A3-nl gold_hit **5/5**，A1-nl 亦 **5/5** ❌ 不满足。

→ 双条件未同时成立，**Phase 3（grep 结果内 hint）按现行规则不触发**。
但 Phase 1 四项里 ①② 未过——本轮数据的直接读数是：**引导未生效，也未造成损害**
（成功率与 A1 持平，成本无收益）。是否因此收尾 Phase 1、改文案、扩样本或开其他卡，
是 D5 的判定，不在本卡范围。

## 观察与 caveat

1. **项目约定与引导竞争**：eval 跑在 seek 自己的仓库，`AGENTS.md` 的
   "Tool usage workflow" 明确要求 *grep first*（`grep → read`），而 MCPSection 的路由文案
   在 prompt 尾部（segment 5）说"自然语言意图 → semble__search"。实测在"精确 file:line
   定位式"提问 + 项目级 grep-first 约定下，tier-2 文案不足以改变工具选择。
   PRD §3.3 已预留此节（"本 repo 的 AGENTS.md 是否加 Semble 条目，等 D4 数据说话"）——
   本轮数据即是该问题的输入。
2. **D3 内置文案与 semble 0.7.3 实际 schema 有漂移**（不影响本轮判定，因模型未调用）：
   自动生成的 factual `Tools:` 行是
   `semble__search(query, repo, top_k?, max_snippet_lines?, content?)` 与
   `semble__find_related(file_path, line, repo, …)`；而 known-server 文案写的是
   `semble__search(repo, query, content?)` 和 `semble__find_related(file, line)`。
   §3.2 golden 与事实参数不符，建议 D3 所有者订正（golden 测试同步）——本卡不改，避免越界。
3. **`mcp_calls` metric 自检**：`tool_start.name` 含 `__` 计数，用合成流验证过
   （`semble__search` → 1）；内置工具名无 `__`（代码里仅测试 fixture 出现）。
4. **无效 run 明示**：a2-mixed 的 1 次瞬时 API EOF 已排除并补跑；行仍在 JSONL 里
   （`agent_ends=0` / `nonzero_exits=1`），统计时按无效过滤。
5. **gold_hit 口径**：§5.2 要求"引用 gold 文件且在窗口内"；a2-nl 的一个 miss 是
   事实正确但不引 gold 文件的答案（见上），属口径边界。

## 复现

```bash
go build -o seek ./cmd/seek
bash eval/tools/mcp-arm-setup.sh a3          # semble 索引预热（一次）
for arm in a1 a2 a3; do for probe in nl symbol mixed; do
  for i in 1 2 3 4 5; do bash eval/run.sh "mcp-semble-routing-$arm-$probe"; done
done; done
bash eval/tools/mcp-semble-score.sh eval/results/<本轮>.jsonl     # gold_hit
bash eval/tools/mcp-first-gold-read.sh eval/tmp/mcp-semble-routing/traces  # §5.2 离线指标
```

## D5（c_erzfzx）交接

- 本卡交付：`mcp_calls` metric（D1 后含 `__` 口径）、9 个三臂 case 目录（现含 expect.json）、
  三臂数据（本文件 + 原始 JSONL）、§5.4 四项逐项结论。
- D5 待做：按 §5.4 判 Phase 3 go/no-go，并把 `docs/prd/feature-mcp-client.md` §四 的
  Phase 3 状态从"数据触发"改写（go 附 hint 卡 / no-go 附数据）。
- D5 的两个关键读数：**① 100% 的 A3-nl run 不用 MCP；② 但 gold_hit 与 A1 持平（5/5 vs 5/5）**，
  Phase 3 双条件未同时成立。
- **主会话补动作**：c_clip4i 的 `eval/run.sh` SEEK_HOME 全局钉死**未进 main**（见「采集参数」），
  卡面「已合并」与仓库现状不符——请合并 `drive/c_clip4i` 后复核 harness 级隔离。
  本卡采集不受影响（case 组 runner 自钉 per-arm 沙盒，隔离探针实证无 eval 写入 `~/.seek`）。
