# Feature: Monitor + 后台 bash（v6 柱 K）

**所属版本**：v6（单点工具补齐 umbrella）
**前置阅读**：[`v6.md`](v6.md) §柱 K + §2 跨柱约束、[`docs/comparison.md`](../comparison.md) §"第二档单点工具缺口"、现有 bash 工具（`internal/tools/bash/{bash.go,proc_linux.go,bash_unix.go}`）
**状态**：🚀 已交付（v6 柱 K）。`internal/bgjob`（Manager + ring buffer + 生命周期）→ `bash` 接 `run_in_background`（session-ctx 分离）→ `monitor` 工具（poll/wait/kill）→ `cmd/seek` 注入 + Shutdown + sysprompt。新增测试全过，`internal/{bgjob,tools/bash,tools/monitor,sysprompt}` 全 `-race` 绿，全仓 `go test ./...` 绿。Windows 后台为降级路径（kill 走 `taskkill /T /F /PID`，见 §8）。文档见 [`docs/guide-background.md`](../guide-background.md)。
**目标里程碑**：M-K.1 ~ M-K.4（全部落地）
**目标发版**：v0.7.x
**估时**：~5 天（v6 umbrella 表）

---

## 1. 动机

当前 `bash` 工具是**一次性 exec + 阻塞整个 turn**：

```go
cctx, cancel := context.WithTimeout(ctx, timeout)   // ctx = 当前 turn 的 ctx
cmd := exec.Command("/bin/sh", "-c", a.Command)
// ... 阻塞等 cmd.Wait()
```

三件具体痛点指向同一缺口——**没有"边跑边看"的长任务通道**：

1. **长 build / test 阻塞 turn**：`go build ./...`、`npm run build`、大型测试套件——要么撞 600s 上限被砍，要么模型干等输出、整个 turn 卡死。
2. **dev server / watch 起不来**：`npm run dev`、`go run`、`vite`——这类进程**本就不该退出**，前台模式下它会一直占着 turn 直到超时被杀。
3. **想"先起一个、再起一个、回头看进度"无能为力**：并行跑 lint + test + build 然后汇总，现在只能串行阻塞。

Claude Code 的对应答卷是 `Bash(run_in_background)` + `BashOutput` + `KillBash` + `Monitor`（条件等待）。柱 K 是 seek 的对应实现——**复用 bash 已有的进程组管理**，加一层后台句柄注册 + 一个 `monitor` 工具。

**为什么 K 在 L（LSP）前**：估时更小（5d vs 7d）、地基已有（`killProcessGroup` 进程组管理）、且直接咬合 v5 的 cron/"ships while you sleep" 叙事——后台跑长任务 + Monitor 盯着，是无人值守自治的天然补全。

---

## 2. 设计目标与不做什么

### 目标

1. **`bash` 加 `run_in_background`**：置 true 时非阻塞启动，立即返回句柄 `bg-N`，命令在后台继续跑。
2. **新 `monitor` 工具**：`action ∈ {poll, wait, kill}` 跟踪后台 job。
3. **复用现有进程组管理**：`Setsid` + `killProcessGroup` + `/proc` 后代遍历照搬，不重写。
4. **会话级生命周期**：job 随 session 生死——session 退出时全部 kill，**绝不 orphan，绝不持久化、绝不跨重启**（与零 daemon 约束一致，见 §3）。
5. **输出 bounded ring buffer + cursor**：write-time 截断（继承 token 约束），`poll` 返回增量窗口。
6. **权限沿用**：后台启动仍走 `KindBash` 门（plan-analyze 拒 / plan-execute per-step / yolo 放行），不新增 Kind、不碰 `permission` 接口。

### 不做什么（明确延后）

- ❌ **跨 session 持久化 bg job**——违背零 daemon。job 是 live session 的进程，session 死则 job 死。
- ❌ **异步"job 退出自动 re-invoke agent"**——seek 是 turn-based 模型；MVP 用 `monitor(wait)` **同步**阻塞当前 turn 等结果。"job 完成自动唤醒下一 turn"复用 `wakeup`/cron 机制是 future（见 §8 风险）。
- ❌ **真正的 job control**（fg / bg / suspend / resume 信号栈）——只 start / poll / wait / kill。
- ❌ **stdin 注入 bg job**——`detachStdin` 仍 detach（bg job 非交互；交互命令本就该在前台被用户 y/N）。
- ❌ **改 `pkg/agent` / `pkg/deepseek` / `internal/permission` 接口**（v6 §2.1 硬约束）。job store 是新 `internal` 包，注入两个 Tool。

