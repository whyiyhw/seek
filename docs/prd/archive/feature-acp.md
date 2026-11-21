# Feature: ACP 编辑器集成（Agent Client Protocol）—— 追平 Reasonix 唯一明确领先项（v7 柱 P · seed）

**所属版本**：v7（v0.8.x）· 柱 P
**前置阅读**：[`v7.md`](../v7.md) §7.3、[`comparison.md`](../../comparison.md) §R.2（IDE 集成行：源码确认 Reasonix 用 ACP、领先于 seek 的自定义 `-rpc`）、`internal/rpc/server.go`（seek 现有 JSON-RPC server 模式）、`internal/lspclient`（柱 L 刚做的 stdio JSON-RPC client，帧/关联范式可借鉴）、`pkg/mcp/client.go`（JSON-RPC over stdio 先例）
**状态**：✅ **实现 + 真 server stdio e2e 跑通**。`internal/acp`：手写 stdlib JSON-RPC 2.0（newline-delimited，无 Content-Length，复用 pkg/mcp 风格）—— `initialize` / `session/new` / `session/prompt`（异步、可 `session/cancel`）+ `session/update` 通知；`cmd/seek`：`seek acp` 子命令 + `acpBackend`（适配现有 agent）+ `acpUpdate`（Event→session/update 映射，含 reasoning/turn-bookkeeping **不外泄** 的单测）。**e2e**：脚本化 newline-JSON-RPC 客户端驱动真 `seek acp` + 真 agent —— initialize→`{protocolVersion:1, agentCapabilities}`，session/new→`sessionId`，session/prompt→流式 `agent_message_chunk`（"P"/"ONG"）→`{stopReason:"end_turn"}`，session/cancel 已完成会话不崩，stdin EOF 干净退出 rc=0。

