# Feature: Web Frontend——control-plane 首个真实渠道 + inspect-rpc 数据面并入

**所属版本**：独立 feature（平台化线，挂在 remote-control-plane 之下）
**前置阅读**：[`feature-remote-control-plane.md`](feature-remote-control-plane.md)（父设计——Frontend 端口 / M2 路由 / M3 执行器 / D-C1…D-C5 全部本文复用；其 §8「未来增强」第一条「网页（WS+SSE）」即本文）、[`feature-orchestration.md`](feature-orchestration.md)（P2 任务面板对齐对象）、[`feature-inspect-rpc.md`](feature-inspect-rpc.md)（P3 并入评估对象）、[`docs/dsh-analysis.md`](../dsh-analysis.md) §7/§8.0（dsh web app 参考与两赛道论）、[`docs/guide-serve.md`](../guide-serve.md)
**状态**：📐 设计稿（2026-11-13，卡 c_7pptol；同日返工：对齐 c_qucxtu 已交付的 web MVP，全文口径改为「吸收/就地演进」，见 §1.4）
**定位依据**：用户 2026-11-13 指认——多端通用主轴的关键缺口是「无 web 页」；同日飞书 adapter（M4/M5）撤销排期、首个 frontend 选型开放，web 是第一个落地的真实渠道。最小聊天回路已随卡 c_qucxtu 落地并进 main（§1.4），本文是它的完整化设计。

---

> **一句话**：把 c_qucxtu 已交付的 web MVP（`internal/controlplane/webfrontend.go`：单文件纯 stdlib、默认 `127.0.0.1:8790`、`GET /events` SSE + `POST /msg`、`?token=` 鉴权 + 首启生成 0600 `~/.seek/serve/web.token`）**就地演进**成完整 web 渠道——实现的是既有 `Frontend` 端口，浏览器即渠道；复用 M2 会话绑定路由与 M3 子进程执行器，ConvID 进同一张 bindings 表（现状固定 `web:default`，演进目标 `web:<uuid>`）。聊天补全（流式渲染 / `/bind` 切换 / token-$ 状态栏）→ P2 任务面板（对齐编排层 P1）→ P3 资产面（**并入** feature-inspect-rpc 的数据面，独立 tarball 面板取消）。静态资产 `go:embed` 进单二进制，无 node 构建链，默认只听 127.0.0.1。

## 0. 设计六问总览（本文骨架）

| # | 问题 | 结论（章节） |
|---|---|---|
| ① | 形态 | `seek serve --frontend=web`（c_qucxtu 已注册）：Frontend 端口的 SSE+POST 映射，Run=浏览器事件流下行进平面、Send=回复上行回浏览器；浏览器即渠道，ConvID=会话键（§2）；**演进而非重写**已交付 MVP（§1.4） |
| ② | 分期 | MVP 聊天 → P2 任务面板（编排 P1 可视化）→ P3 资产面；**P3 结论=并入 inspect-rpc 数据面、替代其 transport+tarball 两部分**（§3） |
| ③ | 零依赖 | 纯 stdlib + go:embed 单二进制，无 node 链；CI 体积 gate <200KB；超预算的 escape hatch=build tag（§4） |
| ④ | 鉴权 | 对齐 butler `/today`：单 token + `?token=` 303 换 HttpOnly cookie + Bearer；**比 /today 多一道写面 CSRF 防护**（本渠道能驱动执行）（§5） |
| ⑤ | 与 ACP 关系 | **不复用 acpBackend 会话栈，走平面子进程执行器**——per-session cwd 靠进程 chdir，单进程单会话才成立，与多会话并发冲突（§6） |
| ⑥ | dsh 参考 | 借「从会话事件流渲染」的形态；不抄 React/bundle/Cordis 运行时（两赛道论）（§7） |

## 1. 动机与现状校准

### 1.1 端口已就位，第一个真实渠道已过最小回路

control-plane 的 M1–M3+M6a/M7a 已进 main：`Frontend` 端口（`internal/controlplane/frontend.go`）、路由与指令（`router.go`）、子进程执行器（`executor.go`）、serve 生命周期（`serve.go`）、CLI 接线（`internal/servecli`）。飞书 adapter（M4/M5）撤销排期后，web MVP（卡 c_qucxtu）作为第一个真实渠道落地并已进 main：`frontends` 注册表是 `fake` + `web`，浏览器最小聊天回路（发消息、SSE 收回复、绑定状态行、token 鉴权）可跑通——PRD §6 末抽象验收（Telegram 纸面验证）的接缝已由真实消费者兑现过一次。

但它止于最小回路（§1.4 逐项边界）：无流式渲染（跑完一次性收）、无 token/$ 状态栏、单固定会话、浏览器入口的 token 走 URL 查询参数（无 cookie 换票/CSRF 闸）。**本文的工作不是「从零造第一个渠道」，而是「把已落地的 MVP 演进到完整形态」**——流式 / 会话切换 UI / 状态栏，再到只有 web 接得住的可视化面（P2 任务面板 / P3 资产面；聊天渠道里做面板是逆势）。web 作为首渠道的先天优势不变：零外部依赖、单人自用天然（本机浏览器）。

### 1.2 已就绪、直接复用（不重造）

| 地基 | 出处 | 本文复用方式 |
|---|---|---|
| `Frontend` 端口 + `ConvID` + `Inbound`/`Reply` | M1（frontend.go） | web 适配器实现同一端口；ConvID 前缀 `web:` |
| 会话绑定路由 `/bind` `/projects` `/switch` `/new` `/cancel` | M2（router.go） | **零改动**——浏览器发的就是同款指令文本 |
| 子进程执行器：per-Conv 串行、全局并发、超时 kill、resume 自愈 | M3（executor.go） | **零改动**（MVP）；流式渲染只加一条可选 seam（§2.4） |
| `seek -p --json` JSONL 稳定契约 | `cmd/seek/main.go` jsonLine（L2194–2232） | 流式渲染与 token/$ 状态栏的数据源：`text_delta`/`tool_start|delta|end`/`turn_end`（含 PromptTokens/CompletionTokens/CacheHitTokens）/`agent_end`（含 session_id）全部已在流里 |
| bindings 持久化 + 损坏自愈 | M1 bindings.go | web 会话跨 serve 重启续接，白拿 |
| D-C5 权限姿势（print 模式 deny、per-binding yolo） | 平面安全模型 | web 渠道默认非 yolo，白拿 |
| net/http 已在默认构建依赖树 | webfetch 工具引入 | HTTP server 零新增 go.mod 依赖 |

