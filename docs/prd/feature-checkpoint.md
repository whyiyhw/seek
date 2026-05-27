# Feature: 双层 Checkpoint（git + 文件 CAS）

**所属版本**：v3（柱 A · 可恢复性）
**前置阅读**：[PRD v3 umbrella](v3.md) §3（跨柱约束）、[PRD v0](v0.md) §4.7（Permission）、§4.8（Tool）
**状态**：📐 设计稿
**预估工作量**：~5.5 天（M9.0 + M9.1）

---

## 1. 动机

模型动手以后，用户唯一的撤销手段是 git，且要先记得自己处于哪个干净基线。当 bash 工具执行 `rm -rf` 或 edit 工具改坏一个文件，没有一条命令能"把刚才那一步退掉"——这让 `--yolo` 模式心理成本很高，逐步降低了用户的授权深度。

Claude Code 在破坏性操作前会自动 commit + 提供文件级 undo/redo 作为安全网。seek 缺这一层。本 PRD 一次性补齐双层 checkpoint，**两层互补不冗余**：

- **Git checkpoint** 回答"把整个 working tree 退回到 X 时刻"——粒度大、跨工具有效（包括 bash 改的文件、外部脚本改的文件）。
- **文件 checkpoint** 回答"撤销刚才那一次 write/edit"——粒度小、不依赖 git（临时目录里调试也能用）。

两者**目标不同、可单独失败**（无 git 仓库时 git checkpoint 失活但文件 checkpoint 仍有效），UI 上分别叫 `/restore` 和 `/undo`，名字差异本身就承担教学功能。

## 2. 设计目标与不做什么

### 目标

1. **零打扰**——checkpoint 写入不弹 picker、不打断模型回合；UI 上只在状态栏简短显示 "checkpoint #N"。
2. **不污染用户 git 历史**——git checkpoint 用自定义 ref 命名空间（`refs/seek/checkpoints/`），不进 reflog，不动 HEAD，不写 stash list。
3. **不让 `.seek` 目录无限增长**——文件 checkpoint 会话结束默认清理；git checkpoint 进入 git 自己的 GC 周期（90 天默认过期）。
4. **可恢复但不可分支**——`/restore` 是覆盖式回滚，不衍生分支；想做实验请用 git 自己。
5. **失败降级而非阻断**——非 git 仓库时 `/restore` 不可用但 `/undo` 仍工作；blob 缺失时 undo 拒绝但不崩溃。

### 不做什么（v3 明确延后）

- ❌ **跨 session 历史浏览器**——`/checkpoints` 只列当前 session；想找历史用 git reflog 或 session JSONL。
- ❌ **Checkpoint 分支 / 合并**——回滚是单向的；想做实验自己 `git branch`。
- ❌ **文件 checkpoint 跨 session 留存**——会话结束清理（避免 GB 级灾难）；启动加 `--keep-checkpoints` 才保留。
- ❌ **跟 IDE undo 协同**——文件 checkpoint 只追工具调用，不监听文件系统；外部修改导致冲突时 `/undo` 拒绝而非"再撤销"。
- ❌ **远程 push checkpoint**——`refs/seek/` 故意不在 default refspec，避免误传。

## 3. 数据模型

### 3.1 Git checkpoint

**触发**：在 `permission.Policy.Check` 内，当本 turn 是第一次破坏性操作时（`KindWrite` / `KindEdit` / `KindBash` 且 `Action.ReadOnly == false`），且当前目录是 git working tree。

**存储**：用 `git stash create`（不弹到 stash list）生成 commit object，再用 `git update-ref` 写到自定义命名空间：

```
refs/seek/checkpoints/<session-id>/<turn-index>
```

- `session-id` 是当前 session 的 short ID（v1 已存在）
- `turn-index` 从 1 开始单调递增
- 每个 ref 指向一个 commit 对象（来自 `git stash create` 的 working tree snapshot）
- ref 进入 git 自己的 GC 周期，不污染 reflog；用户 `git gc` 时按 `gc.reflogExpire` 默认 90 天清理
- **不在 default refspec**——`git push` 不会误传到远端

