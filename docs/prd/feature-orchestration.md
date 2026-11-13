# Feature: 编排层追平——后台子代理 / 类型化 agent / 模型路由 / hooks 面

**所属版本**：独立 feature（编排承重轴，平台化线）
**前置阅读**：[`feature-subagent.md`](feature-subagent.md)（柱 G 已交付面——本文的扩基线）、[`feature-bash-monitor.md`](feature-bash-monitor.md)（柱 K 进程级后台先例）、[`v5.md`](v5.md) §2（跨柱约束六条）、[`v7.md`](v7.md)（autopilot/sandbox 已交付面）、[`docs/comparison.md`](../comparison.md) 头部「定位变更」+ §10
**状态**：📐 设计稿（2026-11-13，卡 c_ur7bup）
**定位依据**：用户 2026-11-13 指认（butler 决策流）——comparison.md 降级的正确口径是「**外围功能不追平，承重轴追平甚至反超**」，编排层四件是承重轴：①复杂任务拆解天花板 = 编排天花板；②dogfood 现在依赖 CC 形态会话管理 seek，编排不补则自举隔一层；③模型路由是三车道设计（DeepSeek 真值 / GLM 量 / 兜底）在编排层的兑现点。领先面确认不重做：无人值守编排（零 daemon cron / autopilot 舰队 / goal headless）与成本纪律。

---

## 1. 动机

v5 柱 G 交付了**同步**子代理：同 turn 内并行 spawn（`agent` 工具标 `ReadOnlyTool`，`pkg/agent` 对 ReadOnly 工具每调用一 goroutine）、worktree 隔离、权限单调收紧、成本归集。但编排轴上仍有四个"模型想表达而系统不让表达"的缺口，全部在复杂任务拆解的主路径上：

1. **父 turn 被子拖死**——`Manager.Spawn` 同步阻塞（`internal/subagent/manager.go` "blocks until it terminates"）。子跑 10 分钟，父 turn 卡 10 分钟；模型想"先派三个长任务出去、自己继续干别的"不可表达。CC 形态的对应物是 `Agent(run_in_background=true)` + 完成通知回注。
2. **子代理角色只能三选一**——`subagent_type` 是硬编码 enum（`internal/subagent/types.go` 的 `IsValid` switch），per-type 工具白名单/模型/努力档全部写死在 `templates.go`。项目想要一个"只准跑测试的 verifier"、一个"固定走 GLM 的量贩 explorer"，只能改源码。CC 形态的对应物是 `.zcode/agents/*.md` frontmatter 定义类型（per-type model / effort / tools）。
3. **全部子代理继承父模型**——`buildSubagentRunner` 里子 `agent.Config{Model: opts.getModel()}`（`cmd/seek/main.go:578`）。explore 型量贩调研本该走 GLM 量车道、judge 型该走 DeepSeek 真值，现在只能整机切 `--provider`。两车道地基（DeepSeek first-class + `pkg/llm/compatible` GLM 车道 + pricing 三口价）已全部就位，缺的只是**按 agent 类型选车道**的一层路由。
4. **hooks 面够不到编排生命周期**——子代理启动/结束、turn 结束、compact 前夜、通知发出，这些点既无 in-process 观察者也无 shell hook 事件；用户想在"每次子代理结束后跑个 lint"无法表达。同时注入通道与 prefix-cache 字节纪律的张力从未给出受控出口。

四件合起来是"**把柱 G 的同步子代理升级成一个可编排的代理团队**"。分期：**P1 后台子代理 + 模型路由**（体感最大、地基最全）→ **P2 类型化 agent**（P1 路由的配置面自然生长点）→ **P3 hooks 面**（编排可观测性 + 注入通道定规矩）。

## 2. 差距矩阵（对 CC 实测行为）

**证据口径**：CC 侧 = ZCode（CC 形态 harness）工具 schema / 系统提示**原文**，2026-11 实测（`run_in_background`、`SendMessage`、`TaskOutput`、agent-types frontmatter 等关键语句逐字引用）+ [`docs/comparison.md`](../comparison.md) 已核事实；seek 侧 = 本仓主干代码逐项回核（包/文件引用）。本节是**差距定性**，不是排期表——排期见 §4–§6 分期。

### 2.1 后台子代理生命周期

