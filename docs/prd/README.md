# seek 产品需求文档（PRD）

本目录按版本组织 PRD，每个版本独立成文，避免新旧需求混杂。

## 版本一览

| 文件 | 对应版本 | 状态 | 说明 |
|------|---------|------|------|
| [`v0.md`](v0.md) | seek v0.0.x ~ v0.1.2 | ✅ 已归档 | 初始版本，M0–M7 全部交付。包含核心设计决策、架构分层原则、里程碑记录。新功能开发时作为架构参考，不再追加新需求。 |
| [`v1.md`](v1.md) | seek v0.2.x | ✅ 已归档 | 三层认知记忆子系统（L/M/S）+ 自动化层。M5.0–M5.8 全部交付，commit `08660a1` → `374cfad`。 |
| [`v2.md`](v2.md) | seek v0.3.x（目标） | ✅ 已交付 | Skill 生命周期管理：目录包、install/uninstall/update CLI、本地调用统计。M8.0–M8.7 全部交付，commit `b7d7996` → `75dae10`。 |
| — | seek v0.3.x+（扩展） | ✅ 已交付 | AI 侧 skill 安装（`skill_fetch`/`skill_commit` 工具）、`ask_user` TUI picker 工具、`/plan` 只读模式、`/steer`/`/review` 命令、`@-highlight`、skill 安装 scope 选择（user vs project）。 |
| [`feature-mcp-client.md`](feature-mcp-client.md) | M5.4（已交付）+ 规划中 | ✅ MCP infra 已交付 / 📐 深度集成设计中 | MCP 客户端（pkg/mcp/）已于 M5.4 交付。本文是后续深度集成设计：以 Semble 为第一验证目标，定义 prompt 引导、工具路由、效果评估方案。参见 `docs/book/chapter-12.md`。 |

## 阅读指引

- **第一次了解 seek 架构**：先读 `v0.md` §1–4（设计决策比交付记录更重要）
- **参与当前开发**：读路线图（`../README.md` §路线图）或查看最近 commit 了解最新交付
- **想知道 Memory 怎么工作**：读 `v1.md` —— 已交付 reference，不再追加需求
- **追 bug / 理解某个包为什么这样设计**：回 `v0.md` 查对应章节 + `docs/book/` 系统设计书

## 版本演进规则

1. 每个大版本一个独立 `.md` 文件，不追加已有文件。
2. 版本之间是**叠加关系**——v1 依赖 v0 的架构基础，除非显式说明"此决策在 v1 中已变更"。
3. 一个版本交付后，其 PRD 标记为已归档，不再修改（勘误除外）。