---

## 3. 跨柱约束（继承 v3-v5 + 柱 K 特有）

| 约束 | 柱 K 如何满足 |
|---|---|
| **prefix-cache 字节确定性** | tool schema 是 `[]byte` const；job ID `bg-N` 只进 **result**（本就随轮变化）不进 schema。`bash` schema 新增 `run_in_background` 字段会**一次性**失效 cache（schema 字节变了），发版即稳定，可接受。 |
| **输出 write-time cap** | 每 job 一个 bounded ring buffer（上限 64 KiB）；`poll` 返回 `[cursor, end)` 窗口。**不在 send 时改历史**——和现有 `maxOutputBytes` 同一原则。 |
| **零常驻 daemon** | bg job ≠ daemon——它由 **live session 持有**，不写盘、不跨重启、session 死则进程组全杀。这与 cron"每 tick 独立子进程"是两套机制：cron 跨进程调度委派 OS，bg bash 是**会话内**进程。 |
| **permission 单调收紧** | 后台启动 = `KindBash`，沿用现有门（analyze 拒、execute per-step、yolo 放）。`kill` 是减副作用、安全无门。 |
| **v6 §2.1 不碰核心接口** | `Manager` 是新 `internal/bgjob` 包；`bash.New(policy, mgr)` + `monitor.New(mgr)` 注入。`pkg/agent` 零改动。 |
| **v6 §2.2 独立可回滚** | 删 `monitor` 后 `bash` 仍能跑（`run_in_background` 降级报错或回退前台）；柱 K 不依赖柱 L。 |

---

## 4. 设计决策

### D1 — 一个 `monitor` 工具（action 分发） vs 三个工具（BashOutput / KillBash / Monitor）

**选：单 `monitor` + `action ∈ {poll, wait, kill}`。**

- 理由：seek 重 **tool 数最小化**（schema 字节预算 + 模型注意力预算，见 CLAUDE.md "Tool descriptions" 段）；"Monitor + 后台 bash" 命名本身就暗示单一 Monitor 面。
- **`monitor` 故意不标 `ReadOnlyTool`**——不是因为权限，而是因为 `wait` 会**阻塞**：把一个阻塞的 wait 和只读工具一起并发批处理会拖住整批。non-ReadOnly = agent 单独跑它。
- **实现收紧（as-built，M-K.3）**：`monitor` **不注入 `permission.Policy`**。它只操作**已经过 `bash` 门审批**启动的 job——poll/wait 是纯读、kill 是减副作用，没有任何**新的**危险动作要 gate。因此无权限门、也不需要 plan-analyze hint（初稿设想的"analyze 拒 monitor + Hint 指向 propose"在实现中被证明是多余的）。

### D2 — bg job store 放哪

**选：新 `internal/bgjob` 包，`cmd/seek` 构造一次 `mgr`，注入 `bash` + `monitor` 两个 Tool。**

- 理由：bash（写 job）和 monitor（读/杀 job）**跨工具共享状态**——正是 `feature-edit-read-before.md` 里 read/edit 经 Registry 共享的同款模式。
- 不放 `pkg/agent`（v6 §2.1）。不放 `internal/tools/bash` 导出（避免 monitor 反向 import bash 造成耦合；两者都 import 中立的 `internal/bgjob`）。

### D3 — job ID 方案

**选：session 内单调递增 `bg-1` / `bg-2` / …**

- 理由：确定性、人类可读、模型好引用（"poll bg-2"）。Manager 持 `counter int + mutex`。
- 不用 UUID：不可读、对 prefix-cache 无益（ID 本就在 result 非 schema）。

### D4 — 输出缓冲

**选：每 job 一个 bounded ring buffer（lastN = 64 KiB）+ 单调写入 offset。**

- `poll` 返回 `[clientCursor, writeOffset)` 窗口 + 新 cursor + 状态。
- 环形丢弃头部时，窗口前缀标 `[bg-N: ... M bytes dropped before this window]`。
- 复用现有 `lockedBuffer` 的 mutex 思路，但底层换成固定容量环（写满覆盖头部，记 `droppedBytes`）。
- write-time 截断，绝不 send 时改历史。

### D5 — 生命周期 & ctx（**最关键，最大坑**）

**选：前台 bash 用 turn ctx；后台 job 用 session ctx（Manager 持有），二者分离。**

