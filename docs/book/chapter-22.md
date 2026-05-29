# 第 22 章：M11.2 + M11.3 — v5 柱 H 时序触发 Routines

> **对应版本**：v0.6.1
> **对应代码**：`internal/routines/`、`internal/routinescli/`、`internal/paths/`、`internal/tools/wakeup/`、`cmd/seek/`（cron dispatch + auto-tick）
> **PRD**：[`docs/prd/v5.md`](../prd/v5.md) · [`docs/prd/feature-routines.md`](../prd/feature-routines.md)
> **验收**：M11.2 + M11.3 全部落地；`go test -race ./internal/routines/... ./internal/routinescli/...` 全绿
> **起点**：第 21 章（v5 柱 G 子代理 + Worktree）。柱 G 让 seek 在空间维度上可以分裂自己——一个 turn 内 spawn 多个隔离子代理并行干活。但 seek 仍然是"用户敲完回车才跑"，时序维度上是个空洞。柱 H 补上这一半。

---

## 22.1 三个具体痛点

走完柱 G 后有三个反复出现的使用场景，每个都指向同一个缺口：

1. **定时维护**——"每天早上 9 点跑一次 dependabot PR 报告"。当前只能用户手动敲。
2. **延迟检查**——"30 秒后回来看看 CI 跑完没"。模型只能挂个 `bash sleep 30 && curl ...` 卡住整个 turn。
3. **被动通知**——"CI 完成后通知我"。没有事件入口。

三件表面不同，但底层需求一致：**non-interactive execution channel**——一条"我不在键盘前时 seek 也能干活"的路径。

Claude Code 的对应工具集是 `CronCreate` / `ScheduleWakeup` / `RemoteTrigger` / `PushNotification`。v5 柱 H 是 seek 的答卷。

## 22.2 为什么三件绑在一起做

如果分别做，会有三份调度面、三份运行记录格式、三套去重锁：

- Cron job 需要 schedule + run + store
- Wakeup 需要 schedule + run + store（和 cron 几乎一样，只是触发方式不同）
- Remote trigger 需要 file bridge + run + store（同样 share schedule + store）

把它们绑在一起做的决定，根源在于它们共享同一个三段式生命周期：**触发 → 运行 → 通知**。差异只在触发端（时间 vs 文件桥 vs 用户输入），运行和通知是完全一样的。

## 22.3 关键设计决策

### 22.3.1 零常驻 daemon（继承 v5 §2.5）

seek 不引入自己的后台进程。OS 级调度器（launchd / systemd / cron / Task Scheduler）已经免费提供进程拉起；seek 只负责"被拉起后做什么"。

这意味着：
- `seek cron tick` 是核心入口——被 OS scheduler 每分钟拉起一次
- 每次 tick 是**独立子进程**，与主 TUI 完全隔离
- 无 socket、无 goroutine loop、无常驻内存

代价是 tick 频率受 OS scheduler 精度限制（通常 1 分钟），但这对 cron 类任务已经足够。对于需要秒级精度的 `schedule_wakeup`，用 `@every <duration>` 配合较短 interval。

### 22.3.2 文件桥而非 socket

远程触发不走 HTTP/TCP socket，而是写文件：`~/.seek/cron/triggers/<id>.json`。下一次 tick 扫描到该文件后执行对应 job。

这个决定的三个理由：
- **跨平台**：Windows 没有 UNIX domain socket，但所有平台都有文件系统
- **可审计**：trigger 文件包含 caller、timestamp、intent，可事后查证
- **零守护进程**：没有 socket 就不需要 listen → 就不需要常驻进程

代价是触发到执行之间有延迟（最长一个 tick interval），但对于"CI 完成后通知"这类场景已经足够。

### 22.3.3 操作系统原生通知

通知走三条路：
- **macOS**：`osascript -e 'display notification ...'`
- **Linux**：`notify-send`（libnotify）
- **Windows**：PowerShell `[Windows.UI.Notifications.ToastNotificationManager]`

零新依赖——全部走系统自带的 CLI 工具。如果通知发送失败（比如 `notify-send` 没装），静默降级不影响任务本身。

### 22.3.4 `@every <duration>` 优先

MVP 不实现 5-field cron 表达式。不是因为难（写一个正确的 cron parser 大约 200 LOC），而是因为 seek 的 cron 主要场景是"每 N 秒/分钟/小时"而非"每个月第二个星期二 3:15 AM"。`@every 30s`、`@every 1h`、`@daily` 覆盖 95% 的用例。

5-field cron 放在 v0.6.x dot release 作为补充，不推迟 MVP。

## 22.4 数据模型与目录布局

```
~/.seek/cron/
├── jobs.jsonl              # cron 定义存储
├── env                     # 可选：子进程环境变量叠加
├── runs/<run-id>.jsonl     # 每次触发的运行记录
├── .malformed/             # 写入异常时移入的恢复目录
└── triggers/<id>.json      # 文件桥：外部触发入口
```