### 1.3 与既有「web 计划」的关系

仓里已有两份涉及 web 的设计，本文是它们的收敛点：

- **feature-remote-control-plane.md §8**：把「网页（WS+SSE）」列为未来增强之一——本文把它做实，并落在端口上（而非旁路）。
- **feature-inspect-rpc.md**：规划了 HTTP+SSE transport + 独立 tarball 面板（尚未实现）。本文 §3.3 给出结论：其数据面**并入**本文 P3，transport 宿主从「TUI 进程 `--rpc-listen`」迁到 serve 进程，tarball 面板取消。其 stdio transport / registry 重构（IDE 线）不动。

### 1.4 与 c_qucxtu MVP 的关系（scope 边界——本文的地面基准）

c_qucxtu 已交付并进 main：`internal/controlplane/webfrontend.go`（368 行，默认构建、纯 stdlib、无 build tag）+ go:embed 的 `webfrontend.html`（120 行单页）+ `webfrontend_test.go`（362 行 8 条测试）。**本文全部 W 卡是它之上的增量演进，不推倒、不另起实现**；每张卡的硬验收都含「现有测试家族零回归」。逐项边界：

| 维度 | 现状（webfrontend.go，已交付） | 本文增量 |
|---|---|---|
| 代码位置 | `internal/controlplane` 包内三文件（.go/.html/_test.go），同包无 tag 是头注释里记下的有意决策 | **就地同包生长**，不迁移独立包（取舍结论见下） |
| 注册 | `servecli.frontends["web"] = NewWeb(WebConfig{})`，零值 config 即生产形状 | 构造器携带 config（`--listen` 等 flag 注入，W1.1） |
| 监听 | `DefaultWebAddr = "127.0.0.1:8790"` 固定口，loopback only，启动行印 `web: up: http://127.0.0.1:8790/?token=<t>` | 非 loopback bind 的 fail-closed 闸（W1.1）；端口保持 8790 不改 |
| 路由 | `GET /`（页面，不鉴权）/ `GET /events`（SSE：广播 + 15s 心跳 + `retry:3000`）/ `POST /msg`（`{"text"}`→Inbound，202）/ `GET /state`（`{bound,project,session}`） | 四个名字原样沿用；流式= `/events` 增事件类型（W1.4）；新端点（`/subagents`、`/rpc`）按同一无前缀风格命名（§2.5） |
| 鉴权 | `?token=` 或 `Authorization: Bearer`，constant-time；token 首启生成 hex(24B) 写 `~/.seek/serve/web.token`（目录 0700/文件 0600） | 首跳 303 换票→HttpOnly cookie + URL 擦净、写面 CSRF 闸、Host 校验（W1.2） |
| 会话 | 固定单会话 `web:default`（`webConvNative="default"`，所有 tab 同一会话，D-C5 取舍） | conv-uuid per 会话（D-W3，W1.3），旧绑定兼容 |
| 下行 | 单一事件 envelope `{"conv","text"}` 广播全部订阅者；无订阅者即丢（MVP 文档化限制：seek 跑数秒、SSE 毫秒级连上，竞态可忽略） | `delta/tool/usage/reply` 事件族（W1.4）；历史回放到 P3 面板再补 |
| 上游 Inbound | `Conv=web:default`、`EventID` 有意留空（浏览器不重投、平面契约空 id 永不去重）、`Sender="web"`、`DirectAt=true` | Conv 换 conv-uuid（W1.3）；其余语义保持 |
| 页面 | `webfrontend.html`：消息列表、输入框（`/bind` 等指令纯文本透传）、`/state` 状态行、SSE 自动重连、textContent-only 渲染 | 流式气泡 / 指令快捷按钮 / token-$ 状态栏（W2） |

**包位置取舍结论（迁移独立包与否）**：**不迁移**——增量以 `webfrontend*.go` 文件族在 `internal/controlplane` 内就地生长。理由：① feishu 独立子包的成因是 build tag（SDK 隔离，D-F1），web 纯 stdlib 无此强制力，「adapter 必须子包」不是本仓通例，MVP 把同包无 tag 记为有意决策；② 搬家是零行为变化的纯 churn，与减法纪律相逆；③ 平面类型（`ConvID`/`Inbound`/`Frontend`/`BindingStore`）就在本包，同包零 import 跳数。重评触发条件：web 文件族超过 ~5 个文件、或出现第二个 build 维度时，一次性 `git mv` 到 `internal/controlplane/webfrontend/`（feishu 同款布局，历史保留）。**原稿规划的 `internal/webfrontend/` 顶层新包方案作废。**

## 2. ① 形态：seek serve + web Frontend

### 2.1 D-W1 — 吸收 c_qucxtu MVP，就地演进（不建新包）

web 是 `Frontend` 端口的第二个实现（fake 之后第一个真实的），主体已由 c_qucxtu 交付（§1.4 清单）。本文全部后端工作 = 在 `internal/controlplane` 包内的 `webfrontend*.go` 文件族上增量生长：

```
internal/controlplane/         # 就地，不迁移（§1.4 取舍结论）
    webfrontend.go             # MVP 主体（已交付）：WebConfig/WebFrontend/四路由/鉴权/token
    webfrontend.html           # 已交付单页（go:embed，零构建链）
    webfrontend_stream.go      # W1.4：SendStream→SSE 事件族（delta/tool/usage/reply）
    webfrontend_conv.go        # W1.3：per-conv hub 演进（现状全 tab 共享 web:default）
    webfrontend_static.go      # W1.5：CSP/gzip（页面膨胀拆 static/ 时引入）
```

（文件切分是示意——增量怎么落文件由实现卡定，约束只有一条：MVP 三个文件不搬家、不重写、语义不动。）

- **build tag（D-F1）维持不引入**：MVP 头注释已论证——D-F1 的存在理由是 SDK 依赖（larkws），web 适配器纯 stdlib（`net/http` 已在默认构建依赖树，`go:embed` 是编译器内建），没有要隔离的东西，build tag 只会制造第二个构建产物。`buildtags_test.go` 只门 feishu 包（SDK 防泄漏），不与默认构建冲突。
- 注册已在（`servecli.frontends["web"]`，`NewWeb(WebConfig{})` 零值即生产形状）；增量只是让构造器可携带 config——`--listen` 覆盖 `DefaultWebAddr`、token 路径可指（W1.1 落）。

### 2.2 D-W2 — 端口映射：Run=浏览器事件流下行进平面，Send=回复上行回浏览器

Frontend 端口收发分离（D-C1）映射到 HTTP 形状（方向词取平面视角：**下行**=推进平面 inbound 队列，**上行**=平面把答复送回渠道）：

