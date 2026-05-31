# seek 安全网 — 双层 Checkpoint / Two-layer safety net

seek 内置了**双层 checkpoint 安全网**，让你在模型动过代码后可以随时撤回——不管是整个工作区回滚到上一轮，还是单文件撤销上一步 write/edit。

> seek has a two-layer safety net that lets you undo model changes at two granularities: roll back the entire working tree to the previous turn, or revert a single file to before the last write/edit.

设计决策与验收标准见 [`docs/prd/feature-checkpoint.md`](prd/feature-checkpoint.md)。

---

## 1. 两层安全网 / Two layers

| 层 | 粒度 | 命令 | 依赖 |
|---|---|---|---|
| **Git checkpoint** | 整轮（跨工具） | `/checkpoints`／`/restore`（TUI）／`seek checkpoint`（CLI） | git 仓库 + `git` 二进制 |
| **文件 checkpoint** | 单次 write/edit | `/undo`／`/redo`（TUI）／`seek undo`／`seek redo`（CLI） | 无（独立于 git） |

两层**互补不冗余**：Git checkpoint 覆盖 bash 改的文件、外部脚本改的文件；文件 checkpoint 不依赖 git，临时目录里也能用。两者可单独失败（无 git 仓库时 git checkpoint 失活但文件 checkpoint 仍有效）。

---

## 2. Git checkpoint — 整轮回滚 / Per-turn snapshots

每当模型发起**第一个破坏性操作**（write / edit / 带副作用的 bash），seek 自动快照工作区——通过 `git stash create` + `git update-ref` 将当前工作树的状态固定到 `refs/seek/checkpoints/<session-id>/<turn>`。

### 查看检查点 / List

```bash
# TUI 中
/checkpoints

# CLI 中
seek checkpoint list
seek checkpoint list --session <id>   # 指定会话（默认最近更新的）
seek checkpoint list --json           # JSONL 格式输出
```

### 回滚 / Restore

```bash
# TUI 中 — 无参数恢复到最新检查点，或指定轮次
/restore             # 恢复到最新检查点
/restore 3           # 恢复到第 3 轮
/restore last        # 同无参数

# CLI 中 — 指定轮次
seek checkpoint restore last
seek checkpoint restore 3
seek checkpoint restore 3 --session <id>
```

回滚用 `git read-tree --reset -u <ref>` 完成，**覆盖式回滚**（不衍生分支——想保留时间线请用 git 自己）。

### 清理 / Prune

检查点由 git 自身的 GC 管理（默认 90 天过期）。也可以手动清理：

```bash
seek checkpoint prune --before 2025-01-01
```

---

## 3. 文件 checkpoint — 单步撤销 / Per-file undo/redo

每次 write/edit 前，seek **自动备份旧文件内容**到内容寻址 blob（SHA-256 哈希），并记录事件日志。支持撤销（undo）和重做（redo）。

### 撤销 / Undo

```bash
# TUI 中
/undo          # 撤销最近一次 write/edit（全局）
/undo <path>   # 撤销对指定文件的最近一次 write/edit

# CLI 中
seek undo
seek undo -n 3        # 撤销最近的 3 步
seek undo <path>      # 仅撤销指定文件
```

### 重做 / Redo

```bash
# TUI 中
/redo          # 重做最近一次被撤销的变更
/redo <path>   # 重做对指定文件的最近一次撤销

# CLI 中
seek redo
seek redo -n 3        # 重做最近的 3 步
```

### 工作机制

1. **SnapshotFile**——write/edit 工具在修改前调用快照，将原文件内容存入 `<session-dir>/checkpoints/blobs/sha256/<aa>/<bb>/<rest>`（内容相同则共享 blob）
2. **FinaliseSnapshot**——写入后记录"修改后内容"的哈希，形成 complete event
3. **/undo**——反向遍历事件日志，跳过已撤销的，恢复 blob → 工作区
4. **/redo**——正向遍历，恢复最近一次 undo 撤销的变更
5. **外部修改检测**——恢复前重新计算当前文件哈希，若与预期不符则拒绝（除非 `--force`）

> **注意**：新的 write/edit 操作会截断重做历史（经典编辑器语义）。

---

## 4. 配置 / Configuration

文件 checkpoint 默认绑定到当前会话生命周期——会话结束时清理 blob。保留检查点：

```bash
seek --keep-checkpoints   # 保留 <session>/checkpoints/ 目录
```

---

## 5. CLI 参考 / CLI reference

```text
seek checkpoint — 检查安全网层

用法:
  seek checkpoint <command> [flags] [args]

命令:
  list                   列出某个会话的 git 检查点（默认最近更新的会话）
  restore <turn>         将工作树恢复到指定检查点
  prune --before <date>  删除早于某个日期的检查点 ref（RFC3339 或 YYYY-MM-DD）

共享标志:
  --session <id>         操作特定会话（默认最近更新的会话）
  --json                 （list 时）输出 JSONL

另见:
  seek undo / seek redo  文件级撤销/重做
```

### TUI 斜杠命令

| 命令 | 作用 |
|---|---|
| `/undo` | 撤销最近一次 write/edit |
| `/undo <path>` | 撤销对指定文件的最近一次 write/edit |
| `/redo` | 重做最近一次被撤销的变更 |
| `/redo <path>` | 重做对指定文件的最近一次撤销 |
| `/restore` | 回滚到最新 git checkpoint（或 `/restore <turn>` 指定轮） |
| `/checkpoints` | 列出当前会话的 git 检查点 |

---

## 6. 设计要点 / Design notes

- **零打扰**——checkpoint 写入不弹 picker、不打断模型回合；TUI 状态栏简短显示 "✓ checkpoint #N"
- **不污染用户 git 历史**——使用 `refs/seek/checkpoints/` 自定义 ref 命名空间，不动 HEAD 和 stash
- **优雅降级**——非 git 目录时 git checkpoint 静默跳过，文件 checkpoint 仍工作
- **内容寻址去重**——相同内容的文件只存储一次 blob
- **See also**：`docs/guide-references.md`——`references` 工具用 LSP 做语义符号引用查找（checkpoint 的 undo/redo 保护这些查找后的编辑操作）