**真 Zed GUI 验证通过**（Zed 1.4.4）：seek 出现在 Agent 面板 External Agents 下拉里，`initialize`（带 `clientCapabilities`）→`session/new`（cwd=项目）→多轮 `session/prompt`，工具调用（read/grep/list_dir/git/**edit**）映射成 Zed 步骤卡片、实测成功改了工作区文件（`.gitignore` 加行），多轮均 `end_turn`。新增 opt-in 协议 trace（`SEEK_ACP_LOG=/path` → tee 收发原始 JSON-RPC，零默认开销）便于调试握手。**用法见 [`docs/guide-zed.md`](../../guide-zed.md)。**

**剩（MVP 优化 backlog，见 guide §5）**：① ACP `session/request_permission` 把工具审批弹到 Zed 原生 UI（**已修 2026-11-21，ACP·P0-2**：非 yolo 车道权限姿态 `Ask`，`internal/acp` 的 `Server.Call` 承载 agent→client JSON-RPC 请求 + 应答路由/断线全部 fail-fast 解锁，`cmd/seek/acp_approval.go` 是接 `permission.Policy.SetAskFn` 的第三审批车道（web hub / `--approval-stdin` print 之后的第三个），`allow_always`→`SetAlwaysAllow` 同 `[a]` 语义；无应答（超时默认 600s、`SEEK_ACP_PERMISSION_TIMEOUT` 可调 / 断线 / 客户端 `-32601`）默认拒；`SEEK_ACP_YOLO=1` 短路优先不装门。e2e 四车道（allow/reject/timeout/yolo）走真二进制+假 LLM：`cmd/seek/acp_approval_e2e_test.go`）；② `session/load`（**已修 2026-11-19**：`loadSession` 按持久化能力申报，`session/load` 读回 transcript、重锚 cwd、恢复车道、回放历史后再回响应；ACP 车道同时补上了按 turn 落盘。见 [`feature-paseo-provider.md`](feature-paseo-provider.md) §2.4）;③ 多 session/进程;④ slash 命令子集;⑤ `promptCapabilities`（图片）+ 版本协商（**已修 2026-11-21，ACP·P1-3 卡 c_4pczu3**：`initialize` 申报 `promptCapabilities.image=true`，`protocolVersion` 走 `acp.NegotiateVersion` 取交集最高版、超出则降级申报；`ContentBlock` 收 image 块（base64 + mimeType），`PromptParams.Segments()` 保序，`buildACPPromptText` 按序把每张图的标记插在原位置——出口接柱 Q 的继任者 `visionRouter`（原生视觉 / 非视觉行内注明），不是 §8 原文的 `ocr.Run`（柱 Q 已下线，理由写在 §8）；坏 base64 / 非图 mimeType 静默跳过。两态 fixture e2e（`cmd/seek/acp_image_e2e_test.go` + `cmd/seek/testdata/acp-image/*.json`）+ `scripts/acp-smoke-test.sh` 补断言;⑥ per-session cwd（**已修**：`session/new` 的 cwd 生效——`acp.AnchorSession` 同时 chdir 进程 + `Policy.Reanchor`，`internal/acp/session.go`，见 [`feature-paseo-provider.md`](feature-paseo-provider.md) §2.2 D-1）；⑦ Zed `mcpServers` 透传。下方 seed 设计与实现一致。
**机制预算（P0-2 验收⑧）**：**零常驻、零新脚本**。新增机制面仅三处，全部随 `seek acp` 进程生灭：① `internal/acp` 的 `Server.Call`（agent→client JSON-RPC 传输半边：waiter 表 + 帧形状路由 + 全量 fail-fast 解锁）；② `cmd/seek/acp_approval.go`（`permission.Policy.SetAskFn` 的第三个适配器，与 `cmd/seek/approval_stdin.go`、TUI 的 approval channel 同形，无新抽象层）；③ 一个新 env `SEEK_ACP_PERMISSION_TIMEOUT`（缺省 600s，只有 ACP 车道读）。据实核对：**无**新 `scripts/` 脚本（`scripts/acp-smoke-test.sh` 是既有脚本补断言，早已在 `scripts/selftest.sh` 发现名单内）、**无** cron/定时任务、**无**常驻 daemon/launchd 条目、**无** `docs/budget.json` 新条目（`bash scripts/weight.sh` 的 AREAS 六项未变）。**架构页同步义务（验收⑨）**：**已同步**——`docs/guide-architecture.md` §2 权限系统补一行「askFn 三车道」，因为「同一条缝、三种传输」是跨车道命题（第四车道出现时该行是唯一索引）；不触部署形态、常驻设施或 butler 侧接线。

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
| M-P.5 ✅ | **图片输入 + 版本协商（2026-11-21 按 P1-3 收口）**：`initialize` 申报 `promptCapabilities.image` 并**真协商** `protocolVersion`（`acp.NegotiateVersion`，交集最高版、降级申报）；`ContentBlock` 带 `Data`/`MimeType`，`PromptParams.Segments()` 给出保序的 text/image 序列；`buildACPPromptText` 按序拼接，图块解码后走**柱 Q 的继任者** `visionRouter`（原生视觉 / 非视觉模型行内注明），坏 base64 / 非图 mimeType 静默跳过。两态 fixture e2e（纯图 / 图文混排，真二进制 + 假端点）+ smoke 断言 + `-race` 绿。**剩：真 Zed 附图的人跑实机验证**。与 §8 原文第 ③ 步（`ocr.Run`）的偏离理由见 §8 |

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

## 8. 图片输入：ACP image content block（M-P.5 · 2026-11-21 按 P1-3 落地）

**状态（2026-11-21，卡 c_4pczu3 / ACP·P1-3）**：三步已落地，**第 ③ 步的出口与本节原稿不同**，理由见下「与原文的偏离」。

**落地现状**：
1. `initialize` 申报 `promptCapabilities.image=true`（`AgentCapabilities.PromptCapabilities`），并**真正协商** `protocolVersion`（`acp.NegotiateVersion`：取「客户端请求 ∩ seek 支持」最高版，请求更高版则降级申报 + stderr 一行；旧行为是回显）。
2. `ContentBlock` 带 `Data`（base64）+ `MimeType`，`type=="image"` 收下不炸；`PromptParams.Segments()` 按 **wire 顺序**给出 text/image 序列（`ImageBlocks()` 保留给"只要图"的调用方）。
3. `buildACPPromptText` 逐段消费：文本段之间仍以单换行拼接（与旧 `PromptText()` 字节一致，文本型 prompt 零变化），图片段解码后交 `visionRouter.routeBytes`，得到的 `[image: …` 标记作为独立段落插在**原序位置**（不再是"全部追加到末尾"）。

**与原文的偏离（照单不该重设计，但前提已失效——据实记录）**：本节原文第 ③ 步写「写临时 PNG → 复用 `ocr.Run`（柱 Q Vision 助手）→ 注入 `[image: … — OCR]`」。柱 Q 的 OCR 引擎已在 `0ecfd6f6`（feature-vision M-V.0）**整体下线**——`internal/ocr` 包不存在，`ocr.Run` / `ocr.RunBytes` 无从复用，且 [`feature-vision.md`](feature-vision.md) 明确记录了「OCR 收敛为非视觉模型的降级路径」这一方案**同日被推翻**。因此本卡落的是柱 Q 管道的**继任者**：同一条 `visionRouter` 缝（`cmd/seek/visioninput.go`，TUI 粘贴与 `@引用` 共用），视觉模型原生附图、非视觉模型行内注明。原方案的三条验收意图——图块收下、按序拼接、只注入文字不送字节给吃不下图的模型——全部保留，仅"文字从哪来"由 OCR 换成"看得见的模型自己看 / 看不见的模型收到一行提示"。

**降级选型（卡面要求写明）**：无 OCR 引擎 / 坏 base64 / 非图 `mimeType` / 超过单图 32 MiB / 资产库不可用 → **跳帧 + 注明，不拒收整条 prompt**，且**不**把图片字节送给非视觉模型（那会让 API 报 400）。理由：ACP 的 prompt 是整轮用户意图，一次附图坏掉不该让整段提问石沉大海；注明让模型能主动说"这张图我没看到，换个支持视觉的模型"。

**测试（本卡新增，全部 `-race` 绿）**：
- `internal/acp`：`TestNegotiateVersion`（表驱动：1→1、2→1、99→1、0/−3→兜底）、`TestPromptParams_Segments`（顺序 + 空块/未知块剔除 + 别名指向原块）。
- `cmd/seek`：`TestACPBackend_InitializeNegotiatesVersion`（含"降级不得丢 image 能力"）、`TestBuildACPPromptText_Ordering`（text/image/text 三段标记落中、纯文本仍逐字节等同 `PromptText`）。
- **两态 fixture e2e**（真二进制 + 假 OpenAI 兼容端点，`cmd/seek/acp_image_e2e_test.go` + `cmd/seek/testdata/acp-image/{image-only,mixed}.json`）：①纯图 prompt 现在真的到达模型（旧行为 `text==""` 空转）且不泄露图片字节；②图文混排的顺序在**实际发出的请求体**里成立。fixture 自身另有守卫（PNG 签名 + 可解码成协议块），防止 base64 损坏让两条用例"空转式通过"。
- `scripts/acp-smoke-test.sh`：真二进制断言 `initialize(protocolVersion:2)` 被协商成 1 且在 stderr 申报、image content block 被收下且坏块静默降级不崩。

**剩**：真 Zed 附图的人工实机验证（沙盒内只能证到"块被收下、标记按序进请求"；Zed 侧附件 UI 是否真的出现需人跑）。

**机制预算（本卡验收⑧）**：**零常驻、零新脚本、零新 env**。新增面三处，全部随 `seek acp` 进程生灭：① `internal/acp` 的 `NegotiateVersion` + `Segment`/`Segments()`（纯函数，无状态）；② `PromptParams.Segments()` 的消费方 `buildACPPromptText` 从"拼字符串"改成"按段拼"，同文件同函数；③ `initialize` 的一行 stderr 降级记录（无新配置项）。据实核对：**无**新 `scripts/` 脚本（smoke 是既有脚本补断言，仍在 `scripts/selftest.sh` 发现名单内）、**无** cron/定时任务、**无**常驻 daemon/launchd、**无** `docs/budget.json` 新条目（六区 AREAS 不变；`docs/prd/archive` 增 ~1KB，仍在预算内）、**无**新依赖。

**架构页同步（本卡验收⑨）**：`docs/guide-architecture.md` 新增 §12「Editor / ACP lane」——收录三条容易再踩的设计点（能力是申报、版本是协商；能力**不**按模型 gate；三条图片入口共用一条 router 缝 + 保序）；不触部署形态与常驻设施。

### 8.1 历史：原设计稿（2026-11 上旬，已被上面的落地现状取代）

以下为卡面照单的那份原文，保留是因为「为什么这条路线走不通」比结论更值钱；动手时按上面「落地现状」执行。

**落地前现状**：seek 的 ACP **直接丢弃图片**。`ContentBlock{Type, Text}`（`internal/acp/server.go:59`）**没有图片数据字段**；`PromptText()` 只收 `type=="text"` 的块（`server.go:243`）；纯图 prompt → `text==""` → `acpBackend.Prompt` 直接返回 `end_turn`、什么都不做。且 `initialize` 没声明 `promptCapabilities`，Zed 多半**根本不让你**给 seek 线程附图（按 agent 声明的能力 gate 附件 UI）。

**为什么这是三条图片输入里最干净的一条**：`@路径`（柱 Q）从磁盘读、TUI 粘贴要去抓剪贴板，而 **ACP 的图片数据就在协议消息里**（image 块带 base64 `data` + `mimeType`）——无需抓取，解码即得。三条入口都汇入柱 Q 的**同一个** OCR 注入。

**原设计（复用柱 Q OCR，三步）**：
1. **`initialize` 声明 `promptCapabilities: { image: true }`**（顺带兑现 backlog ⑤ 的版本协商）——让 Zed 肯发图。
2. **扩 `ContentBlock`**：加 `Data string \`json:"data"\`` + `MimeType string \`json:"mimeType"\``（ACP/MCP image 块形状）；`type=="image"` 时收下。
3. **`acpBackend.Prompt`**：每个 image 块 base64 解码 → 写临时 PNG（`paths.Cache()/clipboard/` 或 temp）→ **复用 `ocr.Run`（柱 Q Vision 助手）** → 把 `[image: … — OCR]` 文本拼进 `PromptText()` 的结果，再喂 `Agent.Prompt`。文本块 + 图片块按原顺序拼接。

**不变量（继承柱 Q）**：只注入 OCR **文字**，**绝不把图片字节送给模型**（DeepSeek 文本模型不吃图）；无 OCR 引擎 / 非图 `mimeType` / 损坏 base64 / 超大图 → 优雅降级（忽略该块 + 一次性说明），不崩。

**与 `feature-image-paste.md` 同族**：共用柱 Q 的 OCR 注入轨道；那份做 **TUI 剪贴板**入口，本节做 **ACP** 入口。

**原测试清单**（已由 §8 顶部「测试」小节按落地实现改写）：
- image 块 base64 → 临时文件 → OCR 注入（复用柱 Q 假 OCR 引擎测法，不依赖真模型/真 Zed）。
- `initialize` 回包含 `promptCapabilities.image`。
- 曾经的"纯图 prompt → end_turn 空转"现在能产出 OCR 块。
- text + image 混合块：顺序正确、文本不丢。
- 非图 `mimeType` / 损坏 base64 / 超大图 → 忽略该块、不崩。
- 真 Zed 附图 e2e（手动，像柱 P 的 Zed 冒烟）。