**索引**：`<session-dir>/checkpoints.jsonl`（append-only），便于 TUI 拉列表不调 git：

```json
{
  "turn": 3,
  "ts": "2026-06-15T10:24:13Z",
  "ref": "refs/seek/checkpoints/20260615-102331-a3b9/3",
  "head_before": "a8f2c1e",
  "branch": "feat/foo",
  "label": "before bash: rm -rf node_modules"
}
```

`label` 由触发动作生成：第一次破坏操作前的工具名 + args 摘要（≤ 60 字符）。仅用于 UI 显示，回滚不依赖。

**回滚（`/restore <turn>`）**：内部走

```
git read-tree --reset -u <ref>
```

- 重置 working tree + index 到 checkpoint 时的快照
- **HEAD 不动**——用户提交历史完全不变；想彻底"回到那一刻"用户再自己 `git reset --hard`
- 干净的 working tree 检查：如果当前有用户**手动**改动（不是模型改的），先提示 + 要求 `--force`

**Untracked 文件**：`git stash create` 默认不收 untracked，v3 显式带 `--include-untracked`。`.gitignore` 仍生效——`node_modules/` 这种不会进 checkpoint，符合 git 直觉。

**非 git 仓库**：first-turn 检测无 `.git` 时打印一次 hint `"git checkpoint disabled (not a git repo); file checkpoint still active"`，全程 no-op。**不报错**，不阻塞功能。

### 3.2 文件 checkpoint

**触发**：`write.Tool.Execute` 和 `edit.Tool.Execute` 在写入前，**总是**快照原文件内容（包括"文件不存在"的 case——快照存一个 sentinel）。bash 工具**不**触发文件 checkpoint（它的撤销靠 git checkpoint）。

**存储结构**：

```
<session-dir>/checkpoints/
  blobs/
    sha256/<aa>/<bb>/<rest>            # content-addressed blob
  index.jsonl                          # append-only event log
```

**index.jsonl 每行一条事件**：

```json
{
  "seq": 42,
  "ts": "2026-06-15T10:24:13Z",
  "path": "internal/foo/bar.go",
  "kind": "edit",
  "before_sha": "a3b9f1...",
  "after_sha": "d2e8c4...",
  "tool": "edit",
  "tool_call_id": "toolu_01..."
}
```

- `kind`: `create` / `edit` / `undo` / `redo`（write 工具改写既存 = edit；写新文件 = create）
- `before_sha = "0"` 表示"原本不存在"
- `after_sha = "0"` 表示"被删了"（v3 不暴露 delete，schema 预留）

**Undo 语义**：

- `/undo` 不带参数 → 撤销 index.jsonl 最后一条**没被 undo 标记**的事件
- `/undo <path>` → 撤销该路径最近一次事件
- 撤销动作本身也写一行 `kind: "undo"` 事件到 jsonl（避免 stack 状态分裂出独立文件）

**Redo**：撤销后可以 `/redo`，机制是反向消费 `undo` 事件。新的 write/edit 进来会**截断 redo 历史**（编辑器经典语义）。

**外部修改检测**：执行 `/undo` 前先校验当前磁盘内容 sha256 是否等于 `after_sha`。不一致 → 拒绝，提示"file modified externally since last seek edit"，强制覆盖走 `/undo --force`。

**生命周期**：session 关闭时（`SessionEnd` observer）**默认整目录清掉**——文件 checkpoint 是"会话内可逆"工具，不是"长期历史"。提供 `--keep-checkpoints` 启动参数给极少数想保留的用户。

**Blob 去重**：相同内容只存一份。一个 5KB 文件被 edit 10 次平均生成 ~10 blob 但每个 blob 平均 5KB，去重后只增加 1× 文件本体大小（如果每次改的是局部）。

**跳过路径**：以下路径不触发文件 checkpoint（防止噪声）：

- `.seek/` 目录下任何文件（skill / memory / session 自己管理）
- 二进制文件（探测：前 1024 字节含 `\x00` 或非 UTF-8）→ 仅记 path 不存 blob

### 3.3 两层互补对照表

