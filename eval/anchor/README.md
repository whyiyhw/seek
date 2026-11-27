# eval/anchor/ — 跨 harness 对照（私有，#2575）

**边界（先读）**：本目录属于 eval v3 A3（`docs/prd/archive/feature-eval-v3.md` §2.2）。
#2575（2026-11-12 用户决定）禁止公开跑分——本目录的任何数字**永不发布**：
不进 README、不进 release notes、不做 leaderboard、不给外部对照引用。它是
**私有外部校准**：回答「同一模型、同一任务集下，seek 这个 harness 相对
其它 harness 的净值」，仅供本仓的版本间趋势与 harness 设计决策使用。

## 纪律

- **对照产物只落本目录**（`eval/anchor/`），结构与 `eval/ablation/` 同款：
  永不进 `eval/results/`——那边是 trend 数据源，混入即伪造回归。
- **唯一被隔离变量是 harness**：任务集（`eval/cases/anchor-*/`）、模型、
  provider 车道必须与 seek 侧锚点跑完全一致（同一订阅车道、同一模型 id）。
- 跑法见 `eval/tools/anchor-crossharness.sh`；环境没有对方 CLI 时脚本降级
  为打印手工协议（见下）。

## 手工协议（CLI 缺席时的降级路径）

1. `git status` 干净，记下 seek 侧最近的锚点行（`eval/results/` 里
   `anchor-*` 的聚合行）。
2. 对每题 `eval/cases/anchor-<name>/`：
   - 复制 `workspace/` 到一个 scratch 目录，cd 进去；
   - 用对方 harness 跑 `prompt.txt` 的原文（同模型、同订阅车道、
     等价的最大轮数/自动批准设置——各 harness 的旗标自查其文档）；
   - 在 scratch 里跑 `bash <case>/check.sh`，记 solved 与耗时。
3. 结果手写成 `cross-<UTC日期>-<harness>.md`（表：task / solved /
   turns 或 tokens / 备注），只落本目录。

## 对照读法

- 单题差 1 次解出**不是信号**（任务级噪声 ≥ 行为用例）；按
  「多轮 × 多题」的解出率差读，且与 seek 侧同轮数的行对照。
- seek 侧基线在 `eval/results/`（`--lane anchor` 的行）；对方侧在本目录。
  两侧的 check.sh 是同一份——判分不可作弊面。
