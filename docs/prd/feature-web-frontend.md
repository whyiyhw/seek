# Feature: Web Frontend——control-plane 首个真实渠道 + inspect-rpc 数据面并入

**所属版本**：独立 feature（平台化线，挂在 remote-control-plane 之下）
**前置阅读**：[`feature-remote-control-plane.md`](feature-remote-control-plane.md)（父设计——Frontend 端口 / M2 路由 / M3 执行器 / D-C1…D-C5 全部本文复用；其 §8「未来增强」第一条「网页（WS+SSE）」即本文）、[`feature-orchestration.md`](feature-orchestration.md)（P2 任务面板对齐对象）、[`feature-inspect-rpc.md`](feature-inspect-rpc.md)（P3 并入评估对象）、[`docs/dsh-analysis.md`](../dsh-analysis.md) §7/§8.0（dsh web app 参考与两赛道论）、[`docs/guide-serve.md`](../guide-serve.md)
**状态**：📐 设计稿（2026-11-13，卡 c_7pptol；同日返工：对齐 c_qucxtu 已交付的 web MVP，全文口径改为「吸收/就地演进」，见 §1.4）；2026-11-19 增补 §13「W5 对齐 dsh web UI」（卡 c_zdkwoa——本组 14 张卡的唯一口径源），§2.6 三项翻案随之生效
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

control-plane 的 M1–M3+M6a/M7a 已进 main：`Frontend` 端口（`internal/controlplane/frontend.go`）、路由与指令（`router.go`）、子进程执行器（`executor.go`）、serve 生命周期（`serve.go`）、CLI 接线（`internal/servecli`）。飞书 adapter（M4/M5）撤销排期后，web MVP（卡 c_qucxtu）作为第一个真实渠道落地并进 main：`frontends` 注册表是 `fake` + `web`，最小聊天回路（发消息、SSE 收回复、绑定状态行、token 鉴权）已跑通——PRD §6 末抽象验收（Telegram 纸面验证）的接缝由真实消费者兑现过一次。

但它止于最小回路（§1.4 逐项边界）：无流式渲染（跑完一次性收）、无 token/$ 状态栏、单固定会话、token 走 URL 查询参数（无 cookie 换票/CSRF 闸）。**本文不是「从零造第一个渠道」，而是「把已落地的 MVP 演进到完整形态」**——流式 / 会话切换 UI / 状态栏，再到只有 web 接得住的可视化面（P2 任务面板 / P3 资产面；聊天渠道里做面板是逆势）。首渠道优势不变：零外部依赖、单人自用天然（本机浏览器）。

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
- **feature-inspect-rpc.md**：规划了 HTTP+SSE transport + 独立 tarball 面板（尚未实现）。本文 §3.3 给出结论：其数据面**并入**本文 P3，transport 宿主从「TUI 进程 `--rpc-listen`」迁到 serve 进程，tarball 面板取消。其 IDE 线（stdio transport / registry 采纳）2026-11-16 关闭归档——`--rpc` stdio 三 method 冻结保留、永不扩；见其状态头。

### 1.4 与 c_qucxtu MVP 的关系（scope 边界——本文的地面基准）

c_qucxtu 已交付进 main：`internal/controlplane/webfrontend.go`（368 行，默认构建、纯 stdlib、无 build tag）+ go:embed 的 `webfrontend.html`（120 行单页）+ `webfrontend_test.go`（362 行 8 条测试）。**本文全部 W 卡是其上的增量演进，不推倒、不另起实现**，每张卡硬验收含「现有测试家族零回归」。逐项边界：

| 维度 | 现状（webfrontend.go，已交付） | 本文增量 |
|---|---|---|
| 代码位置 | `internal/controlplane` 包内三文件（.go/.html/_test.go），同包无 tag=有意决策 | **就地同包生长**，不迁移（结论见下） |
| 注册 | `servecli.frontends["web"] = NewWeb(WebConfig{})`，零值 config 即生产形状 | 构造器携带 config（`--listen`，W1.1） |
| 监听 | `DefaultWebAddr = "127.0.0.1:8790"`；`serve.web_addr` 可改，scope 闸只放行 loopback + `100.64.0.0/10`（2026-11-15）；启动行印 `web: up: http://<addr>/?token=<t>` | 端口仍 8790；W1.1 原 fail-closed 闸已被 scope 闸取代（`0.0.0.0` 拒，§4.4） |
| 路由 | `GET /`（不鉴权）/ `GET /events`（SSE：广播 + 15s 心跳 + `retry:3000`）/ `POST /msg`（`{"text"}`→Inbound，202）/ `GET /state`（`{bound,project,session}`） | 四名沿用；`/events` 增事件类型（W1.4）；`/subagents`、`/rpc` 同无前缀风格（§2.5） |
| 鉴权 | `?token=` 或 `Authorization: Bearer`，constant-time；首启生成 hex(24B) 写 `~/.seek/serve/web.token`（0700/0600） | 303 换票→HttpOnly cookie + URL 擦净、CSRF 闸、Host 校验（W1.2） |
| 会话 | 固定 `web:default`（`webConvNative="default"`，全部 tab 同一会话，D-C5） | conv-uuid per 会话（D-W3，W1.3），旧绑定兼容 |
| 下行 | 单 envelope `{"conv","text"}` 广播全部订阅者，无订阅者即丢 | `delta/tool/usage/reply` 事件族（W1.4）；历史回放 P3 再补 |
| 上游 Inbound | `Conv=web:default`、`EventID` 留空（空 id 永不去重）、`Sender="web"`、`DirectAt=true` | Conv 换 conv-uuid（W1.3） |
| 页面 | `webfrontend.html`：消息列表、输入框（`/bind` 等指令纯文本透传）、`/state` 状态行、SSE 自动重连、textContent-only | 流式气泡 / 指令按钮 / token-$ 状态栏（W2） |

**不迁移**——增量以 `webfrontend*.go` 文件族就地生长：① feishu 子包源于 build tag（SDK 隔离，D-F1），web 纯 stdlib 无此强制力；② 搬家是零行为 churn，与减法纪律相逆；③ 平面类型（`ConvID`/`Inbound`/`Frontend`/`BindingStore`）就在本包，零 import 跳数。重评触发：文件族超 ~5 个或出现第二个 build 维度时一次性 `git mv` 到 `internal/controlplane/webfrontend/`。**原稿顶层 `internal/webfrontend/` 新包方案作废。**

## 2. ① 形态：seek serve + web Frontend

### 2.1 D-W1 — 吸收 c_qucxtu MVP，就地演进（不建新包）

web 是 `Frontend` 端口的第二个实现（fake 之后第一个真实的），主体已由 c_qucxtu 交付（§1.4 清单）。本文全部后端工作 = 在 `internal/controlplane` 包内的 `webfrontend*.go` 文件族上增量生长：

```
internal/controlplane/         # 就地，不迁移（§1.4 取舍结论）
    webfrontend.go             # MVP 主体（已交付）：WebConfig/WebFrontend/四路由/鉴权/token
    webfrontend.html           # 已交付单页（go:embed，零构建链）
    webfrontend_stream.go      # W1.4：SendStream→SSE 事件族（delta/tool/usage/reply）
    webfrontend_conv.go        # W1.3：per-conv hub 演进（现状全 tab 共享 web:default）
    webfrontend_static.go      # W1.5：CSP/gzip（已交付 c_ph2zax；拆 static/ 时沿用）
```

（文件切分是示意——增量怎么落文件由实现卡定，约束只有一条：MVP 三个文件不搬家、不重写、语义不动。）

- **build tag（D-F1）维持不引入**：MVP 头注释已论证——D-F1 的存在理由是 SDK 依赖（larkws），web 适配器纯 stdlib（`net/http` 已在默认构建依赖树，`go:embed` 是编译器内建），没有要隔离的东西，build tag 只会制造第二个构建产物。`buildtags_test.go` 只门 feishu 包（SDK 防泄漏），不与默认构建冲突。
- 注册已在（`servecli.frontends["web"]`，`NewWeb(WebConfig{})` 零值即生产形状）；增量只是让构造器可携带 config——`--listen` 覆盖 `DefaultWebAddr`、token 路径可指（W1.1 落）。

### 2.2 D-W2 — 端口映射：Run=浏览器事件流下行进平面，Send=回复上行回浏览器

Frontend 端口收发分离（D-C1）映射到 HTTP 形状（方向取平面视角：**下行**=推进平面 inbound 队列，**上行**=平面把答复送回渠道）：

| 端口方法 | web 落点 | 说明 |
|---|---|---|
| `Name()` | `"web"`（已交付） | ConvID 前缀、日志、bindings 键 |
| `Run(ctx, in chan<- Inbound)` | **拥有 `http.Server` 生命周期**（已交付）：起 listener、serve 到 ctx.Done、`Close()` 强断——SSE 长连接永不空闲，优雅 `Shutdown` 会永等。`POST /msg` → 归一化 `Inbound` → `in`（已交付，202） | server 即传输层；浏览器侧重连由 EventSource `retry:3000` 自管 |
| `Send(ctx, conv, Reply)` | 广播到该 conv 的 SSE 订阅者（现状全部订阅者同属 `web:default`，per-conv 寻址是 W1.3）；`reply` 携 authoritative 最终文本（现状 envelope `{"conv","text"}`，W1.4 扩） | best-effort：无订阅者不报错（现状**丢弃**）；重开补看需历史回放，P3 面板（session/messages）落地后自然补上 |

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