| 端口方法 | web 落点 | 说明 |
|---|---|---|
| `Name()` | `"web"`（已交付） | ConvID 前缀、日志、bindings 键 |
| `Run(ctx, in chan<- Inbound)` | **拥有 `http.Server` 生命周期**（已交付）：起 listener、serve 到 ctx.Done、`Close()` 强断——SSE 长连接永不空闲，优雅 `Shutdown` 会永等（MVP 注释已论证，设计沿用）。浏览器 `POST /msg` → 归一化 `Inbound` → `in` channel（已交付，202） | HTTP server 就是「传输层」，Run 的「自管传输含重连」契约对 server 而言=监听到取消；浏览器侧重连由 EventSource `retry:3000` 自管（已交付） |
| `Send(ctx, conv, Reply)` | 广播到该 conv 的 SSE 订阅者（现状：全部订阅者同属 `web:default`，per-conv 寻址是 W1.3 增量）；`reply` 事件携 authoritative 最终文本（现状 envelope `{"conv","text"}`，W1.4 扩事件族） | best-effort 语义同端口契约：无订阅者（页面关了）不报错——现状是**丢弃**（MVP 文档化的取舍：seek 跑数秒、SSE 毫秒级连上，竞态可忽略）；「重开页面补看」需历史回放，P3 面板（session/messages）落地后自然补上，W1 不做 |

`Inbound` 归一化（现状字段值，W1.3 只换 Conv）：

```go
Inbound{
    Conv:     NewConvID("web", "default"),    // 现状：固定单会话；conv-uuid 演进见 D-W3
    Text:     <表单 text>,
    EventID:  "",                             // 现状有意留空：浏览器不重投，平面契约空 id 永不去重
    Sender:   "web",                          // 现状即此值；D-C5 单人下无语义作用
    DirectAt: true,                           // web 是私聊语义：永远触发
}
```

**为什么 POST 而非 WebSocket 上行**：端口契约本就不假设双工（§1.3 收发分离）；SSE 下行 + POST 上行是零依赖的经典组合（EventSource + fetch 都是浏览器原生），且让鉴权/CSRF 走标准 HTTP 语义（cookie/header），不必在 WS 帧协议里重造。dsh web app 用 WS（§7）——它的 UI 需要双向低延迟（编辑器协作形态），聊天场景不需要。

### 2.3 D-W3 — ConvID=会话键：从固定 `web:default` 演进到浏览器持有 uuid

- **现状**：MVP 是固定单会话 `web:default`（`webConvNative = "default"`，所有 tab 同一会话）——单人单会话零状态的刻意取舍。bindings 表本就按 ConvID 键控，`web:default` 是一条普通绑定，演进不需要新存储。
- **演进（W1.3）**：conv-uuid 由浏览器生成（crypto.randomUUID，localStorage 持久），首条消息起即随请求携带（`X-Seek-Conversation` 头）。`ConvID = "web:<uuid>"`。
- **服务端零会话状态**（演进后依然成立）：会话↔项目绑定、session_id 续接全在既有 bindings 表（`~/.seek/serve/bindings.json`）——换设备/清 localStorage 只是换一个 conv（旧绑定还在表里，`/bind` 回同一路径即新开）。这是「ConvID=会话键」的全部含义：**web 渠道没有自己的会话存储**。
- 浏览器 UI 提供「新对话」按钮 = 新 mint 一个 uuid（等价 `/new` 但连 binding 也换新）；`/bind` `/switch` 沿用平面指令（M2 已有），UI 只是快捷按钮发送同款指令文本（§3.1 MVP）。
- **兼容**：存量 `web:default` 绑定不迁移不删除——演进后它退为「未带头请求」的默认 conv（或一次性收编，W1.3 实现卡定）；验收含旧绑定可见。

### 2.4 D-W4 — 流式渲染：sibling 接口 + 执行器流式 seam，两个零破坏缝

MVP 要求「消息收发流式渲染」，但端口 v1 是「一条 Inbound 一条 Reply」（跑完一次性发）。两条缝，全部走本仓既有的 sibling-interface 纪律（AGENTS.md「Sink interfaces」条；propose 工具四个 sibling 接口的先例）：

**缝 1（Frontend 侧）**——可选流式扩展，端口 3 方法不动：

```go
// StreamSender is the OPTIONAL streaming extension of Frontend.
// The router upcasts at the send site: frontends that don't
// implement it keep the exact one-reply-per-inbound behaviour.
type StreamSender interface {
    // SendStream forwards one live run event (the seek -p --json
    // JSONL contract, §2.5) to the conversation. Best-effort, same
    // posture as Send.
    SendStream(ctx context.Context, conv ConvID, ev RunEvent) error
}
```

**缝 2（执行器侧）**——`Run` 保持字节不变，流式读是新方法：

```go
// RunEvent is one line of the stable `seek -p --json` JSONL contract
// (cmd/seek jsonLine). Named + exported so frontends can render the
// stream without re-parsing raw bytes; the field set mirrors jsonLine.
type RunEvent struct { … }

// RunStream is Run with an optional per-line observer. Run delegates:
// Run(req) ≡ RunStream(ctx, req, nil). The observer sees each parseable
// JSONL line AS IT ARRIVES (line-buffered stdout read), before the
// process exits.
func (e *Executor) RunStream(ctx context.Context, req ExecRequest, emit func(RunEvent)) (ExecResult, error)
```

现状 `attempt` 用 `lockedBuf` 收完 stdout 再整体 parse；`RunStream` 把 stdout 换成 bufio 行级读 + 逐行 parse + `emit` 回调，末态聚合逻辑（sawAgentEnd/sessionID/自愈重跑）不变。**emit 的事件是完整 jsonLine 契约**（含 `tool_start/tool_end/turn_end/agent_end`），不是只有 text_delta——这是状态栏与 P2 面板的共同数据源。

**router 接线**：`Handle` 里执行前 upcast `if ss, ok := r.fronts[conv.FrontendName()].(StreamSender); ok`，命中则走 `RunStream` 并把事件转 `SendStream`；未命中走 `Run`——**fake/feishu 路径字节不变**。最终 Reply 照旧经 `Send` 下发（流式渲染的 bubble 由 `reply` 事件收口替换，客户端按 run 幂等）。

### 2.5 事件与 API 面（沿用 MVP 路由名，增量全部是加法）