| 维度 | Git checkpoint | 文件 checkpoint |
|---|---|---|
| 粒度 | 整个 working tree | 单文件 |
| 频率 | 每 turn 首次破坏前一次 | 每次 write/edit 都一次 |
| 失效场景 | 非 git 仓库 / `git` 不可用 | 永不失效（纯本地文件操作） |
| 能否撤销 bash 改的文件 | ✅ | ❌（只追 write/edit 工具） |
| 能否撤销编辑器自己改的文件 | ✅（如果在 turn 间发生） | ❌ |
| 跨 session 留存 | ✅（git 管理周期） | ❌（session 结束清理） |
| 用户暴露的命令 | `/restore` | `/undo` `/redo` |
| 实现包 | `internal/checkpoint/git*.go` | `internal/checkpoint/file*.go` |

## 4. CLI 与 TUI 命令

### 4.1 CLI

```
seek checkpoint list [--session <id>] [--json]
    列出 checkpoint（默认当前 session）。输出列：
    turn | ts | label | files-touched-count
    --session all 列所有 session 的 git checkpoint（文件 checkpoint 不跨 session）

seek checkpoint restore <turn> [--session <id>] [--dry-run] [--force]
    git checkpoint 回滚。
    --dry-run：打印将影响的文件不动文件系统
    --force：working tree 有未追踪改动时强制覆盖
    无 git 仓库 → exit 2 + 明确错误

seek checkpoint prune [--before <date>] [--session <id>]
    显式清理 refs/seek/checkpoints/<session>/*；默认清理 90 天前的

seek undo [<path>] [--session <id>] [-n N] [--force]
    文件 checkpoint 撤销。
    无参数：撤销最后一次 write/edit
    带 path：撤销该路径最后一次 write/edit
    -n N：连续撤销 N 步
    --force：忽略外部修改检测

seek redo [<path>] [--session <id>] [-n N]
    对称命令。
```

### 4.2 TUI slash 命令

```
/restore [last|<turn>]              git checkpoint 回滚（无参数 = last）
/checkpoints                        列当前 session 的 git checkpoint（表格）
/undo [<path>]                      文件 checkpoint 撤销
/redo [<path>]                      对称
```

### 4.3 状态栏反馈

每次 checkpoint 写入后，TUI 状态栏右侧短暂闪一次 `✓ checkpoint #N`，2 秒后消失。这是给用户的"安全网在线"信号，不需要任何操作。

## 5. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/permission` | `Check` 内调用 `checkpoint.MaybeCreateGit(ctx, action)`——内部判断是否需要 git checkpoint | 小（一处调用） |
| `internal/tools/write` | 写入前调 `checkpoint.SnapshotFile(path)`；失败不阻断工具（写 warning 到 sink） | 小 |
| `internal/tools/edit` | 同 write | 小 |
| `internal/checkpoint`（新包） | 整个柱 A 的核心：git ref 写入、blob 存储、index 维护、restore/undo/redo 实现 | 中-大 |
| `internal/paths` | 新增 `SessionCheckpointDir(sid)` | 极小 |
| `internal/tui/commands.go` | 注册 4 个新 slash 命令 | 小 |
| `internal/hooks` | 注册 `SessionEndObserver` 用于清理文件 checkpoint 目录 | 小 |
| `cmd/seek` | 新增 `checkpoint` / `undo` / `redo` 子命令 | 中 |
| Session 文件格式 | **不变** | 0 |
| `pkg/` | **不变** | 0 |

### 5.1 Prefix cache 影响

