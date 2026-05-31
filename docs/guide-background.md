# seek 后台任务 — 长任务跟踪指南 / Background jobs guide

`bash run_in_background` + `monitor` 让模型把**长任务**（build / 测试套件 / dev server / watcher）丢到后台，立即拿回控制权，再随时回来看进度——而不是让一条 `go build` 把整个 turn 卡死到超时被砍。

> `bash run_in_background` plus the `monitor` tool let the model push **long-running commands** to the background, get the turn back immediately, and check progress whenever. No more a 5-minute build holding a turn hostage until it times out.

这套机制是**会话级**的（柱 K，v6）：后台任务随当前 seek 会话生死，**不写盘、不跨重启**——和 [`seek cron`](./guide-cron.md)（委派 OS 调度器、跨进程）是两套完全不同的东西。设计与决策见 [`docs/prd/feature-bash-monitor.md`](./prd/feature-bash-monitor.md)。

---

## 1. 启动一个后台任务 / Launch

给 `bash` 传 `run_in_background: true`，它**立即返回**一个句柄 `bg-N` 而不是阻塞：

```
bash(command="go build ./...", run_in_background=true)
→ [bg: started bg-1] $ go build ./...
  Track with the monitor tool: monitor(job="bg-1", action=poll|wait|kill).
```

- 前台行为完全不变——不传这个字段，`bash` 还是一次性 exec + 阻塞。
- 后台模式下 `timeout_ms` 被忽略：任务一直跑，直到它自己退出、被 kill、或会话结束。
- 适用场景：**长 build、完整测试套件、dev server、文件 watcher**——任何"会跑很久"或"本就不该退出"的命令。一个前台 dev server 只会撞超时被杀；后台跑它。

---

## 2. 跟踪 / Track with `monitor`

`monitor(job, action?, timeout_ms?, until_regex?)`，三个 action：

### `poll`（默认）——看自上次以来的新输出 + 状态

```
monitor(job="bg-1")
→ [bg-1: running, elapsed=12s]
  <自上次 poll 以来的新输出>
```

游标是**服务端**记的——每次 poll 只给你**增量**输出，你不用自己记偏移量。没有新输出时返回 `(no new output)`。

### `wait`——阻塞直到完成 / 命中 / 超时

```
monitor(job="bg-1", action=wait, timeout_ms=60000)
→ [bg-1: exited code=0, elapsed=43s]
  <最终输出>
```

三条退出路径：
- **任务退出** → `[bg-1: exited code=N, ...]`
- **`until_regex` 命中**新输出 → `[bg-1: running, ..., until_regex matched]`（任务**仍在跑**，只是等到了想要的那行）
- **超时** → `[bg-1: running, ..., wait timed out]`（再 `wait` 一次即可）

`until_regex` 是"等就绪信号"的关键——等 dev server 打印 `Listening on`：

```
monitor(job="bg-1", action=wait, until_regex="Listening on")
```

> 按 **Esc** 中断一个 `wait`：只是**停止观察**，后台任务**继续跑**。下一 turn 再 `poll` 即可。

### `kill`——停掉一个不再需要的任务

```
monitor(job="bg-1", action=kill)
→ [bg-1: killed]
```

kill 走进程组（Unix `killProcessGroup` / Windows `taskkill /T`），连同子孙进程一起收掉。kill 一个已经退出的任务是 no-op，会如实报告它的真实状态。

---

## 3. 生命周期 / Lifecycle（重要）

- **会话级**：后台任务由当前 seek 会话持有。会话退出时 `Manager.Shutdown()` 杀光所有存活任务——**绝不留 orphan 进程**。
- **不持久化**：任务状态不写盘、**不跨 `seek -resume` 或重启存活**。想要"跨会话定时跑活"用 [`seek cron`](./guide-cron.md)，不是后台 bash。
- **不绑 turn**：后台进程**不**继承 turn 的 ctx——所以 turn 结束（或 Esc）杀不到它。只有显式 `kill` 或会话退出才会终止它。

---

## 4. 限制 / Limits

| 限制 | 值 | 说明 |
|---|---|---|
| 并发存活任务 | **8** | 第 9 个 `run_in_background` 报错，提示先 `kill` 或 `wait` 收掉一个 |
| 每任务输出缓冲 | **64 KiB** 环形 | 超出从头部丢弃，`poll`/`wait` 窗口前缀标 `... N earlier bytes dropped ...` |
| `wait` 超时 | 默认 120s，最大 600s | 命中上限返回 `wait timed out`，任务继续跑 |
| Windows | **降级路径** | kill 走 `taskkill /T /F`（按 PID 杀进程树）。`run_in_background` + `monitor` 可用，但进程树清理不如 Unix 进程组精确 |

---

## 5. 典型用法 / Patterns

**并行 lint + test + build，再汇总**：
```
bash(command="golangci-lint run", run_in_background=true)   → bg-1
bash(command="go test ./...",    run_in_background=true)    → bg-2
bash(command="go build ./...",   run_in_background=true)    → bg-3
monitor(job="bg-1", action=wait)
monitor(job="bg-2", action=wait)
monitor(job="bg-3", action=wait)
```

**起一个 dev server、等它就绪、再跑冒烟测试**：
```
bash(command="npm run dev", run_in_background=true)              → bg-1
monitor(job="bg-1", action=wait, until_regex="Listening on")     # 等就绪
bash(command="curl -sf localhost:3000/health")                   # 前台冒烟
monitor(job="bg-1", action=kill)                                 # 收掉 server
```

**长测试套件，边跑边看**：
```
bash(command="go test -race ./...", run_in_background=true)   → bg-1
# ... 干点别的 ...
monitor(job="bg-1")                                           # poll 看进度
monitor(job="bg-1", action=wait)                              # 最后阻塞等结果
```

---

## 6. 与 cron 的区别 / vs cron

| | 后台 bash（柱 K） | cron / wakeup（柱 H） |
|---|---|---|
| 跑在哪 | **当前会话进程内** | OS 调度器拉起的**独立 `seek -p` 子进程** |
| 生命周期 | 随会话生死 | 跨会话、跨重启 |
| 触发 | 模型在 turn 里主动起 | 时间表 / 文件触发 / 自调度唤醒 |
| 用途 | "这条命令跑得久，我先干别的" | "每天 9 点跑一次"、"30 秒后回来看 CI" |

需要"睡觉时也在跑"或"跨会话定时"——用 [`seek cron`](./guide-cron.md)。需要"这一轮里把长命令丢后台"——用 `run_in_background` + `monitor`。
