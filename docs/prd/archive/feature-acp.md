# Feature: ACP 编辑器集成（Agent Client Protocol）—— 追平 Reasonix 唯一明确领先项（v7 柱 P · seed）

**所属版本**：v7（v0.8.x）· 柱 P
**前置阅读**：[`v7.md`](../v7.md) §7.3、[`comparison.md`](../../comparison.md) §R.2（IDE 集成行：源码确认 Reasonix 用 ACP、领先于 seek 的自定义 `-rpc`）、`internal/rpc/server.go`（seek 现有 JSON-RPC server 模式）、`internal/lspclient`（柱 L 刚做的 stdio JSON-RPC client，帧/关联范式可借鉴）、`pkg/mcp/client.go`（JSON-RPC over stdio 先例）
**状态**：✅ **实现 + 真 server stdio e2e 跑通**。`internal/acp`：手写 stdlib JSON-RPC 2.0（newline-delimited，无 Content-Length，复用 pkg/mcp 风格）—— `initialize` / `session/new` / `session/prompt`（异步、可 `session/cancel`）+ `session/update` 通知；`cmd/seek`：`seek acp` 子命令 + `acpBackend`（适配现有 agent）+ `acpUpdate`（Event→session/update 映射，含 reasoning/turn-bookkeeping **不外泄** 的单测）。**e2e**：脚本化 newline-JSON-RPC 客户端驱动真 `seek acp` + 真 agent —— initialize→`{protocolVersion:1, agentCapabilities}`，session/new→`sessionId`，session/prompt→流式 `agent_message_chunk`（"P"/"ONG"）→`{stopReason:"end_turn"}`，session/cancel 已完成会话不崩，stdin EOF 干净退出 rc=0。

