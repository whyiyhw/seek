# Feature: `/goal` —— 单 agent 死磕到达标的自治循环（v0.8.x · 对标 Claude Code `/goal`）

**所属版本**：v0.8.x（CC-parity 单点；与 v7 柱 N/H 正交但可组合）
**前置阅读**：[`feature-autopilot.md`](feature-autopilot.md)（柱 N —— **多** agent fan-out，本特性是它的**单** agent 对偶，别混）、[`comparison.md`](../comparison.md) §1（CC gap 追踪）、CLAUDE.md「Token & prefix-cache constraints」（本特性的循环必须 append-only、不能改写历史）
**状态**：✅ **全部交付（M-goal.1–4）**。.1 driver+judge+caps+stall（97.5%）· .2 TUI `/goal` 事件驱动循环+徽章+Esc · .3 headless `seek goal run`（真 `AgentWorker` + `Driver.OnTurn`，真环境 e2e）+ session `Goal` 字段+resume · .4 `cron create --goal`（仿 `--autopilot`，jobWire round-trip、`DefaultSubprocess` 起 `goal run`）+ **no-remote 守卫**（主 agent bash `WithDeny(IsRemoteMutating)`，因 goal 跑的是主 agent 而非子代理）+ **yolo-local**（dispatch 时 `SetPref(PrefYolo)`，远程仍被守卫挡）+ webhook push（goal.completed/stopped）。**真环境 e2e**：写文件条件 turn1 not-met→turn2 写 DONE.txt→met（证实 yolo-local 能改、多轮续作通）。全仓 vet+race 绿。两项关键决策已拍板（见 §3）。
**估时**：~3-4 天

**一句话**：`/goal <条件>` —— seek 跨多轮一直干到「条件达成」为止，每轮结束用一个**便宜 DeepSeek 模型**判定是否满足，没满足就自动续一轮，达成/触上限/被取消即停。对标 Claude Code 2.1.139（2026-05-11）刚加的 `/goal`，**但 seek 版能 headless + 被 cron 调度**——这是 CC 的 session 级 `/goal` 做不到的差异化。

---

## 1. 动机 / 为什么现在做

CC 在 v2.1.139 把「设一个完成条件、自己死磕到达标」做成了内置 `/goal`（Haiku 每轮判定）。这正好戳在 seek「自治」叙事的一个空格上:

- seek 已有 **Autopilot（柱 N）**——但那是**多** agent 分解 + 并行 worktree fleet,是"一支团队"。
- seek **没有**「**单** agent 在同一对话里持续推进直到某条件满足」这种轻量循环。
- 不补,pitch 里"CC 不能自己一直干到完成"这句以后会被 `/goal` 反驳。

**补它的同时拿差异化**:CC 的 `/goal` 是 **session 级**(只在交互会话里)。seek 把它做成**也能 headless + cron**,于是有了 CC 做不到的组合——「**每晚朝 `<条件>` 干到达成,push 手机**」(= `/goal` × 柱 H 时序自治 × 柱 M 推送)。

---

## 2. 设计

### 2.1 核心循环（单 agent，同一对话）
```
/goal <condition>
  把 <condition> 作为工作指令注入(它既是方向也是验收标准)
  loop:
    跑一轮 agent turn（正常工作:工具/编辑/bash,走正常 permission 闸）
    turn 自然结束(end_turn)后 →
      judge(便宜模型): 给定 <condition> + 本轮产出摘要 → {met: bool, reason, hint?}
      met=true  → 清除 goal、报告、停
      met=false → append 一条续作 user 消息("尚未达成,因为 <reason>;<hint>。继续。") → 下一轮
    上限守卫:max-turns / 总 token 预算 / wall-clock timeout / Esc 取消 / 无进展 stall 检测
```

### 2.2 判定 = 便宜 DeepSeek 模型每轮判（已决策）
- **复用 autopilot `DeepSeekDecomposer` 的同款模式**:一个独立的 `deepseek.Client.Chat` 调用,模型默认 `deepseek-flash`(可配 `goal.judge_model`),prompt = 条件 + 本轮的助手末条消息 + 工具结果摘要,要求返回 JSON `{met, reason, hint}`,健壮解析(容忍 fence/散文,仿 `parseTasks`)。
- **判定调用是独立的、不进主对话**——单独的 client.Chat,有自己的 cache 命名空间,**绝不污染主对话字节**(见 §2.4)。
- judge 只喂**本轮增量**(末条 assistant + tool outcomes),不喂全历史——控成本,每轮多一次便宜调用而已。
- 为什么不用"工作模型自报 done":模型给自己批作业不可靠;独立便宜模型判更稳,且 token 几乎可忽略。(`/goal force`/`done` 工具留作 v2 的可选加速。)

