# Feature: Autopilot — 无人值守编排交付（spawn 一支团队 · 睡觉时 ship）

**所属版本**：v0.8.x 候选（自治维度；柱 G/H/M 之上的编排层，非新单点工具）
**前置阅读**：[`comparison.md`](../comparison.md) §R.4（**本 PRD 的战略依据**：seek vs Reasonix 唯一结构性领先的两轴）、[`feature-subagent.md`](feature-subagent.md)（柱 G — 并行子代理 + worktree 隔离）、[`feature-routines.md`](feature-routines.md)（柱 H — cron/wakeup/trigger）、[`feature-mobile-push.md`](feature-mobile-push.md)（柱 M — webhook push）、[`feature-bash-monitor.md`](feature-bash-monitor.md)（柱 K — 会话级子进程生命周期 + Shutdown 范式）
**状态**：✅ **真环境 e2e 跑通**。`internal/autopilot`：driver（fan-out/caps/kill/panic 隔离）+ Decomposer（deepseek + 健壮解析）+ report 聚合（带 per-task commit SHA）+ fleet adapter（worktree + subagent.Spawn + **per-task 本地 commit**）+ **no-remote 守卫**（`bash.WithDeny` + `IsRemoteMutating`）。`cmd/seek`：`seek autopilot run "<goal>"` + `cron create --autopilot`，复用 subagent.Manager（整体 no-remote 守卫）+ worktree + 柱 M webhook push。

**e2e 结果**（真 DeepSeek + 真 repo，跑"append 一行到 README"）：分解→并行 worktree fleet→聚合→报告 全链路通；**暴露并修复两个真 bug**：
1. **worktree 隔离洞**——子代理的 `edit/write` 误写了**主树** README（报告却称落在 worktree）。根因双层：(a) `read/edit/write` 用 `filepath.Clean` 按**进程 cwd**解析相对路径，不读 `policy.CWD()`；(b) 更深——`buildSubagentRunner` 复用了**父 tool 实例**（内含父 policy=主树），子 policy 的 worktree CWD 根本没 tool 去用。修：加 `permission.Policy.Resolve`（相对路径锚到 `policy.CWD()`）+ read/edit/write 改用它 + `buildSubagentRunner` 用 `job.Policy` **重建** read/write/edit/bash。修后主树字节不变、edit 落在 worktree。
2. **dirty worktree**——子代理改完不一定 commit。修：Fleet 成功后**确定性 per-task 本地 commit**（msg=`autopilot: <title>`），报告显示短 SHA；no-remote guard 仍挡 push。

~28 测试 `-race` 绿（含 `fleet_test.go:commitWorktree` + `write_test.go:TestWrite_RelativePath_AnchoredToPolicyCWD`），全仓 3-OS build 绿。**剩**：token 上限（需 tracker，可选）。
**目标里程碑**：M-A.1 ✅ · M-A.2（守卫）✅ · M-A.3（聚合/push）✅ · M-A.4（CLI）✅ / cron 待 / e2e 待
**估时**：~6 天（大量复用柱 G/H/M；真正新写的是编排 driver + 安全边界 + 聚合 + CLI）

**为什么做这个（一句话）**：读完 Reasonix `main-v2` 源码后确认，seek 唯一 Reasonix **结构上做不出**的能力是"**并行 worktree 子代理 + 时序自治**"（它子代理串行无隔离、源码零调度）。Autopilot 把这两轴焊成一个**可演示、可信赖的无人值守交付闭环**——tagline 第二三句的兑现，也是打擂台唯一的护城河。详见 `comparison.md` §R.4。

---

## 1. 动机

seek 已经有全部零件，但它们**没被焊成一个闭环**：

- 柱 G：`agent` 工具能 spawn 并行子代理、各自 worktree 隔离、成本归集。但要靠模型在一个 turn 里 ad-hoc 调。
- 柱 H：cron/trigger 能在无人时拉起 `seek -p '<prompt>'`。但拉起后做什么、怎么安全地做，没有成型路径。
- 柱 M：webhook 能在 cron 终态推 (title, status, body) 到手机。但 body 只是状态串，不是"干了什么"的摘要。

**缺口** = 把它们串成：**触发 → 分解目标 → 并行 worktree fleet 各领一个 scoped 任务 → 各自产出本地 commit → 聚合摘要 → push 到手机**。一支"过夜把活干完"的 agent 团队。

**Reasonix 为何追不上**（源码确认）：`TaskTool` 子代理**故意串行、无 worktree**（"keeps the parallel-dispatch path from running two sub-agents at once…writes race"），且**零调度文件**。它结构上做不出"一支队伍并行过夜交付"。这是 seek 唯一能**类别级领先**的地方。

