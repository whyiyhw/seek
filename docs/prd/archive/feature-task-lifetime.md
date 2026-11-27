# 任务寿命脱离进程（v8 柱 V1）+ `ps` / `attach` / `kill` 最小管理面

> 卡片：`c_wvd7a7`（v8 §4 柱 V 第二波）。拍板依据：v8 §6 D2（2026-11-17）——「零 daemon」精确化为不变量 I2：**agent 进程是一次性的，状态不在进程里；常驻只做监听 / 路由 / 拉起。任务寿命的脱离进程在此口径下放行。**
> 状态：📐 本卡只落「最小面」：一个任务进程 + 三个只读/控制子命令。**detach 全语义（会话状态移交、断连续跑的完整闭环）不在本卡**，见 §5。

## 1. 进程模型（三问）

### 1.1 spawn 形状

```
seek task run "<command>"                  ← 用户面（spawn）
  └─ fork/exec  seek task exec --id <tid> -- /bin/sh -c "<command>"   （wrapper）
       └─ fork/exec  /bin/sh -c "<command>"                            （真任务）
```

- **spawn 一次两跳，第二跳即 wrapper**。wrapper 是同一个 seek 二进制（`seek task exec`），与 `internal/sandbox` 的 trampoline 同一先例：需要一个「能被自己的 dispatch 认出来的 argv 前缀」，重编译就是唯一改法。
- **脱离的机制是 `setsid`**（Unix）：wrapper 成为新会话 + 新进程组的 leader，没有控制终端 → 父进程退出、终端关闭（SIGHUP）都不影响它。Windows 用 `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`。
- **stdin 是 /dev/null**（`cmd.Stdin = nil` 的 os/exec 默认），stdout/stderr 是 `~/.seek/tasks/<tid>.log`（父进程打开并以 append 交给 wrapper，wrapper 再以 append 交给任务）。任务不读终端，与 bash 工具 `detachStdin` 的动机同源。
- **为什么不是「一个 daemon 托管队列」**：D2 口径下常驻进程只做监听/路由/拉起；任务执行寿命靠**进程自身脱离**获得，不引入第四种常驻形态。本卡零常驻、零轮询线程：每条子命令按需扫描 `~/.seek/tasks/` 一次。

### 1.2 环境继承

三段，各自可解释：

| 段 | 环境 | 理由 |
|---|---|---|
| seek → wrapper | `childenv.Sanitized()` **+ 显式追加 `SEEK_HOME=<resolved>`** | wrapper 是 seek 自身，需要找到同一个注册表；但父进程的其余 `SEEK_*`、以及任何 `*KEY*`/`*TOKEN*` 不进 wrapper。追加在 scrub 之后 = last-wins（`internal/childenv` 文档里写明的用法）。 |
| wrapper → 任务 | `childenv.Sanitized()`（**不**追加 SEEK_HOME） | 任务命令来自用户/模型，属"代码不由 seek 控制"一类。嵌套 seek 继承父 SEEK_HOME 会把会话写到用户没指定的地方（`internal/childenv` 注释里的同一理由）。 |
| CWD | `--cwd`，缺省 `os.Getwd()` | 与 bash 工具 `policy.CWD()` 同一语义：命令里的相对路径有确定解释。 |

`config.bash_env_passthrough` 的逃生舱**不**跨到本面：那是 bash 工具的旋钮，任务面要显式传 env 时走 `--env K=V`（本卡不做，见 §5）。

### 1.3 退出码回收（两层，互不依赖）

1. **wrapper 自己收**：`cmd.Wait()` → 把 `exit_code` + `finished_at` 追加到 `~/.seek/tasks/<tid>.jsonl`（O_APPEND 单行，≤ PIPE_BUF 故原子）。**这是权威来源**：父进程（发起 spawn 的 seek）随时可以死，记录仍然完整——这正是"寿命脱离"的可观测面。
2. **父进程顺手回收，但不依赖它**：spawn 后 `go cmd.Wait()`。父活着时它负责 reap，wrapper 不会躺成僵尸；父先死则 wrapper 被 init 收养，由 init reap。**两个都不写「父必须活着」的假设**。

`kill` 是第三条路径：wrapper 被信号带走时来不及写记录，所以 `kill` 自己追加一条 `killed` 事实 + `finished_at`。**记录是只追加的事实日志，不是改写式 JSON 对象**——三个写者（spawner / wrapper / kill）因此不需要锁，折叠也与顺序无关；折叠规则里真实的 `exit` 优先于 `killed`，所以 kill 永远不会覆盖已经写下的真实退出码。

## 2. 状态折叠（不存状态，只存事实）

记录文件里只有事实（pid / 启动时刻 / exit_code / killed / finished_at）。状态是**折叠**出来的，不落盘：