```
POST /msg                {text}                  → 202；归一化 Inbound 进平面（已交付）
GET  /events             (SSE)                   → 事件流：现状 {"conv","text"}，W1.4 扩为下表事件族
GET  /                   静态页（embed，不鉴权；已交付）
GET  /state              → 绑定状态 {bound,project,session}（已交付；per-conv 后按 conv 寻址）
GET  /?token=<t>         303 → / + HttpOnly cookie（URL 擦净，§5；W1.2 增量——现状 ?token= 直接过鉴权）
```

新端点（P2 的 `/subagents`、P3 的 `/rpc`）沿用同一**无前缀**命名风格——现状四路由已无前缀，再引入 `/api/` 前缀就是两套风格并存。

SSE 事件（webfrontend 自有 envelope，`data:` 为 JSON；`delta/tool/usage/reply` 均为 W1.4 在 `/events` 上新增的事件类型，`{"conv","text"}` 现状形态保持兼容）：

| 事件 | 载荷 | 源 |
|---|---|---|
| `delta` | `{run, text}` | RunEvent `text_delta`（reasoning_delta 不进 MVP UI，见 §6.4） |
| `tool` | `{run, id, name, phase: start\|end, ok}` | RunEvent `tool_start`/`tool_end`（折叠形态，args/result 不下发——UI 显示工具名+状态即可；`tool_end.result` 可能很大，MVP 不传） |
| `usage` | `{run, prompt, completion, cache_hit, cost_usd}` | RunEvent `turn_end`；**cost 由服务端算**（webfrontend import `internal/pricing`，含 off-peak tier），客户端不算钱 |
| `reply` | `{run, text, session_id}` | 平面 `Send`（authoritative 最终文本 + session_id） |
| `notice` | `{text}` | 平面指令应答（`/bind` 结果等也走 Send→reply；notice 留给鉴权/连接层提示） |

run id = server-minted uuid（SSE envelope 自带字段；**不是** `Inbound.EventID`——后者现状有意留空，见 §2.2）。客户端按 run 聚合：deltas 累积成流式气泡 → `reply` 到达时替换收口（服务端权威，去重天然成立）。

**补一条 seek 侧 additive 字段**：`agent_start` 事件增加顶层 `model` 字段（jsonLine 注释声明字段集稳定、新增为非破坏——同「result 字段新增永远 OK」的 registry 纪律）。usage 的 $ 计算需要 model；stdout 是输出面不进 prompt，prefix-cache 零影响。

### 2.6 非目标（MVP 明确不做）

- ❌ **WebSocket**——SSE+POST 够用（§2.2）；WS 留给未来若出现「编辑器协作」类需求。
- ❌ **多用户/发言人隔离**——单人自用，token 即边界（D-C5 同款立场）。
- ❌ **交互审批 UI**（权限 y/N 弹窗）——print 模式 `ErrDenied` 工具结果已是默认体验（§6 已验证②）；远程审批卡片是 v2+（对齐父 PRD §8）。
- ❌ **图片粘贴/上传**——vision 输入入口已有三条（TUI/print/ACP），web 入口等真实需求。
- ❌ **markdown 渲染**——MVP 纯文本 + 代码块样式（XSS 面最小，§5.5）；P3 资产面再评估 vendor 单文件渲染器。
- ❌ **改 `pkg/agent` / 平面核心语义**——两条缝（§2.4）之外零改动；这是父 PRD「零核心破坏」的延续。

## 3. ② 分期：MVP → P2 任务面板 → P3 资产面

### 3.1 MVP——单页聊天驱动 seek（增量标注）

起点不是零：c_qucxtu 已交付「起服务 → 浏览器带 `?token=` 打开 → 发消息 → SSE 收回复 → `/state` 绑定状态行」这条最小回路（§1.4）。本文 MVP 范围 = 在其上补四项（✅=已交付、🔵=本文增量）：
1. 🔵 **消息收发流式渲染**：输入框 + 消息列表已交付（textContent-only）；增量=deltas 实时上屏（含工具调用行「running read…」折叠），`reply` 收口。
2. 🔵 **`/bind` 会话切换 UI**：指令纯文本透传已交付（输入框直发即可，平面 M2 全部已有）；增量=快捷按钮（/bind /projects /switch /new /cancel）+ 绑定状态常显（`GET /state` 已交付且页面在用）。
3. 🔵 **状态栏 token/$ 实时**：每次 `turn_end` 更新（tokens 累积 + 服务端算的 $）；`agent_end` 后显示终值 + session_id（短）。
4. ✅ **多 tab 扇出**：SSE hub 广播到全部订阅者已交付（同一 conv 看到同一条流）；增量只是 per-conv 后的隔离语义（M-W1.3 验收项）。

### 3.2 P2——任务面板：编排层 P1 后台子代理的可视化（依赖 feature-orchestration P1）

对齐方式（接口预留，orchestration 落地前不抢先实现其语义）：

- **数据源 = `subagents.jsonl`**（柱 G 已交付的事件源索引；orchestration D1/D3 扩展它而不改形状——background spawn 共享 step 1–8、`notified` 事件 append-only）。P2 面板读它 fold 出状态投影，与 orchestration §2.5「直接复用」条目同源。
- **API 预留**：`GET /subagents?parent=<sid>`（列表，无前缀风格对齐现状路由）+ SSE topic `subagents`（started/终态推送，走既有 `/events` 流）。形状刻意对齐 inspect-rpc §3.1 M12.1 的 `subagent/list`（`{sub_sid, parent_sid, type, status, description, tokens, ts}`）——P3 并入后此 API 就是该方法的面板消费形态，不另起炉灶。
- **summary 单源纪律**（orchestration D5）：索引不带 summary，面板点开详情 = 读子 transcript 末条 assistant（`session/messages` 的 cursor 尾读）。
- **时序**：orchestration P1 未落地前，面板先渲染 sync 子代理事件（柱 G 已有）；background 事件形状落地后零改动接入（`notified` 等新事件类型对 fold 透明）。

### 3.3 P3——资产面：**并入 feature-inspect-rpc 的数据面**（结论）

**结论：并入（吸收），且替代其两部分。** 具体拆到部件：

