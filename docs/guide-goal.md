# seek Goal — 无人值守任务 / Unattended multi-turn tasks

Goal 让 seek **自动多轮工作**直到某个条件满足——不要求你在旁边逐轮确认。适合"改完所有 TODO"、"跑通所有测试"、"迁移完这个目录的 API 调用"这类长时间任务。

> Goal runs seek unattended across multiple turns toward a completion CONDITION, with a cheap model judging after each turn whether the condition is satisfied — met, capped, stalled, or canceled.

设计决策见 [`docs/prd/feature-goal.md`](prd/feature-goal.md)。

---

## 1. 快速开始 / Quick start

```bash
# TUI 中：给一个目标
/goal 修复 internal/bgjob/ 下所有测试

# CLI 中：headless 运行
seek goal run "把 docs/ 下所有 .md 文件中的 DeepSeek V3 改为 V4"

# 搭配 cron 定时检查（注意：是 cron create 的 --goal 标志，不是 goal run 的参数）
seek cron create --name dep-check --at '0 6 * * *' --goal "检查依赖是否有新安全更新"
```

---

## 2. 工作方式 / How it works

1. **启动**——你给一个"完成条件"（condition），如"修复所有 lint 错误"
2. **多轮迭代**——agent 每轮自主调用工具（读代码、写代码、跑命令），然后一个**廉价模型**（DeepSeek）判断条件是否满足
3. **停止条件**——满足（met）/ 达到最大轮数（max_turns）/ 超时（timeout）/ 停滞（stalled）/ token 预算耗尽（token_budget）/ 取消（canceled）/ 错误（error）
4. **报告**——结束时输出原因和摘要

### 终止原因

| 原因 | 说明 |
|------|------|
| `met` | judge 判定条件已满足 |
| `max_turns` | 达到轮数上限（默认 25） |
| `timeout` | 超时 |
| `stalled` | 连续多轮无进展（默认 3 轮） |
| `token_budget` | Tokens 预算耗尽 |
| `canceled` | 用户取消（Esc / SIGINT） |
| `error` | 某轮执行出错 |

---

## 3. 安全边界 / Safety boundaries

**注意：以下保护只在无人值守路径（`seek goal run` 和 `cron --goal`）启用。** TUI 里的 `/goal` 走你**当前的权限姿态**——Ask 模式会逐次弹确认，push 也不会被自动拦（你在旁边，由你把关）。

无人值守时（`goal run` / cron），seek 自动：

- **禁止远程提交**——`git push`、`gh pr`、`gh api` 等远程改动操作被拒绝
- **权限提升**——自动进入 `--yolo`（无逐次确认），因为没人能在旁边点"允许"
- **在工作目录里干活，但不是沙箱隔离**——与 autopilot 不同，goal **不开独立 worktree、默认也不上 OS 沙箱**。远程 push 被挡，但本地写入（yolo 下甚至可能在工作区之外）不受内核级限制。无人值守跑长任务前，请自行确认它的作用范围（必要时配 `seek` 的沙箱 / 在专门的检出里跑）。

```bash
# goal run 自动禁止远程操作：
seek goal run "修复 bug 并提交到 main"
# -> git push 被拒绝：remote-mutating commands are blocked in goal mode
```

---

## 4. TUI 使用 / TUI usage

```bash
/goal <完成条件>
```

在 TUI 中输入 `/goal` 后：
- 状态栏显示当前轮/上限和进度（`🎯 goal N/M` 徽章）
- 可随时按 `Esc` 取消（或 `/goal clear`）
- 会话重启后自动恢复未完成的 goal（resume 时重新武装）

---

## 5. CLI 参考 / CLI reference

```text
seek goal run "<condition>"   # 无人值守运行，直到条件满足

# 定时执行：用 cron create 的 --goal 标志（不是 goal run 的参数；
# 与 --autopilot 互斥）
seek cron create --at '0 6 * * *' --goal "<condition>"
```

### 上限 / Caps（当前为内置默认，暂无 CLI / 环境变量开关）

| 参数 | 默认 | 说明 |
|------|------|------|
| 最大轮数 | 25 | 超过后以 max_turns 停止 |
| 停滞阈值 | 3 | 连续 N 轮无工具调用时以 stalled 停止 |
| 超时 | headless `goal run` 30 分钟；TUI `/goal` 无 | 到点以 timeout 停止 |
| Token 预算 | 无 | 预留（接入需 tracker，见 PRD 后续项） |

> 这些目前是 `runGoal` / TUI 里写死的 `goal.Caps` 默认值，还没有 CLI / env 开关——要调得改代码（`feature-goal.md` 列为后续项）。

---

## 6. 与 Autopilot 的区别 / vs. Autopilot

| 维度 | Goal | Autopilot |
|------|------|-----------|
| **架构** | 单 agent，同一对话多轮 | 多 agent，各在不同 worktree 中并行 |
| **适用场景** | 线性任务（改代码 → 编译 → 测试） | 探索性任务（多方案并行实验） |
| **隔离** | 同一工作区 | 每个 subagent 独立 worktree |
| **judge** | DeepSeek 廉价模型每轮判断 | 由 orchestrator 汇总结果 |

---

## 7. 相关功能 / See also

- [`docs/guide-autopilot.md`](guide-autopilot.md)——多 agent 并行探索
- [`docs/guide-cron.md`](guide-cron.md)——定时任务
- [`docs/prd/feature-goal.md`](prd/feature-goal.md)——Goal 的设计文档与验收标准