---

## 2. 设计目标与不做什么

### 目标

1. **`seek autopilot run "<goal>"`**：一条命令跑完整闭环——分解 → 并行 fleet → 聚合 → 报告/推送。
2. **可被柱 H 触发**：`seek cron create --autopilot "<goal>"` / 文件触发 / wakeup → 无人值守跑。
3. **确定性编排**：分解/fan-out/聚合的**控制流在 Go**，模型只做"一次分解 + 每个子任务的实际工作"——无人值守没有人纠偏，控制流必须可复现（seek 一贯偏好确定性 loop/fanout 而非模型自由编排）。
4. **无人值守安全边界（头等）**：worktree 隔离 + per-job 硬上限 + **默认绝不远程 push/开 PR**（产本地 commit + 摘要，留人早上 review）+ kill-switch。
5. **大量复用**：柱 G `subagent.Manager` + worktree、柱 H cron/lock、柱 M `WebhookDispatcher`、柱 K 的 Shutdown 生命周期范式。真正新写的是薄编排层。

### 不做什么（明确延后/拒绝）

- ❌ **通用 workflow 引擎**——只做 autopilot 这一个闭环；不做可编程的任意编排 DSL。
- ❌ **默认远程 push / 自动 merge PR**——人保留"早上 review 再合"的闸。`--open-pr` 是显式 opt-in。
- ❌ **跨机器 / 跨 session 状态同步**。
- ❌ **OS 沙箱**——是配套的 **#2 单独 PRD**（seatbelt/landlock）；本 PRD 的安全边界是 worktree 逻辑隔离 + 无远程，OS 级加固后续。
- ❌ **容器化（Docker）**——已否决（重 runtime 毁单二进制，见 memory `project_containerization_decision`）。
- ❌ **改 `pkg/deepseek` 接口**。`permission` 是否需动见 D2（倾向 orchestrator/工具层守，不碰核心接口）。

---

## 3. 跨柱约束 + 复用地图（"焊接，不重造"）

| 能力 | 复用谁 | 新写什么 |
|---|---|---|
| 并行子代理 + worktree 隔离 + 成本归集 | 柱 G `subagent.Manager` / enterworktree / exitworktree | 确定性 fan-out driver（按任务列表批量 spawn） |
| 无人触发 | 柱 H cron/trigger/wakeup + `tick.lock` 去重 | `--autopilot` 接线 → 拉起 `seek autopilot run` 子进程 |
| 推送到手机 | 柱 M `WebhookDispatcher`（4 format + 重试） | autopilot 摘要 payload（"3/4 done, 2 PR drafts…"） |
| 会话级子进程生命周期 / 杀光不留 orphan | 柱 K `Shutdown` 范式 + subagent.Manager 生命周期 | autopilot run = 一个子进程，kill/退出杀 fleet |
| 安全边界 | permission（worktree 内 yolo）+ worktree 隔离 | per-job 硬上限 + **no-remote 守卫** + 分解上限 |

**继承约束**：prefix-cache 字节确定性（编排不改历史）· 零常驻 daemon（cron 仍委派 OS；autopilot run 是 `seek -p` 式子进程，随 run 生死）· permission 单调收紧 · 失败降级（单个子任务失败不拖垮整 fleet）。

---

## 4. 设计决策

### D1 — 编排器 = 确定性 Go driver，不是自由 skill

**选：控制流在 Go，模型只做"一次分解 + 每子任务工作"。**
- 闭环 = `decompose`（1 次模型调用，schema 化：goal → `[]Task`，N 有硬上限）→ `fan-out`（确定性 spawn N 个 worktree 子代理，复用 `subagent.Manager`）→ `aggregate`（Go 代码遍历结果）→ `report`（Go 代码：摘要 + push）。
- 理由：无人值守**没有人纠偏**——若让模型自由编排，一次跑偏整夜白烧。确定性控制流可复现、可测、可设上限。模型的自由度被关进"每个子任务的 worktree 内"。
- 不做自由 skill 版（prompt 驱动的 decompose+fanout）：MVP 不可靠，留作后续"开放编排"探索。

### D2 — 无人值守安全边界（最关键，决定能不能放心用）

**多层防线，越界默认拒**：
1. **worktree 逻辑隔离**（复用柱 G）：每个子代理在自己的 worktree，碰不到主树、碰不到彼此 → 天然防 write race + 防污染主分支。
2. **worktree 内 yolo / worktree 外拒**：子代理在自己 worktree 里 auto-approve edit/bash（无人值守必须免审批）；但**远程操作（`git push` / `gh pr create` / 网络写）默认拒**。
   - 实现倾向：**orchestrator/工具层守**，不动 `permission` 核心接口——autopilot 子代理拿到的工具集**剔除/拦截远程写**（类比 git 工具的子命令白名单、bash 的 readonly 标记）。若发现必须扩 permission，停下评估（可能是新 Workflow，成本高）。
