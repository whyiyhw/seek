# seek 产品需求文档（PRD）

本目录按版本组织 PRD，每个版本独立成文，避免新旧需求混杂。

## 版本一览

| 文件 | 对应版本 | 状态 | 说明 |
|------|---------|------|------|
| [`v0.md`](v0.md) | seek v0.0.x ~ v0.1.2 | ✅ 已归档 | 初始版本，M0–M8 全部交付。包含核心设计决策、架构分层原则、里程碑记录。新功能开发时作为架构参考，不再追加新需求。 |
| [`v1.md`](v1.md) | seek v0.2.x | ✅ 已归档 | 三层认知记忆子系统（L/M/S）+ 自动化层。M5.0–M5.8 全部交付，commit `08660a1` → `374cfad`。 |
| [`v2.md`](v2.md) | seek v0.3.x（目标） | ✅ 已交付 | Skill 生命周期管理：目录包、install/uninstall/update CLI、本地调用统计。M8.0–M8.7 全部交付，commit `b7d7996` → `75dae10`。 |
| — | seek v0.3.x+（扩展） | ✅ 已交付 | AI 侧 skill 安装（`skill_fetch`/`skill_commit` 工具）、`ask_user` TUI picker 工具、`/plan` 只读模式、`/steer`/`/review` 命令、`@-highlight`、skill 安装 scope 选择（user vs project）。 |
| [`v3.md`](v3.md) | seek v0.4.x | ✅ 已交付 | 可逆性、可扩展性、可定制性三柱的总览。详细设计拆到下面三个 feature PRD；本文只维护跨柱设计约束（prefix-cache 字节确定性、hooks Registry 复用、信任前置）+ 总体实现计划。M9.0–M9.5 全部交付。 |
| [`feature-checkpoint.md`](feature-checkpoint.md) | seek v0.4.0（柱 A）| 🚀 已交付 | 双层 checkpoint：git ref 命名空间快照 working tree（`/restore`）+ 文件 CAS undo/redo（`/undo` `/redo`）。两层互补，分别用 `git read-tree` 和 content-addressed blob 实现。M9.0 + M9.1。 |
| [`feature-shell-hooks.md`](feature-shell-hooks.md) | seek v0.4.1（柱 B）| 🚀 已交付 | 用户可配置的 shell hooks，作为已有 `internal/hooks` Registry 的实现注册（ShellRunner）。只允许 deny 或 observe，不允许改 prompt 字节。VS Code 风格 trust 询问。M9.2 + M9.3。 |
| [`feature-tui-ergonomics.md`](feature-tui-ergonomics.md) | seek v0.4.2（柱 C）| 🚀 已交付 | TUI 工效：斜杠命令 Tab 补全（复用 cmdMeta + picker）+ 自定义 keybindings（封闭 action 集合，toml 配置）。M9.4 + M9.5。 |
| [`v4.md`](v4.md) | seek v0.5.x（目标） | 📐 umbrella | 闭环工效（自校准）的总览。柱 D 是 next-turn 预测 + mispredict 反向注入。本文维护 v4 新增的跨柱约束（旁路调用不污染 transcript、单一开关贯穿全链路）。 |
| [`feature-suggested-reply.md`](feature-suggested-reply.md) | seek v0.5.0（柱 D）| 📐 设计稿 | 模型在 LLM 回合末尾走旁路调用预测用户下一句，TUI 显示淡灰 placeholder，Tab 接受填入输入框。Mispredict 时下一轮注入 calibration system note 让模型自校准。单一 `--no-suggest` 开关贯穿。M10.0。 |
| [`v5.md`](v5.md) | seek v0.6.x | 🚀 umbrella（已交付） | 代理编排的总览。柱 G 是子代理 + worktree 隔离（空间维度），柱 H 是 cron / wakeup / push（时序维度）。本文维护 v5 新增的跨柱约束（子转录字节隔离、权限单调收紧、零常驻进程、编排状态 transcript-外存）。M11.0–M11.3 已交付；M11.4+ 待定。 |
| [`feature-subagent.md`](feature-subagent.md) | seek v0.6.0（柱 G）| 🚀 已交付 | 子代理 + Worktree 隔离：`agent` / `enter_worktree` / `exit_worktree` 三工具 + 三个 `subagent_type` 模板（general-purpose / explore / plan）+ `Policy.Spawn` 权限单调收紧 + `Tracker.AdoptChild` 成本归集 + `/agents` `/worktrees` 面板 + `seek worktree` CLI。M11.0 + M11.1。 |
| [`feature-routines.md`](feature-routines.md) | seek v0.6.1（柱 H）| 🚀 已交付 | 时序触发 routines：`seek cron {create,list,delete,run,tick}` CLI + `jobs.jsonl` 事件存储 + 零 daemon（OS scheduler 拉起 tick）+ `schedule_wakeup` LLM 工具 + OS 原生通知（osascript / notify-send / Windows toast）+ `triggers/` 文件桥 + GC + env overlay。M11.2 + M11.3。 |
| [`v6.md`](v6.md) | seek v0.7.x | 📐 umbrella | 单点工具补齐（P1 closure）的总览。柱 I-M：AskUserQuestion v2 / 复合 code-review skill / Monitor + 后台 bash / LSP 工具 / 移动端 webhook 桥。5 项**互不耦合**——本文负责排序 + 估时 + 依赖梳理，每柱进入实施前再单独起 feature PRD。预估 ~4 周走完整波，对应 v0.7.0 / v0.7.1 / v0.7.2 三段。 |
| [`feature-askuser-v2.md`](feature-askuser-v2.md) | seek v0.7.0（柱 I）| 🚀 已交付 | AskUserQuestion v2：多题 stack（1–4 题/调用）+ `preview` 字段（侧栏渲染 mockup）+ `header` 字段。**polymorphic schema 单工具**——v1 单题 schema 零破坏。Phase 1 schema+API (`cb8ed16`)、phase 2a TUI stack (`5b775fe`)、phase 2b preview panel（本提交）。已校 v6 §3.1 事实错误（multiSelect + "Other" 已在 v1）。 |
| [`feature-code-review.md`](feature-code-review.md) | seek v0.7.0（柱 J）| 🚀 已交付 | 复合 code-review skill：内置 `code-review.md`（方法论 + 4 档 effort framing + `--fix`/`--comment` 工作流）+ `/code-review [low\|medium\|high\|max] [--fix] [--comment] [branch]` slash 命令（参数解析，复用 `/review` 的 diff 采集 + picker）。**职责切分**：参数进命令、方法论进 skill Body——因 `Skill` 工具 schema 只有 `{name}`，effort/flag 传不进去（不扩被 prefix-cache 钉死的 schema）。`--fix` 复用 plan-mode v2 propose 路径，`/review` 收敛为 `/code-review medium` 别名。已校 v6 §3.2 三条隐含假设。 |
| [`feature-inspect-rpc.md`](feature-inspect-rpc.md) | seek v0.6.x dot → v0.7.0 | 📐 设计稿 | Inspect RPC + Web 面板：扩展 `--rpc` JSON-RPC 2.0 read-only method 集（session/memory/project/hooks/stats）+ HTTP+SSE transport + 静态 Web 面板（与主 binary 解耦发布）。作为未来应用的数据平面前置验证。M12.0（独立于 v5）+ M12.1/M12.2（依赖 v5）。 |
| [`feature-mcp-client.md`](feature-mcp-client.md) | M5.4（已交付）+ 规划中 | ✅ MCP infra 已交付 / 📐 深度集成设计中 | MCP 客户端（pkg/mcp/）已于 M5.4 交付。本文是后续深度集成设计：以 Semble 为第一验证目标，定义 prompt 引导、工具路由、效果评估方案。参见 `docs/book/chapter-12.md`。 |
| [`feature-plan-mode.md`](feature-plan-mode.md) | seek v0.3.x+ | 🚀 已交付 | Plan 模式 v2：ANALYZE → propose → approve → EXECUTE → re-plan 闭环。propose/plan 工具、mode reminder 多态（plan-analyze/plan-execute）、permission 子态联动、TUI task list 面板、event-sourcing resume。替代旧的 [`archive/feature-plan-tasklist.md`](archive/feature-plan-tasklist.md)。 |
| [`feature-webfetch.md`](feature-webfetch.md) | seek v0.3.x+ | 🚀 已交付 | WebFetch HTTPS GET 工具，专为 plan-analyze 设计的 SSRF 防御外部文档读取路径。gzip 解码 + HTML 渲染 + 私网 IP 拦截。 |
| [`feature-permission-refactor.md`](feature-permission-refactor.md) | seek v0.3.x+ | 🚀 R1+R1.1 已交付 | Permission 两轴重构：Preference/Workflow 拆分、TUI 元数据剥离、preApproved 封装。 |
| [`feature-edit-read-before.md`](feature-edit-read-before.md) | 持续 | 📝 方案笔记 | Edit 前强制 read 的软性提示已通过 workflowReminder 实施；结构性方案（如 schema 层约束）待评估。 |
| [`vision.md`](vision.md) | 长期（3–5 年） | ⭐ 北向星 | seek 的未来愿景：从编程助手演化为本地计算机智能终端。三层架构（CLI→MCP→系统应用）、视觉闭环、方向而非路线图。 |