- 现有代码 `cctx := context.WithTimeout(ctx, …)`，`ctx` 是**当前 turn 的 ctx**——turn 一结束就 cancel。前台命令该如此；**后台命令绝不能**，否则 turn 一返回 job 就被砍，"后台"语义破产。
- 方案：`Manager` 持有一个 `sessionCtx`（生命周期 = seek 进程/会话）。bg job 的 `exec.Command` 不绑 turn ctx，绑 Manager 的 session ctx + 自己的 kill channel。
- **清理契约**：`Manager.Shutdown()` 在 session 退出 / agent 关闭路径调用，遍历存活 job 调 `killProcessGroup`。注册到 `cmd/seek/main.go` 的 shutdown / signal handler。**绝不 orphan**。
- 每个 job 一个 goroutine 跑 `cmd.Wait()`，退出时回写 `status = exited(code)` 到 Manager（mutex 保护）。

### D6 — `wait` 语义

**选：`monitor(action=wait, job, timeout_ms?, until_regex?)` 阻塞当前 turn 直到：(a) job 退出，或 (b) `until_regex` 命中输出，或 (c) `timeout`。**

- 返回最终/当前输出窗口 + 状态。
- **绑当前 turn ctx**：Esc 中断 `wait` 但**不杀 job**（job 用 session ctx，wait 只是观察者）。这是 turn-based 模型下"跟踪长任务"的主路径。
- `until_regex` 让"等到 dev server 打印 `Listening on`"这类条件等待成立。

### D7 — `kill` 是否要权限门

**选：`monitor` 整体不注入 `permission.Policy`。** poll/wait 纯读、kill 减副作用，且所有 job 都已在 `bash` 启动时过 `KindBash` 门——monitor 没有任何**新**危险动作要 gate。启动门留在 bash（与 read/grep/list_dir 无门同理）。

### D8 — 并发上限 & timeout 交互

**选：session 级硬上限（最多 8 个并发存活 bg job）；bg 模式忽略 `timeout_ms`。**

- 后台 job **无 turn 超时**（否则不算后台）。靠 `monitor(kill)` + `Manager.Shutdown` 兜底回收。
- 并发上限防泄漏：第 9 个 `run_in_background` 启动报错，提示先 `monitor(kill)` 或 `wait` 收掉已有 job。
- `timeout_ms` 在 bg 模式被忽略（result header 注明）；前台行为完全不变。

### D9 — schema 演进

- `bash` schema `properties` **末尾**加 `"run_in_background": {"type": "boolean", "description": "..."}`。一次性失效 prefix-cache，发版即稳。
- `monitor` schema：`{ job (string, required), action (enum, default "poll"), timeout_ms?, until_regex? }`。

---

## 5. 工具 schema 与 wire-format 草案

**`bash`**（新增字段）：
```json
"run_in_background": {"type": "boolean", "description": "Run detached; return a handle bg-N immediately instead of blocking. Track with the monitor tool. Use for long builds/tests/dev-servers. timeout_ms is ignored in background mode."}
```

**`monitor`**（新工具）：
```json
{
  "type": "object",
  "properties": {
    "job":         {"type": "string",  "description": "Background job handle, e.g. bg-1."},
    "action":      {"type": "string",  "enum": ["poll", "wait", "kill"], "description": "poll: return new output since last read + status. wait: block until job exits / until_regex matches / timeout. kill: terminate the job's process group."},
    "timeout_ms":  {"type": "integer", "minimum": 100, "maximum": 600000, "description": "wait only: max block time."},
    "until_regex": {"type": "string",  "description": "wait only: return early when this regex matches new output."}
  },
  "required": ["job"],
  "additionalProperties": false
}
```

**wire-format result 契约**（一旦未来加 reconstruct parser 即为契约——但 MVP 下 bg job **不跨 session/resume**，故暂无 parser）：
- 后台启动：`[bg: started bg-1] $ <command>`
- `poll` 运行中：`[bg-1: running, elapsed=12s]\n<output window>`
- `poll`/`wait` 已退出：`[bg-1: exited code=0, elapsed=...]\n<output window>`
- `kill`：`[bg-1: killed]`
- 引用不存在的 job：清晰错误（含已知 job 列表）。

---

## 6. 测试（load-bearing — 对齐 CLAUDE.md "test the failure modes"）