| 能力 | Claude Code 实测行为（证据） | seek 现状 | 差距定性 |
|---|---|---|---|
| 异步 spawn | `Agent` 工具带 `run_in_background`："runs the agent asynchronously; you'll be notified when it completes. Never fabricate or predict a pending agent's results — **the notification is never something you write yourself**" | `Manager.Spawn` 同步阻塞至子终态；同 turn 并行已具备（ReadOnlyTool 标记 + per-call goroutine） | ❌ 缺 agent 级异步；进程级先例已有（柱 K `bash run_in_background`） |
| 完成通知/唤醒 | harness 在后台代理完成时**注入通知唤醒模型**（同 turn 不阻塞、后续 turn 收到 task-notification） | 无通道。柱 K 明确延后了"job 退出自动 re-invoke agent"（feature-bash-monitor §2 不做什么）；柱 H 的 `schedule_wakeup` 是全新 `seek -p` 子进程、不带会话上下文 | ❌ 缺通知协议；Prepend 注入缝（`hooks.PrePromptOut.Prepend`）与 TUI 事件流是现成地基 |
| 续话复活 | `SendMessage`（to: `agent_<uuid>`）："continue a previously spawned agent with its context intact"（后台复活续话） | 子 transcript 平铺 + `seek subagent resume <sub-sid>` 提升 + `subagents.jsonl` orphaned 扫描**全部已有**；缺的是**父会话视角**的调度面——父 `-resume` 后，未收尾/已收尾未读的后台子结果如何回灌 | 🔶 存储已备，调度面缺 |
| 查询 / 终止 | `TaskOutput(task_id, block=true/false, timeout)` / `TaskStop`——模型侧主动 poll/wait/kill | `/agents` 面板 + `seek subagent kill` 是**人用**面；模型侧无 poll 面（sync 模式下也不需要） | 🔶 P1 靠通知注入送达结果，不做模型侧 poll 工具（§3 不做什么 #4） |
| 后台生命周期边界 | CC 依托 attended harness 常驻进程 | bgjob 语义：**session 死则 job 死**（零 daemon 纪律） | ✅ 架构选择已定，P1 直接继承同语义 |

### 2.2 类型化 agent

| 能力 | Claude Code 实测行为（证据） | seek 现状 | 差距定性 |
|---|---|---|---|
| 自定义类型 | 系统提示注入 Available agent types 列表（带各自描述），"Each agent type's model, reasoning effort, and tools come from its definition (**.zcode/agents/*.md frontmatter**)"——用户/项目可扩展 | 三模板硬编码：`general-purpose` / `explore` / `plan`（`types.go` + `templates.go`）；v5 §2 anti-goal 把注册表开放留给 v6 候选，一直未做 | ❌ 从写死到可配置 |
| per-type 工具白名单 | frontmatter `tools` 字段（如 visual-judge "Tools: Read, Bash"） | 模板内写死 `Filter` 白名单 | ❌ 同上 |
| per-type 模型/努力档 | frontmatter `model` / `reasoning effort`（如 explore 型配便宜快速模型） | 一律继承父（§1 缺口 3） | ❌ 同上（与 2.3 耦合） |
| scope 先例 | 用户级 `.zcode/agents/`（+ 插件分发） | skills 已有 project（`<project>/.seek/skills/`）> user（`~/.seek/skills/`）双 scope + 安装审批先例 | ✅ 对齐 skills 即可 |

### 2.3 模型路由

| 能力 | Claude Code 实测行为（证据） | seek 现状 | 差距定性 |
|---|---|---|---|
| per-type 选 provider/model | frontmatter `model`（CC 本体绑定 Anthropic，无跨 provider 路由） | 子继承父 client/provider/model/effort（`subagentRunnerOpts` 闭包） | ❌ 缺 per-type 路由；**跨 provider 路由本身是 seek 独有优势的兑现点**（comparison §9：多 provider 是 Seek 优势） |
| 车道就绪度 | — | 两车道已通：DeepSeek first-class（`pkg/deepseek`）+ GLM 订阅车道（`pkg/llm/compatible` + pinned bigmodel 端点，GLM 前缀缓存实测 cache_hit 已验证）；pricing 三口价已进表（`internal/pricing` ModelGLM53） | ✅ 地基全备，只缺路由层 |
| 成本/预算打通 | 状态栏不区分模型来源 | `Tracker.Record(u, model, tier)` **已按 record 归因模型**，`CumulativeCost()` 自动多模型合计；`pricing.Cost(model,…)` 已吃 GLM 卡 | ✅ 几乎零改动；`internal/budget` 缺 `glm-5.3` 上下文条目（现落 Default 128K） |
| key 解析 | — | `buildProvider`：flag → env key 探测链；无头车道 flag 不解析的坑已有 pitfall（docs/pitfalls.md「paseo provider 首次无头冒烟」） | 🔶 路由配置必须显式命名 provider，不走探测链 |

### 2.4 hooks 面

| 能力 | Claude Code 实测行为（证据） | seek 现状 | 差距定性 |
|---|---|---|---|
| 生命周期点覆盖 | PreToolUse / PostToolUse / SessionStart / SessionEnd / UserPromptSubmit / Stop / **SubagentStop** / PreCompact / Notification（harness 提示 "Hooks may intercept tool calls"；Stop/SubagentStop 类为文档口径） | in-process Registry 7 点（PrePrompt / PreToolUse 两个 decorator + PreTurn / PostTurn / PostToolUse / SessionStart / SessionEnd 五个 observer）；shell 面 5 事件（`pre_tool` deny、`post_tool`、`pre_prompt`、`session_start`、`session_end`） | 🔶 缺 SubagentStart/Stop、Stop（AgentEnd）、PreCompact、Notification |
| 注入通道 | UserPromptSubmit 类 hook 可附加上下文（API 侧缓存按 TTL 容忍字节漂移） | shell `pre_prompt` **故意**降级为 PreTurnObserver——"PRD §2.1 + §3.2 forbid shell stdout from entering the prompt byte sequence"（`shell_runner.go` 头注）；注入仅 in-process `PrePromptOut.Prepend`（user-role `<context>` 包裹、cache-volatile 区） | 🔶 不是缺功能而是**缺定规矩**：可注入区与缓存稳定区的分离从未成文，P3 给出明确取舍（§6 D16/D17） |