- 写 checkpoint（ref / blob）不进 prompt 字节序列 → **零影响**
- `/restore` `/undo` 改 working tree → 下一 turn 模型 grep 时可能看到变化，但**不改历史 prompt**，cache 命中等同于用户手动改文件

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | 一个 turn 内多次 write 只生成一个 git checkpoint；下一个 turn 的首次 write 再生成一个 | 单元测试（mock git） |
| 2 | 非 git 仓库下 `/restore` 不可用且打印明确错误；`/undo` 仍可用 | 集成测试（tempdir，无 `git init`） |
| 3 | `git read-tree --reset -u <ref>` 恢复 working tree 时 HEAD 不变、用户的提交历史无丢失 | 集成测试（用真 git） |
| 4 | 文件 checkpoint blob 内容寻址：同内容文件多次 edit 只占一份磁盘 | 单元测试 |
| 5 | `/undo` 后立刻 `/redo` 还原；中间发生新 write 则 redo 历史被截断 | 单元测试 |
| 6 | session 结束时 `<session-dir>/checkpoints/` 被清空（除非 `--keep-checkpoints`） | 单元测试 |
| 7 | 外部修改文件后 `/undo` 拒绝且打印 "file modified externally"；`--force` 强制覆盖 | 单元测试 |
| 8 | `.seek/` 路径下的 write 不触发文件 checkpoint | 单元测试 |
| 9 | 二进制文件 write 时 index.jsonl 记一行但不存 blob | 单元测试 |
| 10 | 大文件（10MB+）checkpoint 不会阻塞 UI 超过 1s（异步写入或后台） | 性能测试 |
| 11 | `seek checkpoint list` `--json` 输出可被 jq 解析 | 集成测试 |
| 12 | 现有 v0/v1/v2 测试套件零回归 | 现有测试 |

## 7. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M9.0** | `internal/checkpoint` 包基础：git checkpoint 写入 + 索引 + `/restore` + `seek checkpoint list/restore` CLI | ~3 天 |
| **M9.1** | 文件 checkpoint：blob 存储、index.jsonl、`/undo` `/redo`、session 结束清理；`seek undo/redo` CLI | ~2.5 天 |

**发版策略**：M9.0 + M9.1 合并为 **v0.4.0**——双层一起 ship，避免用户疑问"为什么只有 git 没有文件级"。两层在用户认知里是一个完整功能。

**与其他 feature 的并行关系**：

- 与 [`feature-shell-hooks.md`](feature-shell-hooks.md) 完全独立——前者改 permission/write/edit 工具，后者改 hooks Registry
- 与 [`feature-tui-ergonomics.md`](feature-tui-ergonomics.md) 仅在 `cmdMeta` 注册表层面有交集（都加新 slash 命令），但条目独立无冲突

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 用户的 git 仓库有大量 untracked build artifacts → `git stash create --include-untracked` 拉很慢甚至失败 | 默认尊重 `.gitignore`；状态栏在 stash create > 1s 时提示一次"checkpoint 慢，考虑配 .gitignore"；> 5s 自动降级为 `--no-include-untracked` |
| 文件 checkpoint blob 目录被用户手动删了 → `/undo` 失败 | 检测到缺失时 jsonl 写 `kind: "missing-blob"` 警告，UI 报错"checkpoint 已损坏" |
| `refs/seek/checkpoints/` 不清理 → 仓库 `.git` 目录长大 | 不主动清——git 自己的 `gc.reflogExpire` 会按 90 天默认值过期；文档建议 `git gc` 时它自然消失。提供 `seek checkpoint prune --before <date>` 显式清理 |
| 用户在 turn 之间手动改了文件 → `/restore` 覆盖丢失这些改动 | restore 前检测 working tree 是否有未追踪改动，有则提示 + 要求 `--force` |
| 文件 checkpoint 跟 IDE 的 undo 冲突 | `before_sha` 跟磁盘内容不一致时拒绝 undo，强制覆盖走 `--force` |
| 大量小文件（如 monorepo 一次性 edit 50 个文件） → blob 目录 inode 暴增 | sha256 两层 sharding（`<aa>/<bb>/<rest>`）天然分散；监测 inode 数量超 10000 时打印 warning |
| `git stash create` 在某些 git 版本（< 2.20）行为不一致 | 启动时 `git --version` 检测，< 2.20 打印 warning 但不阻断 |

## 9. 后续版本

- **v4**：Checkpoint 跨 session 浏览（`/checkpoints --all-sessions`）+ 跨 session 文件 undo
- **v4**：Checkpoint diff 浏览（`/checkpoints diff <turn>` 显示该 checkpoint 与当前的 diff）
- **v5**：Checkpoint 分支（从历史 checkpoint 衍生新 working tree 副本，可能配合 worktree）
