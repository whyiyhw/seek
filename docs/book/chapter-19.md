# 第 19 章：M9.0–M9.1 — 双层 Checkpoint 安全网

> **对应版本**：v0.4.0
> **对应代码**：`internal/checkpoint/`、`internal/checkpointcli/`、`internal/paths`
> **PRD**：[`docs/prd/feature-checkpoint.md`](../prd/feature-checkpoint.md)
> **验收**：12/12 验收标准全部通过，`go test -race` 全绿
> **起点**：第 18 章（Plan Mode 确认门）。模型有了一套成熟的交互模式（analyze → propose → execute），但所有破坏性操作（write/edit/bash）仍然是"出去就回不来的"。

---

## 内容预告

本章待撰写。核心内容：

1. **问题**：不可逆的破坏性操作让 `--yolo` 心理成本很高
2. **两层互补设计**：
   - **Git checkpoint**（粗粒度）：每 turn 首次破坏前用 `git stash create` 快照整个 working tree，写入 `refs/seek/checkpoints/<session-id>/<turn-index>` 自定义 ref 命名空间，不进 reflog，不污染 HEAD
   - **文件 checkpoint**（细粒度）：每次 write/edit 前 content-addressed SHA-256 blob 快照源文件，支持 `/undo` `/redo`，外部修改检测
3. **CLI 命令**：`seek checkpoint {list,restore,prune}` · `seek undo` · `seek redo`
4. **TUI 斜杠命令**：`/checkpoints` · `/restore` · `/undo` · `/redo`
5. **风险与降级**：非 git 仓库 `/restore` 不可用但 `/undo` 仍工作；文件 checkpoint 随 session 清理

**关键提交**：`28f2337`（基础框架 + 13 单元测试） → `26d1277`（CLI/TUI/agent 接入）

阅读本章前建议先读 PRD，再 `go test -race ./internal/checkpoint/... ./internal/checkpointcli/...` 跑一遍验收测试理解边界行为。