**真 Zed GUI 验证通过**（Zed 1.4.4）：seek 出现在 Agent 面板 External Agents 下拉里，`initialize`（带 `clientCapabilities`）→`session/new`（cwd=项目）→多轮 `session/prompt`，工具调用（read/grep/list_dir/git/**edit**）映射成 Zed 步骤卡片、实测成功改了工作区文件（`.gitignore` 加行），多轮均 `end_turn`。新增 opt-in 协议 trace（`SEEK_ACP_LOG=/path` → tee 收发原始 JSON-RPC，零默认开销）便于调试握手。**用法见 [`docs/guide-zed.md`](../../guide-zed.md)。**

**剩（MVP 优化 backlog，见 guide §5）**：① ACP `session/request_permission` 把工具审批弹到 Zed 原生 UI（**已修 2026-11-21，ACP·P0-2**：非 yolo 车道权限姿态 `Ask`，`internal/acp` 的 `Server.Call` 承载 agent→client JSON-RPC 请求 + 应答路由/断线全部 fail-fast 解锁，`cmd/seek/acp_approval.go` 是接 `permission.Policy.SetAskFn` 的第三审批车道（web hub / `--approval-stdin` print 之后的第三个），`allow_always`→`SetAlwaysAllow` 同 `[a]` 语义；无应答（超时默认 600s、`SEEK_ACP_PERMISSION_TIMEOUT` 可调 / 断线 / 客户端 `-32601`）默认拒；`SEEK_ACP_YOLO=1` 短路优先不装门。e2e 四车道（allow/reject/timeout/yolo）走真二进制+假 LLM：`cmd/seek/acp_approval_e2e_test.go`）；② `session/load`（**已修 2026-11-19**：`loadSession` 按持久化能力申报，`session/load` 读回 transcript、重锚 cwd、恢复车道、回放历史后再回响应；ACP 车道同时补上了按 turn 落盘。见 [`feature-paseo-provider.md`](feature-paseo-provider.md) §2.4）;③ 多 session/进程;④ slash 命令子集（**已修 2026-11-21，ACP·P2/c_cotikz**：/plan 经 ACP 规范自己的 session-modes 语义协议化——`session/new`/`session/load` 结果带 `modes`（currentModeId + availableModes [default, plan]），`session/set_mode` 切换（回合间 only，prompt 中 -32000 拒），受理后 `current_mode_update` 通知先于 `{}` 响应；plan==WorkflowPlanAnalyze、default==WorkflowNone，映射处 `cmd/seek` `acpBackend.SetMode`（与 TUI SetPlan 同款双分支：SetWorkflow + 下一轮 mode 提醒词）；其余 slash 维持 TUI-only（选型见 P2 段）；传输层 `internal/acp/mode.go` + dispatch，两态行为差异真二进制 e2e `cmd/seek/acp_mode_e2e_test.go`：plan 态 write 被拒不落盘 + plan-analyze 提醒词在轮内 user message，default 态同一 write 落盘 + 提醒词消失。用法与边界（propose 审批门在 ACP 无承载）见 guide-zed §5.3）;⑤ `promptCapabilities`（图片）+ 版本协商（**已细化为 M-P.5 / §8 图片输入**）;⑥ per-session cwd（**已修**：`session/new` 的 cwd 生效——`acp.AnchorSession` 同时 chdir 进程 + `Policy.Reanchor`，`internal/acp/session.go`，见 [`feature-paseo-provider.md`](feature-paseo-provider.md) §2.2 D-1）；⑦ Zed `mcpServers` 透传。下方 seed 设计与实现一致。
**机制预算（P0-2 验收⑧）**：**零常驻、零新脚本**。新增机制面仅三处，全部随 `seek acp` 进程生灭：① `internal/acp` 的 `Server.Call`（agent→client JSON-RPC 传输半边：waiter 表 + 帧形状路由 + 全量 fail-fast 解锁）；② `cmd/seek/acp_approval.go`（`permission.Policy.SetAskFn` 的第三个适配器，与 `cmd/seek/approval_stdin.go`、TUI 的 approval channel 同形，无新抽象层）；③ 一个新 env `SEEK_ACP_PERMISSION_TIMEOUT`（缺省 600s，只有 ACP 车道读）。据实核对：**无**新 `scripts/` 脚本（`scripts/acp-smoke-test.sh` 是既有脚本补断言，早已在 `scripts/selftest.sh` 发现名单内）、**无** cron/定时任务、**无**常驻 daemon/launchd 条目、**无** `docs/budget.json` 新条目（`bash scripts/weight.sh` 的 AREAS 六项未变）。**架构页同步义务（验收⑨）**：**已同步**——`docs/guide-architecture.md` §2 权限系统补一行「askFn 三车道」，因为「同一条缝、三种传输」是跨车道命题（第四车道出现时该行是唯一索引）；不触部署形态、常驻设施或 butler 侧接线。
**机制预算（P0-3 验收⑦）**：**零常驻、零新脚本、零新包**。新增机制面逐项：① 一个 wire 帧 `_seek_turn_stop`（`internal/acp/stopreason.go`，`session/update` 的一个新 `sessionUpdate` 变体 + 两个纯函数分类器 `ResponseStopReason` / `FrameStopFromFinish|Error`——无 goroutine、无计时器、无状态）；② 两处 `recover`（`pkg/agent` 的 prompt goroutine 与 `dispatchTool`，各 6 行，把 panic 降级成既有 `ErrorEvent` / 工具错误，不新增事件类型）；③ `AgentEnd` 加 1 个字段 `Finish string`（既有事件结构的加字段，全部现有构造点零改动，零值是空串）。据实核对：**无**新 `scripts/` 脚本（`scripts/acp-smoke-test.sh` 是既有脚本补断言，早已在 `scripts/selftest.sh` 发现名单内）、**无** cron/定时任务、**无**常驻 daemon/launchd 条目、**无** `docs/budget.json` 新条目（`bash scripts/weight.sh` 的 AREAS 六项未变，本卡无任何 docs 区新增预算行）、**无**新 env 旋钮。体积：生产代码 +391/−66 行（12 文件 diff 净 +950 行，其中 577 行是三份测试）；`-ldflags="-s -w"` 二进制 22 039 266 B → 22 055 858 B（**+16 592 B，+0.08%**）。**架构页同步义务（验收⑧）：适用，已同步**——`docs/guide-architecture.md` §3 Tool System 加一条「Failure is an event, never a process death」并指向 `guide-zed.md` §4.1。理由：这不是 ACP 私有细节，而是跨车道命题（TUI / print / web / ACP 共用 `pkg/agent` 的 panic→ErrorEvent 与 `AgentEnd.Finish`），架构页是「内核不变量」的唯一索引，缺它则下一次动 panic 路径的会话无从知道 ACP 面依赖这条；该条同时写清 ACP 侧的合成保证（恰一帧 + 先于响应 ⇒ 两者皆缺即进程死亡），正是部署形态之外唯一需要跨页索引的事实。部署形态、常驻设施、butler 侧接线一概未触。
**选型（P2 验收①，slash 子集）**：首批**仅 /plan**，其余维持 TUI-only——/model 已有 configOptions 协议通道（§5.2/guide-zed），不重复造；/yolo 是车道姿态、启动期 env `SEEK_ACP_YOLO` 表达，不做会话中翻转（pref 与烤进 sysprompt 的 Mode 标签必须一致，P0-2 脱钩教训）；/compact /reset /new /resume /clear 是会话/上下文生命周期，ACP 下归**客户端**（session/new、session/load 就是客户端的动词），band 内再造等于一个会话两个主人；/lang /skill /agents /distill 等是 TUI 本地 UI/会话状态或人审门，无客户端可消费形态。原则：ACP 车道**不做 prompt 伪 slash 解析**——「模式/配置」类语义走协议方法（modes/configOptions），「会话生命周期」类语义留在客户端。完整逐条版：guide-zed §5.3。
**机制预算（P2 验收⑦）**：**零常驻、零新脚本、零新包、零新 env**。新增机制面逐项：① `internal/acp/mode.go`（4 个纯数据 wire 类型 + `ModeSwitcher` 可选兄弟接口——按 AGENTS.md 惯例扩 Backend 不破任何既有 fake；无 goroutine、无锁、无状态）；② `server.go` dispatch 一个新 case + `NewSessionResult`/`LoadSessionResult` 各一个 omitempty 字段（`modes`，nil 时字节不变——wire 兼容测试钉死）；③ `cmd/seek` `acpBackend.SetMode`（复用 TUI SetPlan 的两个分支：`policy.SetWorkflow` + `ag.SetModeLabel`，下一轮生效、不改 sysprompt）+ `acpSessionModes` 双模式表（currentModeId 从 live workflow 推导）。据实核对：**无**新 `scripts/` 脚本（smoke 是既有脚本补断言）、**无** cron/定时、**无**常驻 daemon/launchd、**无** `docs/budget.json` 新条目、**无**新 env。体积：生产代码 +177/−4 行（`mode.go` 75 + `main.go` +55/−2 + `server.go` +51/−2）；测试 +468 行（`mode_test.go` 206 传输层六测 + `acp_mode_e2e_test.go` 254 真二进制两态 e2e + `server_test.go` +4）；`-ldflags="-s -w"` 二进制 22 055 858 B → 22 089 234 B（**+33 376 B，+0.15%**）。**架构页同步义务（P2 验收⑧）：适用，已同步**——`docs/guide-architecture.md` §2 Workflow 行补「ACP `session/set_mode`」第三入口、§7 补协议表面一句。理由：workflow 轴的入口清单（/plan、propose 流、set_mode）是跨车道命题——动 permission 或 plan 路径的会话需要从架构页一处看到「谁会翻这条轴」，缺它则下一次改 SetWorkflow 语义的人不知道 ACP 面有第三个翻转入口依赖它。部署形态、常驻设施、butler 侧接线未触。

**估时**：~4-5 天

**一句话**：实现 **ACP（Agent Client Protocol）** server 模式，让 **Zed / 任意 ACP 编辑器**直接驱动 seek。这是读源码后确认的**唯一 Reasonix 明确领先、seek 该追平的项**——而且用的是**标准协议**（生态网络效应），不是再造一个自定义 RPC。

---

## 1. 动机

- 读 Reasonix `main-v2` 源码确认：它有完整的 `internal/acp/`（service/server/protocol + e2e/live 测试）——session/new·load·prompt + 事件流 + 审批路由 + MCP 集成。**ACP 是 Zed 等编辑器的标准 agent↔编辑器协议**。
- seek 有 `-rpc`（自定义 JSON-RPC server 模式），但**不是标准协议**——接不进 Zed/任意 ACP 客户端。
- 这是 `comparison.md` §R 里**唯一一项 Reasonix 清楚领先**（其余要么对等、要么 seek 领先）。补 ACP = 一夜之间 seek 跑进整个 ACP 编辑器生态。**追平 + 蹭标准网络效应**，性价比高。
- 注意定位：这是**追平/触达**，不是反超护城河（护城河仍是柱 N/O）。所以优先级 P1、排在护城河两柱之后（除非"扩大触达"变成首要目标）。

## 2. 目标 / 不做什么

### 目标
1. **`seek acp`（或 `--acp`）server 模式**：说 ACP over stdio，被编辑器 spawn。
2. **复用、不重造**：把 ACP 方法映射到 seek **已有的** agent controller / session / `askuser` 审批 / MCP client——不造新 agent 内核。
3. **与 `-rpc` 并列**：不替换现有 server 模式；ACP 是"标准协议"变体。

### 不做什么
- ❌ **新 agent 内核 / 新 session 系统**（全复用）。
- ❌ **ACP 之外的多端**（桌面/Web/Chrome 仍范围外）。
- ❌ 改 `pkg/agent` 接口（适配层在 `internal/acp`，调用现有 `Agent.Prompt` 事件流 + askuser）。
- ❌ 自创协议扩展（先严格对齐 Zed ACP spec，存疑项标注）。

## 3. 关键决策（seed 级，实施前细化）

### D1 — ACP = 又一个 stdio JSON-RPC peer，复用既有范式
ACP 是 JSON-RPC over stdio（与 MCP / LSP 同形）。seek **已经手搓过三次**这套（`pkg/mcp`、柱 L `internal/lspclient`、`internal/rpc`）。帧/关联/读循环范式直接借鉴 —— 但 ACP 是 **server 侧**（被编辑器调），方向与 MCP/LSP client 相反，更接近现有 `internal/rpc/server.go`。先评估能否直接在 `-rpc` server 上加 ACP 方法集，还是新起 `internal/acp`。

### D2 — 方法映射（ACP → seek 既有件）
| ACP 方法（待对齐 spec） | 映射到 seek |
|---|---|
| `initialize` / capabilities | 协商；声明 seek 支持的能力子集 |
| `session/new` | 新建 seek session（复用 `internal/session`） |
| `session/load` | resume（复用现有 transcript 溯源） |
| `session/prompt` | `Agent.Prompt(ctx, userText)` → 把 Event 流转成 ACP 流式事件 |
| 工具审批 / approval request | 路由到 `askuser`（编辑器侧渲染 y/N） |
| MCP passthrough | 复用现有 MCP client，把编辑器要的 MCP server 接进本 session |
| cancel | turn ctx 取消（seek 已有） |

### D3 — 事件流转换（核心工作量）
seek 的 `Agent.Prompt` 返回 `<-chan Event`（柱 N 复用的同一个）。ACP 有自己的事件/流式格式。**适配层 = 把 seek Event 翻成 ACP 通知**（text delta / tool call / approval request / done）。这是本柱主要的真实工作量。

### D4 — 审批/权限过线
编辑器里 y/N 审批要走 ACP 的 approval 流（Reasonix 源码："interactive approval routes 'ask' decisions back through that sink"）。映射到 seek 的 `askuser.Policy` 的 askFn —— 把请求发给编辑器、阻塞等回复。复用 askuser 既有 channel 范式（类比 `-rpc` / TUI 的 askFn 接线）。

### D5 — spec 版本对齐
ACP 仍在演进。实施前**锁定一个目标 spec 版本**（Zed 当前 ACP），存疑/未实现的方法返回标准 error（类比柱 L lspclient 对 server→client 请求回 MethodNotFound 防挂）。

## 4. 测试（实施时细化）
- mock ACP 客户端（in-process stdio peer，说 ACP JSON-RPC）：initialize → session/new → session/prompt → 收到流式 text/tool 事件。
- 审批过线：tool 触发 approval → mock 客户端收到 approval_request → 回 approve/deny → seek 据此继续/拒。
- session/load resume。
- cancel：prompt 中途 cancel → turn ctx 取消、不挂。
- 未实现方法 → 标准 error，不崩。
- 与 `-rpc` 并存不冲突。

## 5. 里程碑（seed）
| M | 内容 |
|---|---|
| M-P.1 | ACP 传输 + initialize/capabilities + session/new·load（复用 session）+ mock-client 测试 |
| M-P.2 | session/prompt：`Agent.Prompt` Event 流 → ACP 流式事件（核心转换）+ cancel |
| M-P.3 | 审批过线（askuser → ACP approval）+ MCP passthrough |
| M-P.4 | `seek acp` CLI 接线 + 与 `-rpc` 并存 + Zed 真机冒烟 + 文档 |
| M-P.5 ✅ | **图片输入（已实现）**：`initialize` 声明 `promptCapabilities.image`（`AgentCapabilities.PromptCapabilities`）+ `ContentBlock` 加 `Data`/`MimeType` + `ImageBlocks()` 取图块 + `buildACPPromptText`（从 `acpBackend.Prompt` 提取的纯函数）解码 base64 → `ocr.RunBytes`/`ExpandImageData`（新增：写临时文件→复用柱 Q）→ 注入 `[image: … — OCR]`。继承柱 Q 不变量（只注入文字、不送字节）；坏 base64 / 非图 mimeType 静默跳过。新增测试 `-race` 绿（`ImageBlocks`、`RunBytes`/`ExpandImageData` 假引擎、`imageExtForMime`、`AdvertisesImage`、`buildACPPromptText`：text/text+image/image-only/坏base64/非图mime）。**剩：真 Zed 附图 e2e（你跑）**。详见 §8 |

## 6. 风险 / 预埋 pitfall
- **ACP spec 演进/版本**——锁定目标版本，未实现方法回标准 error（柱 L 范式）。
- **事件模型阻抗失配**（seek Event ↔ ACP 流）——D3 是主要工作量，先小步对齐一种事件再扩。
- **审批过线的阻塞语义**——复用 askuser channel 范式，注意 ctx 取消时不挂（柱 L/monitor 已有 Esc-propagate 先例）。
- **重造冲动**——严守"映射到既有件"，别新写 agent/session。若发现现有件接不上，停下评估（可能该升级完整 PRD）。
- **定位漂移**——这是追平/触达，别让它挤掉护城河两柱（柱 N/O）的优先级。

## 7. 与其它柱的关系
- **现有 `-rpc`（已交付）**：ACP 与它并列；可能共享传输层，待 D1 评估。
- **柱 N/O（护城河）**：本柱是触达、正交；排在护城河之后（除非触达成首要目标）。
- **MCP client（已交付）**：ACP session 里编辑器要的 MCP server 复用现有 client 接入。

---

## 8. 图片输入：ACP image content block → 本地 OCR（M-P.5）

**现状（核到代码）**：seek 的 ACP **直接丢弃图片**。`ContentBlock{Type, Text}`（`internal/acp/server.go:59`）**没有图片数据字段**；`PromptText()` 只收 `type=="text"` 的块（`server.go:243`）；纯图 prompt → `text==""` → `acpBackend.Prompt` 直接返回 `end_turn`、什么都不做。且 `initialize` 没声明 `promptCapabilities`，Zed 多半**根本不让你**给 seek 线程附图（按 agent 声明的能力 gate 附件 UI）。

**为什么这是三条图片输入里最干净的一条**：`@路径`（柱 Q）从磁盘读、TUI 粘贴要去抓剪贴板，而 **ACP 的图片数据就在协议消息里**（image 块带 base64 `data` + `mimeType`）——无需抓取，解码即得。三条入口都汇入柱 Q 的**同一个** OCR 注入。

**设计（复用柱 Q OCR，三步）**：
1. **`initialize` 声明 `promptCapabilities: { image: true }`**（顺带兑现 backlog ⑤ 的版本协商）——让 Zed 肯发图。
2. **扩 `ContentBlock`**：加 `Data string \`json:"data"\`` + `MimeType string \`json:"mimeType"\``（ACP/MCP image 块形状）；`type=="image"` 时收下。
3. **`acpBackend.Prompt`**：每个 image 块 base64 解码 → 写临时 PNG（`paths.Cache()/clipboard/` 或 temp）→ **复用 `ocr.Run`（柱 Q Vision 助手）** → 把 `[image: … — OCR]` 文本拼进 `PromptText()` 的结果，再喂 `Agent.Prompt`。文本块 + 图片块按原顺序拼接。

**不变量（继承柱 Q）**：只注入 OCR **文字**，**绝不把图片字节送给模型**（DeepSeek 文本模型不吃图）；无 OCR 引擎 / 非图 `mimeType` / 损坏 base64 / 超大图 → 优雅降级（忽略该块 + 一次性说明），不崩。

**与 `feature-image-paste.md` 同族**：共用柱 Q 的 OCR 注入轨道；那份做 **TUI 剪贴板**入口，本节做 **ACP** 入口。

**测试**：
- image 块 base64 → 临时文件 → OCR 注入（复用柱 Q 假 OCR 引擎测法，不依赖真模型/真 Zed）。
- `initialize` 回包含 `promptCapabilities.image`。
- 曾经的"纯图 prompt → end_turn 空转"现在能产出 OCR 块。
- text + image 混合块：顺序正确、文本不丢。
- 非图 `mimeType` / 损坏 base64 / 超大图 → 忽略该块、不崩。
- 真 Zed 附图 e2e（手动，像柱 P 的 Zed 冒烟）。
