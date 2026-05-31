# Feature: ACP 编辑器集成（Agent Client Protocol）—— 追平 Reasonix 唯一明确领先项（v7 柱 P · seed）

**所属版本**：v7（v0.8.x）· 柱 P
**前置阅读**：[`v7.md`](v7.md) §7.3、[`comparison.md`](../comparison.md) §R.2（IDE 集成行：源码确认 Reasonix 用 ACP、领先于 seek 的自定义 `-rpc`）、`internal/rpc/server.go`（seek 现有 JSON-RPC server 模式）、`internal/lspclient`（柱 L 刚做的 stdio JSON-RPC client，帧/关联范式可借鉴）、`pkg/mcp/client.go`（JSON-RPC over stdio 先例）
**状态**：✅ **实现 + 真 server stdio e2e 跑通**。`internal/acp`：手写 stdlib JSON-RPC 2.0（newline-delimited，无 Content-Length，复用 pkg/mcp 风格）—— `initialize` / `session/new` / `session/prompt`（异步、可 `session/cancel`）+ `session/update` 通知；`cmd/seek`：`seek acp` 子命令 + `acpBackend`（适配现有 agent）+ `acpUpdate`（Event→session/update 映射，含 reasoning/turn-bookkeeping **不外泄** 的单测）。**e2e**：脚本化 newline-JSON-RPC 客户端驱动真 `seek acp` + 真 agent —— initialize→`{protocolVersion:1, agentCapabilities}`，session/new→`sessionId`，session/prompt→流式 `agent_message_chunk`（"P"/"ONG"）→`{stopReason:"end_turn"}`，session/cancel 已完成会话不崩，stdin EOF 干净退出 rc=0。**剩**：真 Zed GUI live 验 + `load_session`（capability 现报 false）。下方 seed 设计与实现一致。
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