### 2.3 接口 / 命令（已决策:TUI + headless）
| 形态 | 用法 | 说明 |
|---|---|---|
| TUI | `/goal <condition>` | 设条件并开始循环;状态栏浮层显示 条件/轮数/elapsed/token |
| TUI | `/goal` | 查当前 goal 状态 |
| TUI | `/goal clear` / `Esc` | 提前停 |
| headless | `seek goal run "<condition>"` | 一次性跑到达成/上限,打印进度,退出(仿 `seek autopilot run`) |
| cron | `seek cron create --goal "<condition>" ...` | 仿 `cron create --autopilot`:定时起一个 headless goal 跑;达成后柱 M webhook push |

TUI 接入点 = `internal/tui` 的 `dispatchCommand`(挨着 `/plan` `/yolo`)。headless = `cmd/seek` 加 `goal` 子命令分派(仿 `autopilot run` 那段)。

### 2.4 prefix-cache 约束（load-bearing）
循环靠**追加新 user 消息**续轮 → 主对话历史 **append-only 增长**,缓存保持热(这正是 seek 省钱的命根)。**禁止**为了"省 context"去摘要/改写老消息(违反 CLAUDE.md 的字节确定性)。judge 是旁路调用,不碰主流。

### 2.5 resume
把 active goal 存进 **session header 标量字段**(仿 `Yolo`/`Plan`,加 `Goal string` 到 `session.New` + header):`seek -resume` 时若 header 有未清除的 goal,继续循环。符合"从 transcript/header 重建状态,不另存并行 state 文件"的约定。

### 2.6 无人值守安全（headless / cron 路径）
单 agent `/goal` 工作在**当前工作树**(不像 autopilot 进 worktree)——所以 cron×goal = 无人值守改主仓,风险高于 autopilot。复用现成安全原语:
- **默认套 no-remote 守卫**(`bash.WithDeny` + `IsRemoteMutating`,柱 N 同款):cron/headless goal 不能 push/开 PR。
- **可选 OS 沙箱**(柱 O `WithSandbox`):headless goal 限制写到项目目录。
- 交互 TUI 路径有人在场,走正常 permission 闸(Ask 模式每危险操作仍弹确认),无需额外守卫。
- 文档写清:`cron --goal` 是"无人值守改主仓",建议配 `--yolo` + 沙箱,且默认 no-remote。

---

## 3. 关键决策（已拍板）

| # | 决策 | 选择 | 理由 |
|---|---|---|---|
| D1 | 判定机制 | **便宜 DeepSeek 模型每轮判** | 对标 CC Haiku;复用 autopilot client.Chat;比"模型自评"可靠;成本可忽略 |
| D2 | v1 范围 | **TUI + headless,可 cron** | 拿 CC 做不到的 `/goal × 时序自治` 差异化组合 |
| D3 | 命令名 | `/goal`(镜像 CC) | 熟悉度 + "我们也有"叙事;headless `seek goal run` 仿 `autopilot run` |
| D4 | 与 Autopilot 关系 | **独立特性,不合并** | 单 agent 同对话 vs 多 agent worktree fleet,是两个东西;各自简单 |

---

## 4. 安全 / 上限（默认值）

- `goal.max_turns`(默认 25)、`goal.timeout`(默认 30m,headless)、`goal.token_budget`(硬上限,到顶即停)。仿 autopilot `Caps`。
- **无进展 stall 守卫**:连续 N 轮(默认 3)无工具调用/无编辑且 judge 仍 not-met → 判定卡住,停并报告(防 judge 反复说 not-met 却原地打转烧 token)。
- 取消:TUI `Esc`/`/goal clear`;headless ctx 取消/SIGINT。
- 每轮 judge 的 not-met `reason` 进报告,达成或停止时给完整轨迹(轮数/token/why-stopped)。

---

## 5. 不做什么（v1 边界）

- ❌ **多 goal 并行**(一个 session 一个 active goal)。
- ❌ **goal 套 autopilot**(goal 内再 fan-out 多代理)——留 v2,先把单 agent 循环做扎实。
- ❌ **工作模型自报 done 的快路径**(D1 已选独立判;`done` 工具留 v2 可选)。
- ❌ **judge 看全历史**(只看本轮增量,控成本)。
- ❌ 改写/摘要主对话历史(违反 prefix-cache 字节确定性)。

---

## 6. 测试（按 CLAUDE.md「test the failure modes」）