| 事实 | 折叠出的状态 |
|---|---|
| 有 `exit` 事实 | `exited`（0）/ `failed`（≠0）——**优先于 `killed`** |
| 只有 `killed` 事实 | `killed` |
| 还没有 pid（wrapper 尚未报告） | `starting`（spawn 窗口只有微秒级；把活的任务报成死的是操作者唯一无法补救的方向，所以宁可显示 `starting`） |
| 否则 probe(pid) = 活着 | `running` |
| probe = 僵尸（Z） | `zombie`（父进程还活着但还没 reap——如实呈现，不假装 running） |
| probe = 消失，且没有 exit_code | `lost`（被 OOM/外部 SIGKILL 带走，退出码不可知） |

这与 AGENTS.md「状态可从 transcript 重建就不要镜像」同一条纪律在进程面的投影：**记录存事实，展示算状态**。

## 3. 与既有 `internal/bgjob` 的边界（不重造）

| | `internal/bgjob`（v6 柱 K，保留不动） | `internal/taskproc`（本卡） |
|---|---|---|
| 生命周期 | **会话内**：`Shutdown` 杀掉所有存活者，绝不跨 seek 进程 | **脱离**：跨 seek 进程存活，直到任务自己退出或被 `kill` |
| 状态位置 | 内存（`Manager.jobs` map） | 磁盘 `~/.seek/tasks/<tid>.jsonl`（另一个 seek 进程要看得见） |
| 输出 | 内存 ring buffer（64 KiB，模型侧 poll/wait） | 文件 `~/.seek/tasks/<tid>.log`（`attach` 用 stat+read 尾随） |
| 控制面 | `monitor` 工具（模型调用） | `seek ps/attach/kill`（人在终端调用） |
| 谁用 | bash 工具 `run_in_background` | `seek task run` |

**接缝写死**：bash 工具的 `run_in_background` **不改**（仍绑会话，仍是 bgjob——它服务"本回合内并行、会话结束就收摊"）；`seek task run` 是**新的显式 opt-in**（服务"关掉 seek 也要跑完"）。两条路不共享代码：共享会逼 bgjob 长出磁盘/跨进程字段，违反它"进程无关、纯内存"的定位（`bgjob.go` 开头注释）。要"后台且跨进程"的场景 = `seek task run`，文档里写明。

## 4. 子命令契约（最小面）

```
seek ps                    所有任务一行（ID/状态/退出码/耗时/命令）；默认全列，--running 只看未终结的
seek attach <id>           从日志头部打印并尾随；任务终结后打印 exit=… 并返回
                           --no-follow 只打印已有内容；--timeout 30s 尾随上限
seek kill <id>             先 SIGTERM 整进程组，--grace（默认 3s）内不退则 SIGKILL；落 killed 记录
seek task run "<command>"  spawn（--cwd 指定工作目录）；打印 tid + attach/kill 提示
```

退出码：`ps`/`attach`/`kill` 成功 0；未知 id / 坏参数 1（`kill` 对已终结任务是 0 + 说明，不算错误）。

**`kill` 的起始窗口**：记录里还没有 pid 时（spawner 已写 start 事实、wrapper 尚未报告 pid，窗口是微秒级但仍可从脚本里撞上）`kill` **拒绝执行并原样返回错误**，不写 `killed` 事实——给 pid 0 发信号是空操作，而写下的 `killed` 会让 `ps` 报「已杀」而任务还在跑，这是操作者无法补救的那个方向。重试即可。

## 5. 明确不在本卡（接缝已留）

- **detach 全语义**：会话状态移交（任务跑在哪个 session、transcript 归属）、断连续跑（`seek task run` 重启后接回同一条任务流）。本卡的 `start` 事实只有 `session` / `project` 两个**信息字段**（供将来看，不参与任何状态判定）。
- **`--env` 显式注入**、任务并发上限、任务 GC（现在按需扫描，不删文件；`seek ps` 读不动的坏记录跳过）。
- **模型面工具**：本卡不给 LLM 新工具（不加 schema → 前缀缓存字节不变）。`seek task run` 是人在终端的面。
- 僵尸态在 Windows 不建模（Windows 无 Unix 僵尸语义），`probe` 返回 running/gone 两值。

## 6. 实现落点

- `internal/taskproc`：事实日志读写（追加 + 折叠）、`Store.CopyLog`（attach 的增量读）、`Runner.Spawn` / `Runner.Kill`、`ProbePID` / `SignalGroup`（darwin sysctl `kern.proc.pid` / linux `/proc/<pid>/stat` / windows `OpenProcess`+`GetExitCodeProcess`），`detach` 与 `shellArgv` 按平台分文件。
- `internal/taskcli`：`ps` / `attach` / `kill` / `run` / `exec`（wrapper）分派；所有时间/探测/信号/睡眠/子进程执行都从 `taskcli.Env` 注入，故三子命令可表驱动测试假进程、超时、僵尸态。
- `cmd/seek/main.go`：`task` 早分派（无 API key、无会话）+ 顶级别名 `ps` / `attach` / `kill`。
- `internal/paths`：`TasksDir()` = `$SEEK_HOME/tasks/`（用户态写入的唯一新落点）。
