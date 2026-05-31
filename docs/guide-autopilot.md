# seek Autopilot — 无人值守编排 / Unattended orchestration

Autopilot 让 seek **在无人值守时自主完成复杂任务**：接收一个目标 → 分解为子任务 → 并行 spawn 隔离的 worktree 子代理 → 聚合结果 → 本地 commit。它把你的项目变成一个**睡觉时也在工作的团队**。

> Autopilot lets seek complete complex goals **while you're away**: take a goal → decompose → parallel worktree sub-agents → aggregate → local commit. It turns your project into a **team that works while you sleep**.

设计与决策见 [`docs/prd/feature-autopilot.md`](prd/feature-autopilot.md)。

---

## 1. 快速开始 / Quick start

```bash
# 一条命令完成：分解→并行 worktree fleet→commit→报告
seek autopilot run "重构 User 模型，添加邮箱验证字段，更新相关 service 和 test"

# 查看结果摘要（含 per-task commit SHA）
→ [autopilot] 4/4 tasks succeeded
  User model: 添加 email_verified 字段 + 迁移 (abc1234)
  Service: 添加 verifyEmail 方法 (def5678)
  Tests: User + Service 单元测试 (ghi9012)
  Docs: 更新字段文档 (jkl3456)
```

### 工作原理

```
"重构 User 模型…"
     │
     ▼
  Decomposer (DeepSeek) ──→ 任务列表
     │                        ├─ 1. User model 字段
     │                        ├─ 2. Service 方法
     │                        ├─ 3. Test 用例
     │                        └─ 4. 文档更新
     │
     ▼
  Fleet (并行 worktree 子代理)
     ├─ worktree-1 → edit → 本地 commit
     ├─ worktree-2 → edit → 本地 commit
     ├─ worktree-3 → edit → 本地 commit
     └─ worktree-4 → edit → 本地 commit
     │
     ▼
  Report ──→ 摘要 + 每个任务的 commit SHA
```

---

## 2. 安全边界 / Safety (design-first)

Autopilot 的**默认安全姿态是保守的**：

| 保护层 | 说明 |
|--------|------|
| **Worktree 隔离** | 每个子代理在独立的 git worktree 中工作，与主树物理隔离 |
| **No-remote 守卫** | 所有子代理的 `bash` 拒绝 `git push` / `git remote` 等远程操作——只产生本地 commit |
| **OS 沙箱**（macOS / Linux） | 子代理的 bash 被 seatbelt/landlock 限定在自己的 worktree 目录内（见 [`guide-sandbox.md`](guide-sandbox.md)） |
| **Per-task commit** | 每个子任务成功时产生一个本地 commit，你早上 review + push |
| **无默认远程操作** | 不会自动 push / 开 PR，除非显式 opt-in（未来 `--open-pr`） |

> **一句话**：autopilot 产出本地 commit + 摘要，人在早上 review 再合。不会"睡着时代码被推到生产"。

---

## 3. 前提条件 / Prerequisites

1. **一个 seek 二进制**（>= v0.8.0）
2. **配置文件好的 provider / API key**（`~/.seek/config.json` 或环境变量）
3. **在当前 git 仓库内执行**（autopilot 需要 git worktree 支持）
4. **沙箱**（可选但推荐）：macOS 自动检测 `sandbox-exec`；Linux 需要内核 >= 5.13（Landlock）

---

## 4. CLI 用法 / Command reference

```bash
seek autopilot run "<goal>"           # 执行一个目标
```

| 参数 | 说明 |
|------|------|
| `"<goal>"` | 用引号包裹的自然语言目标。越具体越好。例：`"为 README 添加安装指南的 macOS 截图"` |

当前为 MVP，暂无可选 flags。未来会添加 `--model`、`--max-tasks`、`--open-pr` 等扩展。

也可以在 cron 中使用：

```bash
# 每天检查 CI 状态（结合 cron）
seek cron create --name nightly-refactor --at @daily --autopilot "清理过期 TODO 注释"

# 或通过 schedule_wakeup 让模型自己安排
# （在对话中说"一小时后检查这个"→ 模型调用 schedule_wakeup 工具）
```

---

## 5. 输出解读 / Understanding the report

运行完成后 autopilot 输出类似：

```
[autopilot: 分解] "重构 User 模型…" → 4 个子任务
  ├─ 1. 修改 User 结构体，添加 email_verified, verified_at 字段
  ├─ 2. 添加 verifyEmail 方法和相关校验
  ├─ 3. 编写 model + service 单元测试
  └─ 4. 更新 API 文档

[autopilot: fleet] 4/4 子代理完成
  ✓ task 1: commit abc1234 (worktree-1)
  ✓ task 2: commit def5678 (worktree-2)
  ✓ task 3: commit ghi9012 (worktree-3)
  ✓ task 4: commit jkl3456 (worktree-4)

[autopilot: 摘要]
  4 个任务全部成功。所有改动均已本地 commit。
  建议 review 后手动 push：
    git log --oneline origin/main..HEAD
```

你有三种方式 review 结果：

```bash
# 查看 autopilot 产生的所有 commit
git log --all --oneline

# 复查每个 worktree（清理前）
seek worktree list

# 丢弃不满意的 worktree 改动
seek worktree remove <name>
```

---

## 6. 故障排查 / Troubleshooting

| 现象 | 原因与解决 |
|------|-----------|
| `autopilot requires a saved session in a git repo` | 不在 git 仓库内，或用了 `--no-save`。在 git 项目中运行 |
| `autopilot requires a model provider` | 未配置 API key。运行 `seek setup` |
| 子代理全部失败 | 常见原因：目标过于模糊（"改进项目"）或太大。尝试更具体的指令，如"为 User 模型添加 email_verified 字段" |
| `usage: seek autopilot run "<goal>"` | 只输入了 `seek autopilot` 没加 `run`。正确语法：`seek autopilot run "你的目标"` |

---

## 7. 与 cron / push 集成 / Integration

Autopilot 与 seek 的时序子系统天然集成：

- **cron 定时触发**：`seek cron create --name auto-refactor --at @daily --autopilot "清理过期 TODO"`
- **完成后通知**：autopilot 使用 cron 的推送通知通道，可 push 到手机 / Slack（见 [`guide-webhooks.md`](guide-webhooks.md)）

---

> **下一步**：了解子代理如何安全地隔离执行 → [`guide-sandbox.md`](guide-sandbox.md)（OS 沙箱）