| inspect-rpc 部件 | 处置 | 理由 |
|---|---|---|
| §3.1 method 集（session/memory/project/stats…）+ §3.5 错误码 + 命名稳定性纪律 | **原样并入本文 P3**（搬运，不改名） | 契约纪律是那份 PRD 的核心价值；method 集设计已对齐「5 个想定 app 视图」回路 |
| §3.3 HTTP+SSE transport | **并入但换宿主**：从「TUI 进程 `--rpc-listen`」迁到 serve 进程（`POST /rpc`，SSE 并入既有 `/events` 流新增 topic——与聊天面同源同鉴权同命名风格，§2.5） | ① serve 常驻，SSE 天然有宿主；TUI 进程挂 HTTP 要求「面板开着=TUI 开着」，而资产浏览的典型场景恰是**TUI 没开时看看昨晚的会话**。② 面板与聊天同 origin，cookie/token/静态资产一套。③ inspect-rpc 自己的 §1 就预言「应用是同一份契约的另一类消费者」——web frontend 就是那个消费者 |
| §4.1 独立 tarball 面板 | **取消（替代）**：静态资产 go:embed 进单二进制 | tarball 的存在理由是「保 CLI 单二进制卖点」；§4 取舍后 embed 净增 ~100–150KB 且守住 CI 体积 gate，用户零安装步骤（下载 tarball→解压→打开 是三步摩擦） |
| stdio transport + §3.2 Registry 重构 | **不动，留在 IDE 线**（`-rpc` 已有用户；`internal/rpc/server.go` 照旧） | 两 transport 共享 method 语义但宿主不同；P3 落地时 method 实现放 transport-agnostic 的位置（`internal/rpc/methods/` 按原计划），stdio/HTTP 各挂各的 |
| M12.1/M12.2（subagent/worktree/checkpoint/cron method） | 保留为 P3/P2 的后续 method 增量 | 依赖面不变 |

**并入后的 caveat（如实记录）**：serve 进程没有 TUI 进程的内存态（live hooks 事件、当前 turn 实时 usage）。P3 资产面全部从盘读：session JSONL（append-only，tail 读不锁，避开 inspect-rpc §3.4 的 RWMutex 改造——那是 TUI 进程内并发读写才需要解的问题；serve 进程与写 session 的 seek 子进程天然是两个进程，文件系统 append 语义 + 行级 parse 已够）、memory 文件、skillstats JSONL。「实时观察正在跑的 TUI 会话」降级为「读已落盘部分」——每个 `turn_end` 后 `saveTurn` 已落盘（main.go runJSON），粒度是 per-turn，对资产浏览足够。

**两份 PRD 的状态处理**：本文 P3 章节即数据面的权威设计延续；feature-inspect-rpc.md 状态头加注「HTTP+SSE transport 与 tarball 面板已被 feature-web-frontend.md P3 吸收/替代，stdio/registry 归 IDE 线」——避免两份活设计打架（本次文档卡顺手落，见提交）。

### 3.4 后续（非本期）

- 资产面只读边界打破（`session/branch` 等写 method）——对齐 inspect-rpc §9，应用阶段再议。
- 远程审批卡片（权限 y/N 进 web UI）——父 PRD §8 的杀手锏项。
- 跨项目聚合视图（inspect-rpc §9 cross-project）。

## 4. ③ 零依赖纪律

### 4.1 后端：纯 stdlib，默认构建

- `net/http` + `embed` + 标准库 JSON/SSE（SSE=chunked response + `text/event-stream`，无第三方库）。`go list -deps` 已核实 net/http 在默认构建依赖树（webfetch 引入），**go.mod 零新增**。
- 无 build tag（§2.1 已论证）；**无新包**——增量落在 `internal/controlplane` 的 `webfrontend*.go` 文件族（§1.4 取舍结论）。

### 4.2 前端：无 node 构建链（取舍记录）

| 选项 | 取舍 |
|---|---|
| **vanilla JS + ES modules + 单 CSS，手写（选）** | MVP 规模（现状 `webfrontend.html` 单文件起步，目标 <100KB 源码；膨胀拆文件也只是纯静态多几个）下无损失；浏览器原生 `import`/`EventSource`/`fetch` 全覆盖需求；零工具链=零版本漂移、CI 只跑 Go |
| 最小构建链（esbuild bundle+minify） | 引入 node 依赖换 minify 体积——运行时 gzip handler（stdlib `compress/gzip`）已把传输体积压到同级；**不引入** |
| vendor 单文件库（marked.min.js 类） | P3 markdown 渲染若需要，走 checked-in vendor（inspect-rpc §4.1 先例）——仍是零构建链，只是仓库里多一个不动的文件 |

CI 守门：repo 根无 `package.json`/`node_modules`/锁文件（脚本 grep 断言）；静态资产解压体积预算进 §8 体积 gate。

### 4.3 go:embed 进单二进制 + 体积 gate + escape hatch

- 现状是单文件 `webfrontend.html` 直 embed（零构建链已成立）；页面膨胀后再拆 `static/` 目录（仍 `//go:embed`，不引入构建链），CSP/gzip 见 §5.5。
- **体积 gate**：CI binary-size diff 报警，净增 **<200KB**（直接继承 inspect-rpc §6.13/§8 的预算口径；net/http server 侧代码 + 资产合计预估 ~100–150KB）。
- **escape hatch（决策点写死）**：资产若因 P2/P3 膨胀突破 gate，降级为 build tag `web`（`go build -tags web` 得 seek-with-web，feishu D-F1 同款先例）——**不回头做 tarball**（tarball 已被 §3.3 否决，理由是安装摩擦而非体积）。

### 4.4 默认不监听公网

- 默认 `127.0.0.1:8790`（`DefaultWebAddr`，**已交付**——固定口让 tailscale 转发/书签可预期，启动行印带 token 的完整 URL）；CLI `--listen` 覆盖是 W1.1 增量（`WebConfig.Addr` 注入点已有）。
- `0.0.0.0`/非 loopback bind：显式 flag 且**强制要求 token**（fail-closed：缺 token 拒启动，对齐 inspect-rpc §3.3 `--rpc-bind` 纪律；W1.1 增量——现状 `Addr` 可经 `WebConfig` 配但无 CLI flag、无此闸）。
- 远程访问的正路是 tailnet 转发（§5.4），不是开 bind。

## 5. ④ 鉴权与安全面

### 5.1 姿势：单 token，对齐 butler `/today` 先例

butler `/today`（2026-11-13 上线，卡 c_clvtsp）验证过的形态，原样对齐：

