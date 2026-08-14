# dsh 源码分析：deepseek-harness 架构解剖

> **分析日期**：2026-08-13 · **复核日期**：2026-08-13（对 clone 回树逐条验证，见文末[复核记录](#附-b复核记录2026-08-13)）
> **仓库**：`deepseek-ai/deepseek-harness` @ `47f9438`（clone 至 `~/code/github/deepseek-harness`）
> **规模**：~520k 行 TypeScript，`packages/` 下 **49 个顶层分组 / 226 个 package**（连同 `apps/` `native/` `python/` 共 233），pnpm workspace monorepo
> **定位**：DeepSeek 官方开源的 agent harness，Cordis 插件框架（"everything is a plugin"），developer preview（README 明示 compatibility-breaking changes）
> **分析方式**：4 个只读探索子代理分领域读源码 → 逐条回树复核；所有结论附 `路径:行号` 引用（路径已按真实目录层级补全，首稿曾系统性遗漏 group 目录层）

---

## 0. 一句话结论

dsh 把"**事件溯源 + 可替换缝**"做到了目前开源 agent harness 里的极致：会话日志是唯一事实源，模型看到的任何输入都能从日志重放；fs/shell/subagent/sandbox 全是可替换的服务缝，换云沙箱 = 换三个插件，模型面零改动。它的工程水平比 README 给人的印象高得多。短板在：时序自治弱（无跨会话 cron）、无 DeepSeek 商业面优化（**FIM 与峰谷计价全仓零命中**）、Node 部署链重、developer preview 阶段 API 漂移风险。

> **⚠️ 首稿订正**：本文初版在这里写过"**成本优化无感知**"，**是错的**。dsh 的前缀缓存纪律是**结构性**的（事件溯源推导请求 ⇒ 前缀天然 append-only），比 seek 的约定式更强，且有打真 API 的 e2e 断言缓存命中。完整证据与订正见 [§8.2](#82-订正记录成本优化那一行为什么首稿是错的)。

**与 seek 的本质差异不是"谁优化得更深"，而是"为谁优化"**（详见 [§8.0](#80-先分清两类差异谁在为谁优化)）：dsh 是 DeepSeek 官方为**自己的模型 + 自己的云**做的 harness（e2b 云沙箱 / web app / Python SDK / ACP，全部指向托管服务与编辑器集成）；seek 是第三方为**本地单人开发者**做的（单二进制 / 本地沙箱 / git worktree / 跨会话 cron）。**两条赛道，不是同一产品的两个版本**——这条线能解释下文几乎所有差异。

在这个前提下，可以保留的次级概括是：**seek 是"深度优化 + 编译期纪律"，dsh 是"运行时可组合"**。

---

## 1. 架构基石：Cordis 与三层插件树

- Cordis 框架：插件贡献服务、类型化事件、可逆效果（注册即效果，插件卸载时自动回滚）——**没有 privileged core 需要 patch**（`docs/architecture.md`）
- 运行中的 dsh 是插件树：`profile`（`$DSH_HOME/profiles/<name>` 目录：package.json + `dsh.profile` manifest 的 ordered bundles + 用户 `cordis.patch.yml`）→ `bundle`（npm 包，manifest 声明 `"dsh": {"bundle": {"patch": "./cordis.patch.yml"}}`）→ 层叠顺序：profile 的 bundles 依次 → profile 级 patch → **home 级 patch（outranks profile 级）** → `--patch` 覆盖（`packages/boot/app-boot/README.md:38,43`）
- patch 按 row id **整体替换**该 entry 的 config（不 deep-merge，`app-boot/README.md:60`）；`insert` 加 entry；`!!js` 表达式在 mount 时求值；命不中 row id 的 patch 出 stderr 警告
- `dsh --profile web --dump-config` 可打印整棵配置树（= `renderConfigDump` 离线组装，与 `boot()` 同算法，`app-boot/README.md:22`），任何一行可被用户 patch 替换；`cordis.patch.yml` 通过 `watchUserPatches` 热更新（HMR），失败保留最后好树
- 三个内置 bundle：`dsh-base`（第一层：模型适配器、工具、持久化、沙箱、审批、设置、凭据、遥测）、`dsh-web-app`（浏览器应用）、`dsh-headless`（无服务的一次性 runner）

**与 seek 对比**：seek 的工具是编译期注册（`internal/tools/<name>/`），扩展 = 改代码重新编译；dsh 的扩展 = 挂一个插件。可扩展性天花板 dsh 完胜，但代价是 226 包 monorepo + 运行时组装复杂度。

## 2. 会话日志：全系统的单一事实源

`packages/core/session/` —— append-only 类型化事件日志，seq 连续、lossless JSON（`packages/core/session/src/types.ts:404-434`）。

### 事件全集（**44 种**，`KNOWN_SESSION_EVENT_TYPES` 实测清单）
`turn/start|end`、`step/start|end`、`user/message`、`assistant/chunk|message`、`tool/call|result`、`tool/code-dispatch|code-dispatch-start`、`request/header|context`、`agent/inbox/spliced`、`agent-preset/selected`、`session/end-seed`、`session/title|title-llm-request`、`llm/retry|retry-started`、`compaction/start|end|summary|prune`、`approval/asked|decided|policy`、`permission/preset`、`todo/write`、`goal/change`、`plan/mode`、`hook/invoked|result`、`schedule/change`、`sandbox/mode`、`subagent/descriptor`、`command/run|done`、`feedback/record`、`tool-workflow/run-start|run-end|agent-start|agent-end`、`web/deepseek-search-llm-request`（`packages/core/session/src/known-event-types.ts:19-63`）

> 首稿写 46 种且漏列 `command/*`、`feedback/record`、`permission/preset`、`agent-preset/selected`、`tool/code-dispatch*`、`tool-workflow/*`——实测为 44。注意 `agent/request` 不在此列：它是 Cordis waterfall（运行时钩子），不是会话事件。

### 核心不变量：Model-visible means logged
- `agent/request` waterfall 文档明言："Model-visible content must use logged channels; this waterfall cannot mutate messages"（`packages/core/agent/src/runtime-types.ts:233-236`）
- 每个模型请求由 `session.deriveMessages()` 从日志推导（`packages/core/agent-loop/src/agent.ts:2-3,340`）
- **只有 3 种消息产生型事件**：`user/message`、`assistant/message`、`tool/result`；chunk/boundary/usage 被 `deriveEventMessage` 显式排除（`packages/core/session/src/surface.ts:83-113`）
- SurfaceOp 二值：`append`（进模型可见面尾部）/ `replace`（遮蔽旧区间，`sourceEventSeqs` 指向被盖事件）——**compaction 不是删消息，是替换一段 surface span**，日志永不丢失

### 派生状态全是 fold 投影
plan 状态 = `plan/mode` last-wins fold；goal 状态 = `goal/change` fold；approval = `approval/*` fold；schedule = `foldScheduleEvents()` 重放（`packages/schedule/schedule/src/domain.ts:575`）。fork/resume/telemetry/UI 全部从这条流派生，**没有平行状态文件**。

### ⭐ 这条不变量同时就是缓存纪律——但因果方向和直觉相反
"请求 = 日志推导 + 事件只 append" 的直接推论是：**模型请求的前缀天然按字节 append-only 增长**，不需要任何"别改旧消息"的人为约定去维持。seek 靠纪律（CLAUDE.md 规矩 + `TestCompose_IsDeterministic` 守卫 + 禁止 in-transit 改写）达成同一性质，dsh 靠架构结构性地拿到它。详见 §8.2。

> **⚠️ 二次订正（读到官方设计笔记后）**：本文前一稿把这里写成"事件溯源 ≈ 为了缓存纪律"。**主次是反的**。官方笔记 [`.agents/notes/implemented/architecture/2026-07-05-reconstructable-requests.md`](#11-开发流程1386-篇设计笔记) 的原话：
>
> > **Prefix-cache stability is corollary #1, not the headline** … stability is **emergent, not managed**.
>
> headline 是 **"Model-visible ⟺ durably referenced"**，可检验推论是"持有日志 + 被引用对象 + 钉住的代码版本的任何人，都能**逐字节重建**每一个 loop 请求"。三个推论按序号排：#1 缓存稳定、#2 字节级审计/重放、#3 resume 与 fork 带**可归因的**漂移。
>
> 为什么模型公司要这个：它回答的是模型开发里最值钱的问题——"模型当时到底看到了什么"。**缓存便宜是掉下来的副产品，不是目的。** 这不改变 §8.2 对 seek 的结论（dsh 的缓存纪律确实更强），但改变了"为什么更强"的解释。

**与 seek 对比**：seek 的 transcript JSONL 也是事件流，plan 模式也从转录重建（`plan/reconstruct.go`）——方向一致；但 dsh 把"状态 = 事件投影"推行到了 approval/todo/schedule 全系统，seek 的 approval 是运行时 Policy 状态 + 转录重建混合。这是最根本的架构差异。

## 3. agent-loop：三态状态机 + 事件全落盘

`packages/core/agent-loop/`

- 三态 phase：`idle | maintenance | running`（`agent.ts:39-46`）；`setPhase` 只在状态变化时发 `agent/status`（`agent.ts:104-111`）
- `wakeDriver`：inbox splice 后唤醒；idle→running 开新 driver，用 `ctx.agents.withInitiator` 包裹（`agent.ts:172-193`）；maintenance/aborted 期间的 wake 被 latch（`wakeRequested`），收敛时重放
- `turn()`：先 append `turn/start`，循环 `preStep→step/start→user/message→step()→step/end`，finally 必 append `turn/end {reason}`；inbox 有 pending 就换新 AbortController 继续下一 turn（`agent.ts:255-328`）
- `step()`：`buildRequest`（含 `agent/request` waterfall + `llm.prepareCall`）→ while(true) for-await 流：每 chunk 先 append `assistant/chunk` 再 `assembler.push`（`agent.ts:339-350`，chunk append 在 349、push 在 350）→ 终态 error/aborted 走 `agent/request-error` waterfall，返回 `{kind:'retry'}` 则 continue，否则抛 `LlmError`（`agent.ts:354-370`）→ append `assistant/message`（带 `surfaceOp:'append'` + `sourceEventSeqs: chunkSeqs`，`agent.ts:381-390`）
- max-tokens 是 sticky 的：后完成的 step 不能降级 turn 结局（`agent.ts:285-290`）；空内容 assistant/message 仍持久化（承载 usage）
- **重试是独立插件** `llm-retry`：挂 `agent/request-error`，指数退避+jitter（`initialDelayMs/maxDelayMs/jitterRatio`），重试也 append `llm/retry` 事件进日志（`packages/llm/llm-retry/src/index.ts:58-74,150-207`）——seek 的重试在 `pkg/deepseek/stream.go` 内部，dsh 在事件层
- 工具调度 `executeToolCalls`：exclusive 调用成 barrier，parallel 用有界滚动池（`maxParallelToolCalls`，`tool-calls.ts:131,199`），结果按模型顺序提交；abort 对未启动调用记合成错误结果 `TOOL_ABORTED_BEFORE_DISPATCH` 保 replay 合法（`tool-calls.ts:256`）

## 4. 工具管道：7 事件点 + 强制 canonical output

`packages/core/tools/`

### 注册与作用域
- `defineTool({name, description, parameters, output, execute, ...})`（`schema.ts:545-548`）；schema 是自研 DSL（`ParameterSchemaSpec` 属性映射）
- `register(def): disposer`；在 `agent.ctx` 上调用即注册进该 scope，**scoped 工具 shadow 全局**；同层重名抛错（`index.ts:1037-1060,725-729`）
- `restrict({allow,deny})` 按 scope 过滤全局工具，多个 restriction 求交集；scoped 注册不受影响（`index.ts:1071-1088`）
- 工具名 `run_code` 保留：不可注册/遮蔽/restrict（`index.ts:1054-1056,1085-1087`）
- `isConcurrencySafe(args)` 显式 opt-in → parallel 组；缺省/抛错一律 exclusive（`index.ts:256-269,1276-1283`）

### 强制 canonical output 契约（值得单独标⭐）
每个工具必须声明 `output: {schema, render(args,value): ContentBlock[], presentationMeta?}`：body 返回 lossless JSON 值（`snapshotJsonValue` 校验，否则 `ToolOutputError`），`render` 是**纯函数**投影为模型内容块（`index.ts:212-235`）。工具输出形状由契约保证，模型侧永远看到渲染后的 ContentBlock，原始 value 仅执行期本地、**刻意不进 durable 事件**。

### 执行全序（官方 mermaid，`docs/tool-execution-pipeline.md:10-58`）
```
tool/call 落日志 → presentCall → pre-execute → guard → approval
  → execute waterfall → tool body → fs 门（fs/write-intent|fs/edit-intent）
  → 工具自有会话事件（todo/write、fs/observed、hook/invoked、tool/code-dispatch）
  → post-execute → normalize → finalizeContent → tools/result
  → additionalContexts FIFO 注入 → tool/result(会话事件) → presentResult
```
- **`owned` 节点（首稿遗漏，复核补）**：mermaid 中工具本体可发射自有会话事件（`todo/write`、`fs/observed`、`hook/invoked|result`、`tool/code-dispatch`）——"Model-visible means logged" 的另一面：工具想给模型看什么，也必须走日志（`docs/tool-execution-pipeline.md` mermaid `owned` 节点）
- `tools/pre-execute`（waterfall → PreToolDecision allow/deny/ask）、`tools/execute`（around-dispatch wrapper，只允许换 `exec.signal`）、`tools/post-execute`（accept/replace/block+feedback/additionalContexts）、`tools/result`（同步 emit、deep-frozen）、`tools/change`（`index.ts:152-207`）
- guard：同步检查返回拒绝字符串，在 pre-execute allow 之后、dispatch 之前（`index.ts:1110,1486-1488`）
- 审批：pre-execute 返回 `'ask'` → `serviceAsk` → `ctx.get('approval')`；**服务缺失/无人应答 fail-closed deny**（`index.ts:1479-1496,1689-1696`）；`ApprovalPolicy='ask'|'never'`，`approval/*` 事件走会话日志，resume 重放即状态（`packages/interaction/user-approval/src/index.ts:44-97`）
- `ToolRunContext.deferContext(UserMessage)`：插件上下文在 tool/result 之后 FIFO 注入（`index.ts:404-421`）

### guard 生态（独立插件）
- `packages/guard/timeout-policy`：deadline 换 `exec.signal`、finally 还原（防 post-execute 看到已 abort 的信号），超时替换为结构化 `TOOL_TIMEOUT` 错误（`src/index.ts:55-80`）
- `packages/guard/repeat-tool-reminder`：重复调用阈值 [3,5,8] 渐进提醒，canonical 参数全串判重、预览截 500 字符（`src/index.ts:28-79`）

**与 seek 对比**：seek 的权限是两轴 Policy（Preference×Workflow），在工具执行前单点拦截；dsh 是 7 个可插拔事件点 + guard 插件生态，任何第三方都能在管道任意环节挂逻辑。seek 的 read 上限/输出截断在工具内写死，dsh 的 spill/超时是 guard 插件——**可组合性差距明显**。

## 5. 能力缝：fs / shell / subprocess / sandbox / terminal / lsp / e2b

### fs：抽象类 + 不透明 targetKey
- `FileSystem` 抽象类（Cordis Service），provider：`LocalFileSystem`、`SandboxedFileSystem extends LocalFileSystem`；**换沙箱 = 换插件**（`packages/fs/fs-local/src/index.ts:64`、`packages/fs/fs-sandbox/src/index.ts:59`）
- `resolve()` 产出不透明 branded `targetKey` + `version`（freshness token）；`processPath`/`contains` 由 backend 拥有，消费方不得解析 key（`packages/fs/fs/src/index.ts:116-144`）
- 读写策略：`readText` 只收 UTF-8，二进制 backend 拒（`FS_NOT_TEXT`）；`readBytes` 带 `maxBytes`，超出报 `FS_TOO_LARGE` **而非截断**（`packages/fs/fs/src/index.ts:176-199`）
- 原子写：`createIfAbsent`/`replaceIfVersion` 两种 guarded intent，返回 before/after+version；`editText` 是字面替换（`packages/fs/fs/src/types.ts:123-168`）
- `fs/write-intent`、`fs/edit-intent` 是 waterfall 决策点（首个 listener 定夺）；`fs-observation-policy` 用 WeakMap 记 observed-state 供 stale guard（`packages/fs/fs/src/index.ts:58-76`、`packages/fs/fs-observation-policy/src/index.ts:91-114`）

### subprocess / shell
- `SubprocessRuntime`：spawn spec 全显式无默认（argv/cwd/stdio/graceMs）；输出收集 = 内存 tail 上限 + 可选 spill 文件，offset-based reader 多读者互不消费（`packages/subprocess/subprocess/src/types.ts:44-67,121-148`）
- kill 树级：POSIX 进程组信号 + Windows `taskkill /T`；`graceMs` SIGTERM→SIGKILL（`packages/subprocess/subprocess/src/types.ts:161-167`）
- ⭐ **环境消毒**：含 `KEY|PASSWORD|SECRET|TOKEN` 的 env 与 `DSH_*` 永不隐式下传子进程，显式覆盖层后合并（`packages/subprocess/subprocess/src/index.ts:44-65`）
- `ShellExecutor`：request/spec 分离，`resolve()` 填默认并 cap timeout（`maxTimeoutMs`）；background 进程无 executor 超时（`packages/shell/shell/src/types.ts:38-110`）；bash-local 默认 120s/上限 600s/输出 64KB tail+64MB spill/3s grace（`packages/shell/bash-local/src/index.ts:105-112`）
- 结果渲染契约：以 `\n[exit code: N]` 或 `\n[killed by signal: X]` 结尾，`parseExitStatus` 可逆解析（`packages/shell/shell/src/render.ts:36-42`）

### sandbox：多平台 runner 链
- Linux `bwrap`→Landlock（node-addon launcher）、macOS Seatbelt（`sandbox-exec`）、Windows windows-acl restricted-token；每 rung 先功能探测（真跑 `true`），缺失即 fail-closed（`packages/sandbox/sandbox-local/src/index.ts:159-166,85-91`）
- 词表只有文件效果：`read-only`/`workspace-write`/`danger-full-access`；**网络与进程可见性明确不在内**（`packages/sandbox/sandbox/src/index.ts:24-29`）
- `confine()` 返回 wrapped argv + `enforcement`(full/partial) + `denialSignatures` + `runnerFailureRules`（exit-gated stderr 签名，区分"没跑成"与"被拒"）（`packages/sandbox/sandbox/src/index.ts:95-116`）
- ⭐ **升级阶梯在运行时检查**：`WIDER_MODES` 严格递增表，`sandbox_permissions` + `justification` 必须成对（`packages/sandbox/sandbox/src/escalation.ts:28-47`）
- 策略默认 `read-only`（fail-safe），session 模式可覆盖；策略写入 cache-safe runtime context 供 replay（`packages/sandbox/sandbox-policy/src/index.ts:62-110`）

### e2b：云沙箱 = 三件套 provider 组合
- `e2b` 生命周期 + `fs-e2b` + `subprocess-e2b` 同时替换 ctx.fs/ctx.subprocess；**bash-local/terminal/lsp-stdio 无需 fork**（`packages/e2b/README.md:7-13`）
- 超时/销毁必删沙箱；随机 HOME + shell 引号隔离 SDK 硬编码 login shell

**与 seek 对比**：seek 沙箱（seatbelt/landlock）零依赖、工具内嵌；dsh 是缝级可替换 + 云沙箱 + 升级阶梯。seek 无环境消毒、无 spill 文件、无 freshness token（有 checkpoint 快照但机制不同）。

## 6. 产品层：skill / mcp / plan / goal / todo / jobs / schedule / workflow / compaction / spill / hooks / subagent

### skill（只发现，不安装）
- `ctx.skills` SkillRegistry：provider 注册模型（list/get），rank 去重，global+agent scope 分层（`packages/skill/skill/src/index.ts:357,391,471-518`）
- 模型可见形式：`skill` 工具按名加载全文 → 渲染 `<skill_content name=...>` 指令块（form:'instructions'）（`packages/skill/tool-skill/src/index.ts:81-161`）
- 会话级 `<available_skills>` 目录以 user-message 发布，sha256 digest 去重、整表替换；用户 `/<name>` 直接调用经 `agent/pre-step` 钩子拦截（`packages/skill/tool-skill/src/index.ts:217-288`）
- SKILL.md 格式：`<dir>/SKILL.md` 或平铺 `<name>.md`，YAML frontmatter：`name/description/whenToUse/disable-model-invocation/user-invocable`；chokidar 监听（`packages/skill/skill-filesystem/src/index.ts:672-683`）
- **无安装/卸载机制**——只有发现（project/user/bundled 根；trustedHost 走 node 直读 vs `ctx.fs` 沙箱读）。注意这是**形态选择而非缺陷**：dsh 的分发单位是 bundle/plugin，skill 安装这层不在它的抽象里（见 §8.0）

### mcp（stdio + streamable-http，无 OAuth）
- transport 仅两种：stdio（spawn，env 经 `scrubbedParentEnv` 脱敏）与 streamable-http（自定义 headers = 唯一"认证"）（`packages/mcp/mcp-client/src/transport.ts:31-49`）
- 桥接名 `mcp__<serverName>__<rawName>`，规范化到 64 字符，冲突追加 SHA-256 12hex；原始名只在 wire 上发（`packages/mcp/mcp-client/src/tools.ts:6-10,42-48`）
- 两阶段同步：先拉全量 tools/list 再整体 swap，半途失败回滚到零注册；单次调用 60s 超时（`packages/mcp/mcp-client/src/tools.ts:128-172`）

### plan（per-agent 协作状态，非步骤 FSM）
- 激活时注入 `plan:policy` 引导段；`exit_plan_mode` 工具把计划交给 user-question 审批（Approve/Keep planning，intent:'plan-review'）（`packages/plan/plan-mode/src/index.ts:1-18,184-392`）
- 状态事件溯源：`plan/mode` last-wins fold，resume/fork 零镜像恢复；用户选择挂起到下一个 pre-step（`packages/plan/plan-mode/src/index.ts:129-138,205-266`）
- **沙箱与审批策略独立，不读 plan 状态**——与 seek 的"plan-analyze 终端只读、workflow 压过 pref"的强耦合设计相反。⚠️ **这是两种 plan 语义，不是优劣**：dsh 解耦 ⇒ plan 只是**协作仪式**（模型自律 + 一道人类审批门），不构成安全边界；seek 耦合 ⇒ plan 是**真的只读闸门**（`PrefYolo + WorkflowPlanAnalyze` 仍然只读）。要 plan 当安全边界，seek 的耦合是必需的；要 plan 当轻量协作态，dsh 的解耦更省心

### goal / todo
- goal：`goal/change` 事件溯源，id/revision(乐观锁)/objective/phase(active|paused|blocked|complete)/maxGoalRounds(默认 256)（`packages/goal/goal/src/index.ts:66-79,183-266`）
- `goal-round-driver`：agent 静默时自动排队下一轮消息（source `{kind:'goal',round}`），超轮数→block 'round-limit'；reservation 竞态栅栏 + 轮间持久化检查点（`packages/goal/goal-round-driver/src/index.ts:138-204,349-406`）
- 工具 `get_goal/create_goal/update_goal`；create/edit/pause/resume 要求直接人类请求（`packages/goal/tool-goal/src/index.ts:114-120`）
- todo：单工具 `todo_write` 整表替换（无部分更新），投影在下一 turn/start 清空（`packages/todo/tool-todo/src/index.ts:26-43`）
- 三者分工：**plan=人类审批门；goal=自动循环（轮数上限）；todo=模型自管清单**

### jobs / schedule（无 cron）
- jobs：JobRegistry，id `<kind>-N`，owner-session 隔离、first-wins 结算；完成通知：忙时注入下一步、闲时 wakeup 开一轮（有 wake 预算）或 quiet（`packages/jobs/jobs/src/index.ts:40-107`）；`job_output` wait+timeout 上限 10min，超时返回 `[status: running]` 而非报错（`packages/jobs/tool-jobs/src/index.ts:302-401`）
- schedule：**没有 cron**——只有 `after`(N秒后)/`at`(绝对时刻, RFC3339 offset 或 local+IANA 时区)/`every`(固定间隔, 下限 300s)（`packages/schedule/schedule/src/domain.ts:24,730`）；事件 append 进日志、fold 重放即状态；进程内 timer 仅在 session 有活 root agent 时存在，冷 session 复活时补投过期项（`packages/schedule/schedule/src/runtime.ts:22,77`）

### workflow（模型写 JS 脚本编排子代理）⭐ seek 没有
- 引擎 `ctx.workflowEngine`，脚本在 **worker thread** 执行（"vm 非安全边界，worker 提供 host-loop 隔离+强制终止"）（`packages/workflow/workflow-worker-thread/src/host.ts:149`）
- 脚本 hooks：`agent()/pipeline()/parallel()/phase()/log()/args`；**无 fs/网络/timer**，有并发与总 agent 数 cap；普通子代理失败→该项为 null，hook 误用→致命错误（`packages/workflow/tool-workflow/src/index.ts:138-150`）
- meta 块沿用 Claude Code dynamic-workflows 词表（`packages/workflow/workflow/src/types.ts:46`）
- `tool-ralph`：固定"fresh-agent 轮次"循环（每次新 child + 有界结构化 handoff，默认 256 轮，provider `spawn`），模型只能提供数据不能改循环（`packages/workflow/tool-ralph/src/index.ts:90,153-177`）

### compaction / spill / context
- `ctx.compaction`：压力(threshold)与 provider-confirmed overflow 两类触发；把 surface span [start..end] **替换**为一个 summary node，替换 user message 带 `compactCheckpointSource(CompactionId)`（`packages/compaction/compaction/src/index.ts:88-117`）
- compaction-basic：`thresholdRatio` 默认 0.8×contextWindow、`retainRatio` 保留尾部原文；**摘要请求把会话自身 system+tools+前缀消息原样重放、压缩指令作为最后一条 user message——复用 provider KV 前缀缓存**（`packages/compaction/compaction-basic/src/summarizer.ts:25-30,121`）⭐ 与 seek 的缓存纪律同思路（这是 §8.2 订正的证据之一）
- tool-result-pruner：head/tail 裁剪 + `compaction/prune` shadow-price 事件（`packages/compaction/compaction-tool-result-pruner/src/index.ts:162-173`）
- **spill**：`SpillStore` 只有 `saveText()`；spill-policy 插件在 `tools/post-execute` 转换：超 `maxInlineBytes` 全文存 artifact、模型侧换 head/tail 预览+locator+取回提示；`read` 跳过模型侧防 read→spill→read 循环；存储失败绝不转 isError（`packages/spill/spill-policy/src/index.ts:29-35`）
- `agent.inject(input)` = `send(input,'next-step',false)`：append 到 next-step inbox 但**不唤醒 driver**（idle 时持久化暂存上下文但不开 turn）；拒绝非 JSON 可序列化输入（`packages/core/agent-loop/src/agent.ts:130-132`）
- context/ 族：agent-instructions（AGENTS.md 发现/预算 65536B/SHA-1 去重/touch 驱动刷新）、session-reference、time-context、tmux-context

### hooks（兼容 Claude Code/Codex 生态）
- `hooks-claude-code`：桥接 SessionStart/UserPromptSubmit/PreToolUse/PostToolUse/Stop/SubagentStart/Stop（`packages/hooks/hooks-claude-code/src/index.ts:206-295`）
- 协议解码：exit 0=结构化 JSON 或纯 stdout，exit 2=block(stderr 为 reason)，其余=非阻塞错误

### subagent（6 个 provider，无 worktree 隔离）
- provider 目录：`subagent-acp`、`subagent-claude-code`、`subagent-codex`、`subagent-dsh-sdk`、`subagent-fork-in-process`、`subagent-in-process-driver`、`subagent-spawn-in-process` + 工具 `tool-subagent`/`tool-subagent-control`/`tool-subagent-report`
- 同一接口背后：进程内 fresh child（真并行）→ fork → 委托给 Claude Code/Codex/ACP 客户端 → dsh SDK
- **没有 seek 的 git worktree 隔离**；委托型 provider 依赖父 session 提供 working directory（缺 cwd 会抛错）

## 7. 集成层：web / acp / api / sdk / python / native

- `apps/web`（React 18）+ `packages/web/*`：Web UI（浏览器应用），驱动 `ctx.agents` 并从 `session/event` 渲染；web 族含 web-fetch-http、web-search-deepseek/exa/perplexity
- `packages/acp/`：Agent Client Protocol（编辑器集成，`AcpConfig`/`SessionRecord`，`acp/src/index.ts:51-85`）；`packages/api/`（gateway + remotes）+ `packages/sdk/`（client/protocol/server）+ `packages/client/`：**30+ 个 `ui-*` 前端组件模块**（ui-conversation / ui-plan / ui-subagent / ui-workflow-run / ui-tool / ui-skill / ui-goal / ui-jobs / ui-trajectory / ui-user-questions / ui-permission-presets / ui-model-selection / ui-attachment …）+ connection/hmr/locale/runtime——UI 层本身也是 Cordis 模块化；`python/sdk`（pyproject + src + tests）Python SDK
- `native/landlock-run`：Landlock node-addon launcher（独立 npm 包：packages/ + scripts/ + test/，沙箱链的一环）
- `apps/cli`：headless/CLI 模式；`packages/host/`：宿主层

> 这一节本身就是 §8.0 的证据：**web + ACP + 对外 API/SDK + Python SDK + 云沙箱**，五个方向全部指向"托管服务与编辑器集成"，没有一个指向"本地单人 CLI 的长期驻留"。

---

## 8. 与 seek 的对比

### 8.0 先分清两类差异：谁在为谁优化

对比表最容易犯的错，是把「对方没选的路」记成「对方输的项」。dsh 与 seek 的差异实际分成两类，**赢法完全不同**：

| 类型 | 含义 | 追赶成本 | 属于此类的行 |
|---|---|---|---|
| **架构能力差距** | 对方的机制确实更强，同赛道上比不过 | **要写代码**，可能要改地基 | 事件溯源深度、工具管道可组合性、工具输出契约、编排 |
| **产品形态差异** | 对方没做，是因为不在它的赛道上 | **只要改叙事**，不必追 | 时序自治、skill 安装、部署体积、成熟度阶段 |

判据来自各自的服务对象：

- **dsh = DeepSeek 官方为自己的模型 + 自己的云做的 harness**。e2b 云沙箱、`apps/web`、Python SDK、ACP 编辑器协议、对外 API——全部指向**托管服务 / 编辑器集成**。所以它**不需要**跨会话 cron（云上调度是平台的事）、**不需要**单二进制（部署形态是服务）、**不需要**skill 安装器（分发单位是 bundle/plugin）。
- **seek = 第三方为本地单人开发者做的 CLI**。所以它**必须**单二进制零依赖、**必须**自带跨会话调度（没有平台兜底）、**必须**有 worktree 隔离（本地并行改同一个 repo）。

**结论**：seek 在产品形态四行上的"胜"不构成技术优势叙事，只构成**赛道说明**；真正要正视的是架构能力那四行——**其中三行输**。

### 8.1 对比表（源码证实）

| 维度 | dsh | seek | 差异类型 | 胜者 |
|---|---|---|---|---|
| 事件溯源深度 | 44 事件全系统，状态全是 fold 投影 | transcript + plan 重建，approval 是运行时状态 | 架构能力 | **dsh** |
| 工具管道可组合性 | 7 事件点 + guard 插件生态 | 编译期工具 + 两轴权限单点拦截 | 架构能力 | **dsh** |
| 工具输出契约 | 强制 canonical output + 纯函数 render 投影 | 自由格式文本 | 架构能力 | **dsh** |
| 编排 | workflow worker-thread 脚本 + ralph 循环 | autopilot 无人值守 + /goal | 架构能力 | dsh（脚本化）/ seek（确认门控） |
| **前缀缓存纪律** | **结构性**：请求由日志推导 ⇒ 前缀天然 append-only；打真 API 的 e2e 断言 `cacheReadTokens>0`；compaction 复用前缀 | **约定式**：禁止 in-transit 改写 + `TestCompose_IsDeterministic` 守卫，结果等价但靠纪律维持 | 架构能力 | **dsh（同一性质的更强形式）** |
| DeepSeek 商业面优化 | 无 FIM、无峰谷计价（全仓零命中）；有 cacheRead/Write 计量 | FIM 端点 + 峰谷计价表 | 产品形态 | **seek** |
| 沙箱 | 多平台 runner 链 + e2b 云沙箱 + 运行时升级阶梯 | seatbelt/landlock 本地零依赖 | 混合 | dsh（能力）/ seek（零依赖部署） |
| 子代理 | 6 provider，可委托 Claude Code/Codex/ACP，无隔离 | 并行 + **git worktree 隔离** | 混合 | 各有千秋 |
| 时序自治 | session-local schedule（`after`/`at`/`every≥300s`，无 cron） | 跨会话 cron/wakeup/OS 通知/autopilot | 产品形态 | **seek** |
| skill 管理 | 只发现不安装（分发单位是 bundle） | 安装/更新/卸载/调用统计 | 产品形态 | **seek** |
| 部署 | Node + pnpm + Web 服务（226 包） | 单二进制 ~5MB 零依赖 | 产品形态 | **seek** |
| 成熟度 | developer preview，破坏性变更明示 | v7 已交付，74 包测试全绿 | 阶段 | **seek** |

**读法**：架构能力 5 行 → dsh 拿下 3 行、2 行分歧；产品形态 4 行 → seek 全取，但那是赛道不是优势。

### 8.2 订正记录：「成本优化」那一行为什么首稿是错的

**首稿写**：`成本优化 | 无感知（无缓存字节纪律、无 FIM、无峰谷） | prefix-cache 字节稳定 + FIM + 峰谷计价 | **seek**`

**错在哪**：把三件不同的事捆成一行，其中最重的一件（缓存纪律）判反了。

反证（均为 clone 回树实测）：

1. `packages/core/agent-loop/tests/request-cache.e2e.ts` —— **打真 DeepSeek API 的 e2e**，断言"首次之后每个请求 `cacheReadTokens > 0`"。其 docstring 原文把这件事定位为架构推论：*"log-derived requests translate into real provider cache hits … prefix stability is corollary #1"*。
2. `packages/llm/llm-deepseek/src/translate.ts:46-59` —— 显式处理 DeepSeek 的 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` / `prompt_tokens_details.cached_tokens`，并把 cacheRead 从 `inputTokens` 里减出来做 disjoint 计数。注释精确到"`prompt_tokens` INCLUDES cache hits"这一层 wire 语义。
3. `packages/llm/token-meter/src/index.ts:46-47` —— `cacheReadTokens` / `cacheWriteTokens` 进总账。
4. `packages/compaction/compaction-basic/src/summarizer.ts:25-30,121` —— 摘要请求原样重放会话前缀以复用 KV 缓存。**首稿 §5 自己写了这条并打了 ⭐，与首稿 §8 的"无缓存字节纪律"直接矛盾**——同一篇文档两处结论相反，是复核缺失的信号。

**更深一层**：seek 与 dsh 在这件事上不是"谁做了谁没做"，而是**用什么维持**。seek 用**约定**（CLAUDE.md 的"永不修改旧消息"、token 优化只在 write-time、`TestCompose_IsDeterministic` 守卫 `Compose` 是纯函数）；dsh 用**架构**（请求 = `deriveMessages()` 从 append-only 日志推导，前缀不可能非单调）。**架构强制 > 约定强制**——约定会被下一个不读 CLAUDE.md 的贡献者破坏，架构不会。

**因此对外叙事必须改**：seek 的差异化**不是**"成本纪律"（这条经不起 grep），而是收窄后的两条——
- **FIM 端点 + 峰谷计价**：dsh 全仓零命中，这是 seek 真正独有的 DeepSeek 商业面优化；
- **单二进制零依赖部署**：形态差异，但对本地开发者是实打实的价值。

## 9. 借鉴清单（含落地状态）

> 首稿这一节是 5 条待办候选。两轮实施后重排为**已落地 / 评估后不做 / 待定**三组。
> 落地的每条都注明**与 dsh 的差异**——照抄的部分和分歧的部分同样有信息量，后者往往说明两边约束不同。
> 代码尚未提交，均在工作区；实现细节的坑另见 [`docs/pitfalls.md`](pitfalls.md)。

### 9.1 已落地（7 条）

| # | 模式 | dsh 出处 | seek 实现 | 与 dsh 的差异 |
|---|---|---|---|---|
| 1 | **环境消毒** | `packages/subprocess/subprocess/src/index.ts:44-65`（`scrubbedParentEnv`） | `internal/childenv` | 谓词去掉了 `AUTH`——它会命中 `SSH_AUTH_SOCK`，删掉等于废掉所有走 ssh 的 `git push`。逃生口是 `config.BashEnvPassthrough`（精确名、大小写不敏感、**不做子串匹配**） |
| 2 | **重复调用提醒** | `packages/guard/repeat-tool-reminder/src/index.ts:28-79` | `internal/tools/repeatguard.go` | 阈值 `[3,5,8]` 与 canonical 参数全串判重照抄；但 dsh 是 guard 插件挂在管道事件点上，seek 没有那个管道，所以做成 **Tool 装饰器**，在注册时按需包住 |
| 3 | **输出保尾** | `spill-policy` 的 head/tail 预览形态 | `internal/tools/bash/bash.go:clampOutput` | **只取了形态，没做 artifact 落盘**。8 KiB 头 + ~24 KiB 尾，中间省略并在标记里明说尾部完整。解决的是"`go test` 的 verdict 在尾部被截掉"这个真问题；全文取回是锦上添花，等有真实需求再上 |
| 4 | **read-before-write 守卫** | `packages/fs/fs-observation-policy/src/index.ts` | `internal/fsobserve` + `write`/`read`/`edit` 接线 | **守卫的对象相反**：dsh 的 `editIntent` 抛 `FS_NOT_OBSERVED`（它的 edit 是 CAS 语义，没有观察就构造不出版本基准）；seek 的 edit 是子串匹配语义，exact `old_string` + 计数本身就是守卫，所以 seek 只守 `write`，edit 仅登记不设防 |
| 5 | **守卫下沉到 syscall** | `fs-local/src/index.ts:178-187` 的 guarded intent + `fsio.ts:580` 的 `link()` 发布 | `fsobserve.Plan` 返回 `Decision`，`write.writeGuarded` 执行 | 结构对齐：**不返回裁决，返回该执行哪个带守卫的操作**。不存在→`O_CREATE\|O_EXCL`；存在→不带 `O_CREATE` 打开并 **stat 文件描述符而非路径**再比对令牌。令牌 `(dev,ino,size,mtime)` 对应 dsh 的 `dev:ino:size:mtimeNs:ctimeNs`，Windows 无 identity 时退化为 size+mtime |
| 6 | **缓存纪律变成可测门** | `packages/core/agent-loop/tests/request-cache.e2e.ts` | `pkg/agent/cache_e2e_test.go` | 同构：打真 API、断言首轮之后 `PromptCacheHitTokens > 0`。**第 0 轮刻意不断言**——DeepSeek 缓存在服务端跨进程存活，首轮命中与否只反映上次跑测试的时间。见 §8.2 |
| 7 | **沙箱拒绝的归因** | `packages/sandbox/sandbox/src/index.ts:95-116`（`denialSignatures` + `runnerFailureRules`，exit-gated stderr 签名） | `internal/tools/bash/sandboxhint.go` | 同构，但 seek 只需分辨三态而非 dsh 的完整 rung 链。**门控就是设计本身**：必须同时满足「本次确实配了 confinement」+「退出码非零」+「输出命中平台签名」——`Permission denied` 在普通工具输出里太常见，错误归因比不归因更糟。runner 失败靠 seek 自己的 `seek sandbox:` 标记识别而非退出码，因为 127 也是 shell 的 command not found |

三条补充说明：

- **沙箱归因只在无人值守时才真正值钱**（第 7 条）。`WithSandbox` 只在 autopilot 子代理里开；模型分不清"我没权限"和"构建坏了"，就会换路径重试、烧轮次，而没人在看。端到端由真 seatbelt 拒绝验证（`sandbox_test.go:TestBash_SandboxDenial_IsAttributed`）——签名是关于"内核和 shell 到底打印什么"的断言，只有真实拒绝能证伪它。
- **grep 刻意没改**（第 3 条）。它的截断是对的：grep 输出末尾没有结论，且现有提示语已经在教模型收窄 pattern。给它加头尾保留是净损失。
- **hooks 与 `routines/tick.go` 刻意不消毒**（第 1 条）。前者跑的是用户自己写在自己配置里的 shell 片段，信任级别等同 `.bashrc`，且 hook 常常就是为了够到一个需要凭据的服务；后者重新拉起 seek 自己，**需要** API key。
- **write 守卫没有 per-call 逃生口**，与 dsh 一致：dsh 的 write 工具参数只有 `file_path` 和 `content`，解除守卫只能整机不加载 `fs-observation-policy`。给模型一把自己解锁的钥匙等于没锁。

### 9.2 评估后决定不做

| 候选 | 首稿位置 | 不做的理由 |
|---|---|---|
| **canonical output 契约** | 首稿 §9 #1（标了 ⭐） | **性价比最差的一条**。dsh 需要它，是因为工具是运行时插件——第三方工具的输出形状无法在编译期约束，只能靠契约兜底。seek 的工具编译期注册在 `internal/tools/<name>/`，**Go 的类型系统 + code review 已经提供了 schema+render 换来的那个保证**。retrofit 要动所有工具，换来"输出形状更可预测"，可 seek 本来就可预测。例外：若将来开放进程内第三方工具（MCP 之外），届时再上 |
| **`run_code` 双模式** | 首稿 §9 #4 | 官方为"模型写代码调自家 SDK"这一特定产品形态做的一等能力。seek 有 agent/subagent 编排，没有这个需求 |
| **approval 事件溯源化** | 第二轮候选 | **前提不成立**。seek 没有 per-action 审批记忆可丢：唯一的审批状态是 Preference 一根轴，"always approve" 就是 `SetPref(PrefYolo)`（`permission.go:148-149`），而这个位已经跨 resume 存活（`session.go:62,98`；TUI 经 `update_agent.go:589` 回写，非 TUI 经 `main.go:2009,2152`）。给一个已持久化的单 bit 建溯源机制买不到任何可观察变化。**注意这里否决的是「为 approval 建机制」，不是事件溯源这个方向本身**——方向以纪律形式落地了，见 §9.4 |
| **spill 全文取回** | 首稿 §9 #3 的完整版 | head+tail 已解决"verdict 丢失"这个真问题，全文 artifact + locator 是增量收益 |
| **per-target 写锁** | 第三轮对比新增 | dsh 需要它是为了并发工具调度；seek 的写路径串行，外部竞争已由 O_EXCL 与 fd-stat 覆盖 |

### 9.3 待定

- **hooks 兼容 Claude Code / Codex 协议**（首稿 §9 #5，`packages/hooks/hooks-claude-code/src/index.ts:206-295`，exit 0 = 结构化 JSON / exit 2 = block + stderr 为 reason）。价值不在技术在**获客**——用户写好的 CC hooks 零改动能跑。但 seek 有自己的 `internal/hooks`，协议差多远**尚未调研**，先看差异再决定是桥接还是兼容层。

### 9.4 抄不动的那条：落成纪律，不做重构

§2 那条不变量——**状态 = 事件投影**——是 dsh 与 seek 最根本的架构差异，也是唯一一条**没法当成机制搬过来**的。dsh 结构性地拥有它：每个模型请求由 append-only 日志 `deriveMessages()` 推导，所以状态**不可能**与记录漂移。seek 要拿到同一性质就得重写地基，而那个成本收益对一个单二进制本地 CLI 不成立。

**落地形式因此是一条写进 `CLAUDE.md` + `AGENTS.md` 的纪律**（Code conventions 属结构性内容，按两文件的 sync 规则逐字相同）。原条目只讲"别存平行状态**文件**"，现已扩成两种同源错误：

| 形式 | 症状 |
|---|---|
| 平行状态**文件** | 一个镜像运行时状态的兄弟 `.json` / `.md`——永远在跟漂移搏斗。例外是 artifact（`~/.seek/projects/<id>/plans/`）：write-once 的人类可读快照，**不是状态** |
| 平行运行时**字段** | 同一事实既在 struct 里、又在 resume 时重建 = 两个必须手工对齐的真相源 |

第二种的实例就在 seek 自己身上：`permission.Policy` 的 `preApproved`。因为**没有任何东西推导它**，代码必须记得在步骤 complete/skip、Esc 取消、`/plan` 关闭、以及**每一次** `SetWorkflow` 转换时清零——那一串防御性重置就是这笔利息，每一处都是一个曾经可用的 bug。

核心是一句默认提问：**新功能需要状态时，先问"这能不能从转录 fold 出来"，再考虑"加字段 + 加重建函数"。** 正样板是 plan（`propose` 参数与 `plan(start|complete|skip)` 调用**就是**状态，`reconstruct.go` 重放，无镜像）；反样板是 approval（运行时 Policy + 转录重建的混合态）。嗅探测试：**如果你正在给一个同时还存在 struct 字段里的东西写 `reconstruct()`，你已经有两个真相源了。**

#### 这条纪律的边界（`docs/prd/v5.md:73` 已有定论）

**它只适用于"事件源自 LLM 调用"的那类状态**，也就是 plan-mode 的形状。跨会话 / 跨进程的编排状态（cron、routines）**不适用**——那里的事件源自 OS 级调度器，转录根本不是 single source of truth，硬套会让 `-resume` 的行为变得不可预测。

这个边界值得和纪律一起记：只读 §2 很容易得出"seek 应该把一切都事件溯源化"的结论，而那是错的。dsh 自己也没跨过这条线——它的 schedule 是 **session-local** 的（`after`/`at`/`every`，无 cron，进程内 timer 仅在 session 有活 root agent 时存在），正因为如此，它的 `foldScheduleEvents()` 才成立。seek 的 cron 是跨会话的，同样的 fold 在那里不成立。**两边其实在同一条线上，只是 seek 站在了线的另一侧。**

### 9.5 实施中发现的、dsh 源码里看不出来的三个坑

读 dsh 只能拿到"做什么"，"在 seek 里怎么做才不出事"是另一回事。这三条都是实施时踩出来的，已进 `docs/pitfalls.md`：

1. **Tool 装饰器会静默吃掉可选接口**。`pkg/agent` 不只用基接口：`agent.go:915` upcast 到 `StreamingTool`，`:996` upcast 到 `ReadOnlyTool`。只实现 `Tool` 的包装器编译通过、自测通过，然后让 read/grep/list_dir 静默失去并行、think 静默失去流式。`WithRepeatGuard` 因此按被包装工具的能力返回**四种**具体类型。
2. **挂在 result 上的附加信息会在最需要时消失**。`buildToolResultMsg` 的契约是"errors always win"——`err != nil` 时结果字符串被整个丢弃，而典型循环恰恰是**反复失败**的命令。提醒必须同时挂到 error 上（`%w` 包装保住 `errors.Is`，文本追加在原文之后以免破坏前缀解析）。
3. **opt-in 守卫的关闭路径需要自己的分支**。`Decision` 最初靠"字段恰好是零值"表达"没启用守卫"，结果 nil observer 也走了 `O_EXCL` 创建路径，把所有未启用守卫的合法覆盖全拒了（两个既有测试当场变红）。加了显式的 `Guarded` 位才对。

## 10. 最值得警惕的 5 个复杂度

> **重要限定（读到 `.agents/notes/rejected/` 后补）**：下列成本**是真的，但不是失控的**。他们的 `rejected/` 目录只有 11 篇，而其中绝大多数是**被否决的简化提案**——`drop-bash-output-spill-files`、`drop-durable-step-boundaries`、`collapse-workflow-to-foreground-core`、`fold-compaction-package-split`、`fold-session-persistence-interface`、`prune-unused-skill-registry-api`、`truncate-interrupted-turns`，外加一篇 `dependency-swaps-rejected-by-nih-audit`（做过 NIH 自审，结论是保留自研）。
>
> 配合 `implemented/simplification` 这个独立分类和 `.agents/skills/dsh-find-simplifications`（一个专门用来找简化机会的 skill）——**226 个包不是漂移出来的，有人提议塌掉并且输了辩论**。下面每一条都该读成"知情地在付账"，而不是"没人管"。评估借鉴时这个区别很重要：抄一个失控的设计和抄一个被辩护过的设计，风险完全不同。

1. **7 个工具事件点的全序是隐性契约**——插件必须理解全序才能正确挂点，维护成本高
2. **SurfaceOp replace 遮蔽**——compaction 后"日志里有但模型看不到"的区间，调试排查成本高
3. **226 包 monorepo**（49 个顶层分组）——pnpm-lock.yaml 680KB，依赖树深，单包升级影响面难评估
4. **worker-thread workflow 引擎**——"无 fs/网络/timer"是策略不是隔离，worker 不是真沙箱
5. **developer preview + 插件生态未起**——API 漂移风险下第三方插件（dsh-plugin topic）还没形成网络效应

---

## 11. 开发流程：1386 篇设计笔记

首稿到第三稿都只读了 `packages/`。仓库根目录还有一个 `.agents/`，里面是**这个项目为什么长成这样**的答案——不用再从代码反推。

### 11.1 语料规模与形状

```
.agents/notes/     1386 篇 .md（含 .zh.md 双语版与 .i18n.yaml）
  implemented/     507   已实现（子类：architecture / feature / testing /
                          process / bug-fix / simplification）
  archived/        143
  proposed/         25
  rejected/         11
.agents/skills/          维护流程本身：dsh-code-review、dsh-pre-push-checks、
                          dsh-find-simplifications、dsh-archive-agent-notes、
                          dsh-doc-site-sync、dsh-translate-docs …
```

每篇是固定结构：**Problem → Decision → Alternatives considered → Consequences**，带 `Status:` 生命周期。代码注释直接反向引用笔记路径（例：`fs-observation-policy/src/index.ts:38` 引 `.agents/notes/implemented/process/2026-07-29-oxlint-linter.md` 解释一处 oxlint/tsc 分歧）。

**这不是人类的文档习惯**。这个量级 + 这个格式 + 双语 + 生命周期状态 = **给几个月后冷启动的 agent 读的记忆系统**。配合 `.agents/skills/` 里编码的维护流程，可以确定：**dsh 是一个由 agent 建造、并且为 agent 的再次介入而组织的代码库。**

### 11.2 从笔记里读到的两条设计规则

**规则一：不可表达 > 可检测。** `reconstructable-requests` 笔记否决 detect-and-report 方案（比对相邻请求、偏离即告警）的理由：

> catches violations after the fact; **a violating request is still constructible and ships**. Rejected for **interface-level unrepresentability**.

推到极致的产物：`dsh-agent-loop/invariant` 伴生插件**用一个全新 `Session` 独立重建每个请求**再比对，理由是 *"the live cache cannot vouch for itself"*——活着的缓存不能给自己背书。

> 这正是 §9.1 第 5 条（守卫下沉到 syscall）背后的同一条规则。seek 是撞出来的，dsh 是写下来当规则用的。

**规则二：简化提案要过辩护。** 见 §10 的限定说明。

### 11.3 这解释了「为什么这么设计」

把 §8.0（为谁优化）、§2 的订正（可重建性是 headline）、和这批笔记串起来：

- **架构是可重建性需求的下游**，而可重建性是**模型公司的调试/评估需求**——"模型当时到底看到了什么"是模型开发里最值钱的问题。缓存便宜、fork/resume、审计重放都是它的推论。
- **插件化是分发策略的下游**。dsh 不想成为你的 CLI，它想成为你**已经在用的那个 agent 界面底下的那一层**：6 个 subagent provider 里有三个是委托给 Claude Code / Codex / ACP；hooks 直接兼容 Claude Code 协议；workflow 的 meta 块沿用 Claude Code 的词表（`workflow/src/types.ts:46`）。**一个真想跟 Claude Code 竞争的产品，不会把「把任务委托给 Claude Code」做成一等能力。**
- **developer preview + 明示破坏性变更**是这个定位的最后一块拼图：他们在**保留 churn 的权利**。产品会反过来做这个取舍。

### 11.4 对 seek 的直接启示

seek 已经在小得多的尺度上收敛到同一套流程（`docs/pitfalls.md` 的症状/根因/修法/教训、§9.2 把否决项连理由一起记）。唯一的结构性差异：

| | 记什么 | seek 现状 |
|---|---|---|
| `pitfalls.md` | **出过什么错** | ✅ 已有，格式接近 |
| dsh `notes/` | **做过什么决定、否决过什么、为什么** | ❌ 散在 PRD 与本文 §9.2，无独立语料 |

`rejected/` 这个目录尤其值得学：**被否决的方案连同否决理由，是防止同一个提案每半年重来一次的唯一办法**——而这正是本文 §9.2 存在的理由。

### 11.5 从笔记里落地的三条（与 §9.1 的源码机制分开记）

§9.1 那七条来自读**源码**；这三条来自读**笔记**，性质是流程/门控而非运行时机制，所以单列。

| 出处笔记 | seek 落地 | 要点 |
|---|---|---|
| `docs/postmortem/0004-landlock-partial-notice-misclassified-child-failures.md` | `internal/tools/bash/sandboxhint.go` 加固 | 他们把「良性提示行」和「致命行」用同一个子串匹配，导致 ripgrep 的 exit 1（无匹配 = 成功）被报成沙箱故障。seek 改为**逐行匹配 + 精确良性排除**，排除表现在为空但有强制注释与回归测试守着。另注：他们用 **exit 125** 专表 launcher 失败以避开 shell 的 127，seek 复用 127 靠标记补偿，是较弱的构造 |
| `.agents/notes/implemented/testing/2026-06-19-real-api-e2e-ci.md` | `.github/workflows/e2e.yml` | 自跳过的测试套件 + 缺失的 secret = **静默全绿**。独立 workflow（保住 `ci.yml` 的无密钥/可 fork/恒绿）+ 无条件 preflight + 对 `--- PASS:` 字面量的 grep（退出码 0 分不出"跑过且通过"和"全跳过"）。不可信 PR 的跳过按 **PR 作者**判断而非 `github.actor` |
| `.agents/notes/implemented/process/2026-07-04-doc-tiers-and-budgets.md` | `scripts/verify-doc-budgets.sh` + `.tsv` + CI job | `AGENTS.md`(3178) / `CLAUDE.md`(3054) 已**冻结**在当前字数，目标 1800——从此加一段必须挪走一段。范围刻意窄：reference / PRD / `pitfalls.md` **不设限**。他们那句否决理由是关键依据："accretion happened **while** the current-state rule and reviewer attention already existed" |

> 三条的完整教训已进 [`docs/pitfalls.md`](pitfalls.md) 的 `## CI / gates` 一节。

### 11.6 姊妹文档：它是怎么被造出来的

本节只覆盖"笔记语料存在、且形状说明什么"。**完整的构建过程反推**——12,293 个提交的节奏、44% 的 PR 来自 agent 分支、第一天就立全套质量门的引导顺序、子系统落地时间线——单独成文：

📄 **[`dsh-build-process.md`](dsh-build-process.md)**

一句话摘要：**九周、12,293 个提交、2,519 个 PR，由 25+ 人加大量编码 agent 共同写出；让这个速度不塌掉的不是人力，是第一天就立好、此后再没松过的制度。**

---

## 附 A：分析时的源码锚点

- 核心循环：`packages/core/agent-loop/src/agent.ts`、`tool-calls.ts`；缓存 e2e：`packages/core/agent-loop/tests/request-cache.e2e.ts`
- 会话日志：`packages/core/session/src/types.ts`、`surface.ts`、`known-event-types.ts`
- 工具管道：`packages/core/tools/src/index.ts`、`schema.ts`；`docs/tool-execution-pipeline.md`
- 能力缝：`packages/fs/*`、`packages/subprocess/subprocess/`、`packages/shell/*`、`packages/sandbox/*`、`packages/e2b/`、`packages/guard/*`
- 产品层：`packages/skill/*`、`packages/mcp/mcp-client/`、`packages/plan/plan-mode/`、`packages/goal/*`、`packages/todo/*`、`packages/jobs/*`、`packages/schedule/schedule/`、`packages/workflow/*`、`packages/compaction/*`、`packages/spill/*`、`packages/hooks/*`、`packages/subagent/*`
- LLM 层：`packages/llm/llm-deepseek/src/translate.ts`、`packages/llm/token-meter/src/`、`packages/llm/llm-retry/`
- 官方架构文档：`docs/architecture.md`、`docs/subsystems/*.md`（session/tools/core/llm-streaming/scope/subagent）

## 附 B：复核记录（2026-08-13）

首稿由 4 个探索子代理产出，**未逐条回树验证**；本次复核对 clone（`@47f9438`）抽查约 30 处，修正如下：

| 项 | 首稿 | 实测 | 影响 |
|---|---|---|---|
| 「成本优化」对比行 | dsh 无缓存字节纪律 → seek 胜 | dsh 结构性更强，且有真 API e2e | **结论级错误**，见 §8.2 |
| 包数 | 48 packages | 226 package / 49 顶层分组（含 apps/native/python 共 233） | §10 复杂度论点更强，数字错 |
| 事件数 | 46 种 | 44 种，且漏列 6 类 | 细节精度 |
| 引用路径 | 抽样 24 条，9 条不存在 | 全部系统性缺 group 目录层（`packages/subprocess/src/` → `packages/subprocess/subprocess/src/`、`packages/mcp-client/` → `packages/mcp/mcp-client/`、`packages/session/` → `packages/core/session/`），已全文补齐 | 可追溯性 |
| 对比表方法论 | 单一「胜者」列 | 增设「差异类型」列，区分架构能力 vs 产品形态 | 见 §8.0 |
| plan 语义 | "与 seek 相反"，未评价 | 补充说明是两种语义而非优劣 | §6 |

**仍未验证**（下次复核优先级）：§1 Cordis 层叠顺序与 `--dump-config` 行为、§3 agent-loop 全部行号、§4 七事件点全序、§7 集成层细节。这些来自子代理报告，未回树逐行确认——引用时请自行核对。

### 第二轮定向复核（2026-08-13，上表四项全部回树验证）

| 项 | 复核结果 | 修正 |
|---|---|---|
| §1 Cordis 层叠顺序 | ✅ 顺序正确（bundles → profile patch → home patch → --patch），且补到更精确语义：**home 级 patch outranks profile 级**、patch 整体替换不 deep-merge、`--dump-config` = `renderConfigDump` 离线同算法、`watchUserPatches` HMR 失败保留最后好树、profile 是 `$DSH_HOME/profiles/<name>` 目录 | §1 已补（`app-boot/README.md:22,38,43,60`） |
| §3 agent-loop 行号 | ⚠️ 少量偏移，语义全对：三态 39-46 ✓、setPhase/agent-status 104-111 ✓、wakeDriver 172-193 ✓、turn/start 255 ✓、turn/end 319 ✓、max-tokens sticky 285-290 ✓；step 的 while 实际在 339（非 347）、chunk append 349/push 350、`surfaceOp:'append'` 在 389；tool-calls.ts 的 `maxParallelToolCalls` 实际 131/199（非 84-100）、`TOOL_ABORTED_BEFORE_DISPATCH` 实际 256（非 249-258） | §3 行号已订正 |
| §4 七事件点全序 | ✅ mermaid 全序与文档一致；**补到一个遗漏节点 `owned`**：工具本体可发射自有会话事件（`todo/write`、`fs/observed`、`hook/invoked|result`、`tool/code-dispatch`）——"Model-visible means logged" 的另一面 | §4 已补 |
| §7 集成层 | ✅ apps/web 是 React 18；acp（`AcpConfig`/`SessionRecord`:51-85）、api（gateway+remotes）、sdk（client/protocol/server）、client 是 **30+ 个 `ui-*` Cordis 模块**（ui-conversation/plan/subagent/workflow-run/tool/skill/…）、python/sdk（pyproject+uv）、native/landlock-run 是独立 npm 包 | §7 已补 |

**根因补注（引用路径系统性错误的机制）**：首稿由 explore 子代理产出，其 `read` 被 seek 工作目录边界挡住，读不了 `~/code/github/deepseek-harness`，只能靠 grep 输出重建路径——group 目录层（`packages/subprocess/` → `packages/subprocess/subprocess/`）和少量行号偏移就是在重建时产生的。**流程教训：跨仓库分析时，要么把 clone 放进工作区内，要么在产出时标注"路径为 grep 重建，引用前需复核"**。

**口径遗留（未改，供读者自行判断）**：§8.1 读法行写"架构能力 5 行 → dsh 拿下 3 行、2 行分歧"，但前缀缓存行的胜者列写 "dsh（同一性质的更强形式）"——按胜者列是 4:1，按读法是 3:2（前缀缓存行按"seek 效果等价"算分歧）。两种读法都成立，建议引用时二选一口径。