**为什么 POST 而非 WebSocket 上行**：端口契约不假设双工（§1.3）；SSE 下行 + POST 上行是零依赖的经典组合（EventSource + fetch 皆浏览器原生），鉴权/CSRF 走标准 HTTP 语义（cookie/header），不必在 WS 帧协议里重造。dsh web app 用 WS（§7）——它的 UI 需要双向低延迟（编辑器协作形态），聊天不需要。

### 2.3 D-W3 — ConvID=会话键：从固定 `web:default` 演进到浏览器持有 uuid

- **现状**：固定单会话 `web:default`（`webConvNative = "default"`，所有 tab 同一会话）——单人单会话零状态的刻意取舍。bindings 表本就按 ConvID 键控，`web:default` 是一条普通绑定，演进不需要新存储。
- **演进（W1.3）**：conv-uuid 由浏览器生成（crypto.randomUUID，localStorage 持久），首条消息起随请求携带（`X-Seek-Conversation` 头）。`ConvID = "web:<uuid>"`。
- **服务端零会话状态**（演进后依然成立）：会话↔项目绑定、session_id 续接全在既有 bindings 表（`~/.seek/serve/bindings.json`）——换设备/清 localStorage 只是换一个 conv（旧绑定还在表里，`/bind` 回同一路径即新开）。**web 渠道没有自己的会话存储**。
- UI「新对话」= 新 mint 一个 uuid（等价 `/new` 但连 binding 也换新）；`/bind` `/switch` 沿用平面指令（M2 已有），UI 只是快捷按钮发同款指令文本（§3.1）。
- **兼容**：存量 `web:default` 绑定不迁移不删除——演进后退为「未带头请求」的默认 conv（或一次性收编，W1.3 卡定）；验收含旧绑定可见。

### 2.4 D-W4 — 流式渲染：sibling 接口 + 执行器流式 seam，两个零破坏缝

端口 v1 是「一条 Inbound 一条 Reply」（跑完一次性发）。两条缝全走既有 sibling-interface 纪律（AGENTS.md「Sink interfaces」条；propose 工具四个 sibling 接口先例）：

**缝 1（Frontend 侧）**——可选流式扩展，端口 3 方法不动：

```go
// StreamSender is the OPTIONAL streaming extension of Frontend.
// The router upcasts at the send site; frontends without it keep
// the exact one-reply-per-inbound behaviour.
type StreamSender interface {
    // SendStream forwards one live run event (seek -p --json JSONL
    // contract, §2.5). Best-effort, same posture as Send.
    SendStream(ctx context.Context, conv ConvID, ev RunEvent) error
}
```

**缝 2（执行器侧）**——`Run` 保持字节不变，流式读是新方法：

```go
// RunEvent is one line of the stable `seek -p --json` JSONL contract
// (cmd/seek jsonLine); the field set mirrors jsonLine.
type RunEvent struct { … }

// RunStream is Run with an optional per-line observer. Run delegates:
// Run(req) ≡ RunStream(ctx, req, nil). Observer sees each parseable
// JSONL line as it arrives (line-buffered stdout), before exit.
func (e *Executor) RunStream(ctx context.Context, req ExecRequest, emit func(RunEvent)) (ExecResult, error)
```

现状 `attempt` 用 `lockedBuf` 收完 stdout 再整体 parse；`RunStream` 换成 bufio 行级读 + 逐行 parse + `emit` 回调，末态聚合（sawAgentEnd/sessionID/自愈重跑）不变。**emit 的是完整 jsonLine 契约**（含 `tool_start/tool_end/turn_end/agent_end`），不只 text_delta——状态栏与 P2 面板的共同数据源。

**router 接线**：`Handle` 执行前 upcast `if ss, ok := r.fronts[conv.FrontendName()].(StreamSender); ok`，命中走 `RunStream` 并把事件转 `SendStream`，未命中走 `Run`——**fake/feishu 路径字节不变**。最终 Reply 照旧经 `Send` 下发（流式 bubble 由 `reply` 事件收口替换，客户端按 run 幂等）。

### 2.5 事件与 API 面（沿用 MVP 路由名，增量全部是加法）

```
POST /msg                {text}                  → 202；归一化 Inbound 进平面（已交付）
GET  /events             (SSE)                   → 事件流：现状 {"conv","text"}，W1.4 扩为下表事件族
GET  /                   静态页（embed，不鉴权；已交付）
GET  /state              → 绑定状态 {bound,project,session}（已交付；per-conv 后按 conv 寻址）
GET  /?token=<t>         303 → / + HttpOnly cookie（URL 擦净，§5；已交付 W1.2 c_w5lidm）
```

新端点（P2 的 `/subagents`、P3 的 `/rpc`）沿用同一**无前缀**风格——现状四路由已无前缀，再引入 `/api/` 就是两套并存。

SSE 事件（webfrontend 自有 envelope，`data:` 为 JSON；`delta/tool/usage/reply` 均为 W1.4 在 `/events` 上新增，`{"conv","text"}` 形态保持兼容）：

| 事件 | 载荷 | 源 |
|---|---|---|
| `delta` | `{run, text}` | RunEvent `text_delta`（reasoning_delta 不进 MVP UI，见 §6.4） |
| `tool` | `{run, id, name, phase: start\|end, ok}` | RunEvent `tool_start`/`tool_end`（折叠形态，args/result 不下发——UI 只需工具名+状态；`tool_end.result` 可能很大） |
| `usage` | `{run, prompt, completion, cache_hit, cost_usd}` | RunEvent `turn_end`；**cost 由服务端算**（import `internal/pricing`，含 off-peak tier），客户端不算钱 |
| `reply` | `{run, text, session_id}` | 平面 `Send`（authoritative 最终文本 + session_id） |
| `approval` | `{id, conv, run, kind, summary, command, diff, state, decision}` | 审批 hub（W5 c_n37tnr，`webfrontend_approval.go`）：state=pending 弹卡 / resolved\|cancelled 撤卡回执；决定上行走 `/rpc approval/decide`（不是事件） |
| `notice` | `{text}` | 平面指令应答（`/bind` 结果等也走 Send→reply；notice 留给鉴权/连接层提示） |

run id = server-minted uuid（**不是** `Inbound.EventID`——后者有意留空，见 §2.2）。客户端按 run 聚合：deltas 累积成流式气泡 → `reply` 到达替换收口（服务端权威，去重天然成立）。

**seek 侧 additive 字段**：`agent_start` 增顶层 `model`（jsonLine 字段集稳定、新增为非破坏——同「result 字段新增永远 OK」的 registry 纪律）。usage 的 $ 需要 model；stdout 是输出面不进 prompt，prefix-cache 零影响。

### 2.6 非目标（MVP 明确不做；2026-11-19 W5 增补后口径见各条注，§13）

- ❌ **WebSocket**（维持）——SSE+POST 够用（§2.2）；W5 的新增交互仍是「下行=事件流、上行=POST」（审批决定/附件上传都是 POST、面板数据是 GET+SSE），无双向低延迟帧需求；WS 留给未来的「编辑器协作」类需求（§13.2）。
- ❌ **多用户/发言人隔离**（维持）——单人自用，token 即边界（D-C5）。W5 审批卡片把决定权送到浏览器，但决定者仍是同一 token 持有者——`Sender` 继续休眠（§13.2）。
- ✅ **交互审批 UI**（权限 y/N 弹窗）——**翻案入 W5**（2026-11-19，原列 v2+）：卡 c_n37tnr（§13.1）。print 模式 `ErrDenied` 工具结果仍是默认兜底（§6 ②），远程审批卡片是其上的人机闸——父 PRD §8 杀手锏项的首次落地。
- ✅ **图片粘贴/上传**——**翻案入 W5**（2026-11-19，原「等真实需求」）：卡 c_56yrcj（§13.1）。能力面已就位（modelcap.Vision + `llm.Message.Images`，124b6f4），TUI/print/ACP 三入口已有，web 反而是缺口；`POST /voice` 上传 seam（c_f6ayyg）是现成先例。
- ✅ **markdown 渲染**——**翻案入 W5**（2026-11-19，原「P3 资产面再评估」）：卡 c_x2au4t（§13.1）。最小渲染（围栏+行内码，c_iha3fr）已验证 DOM 纪律可行；vendor 单文件渲染器 ≤25KB 按 §4.2 checked-in 先例进（配额见 §13.3）。
- ❌ **改 `pkg/agent` / 平面核心语义**（维持）——两条缝（§2.4）之外零改动，父 PRD「零核心破坏」的延续。W5 新缝（附件通道、审批回注）沿用 W0 纪律：sibling 接口 + additive 事件字段，fake/feishu 路径字节不变（§13.1）。

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

**结论：并入（吸收），且替代其两部分。** 拆到部件：