### 2.5 已有地基清单（防重造）

| 地基 | 出处 | 本文复用方式 |
|---|---|---|
| sync spawn / 并行分派 / worktree / `Policy.Spawn` / `Tracker.AdoptChild` | v5 柱 G（v0.6.0） | **不动**，P1 在其上加异步入口 |
| `subagents.jsonl` 事件源索引 + orphaned 扫描 + `StatusPromoted` | 柱 G §3.4 | 直接复用：后台子代理的事件流就是它 |
| 子 transcript 平铺（`writeSubagentTranscript`，schema_version=2，可 `-resume`） | 柱 G G1 修复 | 直接复用：通知/复活读子 transcript 末条 assistant |
| turn-ctx vs session-ctx 分离 + `Manager.Shutdown()` 全杀 + ring buffer | v6 柱 K `internal/bgjob` | **语义照抄**到 agent 级（最大的坑已趟过） |
| cron tick / `schedule_wakeup` / triggers 文件桥 / OS 通知 / webhook push | v5 柱 H + v6 柱 M | P1 不改；跨进程长任务的答案仍是它们（§3 不做什么 #2） |
| autopilot 舰队（headless 并行 worktree fleet + no-remote guard） | v7 柱 N | 不动；autopilot 是独立进程 driver，不是会话内后台 |
| GLM 车道（compatible + pricing 三口价 + TurnDone.CachedInputTokens） | token 三车道落地（narrative #2897） | P1 路由的量车道底座 |
| Prepend 注入缝（cache-volatile、`<context>` 包裹惯例） | `internal/hooks` PrePromptHook | P1 通知协议 + P3 注入通道的唯一注入口 |
| skills 双 scope + 写路径包独占红线 | v2 skill 生命周期（skillmgr/skillstats） | P2 类型化 agent 的 scope 与写路径纪律直接对齐 |

## 3. 设计目标与不做什么

### 目标

1. **父 turn 不被长子拖死**——`run_in_background` 异步 spawn，父 turn 立即返回句柄继续干活。
2. **完成必达**——后台子终态：进程内存活时下一 turn 注入通知；进程死了，父 `-resume` 首turn 回灌。通知是系统写的，模型永不编造（对齐 CC 契约原文）。
3. **per-type 车道**——agent 类型可配 provider/model/effort；explore→GLM 量车道、judge 类→DeepSeek 真值，成本照常归集显示。
4. **编排可观测**——子代理启停、turn 结束、compact 前夜、通知发出都有 hook 点；注入通道与缓存纪律的边界成文。
5. **零新常驻**——全部复用现有进程模型与调度面（turn-based + OS cron tick），零 daemon 纪律不破。

### 不做什么（明确边界）

1. ❌ **跨进程后台存活**——后台子代理生命周期 = seek 进程生命周期（同柱 K 语义：session 死则 job 死）。进程退出即全取消 + 事件落盘。跨进程/过夜长任务走 autopilot / cron（已有），不造第二套。
2. ❌ **空闲自动拉起模型**——通知只排队到"下一次模型本来就要跑"（用户发消息 / resume 首turn / 同会话下一 turn），**不**在空闲时主动发起 LLM 调用。CC 的"通知即唤醒"依托 attended harness 常驻循环；seek turn-based + 零 daemon，主动拉起只能是 daemon，否决。（无人值守且确需自动跟进的场景：spawn 时让模型自己叠一个 `schedule_wakeup`——已有工具。）
3. ❌ **模型侧 poll 工具**（`TaskOutput` 等价物）——结果靠注入送达，不靠模型轮询；人工查走 `/agents`。加工具 = schema 面膨胀 + 鼓励忙等，与"通知必达"目标互斥。（若 P1 落地后实战反馈强烈，P2 再评估。）
4. ❌ **嵌套 spawn 深度 > 1**——维持 v5 边界。
5. ❌ **CC 插件市场 / hooks 通用 args 改写**——维持 feature-shell-hooks 边界（deny or observe）。
6. ❌ **未授权的 shell stdout 进 prompt**——红线精确化为"未被显式授权的 shell 输出永不进 prompt"；唯一授权通道是 `pre_prompt_inject`（opt-in、有界、留痕，§6 D17）。

## 4. P1 —— 后台子代理生命周期 + 模型路由

### 4.1 后台子代理（M-O1.1 ~ M-O1.3）

#### D1 — `agent` 工具加 `run_in_background`，Manager 加非阻塞入口

schema `properties` **末尾**追加（一次性失效 prefix cache，同柱 K D9 先例）：

```json
"run_in_background": {"type": "boolean", "description": "Run detached: return a handle immediately instead of blocking until the subagent finishes. The final summary is delivered as a <context> notification on your next turn (or on session resume). Use for long research/implementation tasks you want to overlap with other work. Default false (blocking, current behavior)."}
```