3. **per-job 硬上限**：max 子代理数（默认 8，复用 subagent 既有上限思路）、max 总 tokens、max wall-clock。超限即停 + 报告。防失控/烧钱。
4. **kill-switch + 生命周期**：autopilot run 是单个子进程；kill 它 / 会话退出 → `subagent.Manager` 杀光 fleet（复用柱 K Shutdown 范式）。
5. **默认产出 = 本地 commit + 摘要**，不 push、不开 PR。人早上 review。`--open-pr` 显式 opt-in（且 PR-open 是唯一被允许的远程动作，仍需 `gh` 在 PATH）。
6. **#2 OS 沙箱**是这层的 OS 级加固（worktree 是逻辑边界、sandbox 是内核边界）——单独 PRD，本闭环不阻塞。

### D3 — artifact 聚合

**选：fleet 跑完，Go 遍历各 worktree 收集结构化结果。**
- 每子任务 outcome：`{task, status(done/failed/skipped), commits[], diffstat, oneline 摘要}`。
- 复用 git 工具 / worktree mgr 读各 worktree 的 commits/diff。
- 产 `AutopilotReport{goal, tasks[], totals, startedAt, dur}`。

### D4 — enriched push（复用柱 M）

**选：autopilot 摘要走柱 M `WebhookDispatcher`，新增 `autopilot` payload 变体。**
- body 从状态串 → 摘要：`autopilot "<goal>": 3/4 done · 2 PR drafts · 1 failed(<task>) · $0.42 · 12m`。
- 复用 4 format（ntfy/slack/discord/raw）+ 重试。事件名 `autopilot.completed` / `autopilot.failed`。

### D5 — trigger 接线（复用柱 H）

**选：`seek cron create --autopilot "<goal>"` → cron tick 拉起 `seek autopilot run "<goal>"` 子进程。**
- 继承柱 H 的 `tick.lock` + per-job lock（长 autopilot 跨 tick 不重跑）、env 注入、失败 stash。
- 文件触发 / wakeup 同理（同一 run 入口）。

### D6 — 观测 / replay（MVP 最小）

**选：run 结束写一个 write-once summary artifact + push；深度 dashboard 延后。**
- `seek autopilot status`：最近 run 的 per-task outcome 表。
- replay 复用现有 session/event 溯源（autopilot run 是带 transcript 的子进程）。
- 实时"指挥台" dashboard 是 `comparison.md` §R.2 提的 #3，单独事，不在本 MVP。

### D7 — 与 #2 沙箱 / 旧"不做容器化"决定的边界

- worktree 隔离（逻辑）= 本 MVP 的边界，够用。
- **#2 OS 沙箱**（seatbelt/landlock，零 runtime 依赖）= OS 级加固，单独 PRD，是本闭环的"可信度升级"而非前置。
- **容器化（Docker）仍否决**（见 `project_containerization_decision`）——重 runtime 与单二进制护城河冲突；OS 沙箱不在此列（内核能力、单二进制不变）。

---

## 5. 闭环 / CLI / 数据形态

**CLI**：
```bash
seek autopilot run "<goal>" [--tasks N] [--open-pr] [--max-tokens T] [--timeout D]
seek autopilot status                         # 最近 run 的 per-task 结果
seek cron create --autopilot --at @daily "<goal>"   # 无人值守
```

**闭环（Go 确定性控制流）**：
```
decompose(goal) --1 model call, schema--> []Task         (N ≤ --tasks, 硬上限)
   └─ fan-out: for task in tasks (并行, ≤8): subagent(task, isolation=worktree, autonomy=worktree-yolo, remote=denied)
        └─ each: 在 worktree 内自主完成 → 本地 commit
   └─ aggregate: 遍历 worktree 收集 commits/diff/outcome
   └─ report: 构建 AutopilotReport → (--open-pr? gh pr create per worktree) → WebhookDispatcher push
```

**数据**：
```go
type Task struct { ID, Title, Prompt string; Files []string /*scope hint*/ }
type Outcome struct { Task Task; Status string /*done|failed|skipped*/; Commits []string; Diffstat string; Summary string }
type AutopilotReport struct { Goal string; Outcomes []Outcome; Totals struct{ Done, Failed, Skipped int; TokensUSD float64; Dur time.Duration } }
```

---

## 6. 测试（load-bearing — 对齐 CLAUDE.md "test the failure modes"）