| inspect-rpc 部件 | 处置 | 理由 |
|---|---|---|
| §3.1 method 集（session/memory/project/stats…）+ §3.5 错误码 + 命名稳定性纪律 | **原样并入本文 P3**（搬运，不改名） | 契约纪律是那份 PRD 的核心价值；method 集已对齐「5 个想定 app 视图」回路 |
| §3.3 HTTP+SSE transport | **并入但换宿主**：从「TUI 进程 `--rpc-listen`」迁到 serve 进程（`POST /rpc`，SSE 并入既有 `/events` 新 topic——同源同鉴权同命名，§2.5） | ① serve 常驻，SSE 天然有宿主，资产浏览恰是 TUI 没开的场景；② 面板与聊天同 origin，cookie/token/静态资产一套；③ inspect-rpc §1 已预言「应用是同一份契约的另一类消费者」 |
| §4.1 独立 tarball 面板 | **取消（替代）**：静态资产 go:embed 进单二进制 | tarball 只为「保 CLI 单二进制卖点」；embed 净增 ~100–150KB 且守 CI 体积 gate，用户零安装步骤 |
| stdio transport + §3.2 Registry 重构 | **不动；IDE 线已关闭（2026-11-16，c_np5gzu）**——`-rpc` 已有用户故三 method 冻结保留；`internal/rpc/server.go` 照旧、永不挂 registry | method 实现放 transport-agnostic 位置（`internal/rpc/methods/`，已落地）；HTTP 单侧挂载即终态（IDE 驱动归 ACP，见 feature-acp.md） |
| M12.1/M12.2（subagent/worktree/checkpoint/cron method） | 保留为 P3/P2 的后续 method 增量 | 依赖面不变 |

**caveat（如实记录）**：serve 进程没有 TUI 进程的内存态（live hooks 事件、当前 turn 实时 usage）。P3 资产面全部从盘读：session JSONL（append-only，tail 读不锁，避开 inspect-rpc §3.4 的 RWMutex 改造——那是 TUI 进程内并发读写才需要的）、memory 文件、skillstats JSONL。「实时观察正在跑的 TUI 会话」降级为「读已落盘部分」——每个 `turn_end` 后 `saveTurn` 已落盘（main.go runJSON），per-turn 粒度对资产浏览足够。

**两份 PRD 的状态处理**：本文 P3 即数据面的权威设计延续；feature-inspect-rpc.md 状态头加注「HTTP+SSE transport 与 tarball 面板已被 feature-web-frontend.md P3 吸收/替代，stdio/registry 归 IDE 线」，避免两份活设计打架。后记 2026-11-16（c_np5gzu）：IDE 线关闭，inspect-rpc.md 转入归档态，数据面权威独归本文 P3。

### 3.4 后续（非本期）

- 资产面只读边界打破（`session/branch` 等写 method）——对齐 inspect-rpc §9，应用阶段再议。（注：W5 c_n37tnr 的 `approval/decide` 已开第一个**交互控制面**写 method——它不是资产面写，是审批回执；资产面只读边界不变。）
- ~~远程审批卡片（权限 y/N 进 web UI）——父 PRD §8 的杀手锏项。~~ **W5 已落地全链**（c_n37tnr，2026-11-20）：web 半边（SSE `approval` 事件族 + 卡片 UI + `/rpc approval/decide|list` + 顶栏未决角标）+ **子进程半边**（`seek -p … --json --approval-stdin`：askFn → `approval_request` jsonLine → stdin 决定行回读；平面 `ApprovalRouter` 缝把 hub 的决定写回子进程 stdin，run 终止撤卡）。详见 §13.1 c_n37tnr 行。
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

- 默认 `127.0.0.1:8790`（`DefaultWebAddr`，**已交付**——固定口让 tailscale 转发/书签可预期，启动行印带 token 的完整 URL）；CLI `--listen` 覆盖**已交付**（c_jxhkbr——flag 折进 serve 段再进 `WebConfig.Addr`，与 `web_addr` 同一 scope 闸）。
- `web_addr` scope 闸（**已交付**，2026-11-15 安全面拍板，取代原「`0.0.0.0` + 显式 flag + 强制 token」方案）：只放行 loopback（`127.0.0.1`/`::1`/`localhost`）与 tailnet CGNAT `100.64.0.0/10`；`0.0.0.0`、`::`、局域网/公网 IP、任意域名一律启动即拒并整面退出、不回落默认——web 页挂着能驱动 seek 跑绑定项目的 token，暴露面不由配置笔误决定（`checkWebAddr`，guide-serve §5.3）。
- 远程访问的正路是 tailnet 转发（§5.4），不是开 bind。

## 5. ④ 鉴权与安全面

### 5.1 姿势：单 token，对齐 butler `/today` 先例

butler `/today`（2026-11-13 上线，卡 c_clvtsp）验证过的形态，原样对齐：

- **token**（已交付）：hex(24B)（48 字符），serve 首次启动生成，写 `~/.seek/serve/web.token`（目录 0700、文件 0600，同 `env` 文件姿态）；不进 config.json、错误信息不回显。唯一出现处是启动 banner——stderr 印 `web: up: http://127.0.0.1:8790/?token=<t>`（本机可见；「印出来才好点开」是 butler 同款做法；访问日志永不记 token，§5.5）。
- **浏览器入口**（现状）：`http://127.0.0.1:8790/?token=<t>` 直接过鉴权，页面把 token 续在后续 fetch/SSE 的查询参数上。**增量（W1.2，已交付 c_w5lidm）**：`?token=` 首跳 **303 → `/` + HttpOnly cookie + URL 擦净**（地址栏不留 token，不进历史/不进 referer），查询参数/Bearer 形态保留给 curl/脚本面。
- **API/SSE**（已交付）：`?token=` 或 `Authorization: Bearer <t>`，constant-time 比较；错/无 → 401（标准 JSON 错误体）。cookie 是 W1.2 加入的第三种凭证（已交付）。
- token 轮换：删文件重启即换；多机共享同一 serve 时同理分发。

### 5.2 与 `/today` 的关键差异：本渠道能驱动执行 → 写面加 CSRF 闸

`/today` 只读，cookie 单独够用；web frontend 的 `POST /msg` 会驱动 seek 执行——**cookie 单独不得过写面**（闸已交付 W1.2 c_w5lidm：仅对 cookie 凭证生效——查询参数/Bearer 自带 token 即自带 CSRF 免疫，curl 面零感知）：

- 写面（POST）二选一：同源 `Origin` 头校验（允许集=自身 origin）**或** `X-Seek-Token` 头。跨站页面即使带上 cookie（Samesite=Lax 已挡大半 POST），Origin 不匹配即 403。
- **`POST /rpc` 同吃此闸（c_n37tnr，2026-11-20）**。`approval/decide` 是 /rpc 上**第一个状态变更型 method**（y/N/a 移动的是执行放行权），所以「cookie 单独不得驱动执行」必须对这个 route 成立。闸加在 route 级而非逐 method：method 写集是第二份真相表，忘登记那天就腐了。浏览器面零感知（POST 带 Origin、fetch 同源）；query/Bearer 调用方自带 token，与 /msg 同款免闸。
- 读面（GET 静态/SSE）：cookie 足矣。
- Cookie 属性：`HttpOnly; Secure(若 TLS); SameSite=Lax; Path=/`。

### 5.3 DNS rebinding 与 Host 校验

loopback 的标准威胁：攻击者页面把 `attacker.com` 解析到 127.0.0.1。防线：Host 头允许集 = {bind 地址, `localhost[:port]`, `127.0.0.1[:port]`}，不匹配 403。**token 才是真闸**（rebinding 页面拿不到 token），Host 校验是纵深防御。（已交付 c_w5lidm，实现取 host 部分：`localhost` 或 loopback/尾网 CGNAT 任一 **IP 字面量**即过、端口不比对——字面量无法被 rebinding，而严格比对 bind 地址会把 §5.4 的尾网转发姿势（Host=尾网 IP）与 httptest 随机端口一并 403；名字（`attacker.com`、`127.0.0.1.evil.com`）一律拒。）

### 5.4 三种网络场景

| 场景 | 姿势 |
|---|---|
| **本机** | 默认 bind 127.0.0.1，零配置 |
| **tailnet**（推荐远程形态） | 不改 bind；`tailscale serve`/TCP 转发把 tailnet IP 映射到 127.0.0.1:<port>（butler 8787、paseo 6767 两先例）——WireGuard 加密 + token 兜底，无需 TLS 证书 |
| **LAN 明文** | **已作废**（2026-11-15 scope 闸拍板，§4.4）：`0.0.0.0` 一律拒启动，不再有「显式 flag + 强制 token 即可绑」姿势；LAN 访问一律走 tailnet 转发 |

### 5.5 安全面注意项（列全）

