# Feature: Routines（cron / wakeup / push）

**所属版本**：v5（柱 H · 时序维度编排）
**前置阅读**：[PRD v5 umbrella](v5.md) §2.5（时序触发零常驻进程约束）、§3.2（共享配置目录）、[`docs/prd/feature-subagent.md`](feature-subagent.md)（柱 G 已交付参考）
**状态**：📐 设计稿
**目标里程碑**：M11.2（cron 骨架）+ M11.3（wakeup + push + 远程触发）
**目标发版**：v0.6.1（v0.6.0 ship 完柱 G 后接续）

---

## 1. 动机

走完柱 G 后 seek 已经能"在空间维度分裂自己"——一个 turn 内 spawn 多个隔离子代理并行干活。但**时序维度**仍是个空洞：

- seek 永远只在"用户敲完回车"这一刻醒来。
- 想要"每天早上 9 点跑一次 dependabot PR 报告"？必须用户手动敲。
- 想要"30 秒后回来看看 CI 跑完没"？模型只能挂个 `bash sleep 30 && curl ...` 卡住整个 turn。
- 想要"CI 完成后通知我"？没有事件入口。

三件具体痛点指向同一缺口——**non-interactive execution channel**。Claude Code 的对应工具集（`CronCreate` / `ScheduleWakeup` / `RemoteTrigger` / `PushNotification`）就是把这一缺口做了一遍；v5 柱 H 是 seek 的对应答卷。

把这三件**绑在一起做**而非分别上的理由：它们都需要"非主 turn 的执行通道 + 独立持久化状态 + 触发→运行→通知三段式"，分开做会有三份调度面、三份运行记录格式、三套去重锁。

## 2. 设计目标与不做什么

### 目标

1. **零常驻 daemon**（继承 v5 §2.5）—— seek 不引入自己的后台进程。OS 级调度器（launchd / systemd / cron / Task Scheduler）已经免费提供进程拉起；seek 只负责"被拉起后做什么"。
2. **文件桥而非 socket**——远程触发走 `~/.seek/cron/triggers/<id>.json`，跨平台、可审计、与 Windows 兼容。
3. **OS notification 走系统原生**——`osascript` / `notify-send` / PowerShell toast。零新依赖。
4. **`@every <duration>` 优先**——MVP 不实现 5-field cron 解析（写一个正确的 cron parser 至少 200 LOC）。`@hourly` / `@daily` 作为 sugar。
5. **每次运行 fresh subprocess**——`seek cron tick` 触发 `seek -p '<prompt>'` 作为独立子进程。简单可靠，session/state 完全隔离。
6. **并发安全 + 去重**——`tick.lock` + 每 job 一把 `<name>.lock`，重叠 tick / 长任务跨 tick 都不重跑。
7. **失败降级**——cron 跑失败把 stderr stash 到 `last_status`；下次 tick 继续；不会让一个失败任务拖死调度链。

### 不做什么（v5 明确延后）

- ❌ **5-field cron 表达式**——v0.6.x dot 候选；MVP 用 `@every <duration>`。
- ❌ **seek 自己的后台 daemon**——零常驻进程是 v5 §2.5 硬约束。
- ❌ **持久化运行进程**——每个 tick 是独立 seek 子进程，进程生命周期 = 单次任务。
- ❌ **HTTP webhook 端口**——`feature-inspect-rpc.md` §9 v0.7.0+ 候选；MVP 用文件桥。
- ❌ **`/routines` TUI 面板**——v0.6.x dot release；MVP 只有 `seek cron list` CLI。
- ❌ **跨 cron job 的依赖图**（"A 跑完触发 B"）—— UNIX make-style DAG 不在 v5 范围；用户用 shell 串。
- ❌ **跨用户/跨机器同步**——单用户单机本地工具定位（v5 总约束）。
- ❌ **与 subagent 联动**——cron run 是 fresh subprocess，无父子关系；如需"用 subagent 跑 cron 任务"，prompt 里让模型自己 spawn。

## 3. 数据模型与签名

### 3.1 `~/.seek/cron/` 目录布局（v5 §3.2 已声明）

```
~/.seek/cron/
├── jobs.jsonl                       # registered cron jobs (append + rewrite)
├── tick.lock                        # advisory flock; one active tick per host
├── runs/
│   ├── <run-id>.jsonl              # per-run record (header line + output lines)
│   └── <job-name>.lock             # advisory flock; one active run per job
└── triggers/
    └── <trigger-id>.json           # remote-trigger file bridge (M11.3)
```

