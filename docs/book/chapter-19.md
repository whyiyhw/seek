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

### 相关踩坑

本章涉及的文件系统并发问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. 两 Writer 同时写入相同内容寻址 blob 竞争同个 .tmp 文件名**

- **Saw**：checkpoint 包的并发快照测试失败，约一半事件丢失，日志报 "rename ... .tmp ... : no such file or directory"。
- **Why**：`storeBlobLocked(sha, content)` 使用固定 `<bp>.tmp` 文件名后 `os.Rename(tmp, bp)`。两 goroutine 哈希到相同内容后：（a）Stat—miss；（b）WriteFile(tmp)；（c）Rename(tmp, bp)。后者的 Rename 失败因为 `.tmp` 已被前者 Rename 走（路径不存在而非 fd 问题）。
- **Fix**：用 `os.CreateTemp(dir, base+".tmp.*")` 替代固定文件名，每 writer 独立临时文件。Rename 失败时检查目标是否已存在并静默丢弃自己的临时副本。
- **Lesson**：内容寻址存储对"丢失竞争"宽容（胜者的字节等于你的），但对"共享 .tmp 文件名"不宽容。要么每 writer 独立临时文件，要么每 blob 一把锁。

**2. 原子自替换需要同文件系统临时文件**

- **Saw**：`os.Rename` 跨文件系统挂载点时返回 "cross-device link" 错误。
- **Lesson**：原子写模式必须确保 tmp 文件与目标在同一挂载点。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。