> **归档 PRD**：被取代或彻底废弃的设计移到 [`archive/`](archive/) —— 历史推演保留，但不再算路线图条目。当前 1 个：`feature-plan-tasklist.md`（被 `feature-plan-mode.md` 取代）。

## 阅读指引

- **第一次了解 seek 架构**：先读 `v0.md` §1–4（设计决策比交付记录更重要）
- **参与当前开发**：读路线图（`../README.md` §路线图）或查看最近 commit 了解最新交付
- **想知道 Memory 怎么工作**：读 `v1.md` —— 已交付 reference，不再追加需求
- **追 bug / 理解某个包为什么这样设计**：回 `v0.md` 查对应章节 + `docs/book/` 系统设计书

## 版本演进规则

1. 每个大版本一个独立 `.md` 文件，不追加已有文件。
2. 版本之间是**叠加关系**——v1 依赖 v0 的架构基础，除非显式说明"此决策在 v1 中已变更"。
3. 一个版本交付后，其 PRD 标记为已归档，不再修改（勘误除外）。

## 未来愿景

[`vision.md`](vision.md) 描述了 seek 的长期北向星——从编程助手演化为**本地计算机智能终端**。它不是某个版本的路线图，而是指导远期决策的方向框架，包括三层架构（CLI 核心 → MCP 桥接 → 系统原生应用）、视觉闭环（看见→理解→规划→执行→验证）、以及当前架构与这一愿景之间的差距分析。

愿景文档不随版本迭代更新，只随认知跃迁而修订。