- **token 的权限级**：持 token = 驱动你所有绑定项目上的 seek（等同坐到你电脑前）。分发口径同 butler 机器 token。
- **执行面继承 D-C5**：默认 print-mode deny（能读写项目目录内、不能跑 shell）；per-binding `yolo:true` 慎开——web 渠道 + yolo = 远程任意命令面。
- **XSS**：模型输出与工具结果全部 `textContent` 渲染（无 `innerHTML`）；`Content-Security-Policy: default-src 'self'`（无 inline script/eval）；静态资产同源自供，无外链。（W1.5 落地口径：单文件期页面必有内联 script/style，裸 `'self'` 会把页面打死——policy 改为 `default-src 'self'` + `script-src`/`style-src` 各携内联块的 **sha256 hash**，运行时从 embed 字节推导（`webCSP()`），改页面零维护、非出厂脚本一律拒执行；拆 static/ 后内联块消失，自然退回裸 `'self'`。）
- **SSE 资源**：per-client 连接上限（默认 8/tab 自动重连由浏览器管；hub 满则丢老——对齐 inspect-rpc §4.2 背压策略，UI 提示 resync）；连接随 ctx 关闭（leaktest 验收）。
- **日志**：不打 Authorization 头/cookie/token；访问日志只记 method+path+status。
- **错误面**：401/403 统一 JSON 体，不区分「token 错」与「没带」（不给探测面）；panic recover middleware 不崩 serve。
- **rate limit**：不做（单用户 + 192-bit token（hex 24B，§5.1）暴破不可行；plane 队列天然背压）。如实记录为有意取舍。

## 6. ⑤ 与 ACP 的关系：同为外部驱动面，不复用 acpBackend 会话栈

**结论：web 走平面（M3 子进程执行器），不走 acpBackend 的 in-process 会话栈。**

| | ACP（`seek acp`，v7 柱 P） | control-plane（`seek serve`） |
|---|---|---|
| 驱动方 | 编辑器（Zed）/ paseo（ACP client） | 任意聊天渠道 / 浏览器 |
| agent 形态 | **in-process**：`acpBackend.ag.Prompt` 事件流直转 | **子进程**：`exec seek -p --json`，稳定 JSONL 契约 |
| 会话数/进程 | **一进程一 session**（MVP，feature-acp backlog ③ 未做） | 多会话并发，per-Conv 串行 + 全局上限 |
| cwd 锚定 | `acp.AnchorSession`：**chdir 整个进程** + 共享 `Policy.Reanchor`（D-1） | `cmd.Dir=绑定目录`，每消息一进程 |

不复用的理由：

1. **per-session cwd 的前提在 web 不成立**。`AnchorSession` 靠进程级 chdir + 重锚共享 Policy，只有「one seek process = one ACP session」才安全。serve 同时服务多 conv、各 binding 不同目录，进程级 chdir 会串台；要安全就得分进程，即回到子进程形态。
2. **in-process 破坏平面铁律**。「平面只 exec seek，`pkg/agent`/permission 零改动」（父 PRD §2.1 目标 6）是 control-plane 立足点；in-process 要在 serve 内跑 agent loop，权限/审批/取消/超时全得重接线——ACP 路径当前还**没有 per-action 审批门**（feature-acp backlog ①），搬进 serve 是把已知缺口放大到远程。
3. **子进程形态白拿的资产**：D-C5 权限姿势、per-Conv 串行、resume 自愈、全局并发上限、超时 kill 进程树、`--no-cron-tick` 隔离——M2/M3 现成。
4. **spawn 开销可接受**：每消息一进程（config/AGENTS.md/skills 装配 ~几百 ms）相对 LLM turn 是噪声；若成瓶颈，正路是编排层 P1 成熟后的 in-process 多 agent 面（§9 开放问题）。
5. **共享的只是「事件流形状」而非代码**：SSE 映射对齐 `acpUpdate` 语义（reasoning 不外泄为正文；工具调用 start/in_progress→completed|failed 三态）——只作形状参考，不抽公共层（两处各 ~40 行 switch，抽层是伪 DRY）。

## 7. ⑥ dsh web app：参考形态，不抄运行时

dsh-analysis §7：`apps/web`（React 18）+ `packages/web/*` 30+ 个 `ui-*` Cordis 模块，从 `session/event` 渲染驱动 `ctx.agents`。