### 3.2 `jobs.jsonl` schema

Each line is one cron job, last-write-wins on same `name`. Schema (Go names; JSON keys are snake_case):

```json
{
  "name": "morning-report",
  "schedule": "@every 24h",
  "prompt": "Summarise dependabot PRs touched in the last 24h.",
  "project_root": "/Users/x/code/myproject",
  "created_at": "2026-05-28T09:00:00Z",
  "next_run_at": "2026-05-29T09:00:00Z",
  "last_run_at": "2026-05-28T09:00:00Z",
  "last_run_id": "20260528-090012-7a3f",
  "last_status": "completed",
  "last_error": "",
  "max_runs": 0,
  "run_count": 1,
  "yolo": true,
  "notify": "always"
}
```

**Field semantics**:

- `name` — unique key (alnum + dash + underscore). Validated on `create`.
- `schedule` — string parsed at tick time. Initial supported forms:
  - `@every <duration>` (Go `time.ParseDuration` — e.g. `30s`, `5m`, `2h30m`, `24h`)
  - `@hourly` (≡ `@every 1h`)
  - `@daily` / `@midnight` (≡ `@every 24h`)
  - `@weekly` (≡ `@every 168h`)
  - Reserved syntax for future: 5-field cron (`* * * * *`)
- `prompt` — fed as the user prompt to `seek -p '<prompt>'` at run time.
- `project_root` — `cwd` for the subprocess. Optional; empty → user's `$HOME`.
- `max_runs` — 0 = unlimited; ≥1 = run that many times then auto-delete. Used by `schedule_wakeup` tool with `max_runs=1`.
- `yolo` — pass `--yolo` to the cron subprocess. Default `true` because no human is around to answer permission prompts. Per-job override.
- `notify` — `"always"` / `"on_failure"` / `"never"`. Drives OS notification dispatch after the run completes.

