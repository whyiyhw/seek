# whyiyhw/seek 项目叙事稿

> 数据截至 2026-11-12（北京）；负责人会话（agent，2026-11-12 用户全权移交）。
> 模板与写作纪律见 butler 仓 `docs/narrative/README.md`；事件 `#N` 指 butler 事件 id（memory_search 可查）。

## 目标

**以 projects 表为准**（`project_get whyiyhw/seek`，2026-11-12 落库）：把 seek 从终端
coding agent 核心迭代成**能自我进化的本地 agent 平台**——① 能力对标 Claude Code、
成本断崖（DeepSeek-first）；② 多端通用（`Frontend` 端口，飞书第一个）；③ 自进化闭环
（失败 → eval → 修复 → 发布）。治理层是 butler（卡片/派发/验证/合并），
seek 是执行与产品层；seek 自身也通过 paseo provider 车道回到 butler 的调度池。
**不做公开跑分竞赛**（用户拍板 #2575：无意义；eval 降位为内部回归工具）。

## 现状

- v0.10.3，主干干净；与 Claude Code 的能力对照见 `docs/comparison.md`
  （定位变更：从 roadmap 驱动**降级为参考**，不再逐项追平——#2575 口径）。
- **自进化面已有底子**：skills v2 生命周期（skill/skillmgr/skillstats）、记忆 L/M/S
  + decay GC、`-dream`/`/distill` 蒸馏、pitfalls 台账（commit trailer 双信号）、
  eval harness（14 用例，黑盒行为对比）、goal 达标循环、checkpoint 双层、自升级。
- **多端种子已入库**：`docs/prd/feature-remote-control-plane.md`（📐 Frontend 端口 +
  `seek serve` sidecar，核心零改动）；ACP 已交付并经 Zed 实测（`docs/prd/feature-acp.md`）
  ——paseo provider 接入有现成先例（zcode-acp 桥）。
- **缺口**：失败→eval 用例无自动管道；skill 不会自动蒸馏；事件溯源不变量
  （model-visible ⟺ durably logged）只覆盖 plan 子系统；多端只有纸面设计。

## 关键决策

- **平台化取代跑分对标**（#2575，2026-11-12 用户拍板）：目标=自我进化平台 + CC 能力
  + 更便宜 + 多端；eval 只做内部回归。
- **移交 agent 全权管理**（#2575）：butler 当治理层，开发走卡片/派发/验证流程；
  本仓 AGENTS.md 纪律（pitfalls、doc-budget、eval-first）不变。
- **参考 dsh 不照抄**（`docs/dsh-analysis.md` §0/§8）：取「事件溯源 ⇒ 前缀缓存稳定」
  的不变量思想与可替换缝；弃 226 包 monorepo 与运行时组装复杂度——
  seek 的优势正是编译期纪律 + 单二进制。
- **多端走 sidecar 不动核心**（feature-remote-control-plane.md D-C1）：
  常驻只活在 `seek serve`，对零-daemon 铁律做有意、隔离的偏离。

## 否掉的方案

- **跑分超越作为目标**：用户否（#2575）——没有意义。
- **无脑抄 dsh 插件框架**：dsh-analysis.md 结论——两条赛道（官方云 harness vs 本地
  单机开发者），编译期注册是 seek 的护城河不是债。
- **把常驻塞进核心 agent loop**：feature-remote-control-plane.md §1.4，违背 v5 零-daemon。
- **记分牌式同题对跑（公开口径）**：#2573 待拍版方案的核心卖点，#2575 拍板后降位为
  内部回归，不对外宣传。

## 下一步

- **内环（自进化）**：pitfall→eval 半自动管道（c_w7kob6）→ skill 自动蒸馏设计（待开卡）。
- **多端**：control-plane 三项待验证坐实（c_7hudp6）→ M1 平面骨架（worktree 并行可期）。
- **治理接入**：seek 作 paseo provider 可行性设计（c_27i3zi）→ 落地后成为 butler
  最便宜执行车道，dogfood 飞轮（seek 干活 → 失败沉淀 eval → seek 变强）转起来。