### jobs.jsonl 一行一个 job

```json
{"name":"nightly-deps","schedule":"@daily","prompt":"go get -u ./... && go test ./...","max_runs":0,"created_at":"..."}
```

字段设计上，`schedule` 将来可以从 `@every 1h` 扩展到 `0 9 * * *`，数据模型不动。`max_runs=0` 表示不限次；`schedule_wakeup` 会设置 `max_runs=1` 跑完自动删除。

### runs/ 目录每条运行一条 JSONL 记录

每条记录包含：status（completed/failed/killed）、duration、stdout 截断、stderr 截断。后续版本可以 `seek cron logs <name>` 流式 tail。

### .malformed/ 的来历

在开发过程中我们发现，如果 tick 在写入 runs/ 时被 SIGKILL（比如 OOM 或 `kill -9`），会留下不完整的 JSONL 文件。`Store` 遇到解析错误时**先把损坏文件移到 `.malformed/`**（加时间戳防覆盖），再继续处理后续文件——一个失败不拖死整个调度链。

## 22.5 Tick 引擎：六步走

`seek cron tick` 的核心逻辑在 `internal/routines/tick.go`，分为 6 个步骤：

1. **Lock**：获取文件锁 `~/.seek/cron/tick.lock`。确保同一时刻只有一个 tick 在跑（防止 OS 调度器重入）。
2. **Load jobs**：读 `jobs.jsonl`，解析所有 job
3. **Scan triggers**：扫 `triggers/` 目录，把有效的 trigger 转换为 job 执行请求
4. **Run due jobs**：对每个 `next_run_at <= now` 的 job，获取 per-job lock `<name>.lock`（防同一 job 重叠执行），在 goroutine 中执行 `seek -p '<prompt>'` 子进程
5. **Update store**：写入 run 记录，更新 `LastRun` / `LastStatus` / `LastRunID` / `next_run_at`
6. **GC**：回收过旧的 runs/ 记录（默认保 100 条最近或 7 天）和超过 24h 的 .malformed 文件

### 去重锁的两层设计

- **tick.lock**：全局锁，防 tick 重叠。如果 tick 执行时间超过了调度间隔，下一个 tick 会立即退出，避免调度积累。
- **\<name\>.lock**：per-job 锁，防同一 job 重叠执行。如果上一个 run 还没结束（比如 `@every 10s` 但任务本身跑了 30 秒），后续的 run 被跳过但不报错。

这层设计源于一个真实的 bug：第一个 M11.2 实现没有 per-job 锁，结果一个 `@every 5s` 的 job 在某次网络延迟时产生了 6 个并发子进程——dependabot 检查没问题，但 CI 账单上多了 6 倍的 API 调用费。

## 22.6 schedule_wakeup：模型自己排自己

`schedule_wakeup` 工具是柱 H 的 LLM 接口。它的工作方式：

1. 模型调用 `schedule_wakeup(delay_seconds=120, prompt="check CI status")`
2. 工具写一条 job 到 `jobs.jsonl`：`@every <delay>`，`max_runs=1`
3. 下次 tick 时该 job 触发，运行 `seek -p 'check CI status'`
4. 跑完自动清理（`max_runs` 达到上限 → `Store.Delete`）

这个工具的关键意义在于：**agent 可以在自己当前回合结束时给自己排一个未来回合**。不再是"用户敲回车才醒"，而是"30 分钟后自动醒来看结果"。

### 与 cron 共享同一套基础设施

`schedule_wakeup` 没有自己的存储或调度器——它只是 cron Store 的一个薄包装。`jobs.jsonl` 同时存放用户通过 `seek cron create` 创建的长效 job 和模型通过 `schedule_wakeup` 创建的临时 job。区别只是 `max_runs`：

- 长效 cron：`max_runs=0`
- 一次性 wakeup：`max_runs=1`

## 22.7 启动 auto-tick：best-effort 保障

为了不让用户非得配 `launchd` 才能用 cron，seek 在启动时（`cmd/seek/main.go`）会跑一次 best-effort auto-tick：

```go
go func() {
    if err := cronTick(ctx, opts); err != nil {
        log.Printf("auto-tick skipped: %v", err)
    }
}()
```

- 以 detached goroutine 执行，不阻塞主 TUI
- 如果 `tick.lock` 被占用（另一个 seek 实例刚 tick 完），退出不做任何事
- 不报错、不弹提示——silent best-effort

这意味着：用户打开 seek 时，所有超时的 cron job 和 pending trigger 都会被自动执行一次。对于日常"早上打开 seek → 昨晚的 cron 报告已经生成"的体验，这一行 goroutine 的 ROI 极高。

## 22.8 G3 与环境变量叠加

M11.2 交付后遇到一个真实问题：用户把 `seek cron tick` 挂到 `launchd` 后，cron job 找不到 `go` 命令——因为 `launchd` 的子进程 `PATH` 是 `/usr/bin:/bin`，不含 Go 的安装路径。