`Manager` 新增 `SpawnBackground(ctx, args) BackgroundResult`：与 `Spawn` 共享 step 1–8（校验、容量门、`Policy.Spawn`、AdoptChild、sub_sid、`started` 事件、注册 active），**不调 Runner 同步等待**——改为 goroutine 绑 **session ctx** 跑完 step 9–11。`ManagerOpts` 增加 `SessionCtx context.Context`（main 在构造 Manager 时传入进程级 ctx）。

**ctx 分离（柱 K D5 同款最关键坑）**：sync `Spawn` 继续绑调用方（turn）ctx；background 绑 session ctx——否则父 turn 一返回子就被砍，"后台"语义破产。`Kill` / 容量门 / `ActiveCount` 对两种 spawn 一视同仁（同一 `m.active` map + `MaxConcurrent` 门）。

父 turn 收到的即时 tool result（确定性 bytes）：

```
[agent: background started sub-sid=20261113-103045-a1b2c3] <description>

— runner active; final summary will arrive as a <context> note on a later turn (or on resume). Track via /agents.
```

`background` 加入 `reason=` 封闭枚举吗？**不**——成功路径新前缀 `[agent: background started ...]`，失败路径沿用 `[agent: failed reason=...]`（含 background 启动失败）。前缀即契约，扩展项只加在闭合 token 之后（wire-format 纪律，同 `[plan: approved]` 先例）。

#### D2 — 完成通知协议（进程内存活）

后台子终态时 Manager 已经写 `subagents.jsonl` 终态事件（复用，不加字段）。新增一步：把**通知消息**排队到父会话的注入队列（`Manager` 持有，TUI 每 turn 取走）：

- **注入载体**：现有 `PrePromptHook.Prepend`——user-role message、`<context>` 包裹、落在 cache-volatile（本 turn）区。**不写历史、不碰 system prompt、不重写旧消息**（Token & prefix-cache 约束的全部三条红线一次满足）。
- **注入格式**（确定性模板，summary 体天然 display 区）：

```
<context>
[agent: background done sub-sid=20261113-103045-a1b2c3] <headline>

<summary body, capped at MaxSummaryBytes>

— turns: <n> · tokens: <prompt+completion>
</context>
```

失败/取消同理（`[agent: background failed reason=...]`）。多条完成按终态时间 FIFO 合并为**一条** Prepend（一次注入多条 `<context>` 块，避免多 turn 稀释）。
- **TUI 即时反馈**：沿用柱 G §4.3 先例——状态栏 `⤴ N agents` 计数 + 完成闪 `✓ agent done`；输入框上方提示"background agent finished — send any message to ingest"。
- **谁触发注入**：下一个 `Prompt()` 调用（用户发消息或 /steer）时，main 的 PrePrompt 链先取空注入队列。模型**下一 turn 必然**看到——这就是 turn-based 世界的"通知唤醒"。模型不得预测在途结果（写进工具 description，对齐 CC 契约原句的语义）。

#### D3 — 续话复活（进程死后）

进程退出（含崩溃）时后台子 goroutine 随 session ctx 取消。两条路都已有一半，补最后一公里：

- **干净退出**：main 的 shutdown 路径调 `Manager.Shutdown()`——遍历 active（sync+background）cancel，goroutine 终态分类照常写 `failed reason=canceled`。**不加**新事件类型。
- **崩溃**：下次启动 orphaned 扫描已有（started 无终态 → `orphaned`）。
- **父 `-resume` 首turn 回灌**：resume 装配时 fold `subagents.jsonl`，找出「上次会话 spawn 的、终态为 completed 且**未被注入消费**」的子——消费标记走 append-only 事件源：注入取队时追写一条 `{"event":"notified","sub_sid":...,"parent_turn":N}`（不改写已写的 `completed` 行），读该子 `transcript.jsonl` 末条 assistant（summary 全文，柱 G G1 已落盘），按 D2 同款格式在 resume 首turn Prepend。用户体感："接着上次会话，第一条消息前系统自动汇报昨晚后台代理的结果"。`orphaned`/`failed` 的后台子不回灌 summary（没有可信 summary），只回灌一行状态注记。

#### D4 — 容量、取消与并发

- `MaxConcurrent`（默认 3）共门——sync + background 合计；超门 `too_many_subagents`（现有 reason 复用）。
- Esc 取消当前 turn **不**杀 background 子（其 ctx 是 session 级）；`/agents` kill 照旧（sub-sid 寻址）。
- 并发安全：`m.active` 已有 mutex + `-race` 表（柱 G #6/#14 验收先例），background 不新增共享结构。

#### D5 — summary 持久化不加索引字段

`subagents.jsonl` 维持「Cost 不入索引」同款纪律：事件只带 tokens，summary 全文已在子 transcript。`notified` 事件只带 `sub_sid` + `parent_turn`。理由：索引是状态折叠用的窄流，summary 是 display 数据，塞进去只会让 fold 变贵。

### 4.2 模型路由（M-O1.4 ~ M-O1.5）

#### D6 — 配置面（P1 最小形态：内置三 type 的 override）

`~/.seek/config.json` 新增 `agents` 段（user 级先行；project 级 scope 随 P2 的 agents/ 目录一起开，见 D14）：