- **token**（已交付）：hex(24B)（48 字符），serve 首次启动生成，写 `~/.seek/serve/web.token`（目录 0700、文件 0600，同 `env` 文件姿态）；不进 config.json、错误信息不回显。唯一出现处是启动 banner——stderr 印 `web: up: http://127.0.0.1:8790/?token=<t>`（本机可见；「印出来才好点开」是 butler 同款做法；访问日志永不记 token，§5.5）。
- **浏览器入口**（现状）：`http://127.0.0.1:8790/?token=<t>` 直接过鉴权，页面把 token 续在后续 fetch/SSE 的查询参数上。**增量（W1.2）**：`?token=` 首跳 **303 → `/` + HttpOnly cookie + URL 擦净**（地址栏不留 token，不进历史/不进 referer），查询参数/Bearer 形态保留给 curl/脚本面。
- **API/SSE**（已交付）：`?token=` 或 `Authorization: Bearer <t>`，constant-time 比较；错/无 → 401（标准 JSON 错误体）。cookie 是 W1.2 加入的第三种凭证。
- token 轮换：删文件重启即换；多机共享同一 serve 时同理分发。

### 5.2 与 `/today` 的关键差异：本渠道能驱动执行 → 写面加 CSRF 闸

`/today` 只读，cookie 单独够用；web frontend 的 `POST /msg` 会驱动 seek 执行——**cookie 单独不得过写面**（闸本身是 W1.2 增量；现状写面同样必须带 token，只是凭证只有查询参数/Bearer 两种）：

- 写面（POST）二选一：同源 `Origin` 头校验（允许集=自身 origin）**或** `X-Seek-Token` 头。跨站页面即使带上了 cookie（Samesite=Lax 已挡大半 POST），Origin 不匹配即 403。
- 读面（GET 静态/SSE）：cookie 足矣。
- Cookie 属性：`HttpOnly; Secure(若 TLS); SameSite=Lax; Path=/`。

### 5.3 DNS rebinding 与 Host 校验

loopback 服务的标准威胁：攻击者页面把 `attacker.com` 解析到 127.0.0.1。防线：Host 头允许集 = {bind 地址, `localhost[:port]`, `127.0.0.1[:port]`}，不匹配 403。**token 才是真闸**（rebinding 页面拿不到 token），Host 校验是纵深防御。

### 5.4 三种网络场景

| 场景 | 姿势 |
|---|---|
| **本机** | 默认 bind 127.0.0.1，零配置 |
| **tailnet**（推荐远程形态） | 不改 bind；`tailscale serve`/TCP 转发把 tailnet IP 映射到 127.0.0.1:<port>（butler 8787、paseo 6767 两先例）——WireGuard 加密 + token 兜底，无需 TLS 证书 |
| **LAN 明文** | `--listen 0.0.0.0` + 强制 token；**如实告知风险**：明文 HTTP 下 token 可被同网段嗅探/中间人——内网信任假设，或改走 tailnet。文档明示这是下策 |

### 5.5 安全面注意项（列全）

- **token 的权限级**：持 token = 驱动你所有绑定项目上的 seek（等同坐到你电脑前）。分发口径同 butler 机器 token。
- **执行面继承 D-C5**：默认 print-mode deny（能读写项目目录内、不能跑 shell）；per-binding `yolo:true` 慎开——web 渠道 + yolo = 远程任意命令面。
- **XSS**：模型输出与工具结果全部 `textContent` 渲染（无 `innerHTML`）；`Content-Security-Policy: default-src 'self'`（无 inline script/eval）；静态资产同源自供，无外链。
- **SSE 资源**：per-client 连接上限（默认 8/tab 自动重连由浏览器管；hub 满则丢老——对齐 inspect-rpc §4.2 背压策略，UI 提示 resync）；连接随 ctx 关闭（leaktest 验收）。
- **日志**：不打 Authorization 头/cookie/token；访问日志只记 method+path+status。
- **错误面**：401/403 统一 JSON 体，不区分「token 错」与「没带」（不给探测面）；panic recover middleware 不崩 serve。
- **rate limit**：不做（单用户 + 192-bit token（hex 24B，§5.1）暴破不可行；plane 队列天然背压）。如实记录为有意取舍。

## 6. ⑤ 与 ACP 的关系：同为外部驱动面，不复用 acpBackend 会话栈

**结论：web 走平面（M3 子进程执行器），不走 acpBackend 的 in-process 会话栈。**

两条外部驱动面的现状：

| | ACP（`seek acp`，v7 柱 P） | control-plane（`seek serve`） |
|---|---|---|
| 驱动方 | 编辑器（Zed）/ paseo（ACP client） | 任意聊天渠道 / 浏览器 |
| agent 形态 | **in-process**：`acpBackend.ag.Prompt` 事件流直转 | **子进程**：`exec seek -p --json`，稳定 JSONL 契约 |
| 会话数/进程 | **一进程一 session**（MVP，feature-acp backlog ③「多 session」未做） | 多会话并发，per-Conv 串行 + 全局上限 |
| cwd 锚定 | `acp.AnchorSession`：**chdir 整个进程** + 共享 `Policy.Reanchor`（per-session cwd 先例，D-1） | `cmd.Dir=绑定目录`，每消息一进程 |

不复用的理由：

1. **per-session cwd 先例的前提在 web 场景不成立**。`AnchorSession` 靠进程级 chdir + 重锚共享 Policy——「one seek process = one ACP session」才安全。serve 进程同时服务多个 conv、各 binding 不同项目目录，进程级 chdir 会互相串台；要安全就得 per-session 进程，那就回到了子进程形态。
2. **in-process 会破坏平面铁律**。「平面只 exec seek，`pkg/agent`/permission 零改动」（父 PRD §2.1 目标 6）是 control-plane 的立足点；in-process 意味着 serve 进程内跑 agent loop，权限/审批/取消/超时全部重新接线——ACP 路径当前还**没有 per-action 审批门**（feature-acp backlog ①，直通工作区），把这套搬进 serve 是把已知缺口放大到远程。
3. **子进程形态白拿的资产**：D-C5 权限姿势、per-Conv 串行、resume 自愈、全局并发上限、超时 kill 进程树、`--no-cron-tick` 隔离——M2/M3 全部现成（卡片①的要求「复用 M2 会话绑定路由与 M3 执行器」正是此意）。
4. **spawn 开销可接受**：每消息一个 seek 进程（config/AGENTS.md/skills 装配 ~几百 ms）相对 LLM turn 时长是噪声；若未来成为体感瓶颈，正路是编排层 P1 成熟后的 in-process 多 agent 面（§9 开放问题），不是现在预支。
5. **真正共享的是「事件流形状」而非代码**：webfrontend 的 SSE 映射对齐 `acpUpdate` 的语义取舍（reasoning 不外泄为正文；工具调用 start/in_progress→completed|failed 三态）——作为形状参考写进实现卡的注意事项，不抽公共层（两处各 ~40 行 switch，抽层是伪 DRY）。

## 7. ⑥ dsh web app：参考形态，不抄运行时