**Persistence model**: `jobs.jsonl` is **rewritten on every mutation** (create / delete / tick's run_count update). Each rewrite under `tick.lock` so concurrent tick + create can't interleave half-states. For a single user this is fast enough at 100s of jobs; if we ever push past that, switch to per-job files.

### 3.3 `runs/<run-id>.jsonl` schema

Two-section format (mirrors session JSONL):

**Line 1** (header):
```json
{
  "schema_version": 1,
  "run_id": "20260528-090012-7a3f",
  "job_name": "morning-report",
  "started_at": "2026-05-28T09:00:12Z",
  "project_root": "/Users/x/code/myproject",
  "command": ["seek", "-p", "Summarise dependabot PRs..."],
  "yolo": true
}
```

**Lines 2..N** (one of):
```json
{"event": "stdout", "ts": "...", "data": "..."}
{"event": "stderr", "ts": "...", "data": "..."}
{"event": "completed", "ts": "...", "exit_code": 0, "duration_ms": 8432, "summary": "first 1KB of stdout"}
{"event": "failed", "ts": "...", "exit_code": 1, "duration_ms": 200, "error": "seek: bad API key"}
{"event": "killed", "ts": "...", "reason": "timeout"}
```

`run_id` shares the timestamp + 6-hex shape with session / subagent IDs.

### 3.4 `triggers/<trigger-id>.json` schema (M11.3)

File-bridge for external systems (CI webhooks, IDE plugins, "I just merged a PR" hook):

```json
{
  "trigger_id": "ci-build-12345",
  "prompt": "The mobile-app build #12345 just finished. Summarise the test failures.",
  "created_at": "2026-05-28T14:23:01Z",
  "project_root": "/Users/x/code/mobile-app",
  "ttl_minutes": 60
}
```

External producer (CI / script) drops the file. `seek cron tick` scans `triggers/`, runs each, then **deletes** the file. `ttl_minutes` is a courtesy expiry so a never-ticked file doesn't queue forever.

### 3.5 `tickEngine` API (internal package `internal/routines`)

```go
// Job is the parsed in-memory form of one jobs.jsonl line.
type Job struct {
    Name        string
    Schedule    Schedule    // parsed; see ParseSchedule
    Prompt      string
    ProjectRoot string
    Created     time.Time
    NextRun     time.Time
    LastRun     time.Time
    LastRunID   string
    LastStatus  string  // "completed" | "failed" | "killed" | "scheduled"
    LastError   string
    MaxRuns     int
    RunCount    int
    Yolo        bool
    Notify      string
}

// Schedule is a parsed schedule expression. Today only Every is
// populated; Cron field reserved for future 5-field support.
type Schedule struct {
    Raw   string         // original "@every 5m" / "@daily" / ...
    Every time.Duration  // > 0 when @every / @hourly / @daily / @weekly
}

// ParseSchedule returns the parsed form. Unknown shapes return
// error; only the closed set in §3.2 is recognised in MVP.
func ParseSchedule(raw string) (Schedule, error)

// Next reports the next fire time strictly after `after`.
// Equivalent to after.Add(s.Every) for @every-class schedules.
func (s Schedule) Next(after time.Time) time.Time

// Store reads / writes jobs.jsonl under tick.lock.
type Store struct { ... }

func OpenStore() (*Store, error)
func (s *Store) List() ([]Job, error)
func (s *Store) Create(j Job) error          // upsert by name
func (s *Store) Delete(name string) error
func (s *Store) Get(name string) (Job, error)
// MarkRun atomically updates lastRun/runCount/nextRun fields for
// the named job and (when MaxRuns reached) deletes the entry.
func (s *Store) MarkRun(name, runID, status, errMsg string, ranAt time.Time) error
```

### 3.6 Tick flow (the engine, M11.2)

```
seek cron tick
├── 1. flock ~/.seek/cron/tick.lock (LOCK_NB; skip silently if held)
├── 2. store := OpenStore()
├── 3. now := time.Now().UTC()
├── 4. for each job in store.List():
│     if job.NextRun > now: skip
│     if job.MaxRuns > 0 && job.RunCount >= job.MaxRuns: store.Delete(name); skip
│     ───── per-job goroutine ─────
│     ├── a. flock runs/<name>.lock (LOCK_NB; skip if another tick still running)
│     ├── b. runID := generateID(now)
│     ├── c. write runs/<runID>.jsonl header
│     ├── d. cmd := exec.Command("seek", "-p", job.Prompt)
│     │       cmd.Dir = job.ProjectRoot
│     │       if job.Yolo: cmd.Args = append(cmd.Args, "--yolo")
│     ├── e. stream stdout/stderr → runs/<runID>.jsonl
│     ├── f. wait + classify exit (completed/failed/killed-by-timeout)
│     ├── g. store.MarkRun(name, runID, status, errMsg, now)
│     ├── h. if shouldNotify(job, status): sendOSNotification(job, status)
│     └── i. release runs/<name>.lock
├── 5. for each file in triggers/ (M11.3):
│     parse, run as ad-hoc prompt, delete file
└── 6. wait all goroutines; release tick.lock; exit
```

**Why advisory flock**: cross-platform `golang.org/x/sys/unix.Flock` works on Linux/macOS; Windows uses `LockFileEx`. We wrap via [`internal/pathop`](../../internal/pathop) or a new tiny `internal/flock` helper. Failure to acquire = skip silently — the engine is conservative-pessimistic; "ran zero times this tick" is always recoverable, "ran twice in parallel" is not.

**Subprocess timeout**: hard 30 min default per job; configurable via `--timeout` on `create`. Beyond timeout → SIGTERM, then SIGKILL after grace. Recorded as `killed`.

### 3.7 `schedule_wakeup` LLM tool (M11.3)

Schema:

```json
{
  "type": "object",
  "properties": {
    "delay_seconds": {"type": "integer", "minimum": 60, "maximum": 86400},
    "prompt": {"type": "string"}
  },
  "required": ["delay_seconds", "prompt"],
  "additionalProperties": false
}
```

**Minimum 60s — not 1s** (revised from original spec): the
underlying `Schedule.@every <duration>` enforces `MinSchedule
= 1 minute` (§3.2 "CPU pathology"). Sub-minute wakeup would
require a separate "@once <ts>" schedule form, deferred to
v0.6.x dot. For now wakeup floors at 60s; tool returns
`[schedule: failed reason=delay_too_short]` on smaller values.

Behavior: registers a `max_runs=1` job with `schedule="@every <delay>s"` and `next_run_at = now() + delay`. The job auto-deletes after the single run.

Returns wire format (byte-stable prefix per CLAUDE.md "wire format is contract"):
- success: `[schedule: waking at <iso8601>] (job <name>)`
- failure: `[schedule: failed reason=<...>] <hint>`

Tool is **NOT marked ReadOnly** — it mutates `~/.seek/cron/` filesystem state; serial dispatch is safer for concurrent wakeup requests.

### 3.8 OS notification dispatch (M11.3)

Per-platform implementation in `internal/routines/notify_{darwin,linux,windows}.go`:

**macOS** (`darwin`):
```sh
osascript -e 'display notification "<body>" with title "seek: <job>"'
```

**Linux**:
```sh
notify-send "seek: <job>" "<body>"
```

**Windows**:
```powershell
[System.Windows.Forms.MessageBox]::Show("<body>", "seek: <job>")
```
(or BurntToast if available — but no PowerShell module dependency in MVP.)

**Fallback** (binary missing on the host): write `WARN: notify failed: ...` to the run record, don't fail the cron run itself. The user lost the popup but not the data.

## 4. CLI / TUI

### 4.1 CLI

```
seek cron create [--name N] [--at SCHEDULE] [--cwd DIR] [--max-runs N]
                 [--no-yolo] [--notify on_failure|never] [--timeout DURATION]
                 <prompt>
    Register a new cron job. --name auto-generated if omitted. --at
    defaults to "@daily". <prompt> is the user prompt fed to
    `seek -p` at each run.

seek cron list [--json]
    List registered jobs: name · schedule · next_run · last_status ·
    run_count.

seek cron delete <name>
    Remove a job. Cancels any pending wake-up triggered by
    schedule_wakeup that hasn't fired yet.

seek cron run <name>
    Run a registered job immediately, regardless of schedule.
    Useful for "let me see what this would output before I let it
    run unattended for a week". Updates last_run_at / run_count.

seek cron tick
    OS scheduler entry point. Scans jobs.jsonl + triggers/, runs
    everything due, exits. Designed to be called every 1 minute
    by launchd / systemd-timer / cron / Task Scheduler. Acquires
    tick.lock LOCK_NB; concurrent invocations are no-ops.

seek cron help
```

**Setup hints** (in `seek cron help`):

```
# macOS launchd (every minute):
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.seek.cron.plist <<EOF
<plist><dict>
  <key>Label</key><string>com.seek.cron</string>
  <key>ProgramArguments</key>
    <array><string>/usr/local/bin/seek</string><string>cron</string><string>tick</string></array>
  <key>StartInterval</key><integer>60</integer>
</dict></plist>
EOF
launchctl load ~/Library/LaunchAgents/com.seek.cron.plist

# Linux systemd-timer (every minute):
# create ~/.config/systemd/user/seek-cron.{service,timer}
# systemctl --user enable --now seek-cron.timer

# Windows Task Scheduler (every minute):
schtasks /create /tn "seek cron tick" /tr "seek cron tick" /sc minute
```

### 4.2 TUI slash commands

M11.2 MVP: **none**. Users use CLI. `/routines` interactive panel deferred to v0.6.x dot.

M11.3: `/wakeup <seconds> <prompt>` as a discoverability shortcut for `schedule_wakeup` (lets users test the wakeup mechanism without prompting the LLM to call the tool).

### 4.3 Status bar

Active cron jobs count appears as `⏰ N cron` on the status bar right side when count > 0. Mirrors the `⤴ N agents` pattern from柱 G. Read from `Store.List()` (lazy, called once per render tick).

## 5. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/routines` (新包) | Schedule parser, Store, tick engine, OS notification dispatch | 中-大（柱 H 核心） |
| `internal/routinescli` (新包) | `seek cron {create,list,delete,run,tick}` CLI | 中 |
| `internal/tools/wakeup` (新包，M11.3) | `schedule_wakeup` LLM tool | 小 |
| `internal/paths` | `Cron()`, `CronJobs()`, `CronRuns()`, `CronTriggers()` | 小 |
| `cmd/seek` | Dispatch `seek cron ...`; startup auto-tick (best-effort, after subagent OrphanRecover); register `schedule_wakeup` tool | 小 |
| `internal/tui/statusbar.go` | `CronsActive int` field + render | 极小 |
| `pkg/` | **不变** | 0 |

### 5.1 与 v3 hooks 的交互

cron 子进程**自带**完整的 hook 路径（启动时 `internal/hooks` 加载用户/项目 hooks.toml）。`pre_tool` deny 对 cron 子进程同样生效。如果用户的 hook 弹 UI 询问，cron 子进程会**卡在询问**直到超时。文档建议：cron 用 `--yolo` 默认，再用 hook 做白名单管控。

### 5.2 与 v5 柱 G subagent 的交互

cron run = fresh `seek -p` 子进程。它自己可以再 spawn subagents — 但那是子进程内部的事；从 cron 系统的视角，cron run 是不可分裂的黑盒。每个 cron run 的成本写到 `runs/<id>.jsonl` 的 footer。

### 5.3 与 session 的交互

每个 cron run 创建一个 fresh session with id `cron-<job-name>-<run-id>`。出现在 `seek -list` 输出里，但用 `cron-` 前缀区分。`--no-save` 模式不写 session — `seek cron tick` 自动加 `--no-save` 除非 job 显式 `--save`。

### 5.4 与 permission 的交互

cron 子进程默认 `--yolo`（无人值守）。Per-job 覆盖：`seek cron create --no-yolo ...` 走 `PrefDeny` 模式——任何破坏性动作直接 deny 写到 `last_error`，模型有机会调整 prompt 避开。

### 5.5 Prefix cache 影响

零。cron tick 与 schedule_wakeup 都不修改父 agent 的 prompt 字节序列。schedule_wakeup tool 的 schema 是 package-level 常量，写入 `~/.seek/cron/jobs.jsonl` 是 side-channel，不进 transcript。

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | `ParseSchedule("@every 5m")` 返回 `Every=5min`；`@hourly`/`@daily`/`@weekly` 别名同义 | 单元测试（表驱动） |
| 2 | 未识别 schedule 形式（如 `* * * * *`）返回 error 且 hint "use @every <duration>" | 单元测试 |
| 3 | Store.Create / Delete / MarkRun 顺序操作下 jobs.jsonl 字节稳定（rewrite 序列化保 key 排序） | 单元测试 |
| 4 | tick.lock 防止两个并发 tick 进程重叠执行；后到的 tick 立即 exit 0 | 集成测试（spawn 两个 `seek cron tick` 进程） |
| 5 | per-job lock 防止长任务跨 tick 被重跑 | 集成测试（job 长达 2 个 tick 间隔） |
| 6 | `seek cron tick` 触发到期 job 后，job.NextRun 推进到下次 fire time | 单元测试 |
| 7 | `max_runs=1` 任务运行后被删除 | 单元测试 |
| 8 | 子进程 stdout/stderr 流式落入 `runs/<id>.jsonl`（不缓冲到结束） | 集成测试（spawn 长任务，中间读 runs file） |
| 9 | 子进程超时被 SIGTERM → grace → SIGKILL；runs record status="killed" reason="timeout" | 集成测试 |
| 10 | `seek cron tick` 在非 git 仓库 / 无 ~/.seek/cron/ 的全新环境上跑 exit 0（lazy mkdir） | 集成测试 |
| 11 | OS notification 在三平台缺失对应可执行文件时不阻塞 cron run；写 WARN 到 runs record | 集成测试 + 手测 |
| 12 | `schedule_wakeup` 工具返回 `[schedule: waking at <iso8601>] ...` wire format | 单元测试 |
| 13 | `schedule_wakeup` 创建的 job 在 jobs.jsonl 里 `max_runs=1`、name 自动生成 | 集成测试 |
| 14 | `triggers/<id>.json` 被 tick 消费后文件被删除；TTL 过期的 trigger 也被删（带 WARN） | 集成测试 |
| 15 | `seek -list` 输出包含 `cron-` 前缀的 session（来自 cron run），可用 `seek -resume cron-...` 续传 | 集成测试 |
| 16 | `go test -race ./internal/routines/... ./internal/routinescli/...` 全绿 | CI |
| 17 | 现有 v0–v5 柱 G 测试套件零回归 | 现有测试 |

## 7. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M11.2** | `internal/routines` Schedule + Store + tick engine + `internal/routinescli` 五子命令 + cmd/seek 分发 + 启动 auto-tick best-effort | ~3 天 |
| **M11.3** | `schedule_wakeup` 工具 + OS notification per-platform + `triggers/` 文件桥 + 状态栏徽标 | ~3 天 |

**发版策略**：M11.2 + M11.3 合并为 **v0.6.1**（v0.6.0 ship 柱 G 之后）。两层一起 ship 让用户拿到完整的"我可以让 seek 在我不在的时候帮我做事"故事；只 ship cron 不 ship wakeup 留下"模型不能自己排自己"的空白。

**实现顺序硬约束**：

1. M11.2 第一步：`ParseSchedule` + `Store` + 单元测试（无 LLM、无子进程，纯逻辑）
2. M11.2 第二步：`tick` engine + subprocess plumbing + lock 测试
3. M11.2 第三步：`internal/routinescli` 五子命令 + cmd/seek 分发
4. M11.2 第四步：启动 auto-tick（异步 goroutine，错误不阻塞）
5. M11.3 第一步：OS notification per-platform shim + 测试
6. M11.3 第二步：`schedule_wakeup` 工具 + Manager 注册
7. M11.3 第三步：`triggers/` 文件桥
8. M11.3 第四步：状态栏徽标

**并行可行性**：M11.2 与 M11.3 之间强串行（wakeup 依赖 cron Store）。M11.2 内部步骤 1-2 可与步骤 3-4 并行（不同人写不同包）；M11.3 步骤 1-3 互相独立可并行。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 用户挂的 OS 调度器频率太高（每秒 tick） → tick 之间互锁导致空转 | `tick.lock` LOCK_NB 立即返回，CPU 浪费 < 1ms 每次；文档建议最小间隔 1 分钟 |
| 长任务跨多个 tick → 同一 job 被并发触发两次 | per-job `<name>.lock` LOCK_NB；后到的 tick 看到锁就 skip + 写 `WARN: prior run still active` 到 runs record |
| 用户 API key 不可达（cron 跑在 user shell 之外的 env）→ 每次 cron 静默失败 | 每个 run 的 stderr 落入 `runs/<id>.jsonl`；`seek cron list` 显示 `last_status: failed`；`seek cron logs <name>` 给出最近三次 run 的 head |
| OS notification 在 headless server 上根本没意义 → 用户启动 cron 但收不到通知 | `--notify never` 默认在 `$DISPLAY` 未设置 + 非 macOS 的环境下自动适用；文档明确"无 GUI = 不通知" |
| `jobs.jsonl` rewrite 期间进程被杀（断电）→ 文件损坏 | rewrite 用 write-tmp-rename atomic dance（与 session.Save 同模式）；rename 失败保留 .tmp |
| 用户 `seek cron tick` 跑在错误的 cwd → cron job 的 `project_root` 指向不存在的目录 | `Create` 期 stat `--cwd` 必须存在；run 期 stat 失败 → `failed reason=cwd_missing`，next tick 不重试，直到用户 `delete` 或 `edit`（edit 在 v0.6.x dot） |
| `triggers/<id>.json` 文件被部分写入时被 tick 读 → 解析失败 | tick 读 trigger 前先 stat mtime；mtime < now - 1s 才处理（producer 完成写入 1s 后才认为"已 ready"）。malformed JSON → 文件 rename 到 `triggers/.malformed/<id>.json` + WARN 写 stderr，不阻塞 |
| cron 子进程意外死循环 → 耗光 token quota | per-job `--timeout` 默认 30min；子进程 SIGTERM → grace → SIGKILL；cost 累计 v0.6.x dot 加 `--max-cost` 选项 |
| `@every 1ns` 这种极小 duration → tick 把 CPU 拉满 | `ParseSchedule` 拒绝 duration < 1 分钟；hint "schedule must be at least 1m" |
| 同名 job 重复 create → 静默覆盖原配置（用户丢失原 prompt） | `Create` 默认 idempotent 覆盖；新增 `--force` flag。**不带 --force 时如果 jobs.jsonl 已有同名 job → 报错并 hint "use --force to overwrite or delete first"** |
| 用户在 cron list 输出里看到几十个 schedule_wakeup 残留（job 跑完没自动删） | `MarkRun` 内的 `max_runs` 检查必跑：达到上限即 `Store.Delete(name)`。集成测试 #7 守门 |
| Windows 路径下 flock 实现差异 | 用 `golang.org/x/sys` 包装；测试 race + path-with-spaces |

## 9. 后续版本

- **v0.6.x dot**：5-field cron 表达式（`* * * * *` / `0 9 * * *` 风格），写一个 ~200 LOC parser
- **v0.6.x dot**：`/routines` TUI 面板（仿 `/agents` 表格 + Enter 查看最近一次 run output）
- **v0.6.x dot**：`seek cron logs <name>`（流式 tail 最近 N 次 run 的 jsonl）
- **v0.6.x dot**：`seek cron edit <name>` 改 prompt / schedule 不需要 delete + recreate
- **v0.7.0**：`--max-cost <USD>` 选项让 cron job 自我熔断
- **v0.7.0**：`seek serve` 可选 HTTP webhook → 替代文件桥（保留文件桥作为零依赖默认）
- **v0.7.0**：cron job 依赖图（"A 跑完触发 B"），可能复用 `triggers/` 机制
- **v0.7.0**：跨机器同步（git-backed `~/.seek/cron/`？） —— 设计未定，留 brainstorm