```json
{
  "agents": {
    "explore":         { "provider": "glm", "model": "glm-5.3", "effort": "low" },
    "plan":            { "provider": "glm", "model": "glm-5.3" },
    "general-purpose": {}
  }
}
```

- 键 = 内置 type 名（`general-purpose` / `explore` / `plan`）；值空对象/缺省 = 继承父（现状语义）。自定义 type 名（如 `judge`）在 P2 的 agents/*.md 里定义，P1 不认——未知键启动时警告忽略（不硬错——前向兼容）。
- `provider` ∈ {`deepseek`, `glm`, `anthropic`, `openai`, `gemini`}，**显式命名，不走 key 探测链**（pitfall「paseo provider 首次无头冒烟」的直接教训：探测链对残留 key 零抵抗）。命名的 provider 无 key → 启动警告 + 该 type 回退继承父（失败降级，不硬拒）。
- `effort` 语义沿用 `/effort`（low/medium/high）；`max_turns` 可选覆盖。
- 特权不变：路由只换脑子不换权限——`Policy.Spawn` 的 Restriction 仍按 type 模板走，配置**不能**指定 pref/workflow（那是 P2 frontmatter 的事，且同样只能收紧）。

#### D7 — ProviderRegistry 与 RunnerJob 传递

main 构造一次 `map[string]resolvedProvider`（仅含有 key 的 provider；deepseek 永在首位），注入 `subagentRunnerOpts`。`RunnerJob` 增加 `Provider string / Model string / Effort string`（Manager 从 type 配置解析后填入）。`buildSubagentRunner` 按 `job.Provider` 取 client/Provider 构造子 `agent.Config`；缺省 = 父实例（现状路径零变化）。GLM 车道 = `pkg/llm/compatible`（现有 pinned 端点 + 默认 `glm-5.3`）。

#### D8 — DeepSeek-only 工具按 provider 过滤（坑，必须在 P1 处理）

`think` / `fim_complete` 是 DeepSeek 专属工具。路由到 GLM 的 type，其工具白名单在派生子 Registry 时**剔除 DeepSeek-only 工具**（模板 Filter 之后追加一道 provider 兼容过滤），子 system prompt 的 skill manifest 若引用 dual-model 类 DeepSeek-only 流程，风险留提示级（不阻断）。不做反向检测——provider 能力表写死在代码常量（`map[string]set[string]`），不运行时探测。

#### D9 — 成本/预算/定价打通（几乎零改动，验收要钉死）

- `job.Tracker.Record(u, model, tier)` 已按 record 归因——子用 GLM 时 record 的就是 `glm-5.3` + `pricing.Cost` 的 GLM 三口价（NoOffPeakDiscount 正确处理）。`CumulativeCost()` 自动多模型合计，状态栏不动。
- `internal/budget.contextLimits` 补 `"glm-5.3": 1_000_000`（bigmodel 单一 1M-context 档，pricing 注释同源）；`/compact` 预警从此对 GLM 子会话不再误报。
- 子 footer 仍不含 cost（柱 G §3.2 纪律），显示走状态栏。

### 4.3 P1 验收（全部沙盒可验证：stub Runner / httptest fake / 临时 git repo，零真实 key）

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | `run_in_background=true` 调用立即返回（<1s），父 turn 不等子终态；返回串符合 `[agent: background started sub-sid=...]` 前缀契约 | 集成测试（stub Runner 睡 5s，断言 tool result 先回） |
| 2 | background 子终态后，父下一次 `Prompt()` 的 messages 含 Prepend `<context>[agent: background done ...]`，且**历史消息零改动**（前后 diff 只增本 turn） | 集成测试：对 Prompt 前后 session.Messages 做字节对比 |
| 3 | 多条后台完成合并为一次 Prepend、FIFO、各带独立 sub-sid | 单元测试（注入队列） |
| 4 | 父 turn cancel（Esc）后 background 子仍存活至自然终态；`/agents` kill 生效且写 `killed` | 集成测试（turn ctx cancel ≠ session ctx cancel） |
| 5 | 干净退出：`Manager.Shutdown()` 后所有 active 子终态事件为 `failed reason=canceled`，`subagents.jsonl` 无 started-无终态残留 | 单元测试 |
| 6 | 崩溃复活：手造 started-无终态索引 → 新进程扫描写 `orphaned`（现有）；resume 父会话首turn 注入「completed 且未 notified」子的 summary（读子 transcript 末条 assistant），`orphaned` 子只注入一行状态 | 集成测试（伪造索引+transcript 文件） |
| 7 | 注入消费回写 `notified` 事件；重复 resume 不重复注入 | 单元测试 |
| 8 | 路由：config `explore→glm` 时子 `agent.Config.Provider` 为 compatible 实例、model `glm-5.3`；无配置时与现状字节同路径 | 单元测试（ProviderRegistry stub） |
| 9 | GLM 路由的 type 工具白名单不含 `think`/`fim_complete`；deepseek 路由不变 | 单元测试（模板 Filter + provider 过滤表驱动） |
| 10 | 混合车队成本：父 deepseek + 子 glm 完成后 `CumulativeCost()` == 父 deepseek 费用 + 子 GLM 费用（各按三口价，off-peak 只折 deepseek） | 单元测试（fake Usage + pricing.Cost 交叉） |
| 11 | 命名 provider 无 key：启动警告 + 回退继承父，不 panic 不硬拒 | 单元测试 |
| 12 | `budget.Limit("glm-5.3") == 1_000_000` | 单元测试（一行） |
| 13 | `agent` 工具 schema 无 `run_in_background` 时旧行为字节级不变（不传字段 = sync 路径回归） | 现有柱 G 测试全绿（零回归） |
| 14 | `go test -race ./...` 全绿（subagent 并发表扩 background 用例） | CI |

### 4.4 P1 里程碑

| M | 内容 | 估时 |
|---|---|---|
| **M-O1.1** | `SpawnBackground` + session-ctx 接线 + `Manager.Shutdown` + schema 字段 + wire-format | ~2 天 |
| **M-O1.2** | 注入队列 + Prepend 送达 + `notified` 事件 + TUI 徽标/提示 | ~2 天 |
| **M-O1.3** | resume 回灌（fold + 子 transcript 读取 + orphaned 状态注记） | ~1.5 天 |
| **M-O1.4** | ProviderRegistry + config `agents` 段 + RunnerJob 传递 + D8 provider 过滤 | ~2 天 |
| **M-O1.5** | budget 补条目 + 成本交叉测试 + 文档（guide-subagent / guide-config / comparison 勾选） | ~1 天 |

## 5. P2 —— 类型化 agent（M-O2.x）

#### D10 — 文件形态对齐 skills 布局，scope 先例照抄

```
<project>/.seek/agents/<name>.md     # project 级（checked-in，团队共享）
~/.seek/agents/<name>.md             # user 级
```

单文件 `.md` + YAML frontmatter + body（角色 prompt）。解析复用 skill loader 的 frontmatter 形状处理；scope 解析顺序 project > user，同名 project 覆盖 user（skills 同款）。**写路径只归新包 `internal/agentsmgr`**——对齐 AGENTS.md 红线「`~/.seek/skills/` 只有 skillmgr/skillstats 可写」：`~/.seek/agents/` 同样只有 agentsmgr（+ 统计若做）可写，loader 只读。

#### D11 — frontmatter 契约

```yaml
---
name: verifier
description: Runs the test suite and reports failures. Read-only except tests.
tools: [read, grep, list_dir, git, bash]     # ⊆ 父注册表；省略 = general-purpose 全集
provider: glm                                 # 可选，D6 同款显式命名
model: glm-5.3                                # 可选
effort: low                                   # 可选
workflow: plan-analyze                        # 可选，只能收紧（analyze）或继承；无 execute 放宽
max_turns: 60                                 # 可选
---
You are a test verifier subagent. …（body = 角色 prompt，替换内置模板的 Role 段；
摘要长度提示与 AGENTS.md/M-index/skill manifest 继承段仍由 ComposeSubagent 拼装，§3.6.1 不变）
```

内置三 type 保留为编译期默认（无任何文件时行为与 v0.6.0 完全一致）；文件可覆盖内置名（`explore.md` 覆盖内置 explore——P1 的 config `agents` 段继续有效但优先级最低：**内置默认 < config 段 < agents/*.md**）。

#### D12 — schema 张力：enum 动态化的字节纪律

`subagent_type` 从封闭 enum 改为**启动时从注册表生成的 enum**（含内置 + 文件 type）。约束：

- schema 字节在**进程内**恒定（启动构建一次，package var 缓存）——同 turn 间 cache 不破。
- 跨启动随配置变化 = 一次性失效，可接受（先例：bash schema 加 `run_in_background` 字段、agent 工具 description 更新）。**验收口径从「多次启动间字节一致」改为「同配置多次启动间字节一致」**。
- Available types 的描述（frontmatter `description`）注入 enum 的 per-value description + 工具 description 尾段——对齐 CC 的 agent-types 列表注入形态（§2.2 证据）。描述长度设上限（~200 字/type），防 schema 膨胀。

#### D13 — 安全：只能收紧

- `tools` ⊆ 父注册表 ∩ 非禁注（`agent`/`ask_user` 永不，嵌套防线不破）；未知工具名 = 配置错误，启动警告 + 该 type 降级为 general-purpose 工具集（不静默收窄——白名单拼错不能意外放大）。
- `workflow: plan-analyze` 允许（收紧）；`plan-execute`/放宽 pref 一律拒绝（配置错误）——`Policy.Spawn` 的 Restriction 单调性是硬门，frontmatter 只是它的又一个来源。
- D8 的 provider 过滤照常作用于自定义 type。
- 校验器全量单测：每个非法形状（放宽 pref、未知工具、未知 provider、无 name）都有明确错误。

#### D14 — 与 P1 config 段的关系

P1 的 config `agents` 段是内置 type 的最小 override（不动文件系统）；P2 的 agents/*.md 是全形态（自定义 type + 工具集 + prompt）。两层并存、优先级见 D11；文档引导用户最终形态写 agents/*.md，config 段定位为"只换模型不改行为"的快捷键。

### P2 验收（沙盒可验证）

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | project `.seek/agents/verifier.md` 定义后，`subagent_type` enum 含 `verifier`；同配置两次启动 schema 字节一致 | 单元测试（临时目录 + schema hash） |
| 2 | spawn `verifier` 子的 Policy/workflow/tools/provider 与 frontmatter 声明一致；`plan-execute` 声明被拒 | 表驱动单元测试（覆盖全部非法形状） |
| 3 | 同名覆盖：project `explore.md` > user `explore.md` > config 段 > 内置 | 单元测试（scope 栈） |
| 4 | `~/.seek/agents/` 写路径仅 agentsmgr（loader 只读；grep 审计无越界写者） | 代码审查 + 单元测试（loader 无写 API） |
| 5 | 无任何 agents 文件 + 空 config 时，行为与 v0.6.0 字节同路径（内置三 type 回归） | 柱 G 现有测试零回归 |
| 6 | body 缺失/空 → 配置错误警告 + 拒绝注册（角色 prompt 是必要字段） | 单元测试 |

里程碑：**M-O2.1** loader+scope+校验（~2 天）；**M-O2.2** schema 动态 enum + Manager/模板表改造（~2 天）；**M-O2.3** agentsmgr 写路径 + `seek agents list/check` CLI + 文档（~1.5 天）。

## 6. P3 —— hooks 面扩展（M-O3.x）

#### D15 — 新观察点（全部 observer，无返回值，deny 面不扩）

| 新点 | 触发时机 | in-process | shell（hooks.toml） |
|---|---|---|---|
| `SubagentStart` | Manager 发 `started` 事件后（sync+background） | `SubagentStartObserver` | `[[subagent_start]]` |
| `SubagentStop` | 任一终态事件后（含 killed/orphaned） | `SubagentStopObserver` | `[[subagent_stop]]` |
| `Stop` | `Agent.Prompt` 返回（AgentEnd——一次回合整体落定） | 新增 `StopObserver`（与 per-turn 的 PostTurn 分层：Stop 一回合一次） | `[[stop]]` |
| `PreCompact` | `/compact` 执行前 | `PreCompactObserver` | `[[pre_compact]]` |
| `Notification` | OS 通知 / ask_user 弹出时 | `NotificationObserver` | `[[notification]]` |

env 变量沿 `SEEK_*` wire-format（`SEEK_SUBAGENT_SID` / `SEEK_SUBAGENT_TYPE` / `SEEK_EVENT`…）；audit log 加 `subagent_sid` 字段（柱 G §5.3 已预留口径）。全部 observer-only：shell hook 超时只 log 不 deny（现 observer 语义）。SubagentStart/Stop 是 P1 后台编排的审计刚需（"每次子代理结束跑 lint"用例的落点）。

#### D16 — 注入通道的三区模型（把张力写成规矩）

| 区 | 内容 | 注入政策 |
|---|---|---|
| **cache-stable 区** | system prompt 段 1–3（AGENTS.md / M-index / skill manifest）、tool schema bytes、全部历史 messages | **绝对禁注入**。任何机制（in-process / shell 授权通道）不得改写。已注入字节永不回收重写。 |
| **cache-volatile 区** | 本 turn 的 Prepend（user-role `<context>` 块）与 UserText | **唯一注入区**。P1 通知、P3 授权通道、记忆注入全走这里。天然随 turn 沉淀为历史，旧字节不再变。 |
| **session-boundary 区** | 启动一次性装配：agent types 列表进 schema/description、`SessionDate` header、hooks 快照 | 仅在会话边界（进程启动 / `-resume`）重算，进程内冻结。变更 = 一次性 cache 失效（已接受的发版代价）。 |

取舍一句话：**DeepSeek 前缀缓存按字节命中最强纪律，注入能力以"只写未来、不改过去、启动冻结"三律换取**——比 CC（API 缓存按 TTL 容忍漂移）更严，这是 seek 的成本护城河，不为功能便利松绑。

#### D17 — shell 注入通道：显式授权、有界、留痕的 stdout

红线**精确化而非推翻**：feature-shell-hooks §2.1 的原红线"shell stdout 永不进 prompt"收紧表述为"**未被显式授权的** shell stdout 永不进 prompt"。授权通道：

- hooks.toml 新事件 `pre_prompt_inject`（opt-in，默认无）：

```toml
[[pre_prompt_inject]]
name = "ci-status"
command = "cat .seek/ci-context.txt 2>/dev/null"   # 惯用法：hook 从 context 文件读静态内容，
max_bytes = 2048                                   # 而不是每次现场生成动态输出（确定性自担）
```

- 语义：每 turn PrePrompt 时执行，**stdout 作为 `<context>` 块进 Prepend**（cache-volatile 区），`max_bytes` 硬截断 + 溢出标记。确定性责任在用户（hook 自己保证同输入同输出；文档明示"非确定性输出 = 每 turn cache 半失效"的代价公式——只损本 turn 之后的增量，历史不受影响）。
- 与直接放开 `pre_prompt` stdout 的差别：显式 opt-in 事件名（用户签名的就是这个风险）、长度上限、`<context>` 包裹与 Prepend 同缝复用、audit log 记录注入字节数。未授权路径（`pre_prompt` observer）的字节纪律不变。
- 否决项（记录理由）：让 hook 改写 UserText / 改 system prompt——改写历史区，直接违反三律，永不开放。

### P3 验收（沙盒可验证）

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | 后台子 spawn/终态触发 `subagent_start`/`subagent_stop` shell hook，env 带 `SEEK_SUBAGENT_SID`；audit log 行含 `subagent_sid` | 集成测试（fake hook 脚本写临时文件） |
| 2 | 新观察点全部无返回值、超时只 log；deny 面未扩（`pre_tool` 仍是唯一 deny 点） | 单元测试 + 代码审查 |
| 3 | `pre_prompt_inject` 输出进本 turn Prepend `<context>`；>max_bytes 截断 + 标记；历史消息字节零改动 | 集成测试（前后 messages diff） |
| 4 | 未配置 `pre_prompt_inject` 时 PrePrompt 字节路径与现状一致（零回归） | 现有测试全绿 |
| 5 | 三区模型写入 guide-hooks.md 并有守护测试：schema bytes / system prompt 段在 hook 执行前后 hash 不变 | 集成测试（hash 断言） |

里程碑：**M-O3.1** 新观察点 + shell 事件 + audit（~2 天）；**M-O3.2** `pre_prompt_inject` 授权通道 + 三区文档（~1.5 天）。

## 7. 与已交付面的关系（防重造对照）

| 已交付面 | 本文动作 |
|---|---|
| 柱 G sync spawn / 三模板 / worktree / `Policy.Spawn` / `AdoptChild` / `/agents` / `seek subagent` | **扩展不重写**：Spawn 原路径字节不动；SpawnBackground 共享 step 1–8；模板表从编译期常量长成"常量默认 + 配置覆盖" |
| 柱 K bgjob | **零改动**；只引用其 ctx 分离/Shutdown/并发上限语义作为设计先例 |
| 柱 H routines / wakeup / triggers / 柱 M push | **零改动**；跨进程跟进 = 现有 wakeup（§3 不做什么 #2 的出口） |
| v7 柱 N autopilot fleet | **零改动**；autopilot 继续走自己的 headless driver；P1 落地后 autopilot 可选受益于 per-type 路由（fleet 的 type=general-purpose 可配 GLM 量车道），属配置层红利非代码依赖 |
| v7 柱 O sandbox | 零改动；后台子代理同样走 per-worktree confine（已有 wiring） |
| v3 柱 B hooks Registry / shell hooks | **扩展不重写**：新 observer 走 `Registry.Register` 现有分发；ShellRunner 加事件不改既有五事件语义 |
| feature-shell-hooks「stdout 禁入 prompt」红线 | **精确化不推翻**：从"永不"到"未授权永不"，授权通道有长度上限与文档化代价 |

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 后台子绑错 ctx（turn ctx）→ turn 一结束子被砍 | D1 显式 session ctx + 验收 #4 专测（柱 K 最大坑的直译） |
| 注入队列与用户消息竞态（同 turn 同时到） | 注入在 PrePrompt 链首步同步取空（单 goroutine 时点），无并发写；测试 #2/#3 |
| `notified` 事件与 resume fold 的时序洞（注入后崩溃，notified 未写） | 注入取队时**先写 `notified` 再 Prepend**——最坏丢一次注入（下次 resume 重灌），绝不重复灌 |
| 模型在等后台结果时空转 poll（反复发消息催） | 工具 description 明示"notification arrives on your next turn; do not wait idly"；CC 同款契约原句语义 |
| GLM 车道限流叠加 MaxConcurrent=3 并发放大 | 路由不放宽容量门；config 段可加 `max_turns` 收紧；实战反馈再调 |
| per-type 路由 + DeepSeek-only 工具（think）静默失败 | D8 启动期过滤 + 验收 #9 |
| 动态 enum 后 schema 膨胀（type 描述过长） | D12 描述上限 + schema hash 测试钉字节 |
| frontmatter `tools` 拼错静默放大到全集 | D13 未知工具名 = 配置错误拒绝注册，不 fallback 到全集 |
| `pre_prompt_inject` 被滥用改写历史 | 三区模型 + 验收 #3/#5 hash 守护；UserText/system prompt 改写显式否决 |
| 双 summary 源（completed 事件 vs transcript）漂移 | D5 单源纪律：索引永不带 summary，复活只读 transcript |

## 9. 后续版本与开放问题

- **模型侧 poll 工具**（TaskOutput 等价物）：P1 显式不做；若"通知必达"在实战中被证明不够（如模型需条件等待多个后台子），P2 评估 `monitor` 语义扩展到 agent 句柄。
- **子 → 父流式回显**：柱 G 已留 v0.6.x dot 候选，与本 P1 正交。
- **后台子的前台化**（`TaskOutput(block)` 等待）：同上，依赖 poll 面先立。
- **per-type ask_user 开放**：维持柱 G 保守（disambiguation UI 未做前不开）。
- **编排层 eval**：P1 落地后补 tool-selection eval 用例（"长任务该 background"），进 eval/cases/——内部回归口径，不做公开跑分（#2575）。
- **开放问题**：GLM 1M context 档位若未来分档，budget 表跟随 pricing 同步节奏；`pre_prompt_inject` 的确定性代价是否值得在状态栏给"本 turn 注入 N bytes"提示，M-O3.2 实现时定。