修复方案（G3）：新增 `~/.seek/cron/env` 文件，key=value 格式，支持 `#` 注释。在 `cron run` 和 `cron tick` 时叠加到子进程环境：

```
PATH=/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin
SEEK_CRON=1
```

设计上，env 文件是**叠加**而非覆盖——父进程的 `HOME`、`USER` 等变量仍然传递，env 文件中的值优先级更高。

**教训**：OS scheduler 拉起的进程环境与交互式 shell 差异极大。任何运行子进程的子系统都应该考虑显式环境配置，而不是假设环境继承就够了。

## 22.9 G4+G5：Run 记录 GC

这是另一个"设计时完全没意识到、使用中才发现"的问题。

每条 cron run 写入一个独立的 JSONL 文件到 `~/.seek/cron/runs/`。对于一个 `@every 30s` 的 job，一小时内产生 120 个文件，一天 2880 个。没有 GC 时，一周之后 inode 耗尽。

修复方案（G4+G5）：在 tick step 6 执行双轴 GC：

- **runs/ 轴**：按 age-and-count 保留。每个 job 保留最新的 100 条记录 + 7 天内的记录。超出的删除。
- **.malformed/ 轴**：纯按 age。超过 24h 的损坏文件直接删除。

两个轴的默认参数可以通过 `internal/routines/gc.go` 的常量调整，但当前不暴露 CLI flag——留到 `seek cron gc` 子命令在后续版本中做。

**教训**：任何持久化 append 型存储（运行记录、审计日志、事件流）都必须从一开始就设计回收策略。GC 在功能上线第 1 天不触发，要在第 30 天才触发——但那时 fix 的成本远高于设计时加入 50 行 age-check。

## 22.10 CLI 命令族

```
seek cron create <name> --schedule @every 30s --prompt "go test ./..."
seek cron list              # 表格输出：name | schedule | last_run | status
seek cron delete <name>
seek cron run <name>        # 立即执行一次（不考虑 schedule）
seek cron tick              # 扫一遍所有 job 并执行到期的
```

`seek cron tick` 是唯一被 OS scheduler 调用的命令。`create` 和 `delete` 操作 `jobs.jsonl`。`run` 跳过 schedule 判断直接执行——用于调试。

## 22.11 与柱 G 的对比

| 维度 | 柱 G Subagent | 柱 H Routines |
|---|---|---|
| 触发方式 | 模型在当前 turn 主动 spawn | 时间 / 文件桥被动触发 |
| 与父关系 | 父子 session，成本归集到父 | 独立子进程，无父子关系 |
| 权限模型 | Policy monotonic 收紧 | 继承启动时 Policy（=父若为 Yolo 则 cron 也是 Yolo） |
| 状态存储 | 父子索引存 subagents.jsonl | job 定义存 jobs.jsonl，run 记录存 runs/ |
| TUI 面板 | `/agents` 实时表格 | 状态栏 `⏰ N cron` 徽标 |
| 进程模型 | Manager 管理的 goroutine 池 | OS 拉起 seek -p 子进程 |
| 失败降级 | 结构化错误 tool result | stderr stash + 下次 tick 继续 |

之所以柱 H 不沿用柱 G 的父子 session 模型，是因为 cron job 的触发时机完全独立于父 session 的生命周期——父 session 可能已经结束了，但 cron job 还在跑。父子关系在时序维度上没有意义。

## 22.12 一个观察：同步到异步的换挡

柱 G 让 seek 从"单 agent 串行"进化到"多 agent 并行"，是空间维度的换挡。柱 H 让 seek 从"用户敲回车才醒"进化到"可以在用户不在时干活"，是时序维度的换挡。

两个换挡一起完成后，seek 的能力曲线从**同步编程助手**跨越到了**异步编程智能体**。模型可以：

- 在当前回合 spawn 两个 subagent 并行研究两个目录（柱 G）
- 给自己排一个 30 分钟后的 wakeup 来检查部署状态（柱 H）
- 在 wakeup 触发时再 spawn 子 agent 去修复发现的问题（柱 G + H 联动）

把"我不在的时候帮我盯着"这个能力郑重地交到模型手里，用户和 agent 的关系就从"我敲一下你动一下"变成了"我们是一个团队，你负责 async 我负责 sync"。至少在 CI 检查这个场景上，体验质的飞跃。

---

**关键提交**：`f755324`（Schedule+Store） → `8f3265c`（Tick 引擎） → `67ff685`（cron CLI） → `0cc8beb`（auto-tick） → `68c1241`（OS 通知） → `610a26a`（schedule_wakeup 工具） → `5550759`（triggers/ + 状态栏） → `2d787f0`（G3 env 叠加） → `a7205cb`（G4+G5 GC）

阅读本章前建议先读 PRD，再 `go test -race ./internal/routines/... ./internal/routinescli/...` 跑一遍验收测试理解边界行为。