- **判定 JSON malformed**:judge 返回 fence/散文/缺字段 → 健壮解析,解析不出按 not-met 续轮(别崩、别误判达成)。
- **max-turns / timeout / token-budget 触顶**:停在上限、报告 why-stopped,不无限烧。
- **stall 守卫**:构造连续无进展轮 → 触发停。
- **循环中取消**(ctx.Done / Esc):中途 turn 切断、干净停、状态清除。
- **resume**:存了 active goal 的 session reload → 继续循环;无 goal 的正常 reload 不受影响。
- **prefix-cache 不变量**:断言续轮只追加消息、不改写既有历史(可对历史切片做 byte-equal 回归)。
- **headless × no-remote 守卫**:goal 子代理跑 `git push` 被挡(复用柱 N guard 测法)。
- judge 调用走 `httptest` 假后端(无需真 key)。

---

## 7. 里程碑

| 里程碑 | 内容 |
|---|---|
| **M-goal.1** ✅ | `internal/goal` 包:循环 driver(turn→judge→续/停)+ `Caps`(max-turns/timeout/budget)+ stall 守卫(连续无工具调用)+ 便宜模型 judge(`DeepSeekJudge` client.Chat + `parseVerdict` 健壮 JSON 解析)。`Worker`/`Judge` 窄接口 + 假后端单测:met/续到 met/max-turns/stall+reset/budget/judge-error-不中断/worker-error/pre-cancel/cancel-mid-loop/timeout/parseVerdict 容错。**18 测试 `-race` 绿、97.5% 覆盖、gofmt 干净**。 |
| **M-goal.2** ✅ | TUI 接线:`/goal <cond>` 起循环(命令设状态→`goalStartMsg`→`submit`,避免丢 streaming model)/ `/goal`(状态)/ `/goal clear` + 状态栏 `🎯 N/M` 徽章 + Esc 取消(`handleStreamEnd` wasCanceled→clear)。事件驱动:turn 自然结束→`goalJudgeCmd`(旁路 judge,不污染主对话)→`goalVerdictMsg`→met 停 / max-turns 停 / stall 停 / 否则 `goal.Continuation` 续轮(决策顺序镜像 Driver)。judge 经 `Options.GoalJudge` 注入。**12 TUI 测试 `-race` 绿**(start/no-judge/mid-stream 拒绝/status/clear/met/continue+续作带 hint/maxturns/stall/stale-drop/judge-error-续/judgeCmd/徽章),全仓 vet+race 绿。 |
| **M-goal.3** ✅ | headless `seek goal run "<cond>"`(`runGoal`：复用同步 `Driver` + `goal.AgentWorker`——drain `agent.Prompt` 累计 text/toolcalls/tokens；`Driver.OnTurn` 进度到 stderr，报告到 stdout)。**真环境 e2e 跑通**(scratch dir，turn1 判定 met)。session header `Goal` 字段(persistSession 写、停时清盘、round-trip 测)+ `seek -resume` 自动续跑(构造期 re-arm gated-on-judge、`Init` 自动续 + 显著提示可 Esc)。新增 9 测试(AgentWorker 累计/忽略非 assistant/ErrorEvent/cancel、OnTurn、resume re-arm/gated、goalStartMsg→submit、Goal round-trip)`-race` 绿。 |
| **M-goal.4** ✅ | cron 接线:`cron create --goal`(仿 `--autopilot`,`--autopilot`/`--goal` 互斥)→ `Job.Goal`(jobWire round-trip)→ `DefaultSubprocess` 起 `seek goal run <prompt>`。**no-remote 守卫**:goal 跑主 agent(非子代理),故在主 registry 处给 bash `WithDeny(autopilot.IsRemoteMutating)`(goalMode 早探测)。**yolo-local**:dispatch 时 `policy.SetPref(PrefYolo)` 让无人值守能改本地,push/PR 仍被守卫挡。达成 webhook push(`goal.completed`/`goal.stopped`,复用 `WebhookDispatcherFromConfig`)。**真环境 e2e** 写文件条件跑通。新增 5 测试(DefaultSubprocess goal/autopilot-precedence、Job round-trip、cli `--goal`/互斥)`-race` 绿。 |

---

## 8. 对比定位（写进 comparison.md CC 节）

| | CC `/goal` | seek `/goal`(本特性) |
|---|---|---|
| 判定 | Haiku 每轮判 | 便宜 DeepSeek 模型每轮判 |
| 范围 | session 级(仅交互) | **TUI + headless + cron** |
| 差异化 | — | **`/goal × cron × 手机 push`**:睡觉时朝条件干到达成再叫醒你(CC 做不到) |

> 落地后更新 `comparison.md` §1:`/goal` 从"CC 独有"改为"对等,且 seek 可 headless/cron 组合"。