| 测试 | 覆盖 |
|---|---|
| `TestAutopilot_FanOut_Parallel` | 分解出 N 任务 → N 个 worktree 子代理并行起 |
| `TestAutopilot_PartialFailure` | 1 个子任务失败 → 其余照常完成、报告标 failed、不拖垮 fleet |
| `TestAutopilot_NoRemoteGuard` | 子代理尝试 `git push` / `gh pr create` → **被拒**（默认无远程）；`--open-pr` 时仅 PR-open 放行 |
| `TestAutopilot_Caps` | 超 max 子代理 / max tokens / timeout → 停 + 报告，不失控 |
| `TestAutopilot_KillSwitch` | kill run / 会话退出 → fleet 全杀、worktree 不留 orphan（复用柱 K 断言） |
| `TestAutopilot_Aggregate` | 各 worktree commits/diff 正确聚合进 report |
| `TestAutopilot_PushPayload` | report → webhook 摘要 payload（4 format）正确 |
| `TestAutopilot_CronDedup` | 长 run 跨 tick → `tick.lock` 不重跑（复用柱 H） |
| `TestAutopilot_DecomposeBounded` | 模型分解超 N → 截断到 --tasks 上限 |
| `TestAutopilot_Unattended_NoApprovalHang` | 无人值守路径不卡在 askFn（worktree-yolo 生效） |

---

## 7. 里程碑

| M | 内容 | 产出 |
|---|---|---|
| **M-A.1** | `internal/autopilot` 编排 driver：decompose(schema'd) + 确定性 fan-out（复用 subagent.Manager）+ per-job 上限 + kill-switch | driver + 单测（fan-out/partial-fail/caps/kill） |
| **M-A.2** | 安全边界：worktree-yolo 自主 + **no-remote 守卫**（工具层）+ 无人值守不卡审批 | 守卫 + 单测（no-remote/unattended） |
| **M-A.3** | aggregate（worktree → report）+ enriched push（复用柱 M）+ `seek autopilot run/status` CLI | 聚合 + CLI + 单测 |
| **M-A.4** | 柱 H 接线（`cron create --autopilot` + 文件触发/wakeup）+ `--open-pr`（gh）+ 文档（guide + comparison §R.4 勾选 + README） | e2e + 文档同步 |

---

## 8. 风险与预埋 pitfall

| 风险 | 缓解 |
|---|---|
| **无人值守失控/烧钱**（整夜跑飞） | D2：per-job 硬上限（子代理数/tokens/wall-clock）+ 确定性控制流（非模型自由编排）+ 默认无远程 |
| **子代理并发写竞争** | worktree 隔离（柱 G 已解决）——这正是 Reasonix 串行的原因，seek 用 worktree 绕开 |
| **autopilot 子代理偷偷 push/发 PR** | D2 no-remote 守卫（工具层剔除远程写）；`TestAutopilot_NoRemoteGuard` 钉死 |
| **无人值守卡在审批** | worktree-yolo（worktree 内免审批）；`TestAutopilot_Unattended_NoApprovalHang` |
| **分解质量差 → 任务重叠/遗漏** | schema 化分解 + N 上限；MVP 接受"分解不完美"，留人早上 review（不自动合）；后续可加分解自检 |
| **kill 时 worktree/子进程 orphan** | 复用柱 K Shutdown 范式 + subagent.Manager 生命周期；`TestAutopilot_KillSwitch` |
| **若 no-remote 守卫需要动 permission 核心接口** | 停下评估（可能是新 Workflow，成本高于工具层守）；优先工具层（git 子命令白名单 / bash readonly 的先例） |
| **部分失败的语义** | 失败子任务标 failed 入报告、保留其 worktree 供 debug；整 run 不 abort |

---

## 9. 落地后文档同步清单

- `comparison.md` §R.2/§R.4：autopilot ship 后，把"并行+worktree 子代理 + 时序自治"从"两轴领先"升级为"已焊成 autopilot 闭环"——seek 的护城河从"零件齐"变"成品"。
- `README.md` / `README.zh.md`：tagline 第二三句有了对应的 `seek autopilot`；"And more" / 路线图加一条。
- `docs/guide-autopilot.md`：新建（goal → fleet → review → 可选 PR 的用法 + 安全边界 + cron 接线 + 与 #2 沙箱关系）。
- `AGENTS.md` + `CLAUDE.md`：若 `internal/autopilot` 引入"只此层可发起无人值守 fan-out"类硬不变量，补一行（类比 skill 状态那条）；否则不塞。
- 配套 **#2 OS 沙箱 PRD**（seatbelt/landlock）：作为 autopilot 的可信度升级，单独起。