dsh-analysis §7：`apps/web`（React 18）+ `packages/web/*` 30+ 个 `ui-*` Cordis 模块，从 `session/event` 渲染驱动 `ctx.agents`。

**借鉴（形态层）**：
- **「从会话事件流渲染」**：dsh web 的 UI 状态=fold(session events)；seek web 的 UI 状态=fold(SSE 流)——`delta/tool/usage/reply` 全是事件，前端无独立状态机。这与 seek 自家的 transcript-first 纪律同构（dsh-analysis §2）。
- **UI 模块按视图切文件**（views/*.js 一视图一文件，ES modules）——对应 dsh 的 ui-conversation/ui-subagent/ui-skill 切分，但用浏览器原生模块而非 Cordis 插件。

**不抄（运行时层）**：React、npm/pnpm 构建链、bundle 部署、Cordis 运行时插件化 UI。dsh-analysis §8.0 两赛道论：dsh 是为「托管服务+编辑器集成」优化的（web app 指向自家云），seek 是本地单人 CLI——**单二进制零依赖是赛道底线**，UI 运行时重链与它互斥。dsh 的 UI 模块化是 Cordis 框架强制的（everything is a plugin），不是 UI 需求本身——vanilla JS 在 seek 的规模下无损失。

## 8. 测试 bar（失败路径优先，全部沙盒可验证）

httptest + fake seek 桩（脚本吐分批 JSONL 行）+ 真实 controlplane 组件（Router/Executor 用真实现，seek 二进制用桩）——**零真实 API key**。**基线**：`webfrontend_test.go` 现有 8 条（页面 embed / token 必需 / Inbound 推送 / 坏 body / SSE 回复 / 无订阅者丢弃 / `/state` 绑定 / Run 生命周期）全程零回归，是每张 W 卡验收的一部分：

- **鉴权全路径**（前半已交付有测试，后半 W1.2 增量）：无 token 401 / 错 token 401 / Bearer 过 / `?token=` 过（既有家族）∥ cookie 过 / `?token=` 303+Set-Cookie+Location 无 token / 写面无 Origin 无 X-Token 头 403 / Host 不匹配 403 / `0.0.0.0` bind 缺 token 拒启动。
- **流式**：桩 seek 分批吐行（含延迟）→ SSE 按序收到 delta/tool/usage/reply；进程 kill（/cancel）→ 流以错误事件收口不挂起。
- **SSE 生命周期**：客户端断开 → hub 注销 + goroutine 退出（leaktest）；多 tab 扇出一致；hub 满丢老不阻塞 executor。
- **并发**（`-race`）：同 conv 两 POST 串行（复用 M3 语义，回归）；不同 conv 并行；emit 回调在 executor goroutine 上调用而 SendStream 在 hub 锁内——锁序测试。
- **malformed**：坏 JSON 行跳过（复用 parseSeekJSONL 语义在流式版的保持）；超长行截断；POST body 非 JSON 400 不崩。
- **Run 委托回归**：`Run(req)` 与 `RunStream(ctx, req, nil)` 结果字节一致；现有 executor/router 测试零修改通过。
- **持久化**：serve 重启后同 conv-uuid 续接（bindings 表 round-trip，复用既有测试面 + web 层一条 e2e）。
- **embed**：静态资产 hash 进测试（防手改部署态漂移）；CSP 头存在；无外链断言。
- **体积 gate**：CI binary-size diff <200KB；repo 无 package.json。

## 9. 实现计划（2h 卡粒度）

> 依赖链：W0（平面缝）→ W1/W2（MVP，可两条线并行：W1 后端、W2 前端在 W1.3 后进场）→ W3（P2，依赖 orchestration P1 落地）→ W4（P3，只依赖 W1 骨架）。每卡独立可验收。

### Phase 0 — 平面缝（前置，~3 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W0.1** | executor `RunStream` + 导出 `RunEvent`（bufio 行级读、emit 逐行、末态聚合/自愈不变；`Run` 委托 `RunStream(nil)`） | 流式单测（桩分批吐行，emit 序列断言）；现有 executor 测试零修改绿 |
| **W0.2** | `StreamSender` sibling 接口 + router upcast 接线（命中走 RunStream→SendStream，未命中字节不变） | fake frontend 回归绿；fake StreamSender 录制断言事件序 |
| **W0.3** | `agent_start` 加 `model` 字段（cmd/seek jsonLine，additive）+ 单测 | `--json` 现有消费者（executor parse）零感知；schema 注释更新 |

### Phase MVP-后端（W1，~5 卡；全部= 在 webfrontend.go 之上就地演进，§1.4）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W1.1** | 配置面：servecli 构造器携带 config（`--listen` 覆盖 `DefaultWebAddr=127.0.0.1:8790`、token 路径可指）+ 非 loopback bind 强制 token 的 fail-closed 闸（§4.4）。**不建新包、不搬家**（§2.1） | 默认口 8790 行为零回归；`0.0.0.0` 缺 token 拒启动；`seek serve --frontend=web` 冒烟；现有测试家族零回归 |
| **W1.2** | 鉴权演进（§5.1–5.3；token 生成/0600/Bearer/constant-time **已交付不动**）：`?token=` 首跳 303 换票 + HttpOnly cookie + URL 擦净、写面 Origin/X-Token 闸、Host 校验 | §8 鉴权增量表驱动测试；现有 token 用例（`TestWeb_MsgRequiresToken` 等）零回归 |
| **W1.3** | per-conv 演进：`X-Seek-Conversation` 头 conv-uuid（D-W3；现状固定 `web:default`）、SSE hub per-conv 订阅（现状全 tab 一张订阅表）、`Send` 按 conv 寻址、`/state` per-conv、存量 `web:default` 绑定兼容 | 双 conv 隔离 + 多 tab 同 conv 扇出一致；leaktest；未鉴权 SSE 401 零回归 |
| **W1.4** | 流式接线（§2.4 缝落地）：router SendStream→`/events` SSE `delta`/`tool`/`usage` 事件族（服务端 pricing 算 $）/run 生命周期；`reply` 收口带 session_id（envelope 自 `{"conv","text"}` 扩展，纯加法） | 桩 seek 流式 e2e（§8 流式条目）；/cancel 收口 |
| **W1.5** | 页面资产管线：沿单文件 `webfrontend.html` 生长（膨胀再拆 static/，§4.3）、CSP 头、gzip handler、CI 检查（无 package.json + 体积 gate） | 资产 hash 测试；`curl /` 得 CSP 头 |

### Phase MVP-前端（W2，依赖 W1.3/W1.5，~4 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W2.1** | 聊天 UI 增量（消息列表/输入框/SSE 自动重连/textContent 渲染**已交付**）：流式气泡 delta 累积→reply 替换、工具行折叠、conv-uuid localStorage | 手测脚本（浏览器）；XSS 用例（模型吐 `<script>` 转义——现状 textContent 纪律保持） |
| **W2.2** | 会话指令 UI：/bind /projects /switch /new /cancel 快捷按钮（纯文本透传**已交付**）+ 绑定状态常显（`GET /state` **已交付**，升级为指令后即时刷新） | 指令 e2e（真 Router + 桩 seek） |
| **W2.3** | 状态栏：turn_end 实时 tokens 累积 + $（usage 事件）+ agent_end 终值/session_id 短显 | 桩 seek 多 turn e2e 断言数值 |
| **W2.4** | 文档收尾：guide-serve.md §web（启动/token/tailnet/安全清单）+ README 一行 + 体积 gate 上线 | 文档 review |

### Phase P2 — 任务面板（依赖 orchestration P1，~3 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W3.1** | `subagents.jsonl` 只读 reader（fold 状态投影）+ `GET /subagents?parent=` | 手造 jsonl 的表驱动测试（含柱 G 现有事件 + D1/D3 新事件形状前瞻） |
| **W3.2** | SSE topic `subagents`（tail-follow + 推送，hub 满丢老） | follow 断开注销测试 |
| **W3.3** | 任务面板 UI（后台子列表/状态/tokens + 详情读子 transcript 末条）+ orchestration 落地后事件形状核对 | e2e：spawn background 子（stub Runner）→面板实时出条目 |

### Phase P3 — 资产面（并入 inspect-rpc 数据面，~5 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W4.1** | `POST /rpc` JSON-RPC 2.0 envelope + method registry（inspect-rpc §3.2 形状）+ 错误码（§3.5） | envelope 表驱动测试；未知 method -32601 |
| **W4.2** | session method 集：`session/list`/`session/get`/`session/messages`（cursor 分页、tail 读不锁） | 分页遍历=全文件顺序（inspect-rpc §6.4 验收口径） |
| **W4.3** | memory/projects/stats method 集（`memory/list|get|projects`、`project/list|get`、`stats/cache`） | 临时 SEEK_HOME 夹具表驱动 |
| **W4.4** | 资产面 UI·一：sessions 浏览器（列表 + 消息 cursor 分页浏览） | 手测脚本；长 transcript 分页不卡（虚拟滚动级量级即可） |
| **W4.5** | 资产面 UI·二：memory 浏览器 + stats 视图（cache/cost） | 手测脚本 + 体积 gate 复核 |
| **W4.6** | hooks/audit 只读 + `hooks/list`；feature-inspect-rpc.md 状态注记核对（本卡 §3.3 已预写） | inspect-rpc §6 对应验收项搬运核对 |

**估时合计**：Phase 0 ~1 天 · MVP ~3 天 · P2 ~1.5 天 · P3 ~3 天。

## 10. 风险表

| 风险 | 缓解 |
|---|---|
| 演进把 MVP 已验证行为改坏（注册/端口/路由/鉴权语义漂移） | 每张 W 卡硬验收=现有 `webfrontend_test.go` 8 条零回归（§1.4）；增量只加文件、不动已交付语义 |
| 流式缝把 executor 改坏（自愈/超时语义回归） | W0.1 `Run` 委托 + 现有测试零修改通过是硬验收；emit 只读不写终态 |
| SSE 在代理/ tailscale serve 下的缓冲（事件攒着不吐） | chunked+flush 每事件立即写；X-Accel-Buffering:no；验收含经 TCP 转发的手测 |
| token 泄露=全项目驱动面 | §5.5 权限级明示；默认非 yolo；轮换路径一行文档 |
| cookie 写面 CSRF 残余面 | Origin/X-Token 双闸 + SameSite=Lax；验收 403 用例 |
| embed 资产膨胀破二进制预算 | §4.3 gate + escape hatch 写死（build tag，不回 tarball） |
| 与 inspect-rpc 双活设计打架 | §3.3 状态注记本次落（inspect-rpc 头部指回本文）；method 名不改，契约单一权威 |
| P2 抢跑 orchestration 语义 | W3 卡显式依赖 P1；W3.1 表驱动用柱 G 现有事件 + 形状前瞻双夹具，P1 落地后核对（W3.3 内含） |
| 多 tab 同 conv 的 Send 与 SendStream 乱序 | hub 单 goroutine per conv 保序（发送序=executor emit 序）；验收含序断言 |
| XSS 经模型输出/工具结果 | textContent-only + CSP 'self' + 无 inline；验收含注入用例 |

## 11. 与已交付面的关系（防重造对照）

| 已交付面 | 本文动作 |
|---|---|
| **web MVP（c_qucxtu，已进 main）** | **吸收/就地演进**（§1.4）：W1/W2 全部增量建立在 `webfrontend.go` 之上，不推倒、不搬家、不重写 |
| control-plane M1–M3/M6a/M7a | **扩展不重写**：两条 sibling 缝（§2.4）之外零平面改动；web frontend 是端口第二个实现（已落地，本文使其完整） |
| feishu（build tag 后的占位） | 并存：注册表现有 `fake`+`web`（c_qucxtu 已落）；feishu 何时启用另议 |
| `-rpc` stdio（IDE 线） | **零改动**；P3 method 实现层与 stdio 共享语义（§3.3 表） |
| ACP（柱 P） | **零改动零共享**；形状参考写进实现卡注意事项（§6.5） |
| 柱 G subagents.jsonl / 柱 K bgjob 语义 | P2 只读消费前者；后者仅作编排 P1 的设计先例引用 |
| webfetch（net/http client 先例） | 依赖树已含 net/http 的实证；无代码复用 |

## 12. 开放问题

- in-process 驱动面（serve 进程内跑 agent，省 spawn）：编排层 P1 后台子代理 + 类型化 agent 成熟后再评估——若做，是「serve 内置一个 ACP-ish 会话栈」的形态，与 §6 结论不冲突（那是进程模型升级，不是 web 渠道的实现细节）。
- reasoning_delta 是否在 UI 折叠展示（TUI 已有 reasoning 流）：MVP 不做，P2 顺手评估。
- SSE 经 tailscale serve 的 TCP 转发长连接稳定性（心跳间隔）：W1.4 手测定，必要时加 `:ping` 注释帧。
- `Sender` 字段何时启用（多用户）：维持父 PRD D-C5 立场，web 单 token 即单人。