**借鉴（形态层）**：
- **「从会话事件流渲染」**：dsh web 的 UI 状态=fold(session events)；seek web 的 UI 状态=fold(SSE 流)——`delta/tool/usage/reply` 全是事件，前端无独立状态机。这与 seek 自家的 transcript-first 纪律同构（dsh-analysis §2）。
- **UI 模块按视图切文件**（views/*.js 一视图一文件，ES modules）——对应 dsh 的 ui-conversation/ui-subagent/ui-skill 切分，但用浏览器原生模块而非 Cordis 插件。

**不抄（运行时层）**：React、npm/pnpm 构建链、bundle 部署、Cordis 运行时插件化 UI。dsh-analysis §8.0 两赛道论：dsh 是为「托管服务+编辑器集成」优化的（web app 指向自家云），seek 是本地单人 CLI——**单二进制零依赖是赛道底线**，UI 运行时重链与它互斥。dsh 的 UI 模块化是 Cordis 框架强制的（everything is a plugin），不是 UI 需求本身——vanilla JS 在 seek 的规模下无损失。

## 8. 测试 bar（失败路径优先，全部沙盒可验证）

httptest + fake seek 桩（脚本吐分批 JSONL 行）+ 真实 controlplane 组件（Router/Executor 用真实现，seek 二进制用桩）——**零真实 API key**。**基线**：`webfrontend_test.go` 现有 8 条（页面 embed / token 必需 / Inbound 推送 / 坏 body / SSE 回复 / 无订阅者丢弃 / `/state` 绑定 / Run 生命周期）全程零回归，是每张 W 卡验收的一部分：

- **鉴权全路径**（W1.2 c_w5lidm，`webfrontend_auth_test.go` 表驱动）：无 token 401 / 错 token 401 / Bearer 过 / `?token=` 过（既有家族）∥ cookie 过 / `?token=` 303+Set-Cookie+Location 无 token / 写面无 Origin 无 X-Token 头 403 / `/rpc` 同款（`TestWeb_RPCWriteGate`，c_n37tnr）/ Host 不匹配 403 / `0.0.0.0` bind 拒启动（`TestWeb_AddrScope`/`TestWeb_RunForbiddenAddr`/`TestServePlane_BadWebAddrFails`）。
- **流式**：桩 seek 分批吐行（含延迟）→ SSE 按序收到 delta/tool/usage/reply；进程 kill（/cancel）→ 流以错误事件收口不挂起。
- **SSE 生命周期**：客户端断开 → hub 注销 + goroutine 退出（leaktest）；多 tab 扇出一致；hub 满丢老不阻塞 executor。
- **并发**（`-race`）：同 conv 两 POST 串行（M3 语义回归）；不同 conv 并行；emit 在 executor goroutine 而 SendStream 在 hub 锁内——锁序测试。
- **malformed**：坏 JSON 行跳过（parseSeekJSONL 语义在流式版保持）；超长行截断；POST body 非 JSON 400 不崩。
- **Run 委托回归**：`Run(req)` 与 `RunStream(ctx, req, nil)` 结果字节一致；现有 executor/router 测试零修改通过。
- **持久化**：serve 重启后同 conv-uuid 续接（bindings round-trip + web 层一条 e2e）。
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
| **W1.1** | 配置面：servecli 构造器携带 config（**已交付** `serve.web_addr` b769535；`--listen` **已交付** c_jxhkbr，`effectiveServe` 折 flag > config）+ web_addr scope 闸（**已交付** 76ed883，原「非 loopback 强制 token」被 2026-11-15 取代为限段拒启，§4.4）。不建新包、不搬家（§2.1） | 默认口 8790 零回归（`TestWeb_DefaultAddrPinned`）；`0.0.0.0`/LAN IP 拒启 exit-2（`TestServePlane_BadWebAddrFails`）；`seek serve --frontend=web` 冒烟 |
| **W1.2** | 鉴权演进（§5.1–5.3；token/0600/Bearer/constant-time **已交付不动**）：`?token=` 首跳 303 换票 + HttpOnly cookie + URL 擦净、写面 Origin/X-Token 闸、Host 校验（**已交付** c_w5lidm——`webfrontend_auth.go`：`authKind` cookie 第三凭证、`writeGate` 仅卡 cookie（curl 零感知）、`gateHost` 白名单；页面探测式启动吃 cookie） | §8 鉴权增量表驱动测试；`TestWeb_MsgRequiresToken` 等零回归 |
| **W1.3** | per-conv 演进：`X-Seek-Conversation` 头 conv-uuid（D-W3）、SSE hub per-conv 订阅、`Send` 按 conv 寻址、`/state` per-conv、存量 `web:default` 兼容 | 双 conv 隔离 + 多 tab 同 conv 扇出一致；leaktest；未鉴权 SSE 401 零回归 |
| **W1.4** | 流式接线（§2.4 缝）：router SendStream→`/events` `delta`/`tool`/`usage` 事件族（服务端 pricing 算 $）/run 生命周期；`reply` 带 session_id（**已交付** c_qcmul4——`webfrontend_stream.go`：run id 服务端铸造、named event 不扰旧页 onmessage、usage $ 走 `internal/pricing`、`X-Accel-Buffering:no`+逐事件 flush；`RunEvent` 补 `model`） | 桩 seek 流式 e2e（§8，`TestWeb_StreamE2EEventFamily` 按序+活性+二 run 换 id）；/cancel 收口（`TestWeb_StreamCancelClosesRun`） |
| **W1.5** | 页面资产管线：沿单文件 `webfrontend.html` 生长（膨胀再拆 static/，§4.3）、CSP 头、gzip handler、CI 检查（无 package.json + 体积 gate）（**已交付** c_ph2zax——`webfrontend_static.go`：CSP 以 sha256 钉内联块（§5.5）+ gzip 惰性中间件（SSE 永不压缩）；`scripts/web-gate.sh` + CI `web-gate` job：无 node 链断言 + linux/amd64 对 `scripts/web-size-baseline.txt` 净增 <200KB） | 资产 hash 测试；`curl /` 得 CSP 头 |

### Phase MVP-前端（W2，依赖 W1.3/W1.5，~4 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W2.1** | 聊天 UI 增量：流式气泡 delta 累积→reply 替换、工具行折叠、conv-uuid localStorage（**已交付** c_5xtm74——delta 按 run 累积 + `⚙ read …` 单行折叠行、带 run 的 reply 整泡替换（丢帧回退普通消息），rAF 合帧、贴底跟随；conv-uuid 属 W1.3 不在本卡） | `scripts/web-stream-manual.sh` + `web-stream-headless.mjs`（SSE 族序/§2.5 过滤 + CDP 断言流式态/收口/XSS 不执行/cancel）；XSS 用例 `<script>` 转义（`TestWeb_PageStreamingBubble` + 静态 sink 检查） |
| **W2.2** | 会话指令 UI：/bind /projects /switch /new /cancel 按钮 + 绑定状态常显（`GET /state`）（**已交付** c_evbzyh——零参直发、带参预填，同一 sendText；POST 成功即刷 /state、SSE 重连恢复） | 指令 e2e（`TestWeb_CommandE2E` 真 Router + 桩 seek） |
| **W2.3** | 状态栏：turn_end tokens 累积 + $（usage）+ agent_end 终值/session_id 短显（**已交付** c_npafpi——`#usage` 逐 turn 累计，$ 恒为服务端 cost_usd、客户端不算价；`sid xxxxxxxx`；顺手修 `#status .ok/.warn` 死选择器） | 桩 seek 多 turn e2e 数值断言（`TestWeb_MultiTurnUsageAccumulates` + `TestWeb_PageUsageMeter`） |
| **W2.4** | 文档收尾：guide-serve.md §web（启动/token/tailnet/安全清单）+ README 一行 + 体积 gate 上线（**已交付** c_mn2wdp——guide-serve 新 §5「Web frontend」（76ed883 限段口径保留，Telegram 顺移 §6）；README/README.zh 控制面行刷新；`web-size-baseline` 棘轮至 30246587B——W1.5 钉 30216797 后 W2.1–W4.6 增 29790B） | 文档 review |

### Phase P2 — 任务面板（依赖 orchestration P1，~3 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W3.1** | `subagents.jsonl` 只读 reader（fold 状态投影）+ `GET /subagents?parent=` | 手造 jsonl 的表驱动测试（含柱 G 现有事件 + D1/D3 新事件形状前瞻） |
| **W3.2** | SSE topic `subagents`（tail-follow + 推送，hub 满丢老） | follow 断开注销测试 |
| **W3.3** | 任务面板 UI（后台子列表/状态/tokens + 详情读子 transcript 末条）+ orchestration 落地后事件形状核对（**已交付** c_ujgmpu——面板互斥 + 专属 topic 只听命名事件；`GET /subagents?sub=` 详情 + `subagent.LastAssistant` 尾读；e2e `TestWeb_PanelE2EBackgroundSpawn` 真 Manager(stub Runner)。详情不走 `session/messages`：子 transcript 在项目树不在平铺 store，`?sub=` 是同一只读 reader 的详情面。合并注 2026-11-16：并入 W4.5 四面板 togglePane 闸（`agents-view`），topic 订阅开合挂 `syncTopicStream`） | e2e：spawn background 子（stub Runner）→面板实时出条目 |

### Phase P3 — 资产面（并入 inspect-rpc 数据面，~5 卡）

| 卡 | 内容 | 验收 |
|---|---|---|
| **W4.1** | `POST /rpc` JSON-RPC 2.0 envelope + method registry（inspect-rpc §3.2 形状）+ 错误码（§3.5） | envelope 表驱动测试；未知 method -32601 |
| **W4.2** | session method 集：`session/list`/`session/get`/`session/messages`（cursor 分页、tail 读不锁） | 分页遍历=全文件顺序（inspect-rpc §6.4 验收口径） |
| **W4.3** | memory/projects/stats method 集（`memory/list|get|projects`、`project/list|get`、`stats/cache`） | 临时 SEEK_HOME 夹具表驱动 |
| **W4.4** | 资产面 UI·一：sessions 浏览器（列表 + 消息 cursor 分页）（**已交付** c_hinacy——master-detail：`session/list` 列表面（标题=首条用户消息首行）+ `session/get` 元信息 + `session/messages` next_cursor 50/页「加载更多」，DOM 只装已翻页、工具结果 CSS 限高，textContent-only；c_n63pat 补无头半边 `scripts/web-sessions-headless.mjs`（进面板让位/首页恰 50/翻页 1234 页界连续/切换无串台/XSS 不执行/回聊天零回归）） | `scripts/web-sessions-manual.sh` + `web-sessions-headless.mjs`（curl 分页契约 + 无头全绿，翻页 max 106ms/页）；长 transcript 分页不卡 |
| **W4.5** | 资产面 UI·二：memory 浏览器 + stats 视图（cache/cost）（**已交付** c_wno3o3 / ed9bb04，2026-11-16 进 main：🧠/📊 双面板挂 W4.4 browsing gate（class 隐藏、恰一 pane 点亮，均不卸载）；memory=projects 选择器（LastSeen 新者先）+ 条目列表（📌 pinned / ⚠ stale）+ `memory/get` 走 markdown-lite；stats=session 选择器（复用 W4.2）+ `stats/cache` 卡片（hit/miss、hit_ratio 双色条 width 走 CSSOM、累计 tokens、$）；`rpc/methods/stats.go` additive `cost_usd`（查询时点 tier 估价，客户端不算价，§2.5）；`TestWeb_PageMemoryStats` 钉 method 名/双选择器/cost_usd；gate +12753B 预算内。标注核对 c_zdkwoa 2026-11-19） | `scripts/web-memory-manual.sh` + 体积 gate 复核 |。**W6-7 c_w74tuo（2026-11-28）统一四个资产面板的形状**（记忆 / 统计 / 后台 / 审计）：面板头同 `#conv-head` 款（名 + 条数 | 工具 + 关闭钮），内容进 768 阅读列；记忆 / 后台未选中只有列表，选中后 ≥1200px 列表 320 + 详情并排、更窄时详情替换列表；数据面与 id 不变。
| **W4.6** | hooks/audit 只读 + `hooks/list`；feature-inspect-rpc.md 状态注记核对（本卡 §3.3 已预写）（**已交付** c_igze7m——`internal/rpc/methods/hooks.go`：`hooks/list` 合并 project+user 两层 hooks.toml 按 dispatch 序（只读面不跑 trust gate / bash -n，static-skip 经 audit tail 呈现）；`hooks/audit/tail` 读全局 audit JSONL——`from_ts` RFC3339 截断（坏 ts 保留）、`limit` 缺省 100/上限 1000、`project_id` 经 session header CWD 归属（fork 计入；失联降级不失败）、`follow:true` -32602 拒；inspect-rpc.md 状态头 + §3.1/§6 已按 W4.1–W4.6 逐条注记） | inspect-rpc §6 搬运核对（已核 #2/#4/#8(口径替代)/#9/#10(取消替代)/#13/#14/#15；开放 #1/#3/#5(口径替代)/#6/#7/#11/#12） |

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
| P2 抢跑 orchestration 语义 | W3 卡显式依赖 P1；W3.1 表驱动用柱 G 现有事件 + 形状前瞻双夹具，P1 落地后核对（W3.3 内含；已核对 2026-11-16 c_ujgmpu：真 Manager 事件行与 reader 夹具一致——started 带 background 标记、completed 带 tokens；`notified` 读面就绪、写面仍属未做的 M-O1.3） |
| 多 tab 同 conv 的 Send 与 SendStream 乱序 | hub 单 goroutine per conv 保序（发送序=executor emit 序）；验收含序断言 |
| XSS 经模型输出/工具结果 | textContent-only + CSP 'self' + 无 inline；验收含注入用例 |

## 11. 与已交付面的关系（防重造对照）

| 已交付面 | 本文动作 |
|---|---|
| **web MVP（c_qucxtu，已进 main）** | **吸收/就地演进**（§1.4）：W1/W2 全部增量建立在 `webfrontend.go` 之上，不推倒、不搬家、不重写 |
| control-plane M1–M3/M6a/M7a | **扩展不重写**：两条 sibling 缝（§2.4）之外零平面改动；web frontend 是端口第二个实现（已落地，本文使其完整） |
| feishu（build tag 后的占位） | 并存：注册表现有 `fake`+`web`（c_qucxtu 已落）；feishu 何时启用另议 |
| `-rpc` stdio（IDE 线已关闭 2026-11-16） | **零改动**；stdio 冻结为 3-method 遗留面，P3 method 只挂 /rpc（§3.3 表） |
| ACP（柱 P） | **零改动零共享**；形状参考写进实现卡注意事项（§6.5） |
| 柱 G subagents.jsonl / 柱 K bgjob 语义 | P2 只读消费前者；后者仅作编排 P1 的设计先例引用 |
| webfetch（net/http client 先例） | 依赖树已含 net/http 的实证；无代码复用 |

## 12. 开放问题

- in-process 驱动面（serve 进程内跑 agent，省 spawn）：编排层 P1 后台子代理 + 类型化 agent 成熟后再评估——若做，是「serve 内置一个 ACP-ish 会话栈」的形态，与 §6 结论不冲突（那是进程模型升级，不是 web 渠道的实现细节）。
- reasoning_delta 是否在 UI 折叠展示（TUI 已有 reasoning 流）：MVP 不做，P2 顺手评估。
- SSE 经 tailscale serve 的 TCP 转发长连接稳定性（心跳间隔）：W1.4 手测定，必要时加 `:ping` 注释帧。
- `Sender` 字段何时启用（多用户）：维持父 PRD D-C5 立场，web 单 token 即单人。

## 13. W5——对齐 dsh web UI（2026-11-19 增补；本组 14 张卡的唯一口径源）

> **增补记录**（卡 c_zdkwoa）：W0–W4 全线交付后（§9 表逐卡「已交付」标注），web 渠道进入「向 dsh（deepseek-harness）web UI 的能力面收敛」阶段。本节是本组 **14 张卡**（本 docs 卡 + §13.1 表中 13 张实现卡）的唯一口径源：能力映射（§13.1）、范围翻案（§13.2）、体积预算（§13.3）、W4.5 标注核对（§13.4）、串行序（§13.5）、设计规范双 skill 必读（§13.6）全在本节，后续 web 卡以本节为验收参照——引用本节即自动带上 §13.6 的动工前必读口径，不在各自卡里另立标准；与 §1–§12 冲突处以本节为准（§2.6 已就地翻案对齐）。
>
> dsh 侧包名以本机 clone（`~/code/github/deepseek-harness`，dsh-analysis 同源树）`packages/client/` 实测为准——实测 **38 个 `ui-*` 包**，dsh-analysis §7 只点名其中 13 个（「30+」是对的，点名清单不全），本表一律用实测名。

### 13.1 能力映射表（13 张实现卡 × dsh 对应包 × seek 现状 × W5 增量口径）

seek 现状=W4 完工面（本卡开工时 main）；「精确对应」= dsh 包描述与卡片能力同题。
| **c_x2au4t** markdown 渲染 | `ui-renderer`（slot 装配+应用根）+`ui-chat`（会话节点渲染） | `renderBody`/`appendInline` 最小渲染（围栏+行内码，c_iha3fr）；textContent-only，无 HTML sink（§5.5） | vendor 单文件渲染器 ≤25KB（§4.2）+ static/ 拆分随卡（§4.3，CSP 裸 `'self'`） |
| **c_ux7lvk** 工具调用树 | `ui-tool`（call-tree + tool key slot——**精确对应**） | 单行折叠工具行（W2.1 `⚙ read …`）；`tool` SSE 只带 name/phase/ok（§2.5；`tool_end.result` 可能很大） | args 摘要 + result 截断 + 可展开树；默认单行 |
| **c_5m3fm6** 停止按钮 | `ui-conversation`（composer/queue/view；无独立 stop 包） | `/cancel` 顶栏按钮（W2.2；`TestWeb_StreamCancelClosesRun`） | **已交付 2026-11-20**：「发送」→「停止」（跟 `runs` 表）；半截气泡「—— 已中断 ——」；html +4072B（配额 ≤3KB，超 1KB）；`scripts/web-stop-manual.sh`/`-headless.mjs`；跟进 c_6mbjfq −16B |
| **c_7lsngm** 输入增强 | `ui-input-trigger`（'/'·'@' 触发+候选）+`ui-commands`（命令面——**精确对应**） | 纯文本输入框 + 五指令按钮（W2.2）；无补全无历史 | **已交付 2026-11-20**：`#input` 换 textarea（`grow()`）；`/` 面板（`CMDS`+新增 `/resume`）+ ↑↓/Tab/Esc；`@` 会话（`@<sid>` + `/resume <sid>`，源 `/rpc session/list`）；复用 `#menu`；≤640px 16px；@file 远期（`ui-reference`） |
| **c_56yrcj** 附件上传 | `ui-attachment`（输入区/消息/轨迹三处图片 slot——**精确对应**） | `internal/modelcap.Vision`+`llm.Message.Images` 已就位（124b6f4）；先例 `POST /voice`（c_f6ayyg） | data URL 上行；**平面缝**：Inbound 附件字段（sibling 加法）+ 转发 vision；非图片「不透传」（CHANGELOG） |
| **c_txz6vz** 会话侧栏 | `ui-sidebar`+`ui-session`（**精确对应**） | 四面板 togglePane（W4.4/W4.5/W3.3）；conv-uuid localStorage（W1.3）+ `/new` | **已交付**（drive/c_txz6vz）：可折叠左栏 + `-resume` 续接；后续三卡挂点（§13.5） |
| **c_rqcuaa** 重命名归档 | `ui-session`（`session/title` 事件面）+`ui-sidebar`（树面入口）——dsh 事件溯源 | 无写面；标题派生（W4.4）；JSONL 归子进程 seek | **已交付 2026-11-20**：`/rpc session/rename\|archive` + `session/list` 增 `include_archived`/`archived`；`internal/sessmeta`（`~/.seek/serve/sessions-meta.json` + `sessions-meta-audit.jsonl`，归档幂等）；UI ✏/🗄；三层 Go 测试（-race/幂等/cookie-only 403）+ `scripts/web-rename-manual.sh`/`-headless.mjs`（curl 16 项）；+53096B（超 ≤8KB，~44KB 编译本体；基线 30571003） |
| **c_4doebx** 子代理面板 | `ui-subagent`（目录/续跑路由/@ 引用源） | W3.3 面（c_ujgmpu）+`notified` 读面（写面 M-O1.3 未做） | **已交付 2026-11-20**：徽标三态 + 详情表（`?sub=`）+ 10s ticker/事件双路；`/rpc subagent/list`/`get`（inspect-rpc §6 #12）+`GET /subagents`；html 4.9KB（≤8KB）；多 run 聚合远期 |
| **c_xz2bey** hooks 审计 | **无直接对应**——dsh hooks 即 Cordis 插件（dsh-analysis §1）；参考 `ui-trajectory` | `hooks/list`（两层 hooks.toml 按 dispatch 序）+`hooks/audit/tail`（from_ts/limit/project_id，W4.6 c_igze7m） | **已交付 2026-11-20**（inspect-rpc §6 #6/#7）：`/events` 增 `hooks` topic（`hooksconfig/follow.go`+`webfrontend_hookstopic.go`，`hooksconfig.AuditFollower`；审计行 verbatim（v3 §3.6）、无信封；满丢老同 `pushFollow`/`dropTopicSub`）+ 🪝 `hooks-view`（tail 500；hkFreeze；retry:3000）；`follow:true` 仍 -32602 指路 `/events?topic=hooks`；无 `{"topic":…}` 信封。`webfrontend_hookstopic_test.go` 五测 + `hooksconfig/follow_test.go` 四测 + `-race` + `scripts/web-hooksaudit-manual.sh`/`-headless.mjs` 六景 |
| **c_6xktw4** 统计趋势 | **无对应**（dsh 遥测外发；参考 `ui-trajectory`） | W4.5 stats 单会话快照（c_wno3o3）；`#usage` 本页累计（W2.3） | **已交付 2026-11-20**：`stats/trend`（`{limit?}` 1..100；点 sid/at/model/tokens/cache/`hit_ratio`/`cost_usd`；空=`{"points":[]}` 非 -32001）+ 手绘 SVG 三行图（零 vendor/零 canvas）+ 10/20/50 窗口；`session/list` 加 `cost_usd`；`session.SessionInfo` 增 `Usage` |
| **c_n37tnr** 审批卡片 | `ui-approval`（composer takeover + Remote Event waterfall——**精确对应**）+`ui-user-questions`（远期） | print 模式 `ErrDenied` 即默认体验（§6 ②）；`askFn` 只在 TUI 进程内 | **已交付 2026-11-20（web + 子进程两半）**：web=`webfrontend_approval.go` hub + SSE `approval` 事件族（非 `?topic=`）+ `/rpc approval/decide\|list`（首个状态变更型 method）+ 卡片 UI（y/N/a 对齐 `permission.Policy`）；子进程=`--approval-stdin`（`--json`+`-p`），`initialPref` 单点解析，缝=`ApprovalRouter`（`Route`/`Cancel`，`Frontend` 零改动，对 `ApprovalFrontend` 铸路由；`os.Pipe` 作 stdin，结束 `Cancel()`=`CancelApproval`）；wire：`{"type":"approval_request","req_id":"<子进程铸>","kind":"bash","summary":"…","command":"…","diff":"…"}` → `{"type":"approval_decision","req_id":"…","decision":"y\|n\|a"}`，中止 `{"type":"approval_cancel","req_id":"…"}`；`approval_stdin.go` relay；`scripts/web-approval-manual.sh`/`-headless.mjs`；+~46KB（超 25KB，gate 余 1.25MB） |
| **c_ni7mzw** 主题设置 | `ui-theme`（三态 ThemeRuntime+`--dsw-*` token——**精确对应**） | 单一暗色硬编码 hex（#14161a 等），无变量/主题态 | **已交付 2026-11-20**：`--seek-*` token 两套 + `data-theme` 三态（`system` 跟 `prefers-color-scheme`）；`<head>` bootstrap 防闪烁（sha256 由 `webfrontend_static.go` 现算）；设置**只读**——`/rpc settings/get`（同 `approval/*` 先例；改法=编辑 `~/.seek/config.json`）；增量 `settings/get`/`internal/buildinfo`/`servecli SetSettings`；版本=`VersionString` 迁 `internal/buildinfo`；merge main（2026-11-20）三块裸 hex（`#conn/#ctx`、`#tray/.chip`、`.ag-badge.st-*`）收进 token 层（`--seek-warn-bg`）。**W6-6 c_ekuuof（2026-11-28）把设置面板换成模态框**（遮罩 + 居中对话框，通用 / 服务配置 / 关于三 tab，行式表单）；只读契约与 `/rpc settings/get` 不变 |
| **c_oyp2qf** 顶栏 ContextMeter | **无常驻计量条**；近 `ui-conversation` 的 request/header\|context 面 | `#usage` 会话累计（W2.3）；无 context 占用 | **已交付 2026-11-20（骨架 drive/c_txz6vz）**：末轮 `turn_end` 占用 / 上限 `budget.Limit`；usage 扩 `model`/`lane`/`ctx_limit`；车道=`modelcap.LaneOf`（`pricing.OpenCodeGoModels` 单源）；色阶 0.60/0.75；断线灯 + 重连钮。`scripts/web-ctxmeter-manual.sh`/`-headless.mjs`（26 断言）；+14891B（超 ≤6KB；基线 30517907） |
| **c_lnsdvu** 项目点选 | 无直接对应（dsh 内联 `ui-commands`；复用 `ui-input-trigger` 候源） | /bind /switch 只预填（W2.2）；无只读 fs method | **已交付 2026-11-22（W5 后增卡）**：候选=`/rpc project/list`∪`/rpc settings/get` projects；`#cmd-bind`/`#cmd-projects` 弹层（第三个 `#menu`）→ `/bind <root>`（=D-C2）；**零新 method、零写面、cmdProjects 零改动**；页面钉子（`wireCmd('cmd-bind'…)` banned）+ 无头七景；+8200B；架构页同步不适用（无 docs/architecture.html）。**W5 6e5be518 退役了页面弹层**（左栏工作区树取代），服务端契约与手打指令面保留，无头族 2026-11-27 收窄为指令面三景 |
| **c_heffj3** /bind 目录浏览器（W5 后增补） | `ui-conversation` 的项目/目录路径（dsh 走 filesystem 浏览器；seek 自建） | `internal/rpc/methods` 六族全无 fs 面 | **已交付 2026-11-22**：只读 `fs/list`（`internal/rpc/methods/fs.go`），`{path?, show_hidden?, limit?}` → `{path, parent?, entries:[{name,path,is_dir}], truncated}`，limit 200/上限 1000；根=`project_roots`（`serve.project_roots`，新增）∪`serve.projects`∪`memory.ListProjects()`；根外 -32602 + 逃生口（`~/.seek/config.json`）；页面 `/bind ` 弹「浏览目录…」（复用 `#menu`）。`fs_test.go` 表驱动 + 页面钉子（`rpc('fs/list'`、`sendText('/bind ' + path, false)`、`setBtn(up, 'arrow-up', '上一级')`）+ `scripts/web-dirpick-manual.sh`/`-headless.mjs`；gate 见 §13.3；**不做** `fs/read`/写/删/递归 |
| **c_25d2ey** 图标/按钮体系统一（W5 后补卡，非原 13 张） | 无直接对应——dsh 图标内嵌 `ui-*` 设计语言 | 旧面：emoji 混排、4~5 套按钮样式、裸色点 | **已交付 2026-11-23**：内联 `<symbol>` sprite+`<use>`（vendored Lucide，ISC，沿 2026-11-18 口径），零图标库/CDN（CSP `default-src 'self'`）；三档 `.btn-primary`/soft/`.btn-icon`（26×26，`.sm`=22×22），附件 36×36；`#conn` 8px 圆点+文字标签。`scripts/web-theme-manual.sh` 全绿（sprite≥24 / 无 emoji / 文字≥4.5:1·图标≥3:1；32 截图走 `web-page-review.sh`）+ `go test ./...` + `web-gate.sh`（+4993B） |


### 13.2 范围翻案与维持（§2.6 已就地改注，此处记理由）

**翻案入 W5**（三项）：markdown 渲染（c_x2au4t）、图片/附件上传（c_56yrcj）、审批 UI（c_n37tnr）——各条理由见 §2.6 翻案注，共同背景是 W0–W4 完工后这三项从「等真实需求」变成了「web 渠道与 TUI/print/ACP 的可见能力差」。

**维持非目标**（两项，理由更新到 W5 语境）：

- **WebSocket**：W5 的全部新增交互形状仍是「下行=事件流（SSE，含 topic 化）、上行=POST」——审批决定是一次 POST、附件上传是一次 POST（/voice 先例）、面板数据是 GET+SSE。没有出现需要双向低延迟帧的需求（编辑器协作类），§2.2 的论证原样成立；为 W5 引入 WS 等于为一个不存在的需求重造鉴权/CSRF/背压语义。
- **多用户/发言人隔离**：审批卡片把「决定权」送到了浏览器，但决定者仍是同一单人（token 持有者）——单人自用的 D-C5 立场不变，`Sender` 字段继续休眠。第二个审批者才需要身份隔离，那是 D-C5 的重开条件，不是 W5 的。

### 13.3 体积预算分配（<200KB gate；基线现值 30521979B）

`scripts/web-size-baseline.txt` 现值 **30521979B**（自 c_f6ayyg 202ad58 的 30480715B 起按 delta 追加：c_x2au4t +14109B、c_txz6vz +8192B、c_oyp2qf +14891B、c_5m3fm6 +4072B、c_6mbjfq −16B；W2.4 的 30246587B 与 voice/W4.5/W4.6 增长已并入）。gate（§4.3）=linux/amd64 构建对基线**净增 <200KB**；配额=该卡全部净增上限（含编译产物与 embed 资产，后者按源字节 1:1）：

| 卡 | 配额 | 备注 |
|---|---|---|
| c_x2au4t markdown 渲染 | ≤30KB | 大头先占位：vendor 渲染器 ≤25KB + 接线/样式 ~5KB；static/ 拆分与 embed 骨架计本卡 |
| c_ux7lvk 工具调用树 | ≤15KB | tool 载荷扩展（服务端）+ 树 UI |
| c_5m3fm6 停止按钮 | ≤3KB | 纯 composer UX。**实际 +4072B**（go1.25.8，2026-11-20，页面 CSS/JS/markup ~4.7KB source）；超 ~1KB，按 c_n37tnr/c_oyp2qf 先例吃 W5 包余量；跟进 c_6mbjfq −16B |
| c_7lsngm 输入增强 | ≤10KB | '/' 候选菜单。**实际 ~13KB**（`-trimpath` +13195B，2026-11-20 同尺同路径：页面 12296B + `/resume` 缝 899B）；超 ~3KB，gate 余 1.2MB 未棘轮 |
| c_56yrcj 附件上传 | ≤20KB | 平面缝 + 前端预览 |
| c_txz6vz 会话侧栏 | ≤20KB | 布局骨架 + 会话树 |
| c_rqcuaa 重命名归档 | ≤8KB | 服务端小 method。**实际 ~52.9KB**（go1.25.8，2026-11-20：UI embed ~6.7KB + 两 method/sessmeta store 编译本体 ~44KB，注释非杠杆）；累计 ~127KB/175KB，gate 钉余 ~1.1MB——超在记账 |
| c_4doebx 子代理面板 | ≤8KB | W3.3 面上增量 |
| c_xz2bey hooks 审计 | ≤10KB | 审计面板（服务端 W4.6 已全）。**实际 ~23.7KB**（go1.25.8，2026-11-20：follow.go + hookstopic.go ~14KB + 面板 ~9KB）；累计 ~107KB/175KB，gate ~1.1MB 未棘轮——超在记账 |
| c_6xktw4 统计趋势 | ≤12KB | usage fold + 无依赖图表。**终测 +26 664B**（go1.27.3/linux-amd64，2026-11-21 同树对照 main@1d921d45：31 501 923→31 528 587；首测 +17 520B，session.go→title.go 重构多 ~9KB、卡零新码）：页面三件 ~8.2KB（JS 6.6KB 手绘 SVG、CSS 1.1KB、标记 0.5KB）+ Go 编译本体。超 ~14.7KB——**B 案：超在记账**；基线**不棘轮**（2026-11-22：pin 31 332 461，+169 462B 系 main 35 commits 未棘轮；闸门 196 126B / 204 800B，PASS、余 8 674B，后续合并自行 `--update-baseline`） |
| c_n37tnr 审批卡片 | ≤25KB | 平面缝 + 卡片 UI（本组最重）。**实际 ~46KB**（钉 GOTOOLCHAIN，2026-11-20：注释仅省 200B，余为 hub/RPC/UI 编译本体）；累计 ~60KB/175KB，gate 1.25MB 未棘轮。**子进程半边 +47 683B**（同口径复测 2026-11-20：031f099→29 247 246→29 294 929B；上会话 +52 059B 不可复现，a5b420f 基点 +59 966B，其中 +12 283B 是 031f099 吸收的 c_idisqf/compact_rescue 等无关内容），累计 ~94KB、W5 包 ~108KB/175KB，gate 余 1.2MB |
| c_oyp2qf 顶栏 ContextMeter | ≤6KB | 计量条。**实际 ~14.9KB**（go1.25.8，2026-11-20：页面三件 ~4.4KB + modelcap 车道表/usage 三字段编译本体）；gate ~1.16MB |
| c_heffj3 /bind 目录浏览器（W5 后增补） | 卡面预估页面 3~4KB + Go ~4KB | **实际 +38.0KB**（go1.27.3，2026-11-22：main 269f682a 31557849B → 本分支 31595882B）：页面 embed +7878B（webfrontend.html 161590→169468B）+ `fs.go`/config/servecli 编译本体；超卡面预估，超在记账（c_xz2bey/c_rqcuaa 先例）。**gate 现况**：trip +263421B，其中 +225388B 是 main 既有欠棘轮（c_o3osoh 紧钉 31332461 后主干未记账），本卡 +38KB 在 200KB 内；baseline 只加注释 |
| **合计** | **175KB** | 名义余 25KB（12.5%）。**已交付五卡实测 ≈120KB/175KB**，超配额 ~31KB；包内实余 ~55KB、剩余 8 卡名义配额 86KB——按实测收紧。（200KB 总闸现值 29,395,228B 远低于基线，未棘轮。） |
| c_lnsdvu 项目点选（W5 组后增卡，不入上列合计） | 卡面预估 +3~5KB | **实际 +8200B**（1.27.3 same-path，2026-11-22：页面源 +6193B，无新 Go、零新 method）；超预估 ~1.2~3KB（注释与固定文案占大头），超在记账。gate 另有**先存未棘轮** +237996B（base 269f682a 对 pin 31332461B，c_qjaksw/eval/bash-grep 等合并卡未棘轮）——非本卡所致（web-size-baseline.txt 2026-11-22 注释记了 same-path 测量口径 pitfall） |
| c_25d2ey 图标/按钮体系统一（W5 组后补卡，不入上列合计） | 卡面预估页面 +8~15KB | **实际 +4993B**（205603→210596B：30 枚 Lucide `<symbol>` sprite + 三档按钮 CSS + 动态面接线；无新 Go/method）。页面源 210596B 仍低于 §4.3 的 200KB 触发线。`web-gate.sh` 过：pin 31557490B → built 31696345B = +138855B < 204800B（本卡 ~4.4KB，余为既有未棘轮合并债），只记账。机制预算**不适用**——纯前端资产替换 + 脚本断言扩容 |


参照系：W2.1–W4.6 十卡合计净增 29790B（均 ~3KB/卡）——配额是宽松上限不是目标，**超配额即超 gate**，卡内先做减法（每 minor 一刀的减法纪律）再谈棘轮。源码侧同步约束：`webfrontend.html` 现值 113504B（111KB，c_ni7mzw 交付面：本卡 +16.0KB，另有 merge main 吸收的 c_56yrcj 附件/c_4doebx 子代理/c_oyp2qf 计量三块；static/ 侧 `static/markdown.js` 6267B），static/ 拆分后全部前端源（html+js+css+vendor）合计维持 **<200KB**——超线即触发 §4.3 escape hatch 评审（build tag，不回 tarball）。

### 13.4 W4.5 交付标注核对

§9 W4.5 行此前缺「已交付」标注，本卡核对补齐（详见该行注）：**已交付，c_wno3o3（ed9bb04，2026-11-16 进 main）**——memory/stats 双面板 + `stats/cache` additive `cost_usd`；gate 复核 +12753B 预算内未棘轮。W3.3 合并注「吸收 main 的 W4.5 后任务面板并入其四面板 togglePane 闸」所指即此卡——该合并注与 W4.5 交付自证一致，无悬空引用。W4.5 的浏览器清单人工项按无人值守车道规矩留人工（commit body 记录在案），不属本卡补证范围。

### 13.5 串行序（调度器判序的硬边 + 软序建议）

**硬边（仅两条，其余不预设）**：

1. **c_x2au4t 全组先行**（渲染底座）：消息/面板正文走 `renderBody` 管线——渲染器 + static/ 拆分落地后，后续卡的 DOM 断言才有稳定形状；否则 12 张卡各自绕过渲染器自造格式。
2. **c_txz6vz 先于 c_rqcuaa / c_ni7mzw / c_oyp2qf**（布局骨架）：侧栏改页面骨架，重命名入口挂侧栏、主题 token 随骨架重排、顶栏计量随骨架落位——四卡同骨不同皮。

**其余同文件卡由调度器串行判**（webfrontend.html 与 embed 资产族全组共享，13 张卡实质全同文件冲突）。软序建议（非硬边）：c_ux7lvk 排 c_x2au4t 之后（同改消息 DOM 与 renderBody 出口）；c_4doebx 排 c_txz6vz 之后（面板归侧栏挂点）；平面缝重的 c_56yrcj / c_n37tnr 服务端先行、卡内自含。

### 13.6 设计规范双 skill 必读（2026-11-22 增补，卡 c_xphkui）

**规矩**：动 `webfrontend.html` 或任何 UI 面（static/ 资产、面板、弹层、文案）的卡，**动工前必读**仓内两份设计规范，视觉/交互/文案按其口径：

- **`.seek/skills/frontend-design.md`**（单文件 4.5KB，当普通文档读——非 SKILL.md 布局、无 loader）：管**方向**。先定一个审美方向并贯彻（bold minimalism / editorial / industrial……），反面清单=generic「AI 味」（紫蓝渐变、千篇一律圆角卡片）。
- **`.seek/skills/ui-ux-pro-max/SKILL.md`（含 `data/` 设计数据库：161 套配色、57 组字体搭配、99 条 UX 准则、25 类图表等 CSV）**：管**选型**。配色/字号/间距/交互模式从库里取数。

分工：frontend-design 定 taste、ui-ux-pro-max 供 data；与 §4 零依赖纪律 / §13.3 体积配额冲突时**纪律优先**——规范指导视觉，不解禁 vendor 依赖与体积账。

**机制原因**：`.seek/skills/` 是 **seek 自家运行时**的 skill 目录；drive 派发的 web 会话跑在 zcode/opencode 上，只扫 `~/.agents/skills` 与 `~/.zcode/skills`，**不读仓内 `.seek/skills/`**——规范在仓里 ≠ 会话看得见（用户级拷贝与仓内版本不绑定）。故指路必须写进卡面或本口径源，引用本节即自动带上。教训：W5 组 13+ 张 web 卡全程没读这两份规范（用户 2026-11-22 点破），是页面烂的根因之一；当日在办卡已逐张补「动工前必读」行（todo 态 5 张）。

**「截图过目」与规范的关系——截图是证据，规范是判据**：截图只证明「页面当时长这样」；「这样对不对」拿两份规范核——审美方向是否贯彻、配色/字体有无选型出处、有无踩 AI 味反面清单。看着顺眼 ≠ 过：规范覆盖的维度按规范判，未覆盖的角落才轮到主观判断，写回时说明依据。

**机制预算：不适用**——口径文档，零代码、零新常驻机制；按本节改 UI 的卡照常适用自身机制预算。

### 13.7 审查证据包的视觉趋势计数（`manifest.visual`，2026-11-24 增补，卡 c_nkkyqf）

`scripts/web-page-review.sh` 的证据包里 `manifest.json` 增 `visual{}` 五项：低对比配对数（WCAG 2.2 相对亮度比 <4.5 的「前景 token × 底色 token」，配对表 7×4 写死、亮暗各一套 = 各 28 对）、字号档数、字重档数、`:root` 外 hex 数、圆角档数；后四项是页面源 `<style>` 的去重档位/字面量计数，读法=**逐轮对照趋势**，不看单轮绝对值。
`hex_outside_root` 给两个数：`count` 按卡面字面算法（剥掉 `:root{}` 块再数，今天 **0**——31 个色值全在 token 块），`distinct_in_file` 是全文件去重数（**31**）；两个一起看，避免把 31 误读成「31 处硬编码散落」。
**只记录不拦**：五项是「页面长什么样」的量化旁证，单轮数字不是失败判据（审美/可读性仍由人/审查 agent 看 PNG + 按 §13.6 两份规范判）；退出码只看 32/32 镜头是否拍齐，与 console/navError 同一条「坏页面是审查发现而非脚本失败」口径；采集失败只落 `visual.error`，不折损退出码。