| 测试 | 覆盖 |
|---|---|
| `TestBgJob_LaunchNonBlocking` | 后台启动立即返回、job 仍在跑 |
| `TestBgJob_PollIncremental` | 写→poll→再写→poll，cursor 推进不重复 |
| `TestMonitor_Wait_JobExit` / `_UntilRegex` / `_Timeout` | wait 三条退出路径 |
| `TestMonitor_Wait_CtxCancel` | Esc 中断 wait 但 **job 仍存活**（最易错） |
| `TestMonitor_Kill_ProcessGroup` | kill → 进程组 + grandchildren 全死、不 orphan（复用 `TestBash_Timeout_*` 断言） |
| `TestManager_Shutdown_KillsAll` | session 退出杀光存活 job |
| `TestManager_Concurrent` | `-race`：多 goroutine poll/launch/exit-callback 并发打 Manager |
| `TestRingBuffer_Overflow` | 写 > 64 KiB → 头部丢弃 + 标记，cursor 正确 |
| `TestBgJob_ConcurrencyCap` | 第 9 个 launch 报错 |
| `TestMonitor_UnknownJob` | 不存在 job-id → 清晰错误 |
| `TestBash_Foreground_Unchanged` | 不传 `run_in_background` 时行为字节级不变（回归保险） |

---

## 7. 里程碑

| M | 内容 | 产出 |
|---|---|---|
| **M-K.1** | `internal/bgjob` Manager + ring buffer + 生命周期（session ctx / Shutdown / exit-callback） | 包 + 单测（含 `-race` 并发、ring 溢出、Shutdown） |
| **M-K.2** | `bash` 接 `run_in_background`（session ctx 分离、并发上限、result header） | bash 改动 + 前台回归测试 |
| **M-K.3** | `monitor` 工具 poll/wait/kill（含 until_regex、wait ctx 中断不杀 job） | 新工具 + 单测 |
| **M-K.4** | `cmd/seek` 注入 + Shutdown 接线 + `sysprompt` 描述 + `monitor` plan-analyze hint + 文档（guide/README/对比表勾选） | e2e + 文档同步 |

---

## 8. 风险与预埋 pitfall

| 风险 | 缓解 |
|---|---|
| **turn ctx vs session ctx 混淆**（最大坑）→ 后台 job 被 turn 结束误杀 | D5：bg job 绑 session ctx，前台绑 turn ctx；测试 `TestBgJob_LaunchNonBlocking` 跨"turn 结束"断言存活 |
| **Esc 中断 wait 误杀 job** | D6：wait 是观察者，绑 turn ctx；job 绑 session ctx；测试 `TestMonitor_Wait_CtxCancel` |
| **bg job 输出 race** | ring buffer mutex；`-race` CI |
| **进程泄漏 / orphan** | Shutdown 全杀 + 并发上限 8；复用 `killProcessGroup` 的 `/proc` 后代遍历 |
| **Windows 无 Setsid/进程组** | **已实现（M-K.4 补）**：`killProcessGroup` 在 Windows 走 `taskkill /T /F /PID`（按 PID 杀进程树）+ `HideWindow` 防控制台闪窗。选 taskkill 而非 Job Object：后者要在启动时建句柄并跨 `killProcessGroup(cmd)` 签名传递，taskkill 只需 PID（kill 时点 PID 仍稳定，wait goroutine 未 reap）。**残留**：前台 Windows 仍走 `CommandContext`（只杀直接子进程，树清理待补）——非柱 K 范围 |
| **模型把 dev-server 当前台跑** | `bash` 描述 + `sysprompt` 引导：长任务/server 用 `run_in_background`；前台超时 result 附 `[hint: long-running? use run_in_background]` |

---

## 9. 落地后文档同步清单（全部完成 ✅）

- ✅ `docs/comparison.md`：`后台 bash + 流式监听` 行 ❌→✅；P1 段勾掉柱 K；§核心结论收敛。
- ✅ `README.md` / `README.zh.md`：Roadmap "Next up (柱 K/L)" → 柱 K 移入已交付（柱 I/J/**K**/M）；工具表/"And more"加 `monitor` + guide 链接。
- ✅ `docs/guide-background.md`：新建"后台任务"指南（启动 / poll / wait / kill / 生命周期 / 限制 / vs cron）。
- ⊘ `AGENTS.md` + `CLAUDE.md`：**评估后不加**。`internal/bgjob` 无"只此包可变更 X"那类硬不变量（它就是个普通会话级状态包）；D2 的"bash 拥有进程、bgjob 保持进程无关、monitor 只 import bgjob"已写进包注释 + 本 PRD，再塞进 CLAUDE.md 反而稀释那批真正的架构红线。
- ✅ `v6.md` 柱 K 行状态 → 🚀 已交付。
