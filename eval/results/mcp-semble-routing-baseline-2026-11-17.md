# mcp-semble-routing 三臂对照 — 采集状态与 Phase 1 判定数据

> **已被 [`mcp-semble-routing-three-arm-2026-11-18.md`](mcp-semble-routing-three-arm-2026-11-18.md) 取代**
> （三臂完整数据，A1 同 binary 重采）。本文件保留为 2026-11-17 的采样记录与 A1-only
> 基线历史。

Card **MCP-D4**（c_eyuxqm）· 设计依据 `docs/prd/feature-mcp-client.md` §5（d25aba8 设计稿，
main 尚为旧版 PRD——回填判定时以合并后的 §5.4 为准）。

## 采集状态（2026-11-17）

| 臂 | 环境 | 状态 | 说明 |
|----|------|------|------|
| **A1** 基线 | scratch SEEK_HOME，无 mcp.json | ✅ 已采（3 case × 5 run，15/15 PASS） | 任何树上有效：不涉 MCP 面 |
| **A2** semble 无引导 | mcp.json 配 semble，`mcp_prompt:false` | ⏸ 待采 | 需 **D1**（`server__tool` 前缀——否则 `mcp_calls` 恒 0，模型真调了也计不出）；采集前将 `mcp_prompt` 字段位置与 D3 落地schema 对齐 |
| **A3** semble + 引导 | 同上，引导开 | ⏸ 待采 | 需 **D1 + D3 文案终版**（卡面约定：D3 文案终版后重跑 A3） |

采集参数：binary `v0.11.1-0.20261117130010-27a566e7972b+dirty`（树 = 27a566e + 本卡 eval 改动，
gold 锚点不受影响）；repo = 本 worktree；行入库 `2026-11-17-v0.11.1-...-27a566e_.jsonl`；
评分 `eval/tools/mcp-semble-score.sh`（gold_hit，动态锚点）+ 人工抽检（每 probe 首条 final_text 已核，
三 case 引用与解释均正确）。

## A1 基线（每格 = 5 run 均值）

| 指标 | a1-nl（NL 定位·正例） | a1-symbol（符号·反例） | a1-mixed（混合） |
|------|----------------------|----------------------|-----------------|
| gold_hit | **5/5** | **5/5** | **5/5** |
| mcp_calls | 0（按构造） | 0（按构造） | 0（按构造） |
| grep_calls | 5.0 | 1.0 | 2.0 |
| read_calls | 3.2 | 1.0 | 1.2 |
| total_tool_calls | 8.2 | 2.0 | 3.8 |
| turns | 5.8 | 3.0 | 3.8 |
| completion_tokens | **1597** | 281 | 747 |
| prompt_tokens | 125561 | 51601 | 71653 |
| cache_hit_tokens | 113715 | 49434 | 66637 |
| elapsed_s | 14.8 | 5.6 | 8.6 |

基线解读：三 probe 全部可被 grep 工作流稳定解出（gold_hit 15/15）——A3 对 A1 的比较空间在
**成本**（§5.4 #2 的 completion_tokens），不在成功率。nl 是唯一 grep 需多轮改写查询的 probe
（5 grep / 1597 completion tokens），是语义搜索理论收益所在；symbol 已是最优形态（1 grep + 1 read /
281 tokens），A3 若把 semble 推到这里即纯浪费（#3 防）；mixed 居中。

## §5.4 四项 Phase 1 判定（状态）

| # | 判定 | 现状 |
|---|------|------|
| 1 | A3 vs A2：case nl 的 mcp_calls 从 ≈0 → 每 run ≥1 | ⏸ 待 A2/A3 数据 |
| 2 | A3 vs A1：case nl 的 completion_tokens 降 ≥30% | ⏸ 待 A3。**A1 侧输入已定：1597**（降 30% ⇒ A3 需 ≤1118） |
| 3 | A3 case symbol 的 mcp_calls 仍 = 0 | ⏸ 待 A3 |
| 4 | A3 cache_hit_tokens 相对 A1 无异常塌方 | ⏸ 待 A3。A1 侧参照：nl 113715 / symbol 49434 / mixed 66637 |

## 下一步（重跑清单）

1. D1（c_4ejhhs）落地后：a2 三 case 目录按 README 模板补回 expect.json（臂门闩即解除），
   `for i in 1..5; eval/run.sh mcp-semble-routing-a2-{nl,symbol,mixed}`；采集前先对齐 D3 的
   `mcp_prompt` 字段 schema（runner 现按 server 级写入，见 `eval/tools/mcp-arm-runner.sh` 头注）。
2. D3（c_lgtpgq）文案终版后：同法补 a3 expect.json 并重跑（setup.sh 自动预热 semble 索引）。
3. 数据齐后：重跑 `mcp-semble-score.sh` + 本表更新 + §5.4 四项逐项判定 → 交 D5（c_erzfzx）读数。

## 臂门闩（arm gate）

a2/a3 case 目录**不带 expect.json**（suite 脚手架惯例：无 expect.json 即跳过）——D1/D3 落地前
全量 `eval/run.sh` 不会误跑无效臂、不烧 API。bounds 以模板形式存各 README，采集时落回。
