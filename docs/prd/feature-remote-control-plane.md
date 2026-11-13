# Feature: 远程控制平面（Remote Control Plane）· Frontend 端口为脊椎

**所属版本**：独立 feature PRD · 🚀 已部分交付（独立于 v8 主线；与 hackathon pitch 同源叙事）
**前置阅读**：[`vision.md`](vision.md)（§2.3 "手机遥控版"、"持久化守护"——本文是那条线的第一次落地）、[`feature-mobile-push.md`](feature-mobile-push.md)（v6 柱 M·已交付出站 webhook，含 `feishu`/`feishu-flow` format 及其群播局限）、[`feature-routines.md`](feature-routines.md)（v5 柱 H·零 daemon + `triggers/` 文件桥）、[`internal/routines/webhook.go`](../../internal/routines/webhook.go)（出站 POST 现状 + 自定义机器人局限注释 L246–258）、[`internal/paths/paths.go`](../../internal/paths/paths.go)（`ProjectID` L126 = `sha256(absPath)[:16]`、session 平铺 L171）、[`cmd/seek/main.go`](../../cmd/seek/main.go)（`-p`/`-resume`/`-json` 入口 L717–745、`os.Getwd()` 取项目根 L870、`session: <id>` 打印 L2053）、[`v5.md`](v5.md) §"零常驻进程"约束
**状态**：🚀 **M1–M3、M6a、M7a 已交付进 main**（2026-11-12/13 一夜装配线，含 §6 三项契约实测回填）。**M4/M5（飞书 adapter）2026-11-13 用户拍板撤销排期**：不绑定飞书——Frontend 端口已是资产，首个 frontend 选型开放（自建 web/HTTP、Telegram、任意聊天渠道均可按端口后续加，参考 paseo 形态）；本文飞书章节（§2.2/§9 与 D-F 组决策、larkws SDK 实测）保留作参考。其余为设计依据与验收口径。
**预估工作量**：M4/M5 撤销后无剩余排期项；新 frontend 按适配器另估。

> **一句话**：给 seek 加一个**远程控制平面**——一个常驻进程，把"**远程触达 + 常驻在场 + 跨项目调度**"做成 frontend-无关的核心；**任何聊天/消息渠道只要实现一个 `Frontend` 端口就能驱动 seek**。**飞书是第一个 frontend**（长连接收 + IM API 回），但平面本身不绑定飞书。

---

## 0. 命名与降格说明

本文取代上一稿 `feature-feishu-bridge.md`（同会话产出，未提交）。改名理由：脊椎是**平面**，飞书只是**第一个 frontend**。文件、索引、命令、包名全部按这个层级重排：

- 卖点 / 抽象 = **远程控制平面**（frontend-无关）。
- 飞书 = 平面上的**一个适配器**，与未来的 Telegram / Slack / 网页 / 语音 / MCP frontend **平级**。

## 0.1 已确认的产品决策（本次对话敲定）

这三项都落在**平面层**（frontend-无关），不是飞书特有——这正是抽象成立的证据：

| 决策点 | 选择 | 层级 | 影响章节 |
|---|---|---|---|
| **多项目路由** | **会话绑定**：一个会话 `/bind <项目路径>` 绑到一个项目目录；`/projects` 列表、`/switch` 改绑、`/new` 开新会话 | 平面 | §3 D-C2、§4 |
| **平面 ↔ seek 交付方式** | **直连子进程**：`exec seek -p '<msg>' --json [-resume <sid>]`，`cmd.Dir=项目目录`，解析 stdout 回推 | 平面 | §3 D-C3、D-C4 |
| **使用者范围** | **单人自用（v1）**：不区分发言人、无多用户鉴权；团队多人列为未来 | 平面 | §3 D-C5、§7 |

---

## 1. 背景与真实约束（读码后校准）

### 1.1 这是 `vision.md` 里"手机遥控版"那条线的第一次落地

`vision.md` 整篇讲的是**能力纵深轴**（CLI → MCP → 系统应用 → 视觉闭环：seek 能**摸到**电脑多深）。但它在 §2.3 用两句话埋了**另一条正交轴**——**触达与在场轴**："持久化守护：…通知推送"、"系统应用可替换（例如桌面版 vs **手机遥控版**）"。

本平面就是那条轴的第一次具体实现：把 seek 从"**你坐在终端前用的工具**"变成"**随时够得着、还会回到你身边、能统管你全部项目的常驻代理**"。它**不扩展** seek 能干什么，它扩展 seek **从哪被驱动、怎么回到你**。这点必须诚实——中枢感来自**触达**，不来自新权能（见 §2.3 非目标 + §7 风险）。

### 1.2 出站那一半已存在，但**不够用**（驱动了 frontend 抽象）

`internal/routines/webhook.go` 已内置 `feishu` / `feishu-flow` 出站 format，但注释（L246–258）点明：自定义机器人**只能往一个群单向播报**、寻址不到具体会话、签名/关键词机器人会被拒。真正的双向协作必须升级为**自建应用 + IM API**（`im/v1/messages`，`receive_id_type=chat_id`）。

→ 每种渠道的"收/发"机制都**各不相同**（飞书是 WS+IM API、Telegram 是 long-poll/bot API、网页是 WS/SSE…）。这种"核心逻辑一份、渠道接入多份"的形态，**天生就是一个端口 + 多个适配器**——`Frontend` 抽象不是镀金，是渠道差异逼出来的（见 D-C1）。

### 1.3 入站：聊天渠道的收发天然不对称，平面不该假设双工

以飞书为例：长连接（larkws）**只承载下行事件推送**，上行只有心跳 + ACK、**没有发消息帧**；回消息走独立 HTTPS REST。其它渠道也大多是"一个通道收、另一个通道发"。

→ 所以 `Frontend` 端口把**收（`Run` 推 inbound）**和**发（`Send`）**拆成两个方法，不假设一条双工连接（见 D-C1）。

### 1.4 与 seek "零常驻进程"铁律的冲突 —— 正面处理

`v5.md` 明确 seek 核心**零 daemon**（cron 靠 OS scheduler 拉起 tick、跑完即退）。但远程控制平面**必须有进程一直在**（保活 WS / long-poll / 监听）。**解法（D-C1）**：平面是**独立的 long-running serve 进程**，核心 `seek`（agent loop / cron tick）一字不动。常驻只活在显式 `seek serve` 里——对零-daemon 的**有意、隔离的偏离**。

### 1.5 已就绪、可直接复用（不重造）

- **项目身份**：`paths.ProjectID(abs)=sha256(abs)[:16]`，CWD 即项目根（`main.go:870`）。"指定项目" ≡ 把子进程 `cmd.Dir` 设到该目录——路由落到一个目录即可，无需新身份体系。
- **多轮会话**：`-resume <id>`（session 平铺在 `~/.seek/sessions/<id>.jsonl`）+ `--json` 流式事件。平面从首条运行输出抓 session id，后续 `-resume` 续接。
- **配置体系**：`internal/config` 顶层向后兼容（旧 binary 忽略未知 key）。
- **出站健壮性姿态**：webhook.go 的 best-effort + 重试一次 + scheme 校验——各 frontend 的 `Send` 沿用同款姿态（换具体 API）。

---

## 2. 目标与非目标

### 2.1 平面层目标（frontend-无关，v1）

1. 定义并实现 **`Frontend` 端口**（`Run` 收 + `Send` 发 + `Name`），核心路由/绑定/执行/串行**完全不依赖任何具体渠道**。
2. **会话绑定路由**：`/bind <项目路径>` 把会话绑到项目目录；`/projects` `/switch` `/new` `/cancel`。绑定持久化、重启不丢，**键为 frontend-无关的 `ConvID`**。
3. **直连 seek 子进程**：每条消息 → `seek -p '<msg>' --json [-resume <sid>]`（`cmd.Dir=绑定目录`），解析 JSONL 拿最终回复 + session id；**同会话串行、跨会话并行**、全局并发上限。
4. **常驻健壮性**：多 frontend 扇入、优雅退出（drain 队列）、崩溃后绑定/会话映射可恢复。
5. **`seek serve --frontend=<name>`** 命令：选择并拉起一个或多个 frontend，扇入到同一平面核心。
6. **零核心破坏**：`pkg/agent`/`pkg/deepseek`/`permission`/agent loop **一字不动**；平面是叠加 sidecar。

### 2.2 飞书 frontend 目标（v1 唯一实现的适配器）

1. **入站**：larkws 长连接收 `im.message.receive_v1` → 归一化成 `Inbound`；私聊全收、群里 @ 机器人才 `DirectAt=true`；`EventID` 去重；ACK 由 SDK 自动完成；断线用 SDK 内建自动重连（终态 error 时平面重建 Client）。
2. **出站**：`Send` 经自建应用 IM API（`im/v1/messages`，`chat_id` 寻址）回到原会话；`tenant_access_token` 缓存+刷新；长文本分片；best-effort + 重试一次。

### 2.3 非目标（v1 明确不做）

- **不为零个 frontend 造插件系统**（YAGNI）：抽出端口、但**只实现飞书一个**；第二个 frontend 仅在纸面上验证接缝（§6 末），不写代码。这是抽象的**度**——见 §7"过度抽象"风险。
- **不做团队多人鉴权 / per-发言人隔离**（单人自用；多用户列入未来）。
- **不做公网回调 inbound server**（飞书走长连接，不要公网 IP / 隧道）。
- **不复用 `webhook.go` 自定义机器人做回复**（只能群播，寻址不到会话）。
- **不把常驻塞进核心 `seek` agent loop**（保零-daemon）。
- **不做飞书卡片审批 / 流式逐字更新**（v1 跑完一次性发；列为增强，见 §8）。
- **不在 `pkg/deepseek` 引入任何 frontend SDK 依赖**（零-dep 铁律；依赖边界见 D-F1）。
- **平面不"扩展 seek 能力"**——它是远程驱动 + 在场，不是新权能（§1.1）。

---

## 3. 关键设计决策

> 分两组：**D-C**（平面核心，frontend-无关）与 **D-F**（飞书 frontend 特有）。这个分组本身就是降格的体现——把哪条决策放进哪组，决定了它是脊椎还是适配器。

### 平面核心（D-C）

#### D-C1 — `Frontend` 端口：收/发分离，不假设双工

平面核心只跟这个端口对话；具体渠道全在端口背后。沿用 seek 的接口接缝传统（Sink/Notifier 风格）：

```go
// Frontend 是远程控制平面的可插拔接入面。它把某个外部渠道（聊天 App /
// 网页 / 短信 / 语音）变成一串归一化的 inbound 消息，并把平面的回复送回
// 发起会话。平面核心（路由/绑定/执行）frontend-无关；飞书是第一个实现。
type Frontend interface {
    Name() string                                  // "feishu"，用于日志/配置/ConvID 前缀
    Run(ctx context.Context, in chan<- Inbound) error // 阻塞收，自管传输+重连
    Send(ctx context.Context, conv ConvID, r Reply) error // 发一条回复回该会话
}

type ConvID string // "<frontend>:<native-id>"，如 "feishu:oc_abc"——跨 frontend 不撞键

type Inbound struct {
    Conv     ConvID // frontend-无关会话键
    Text     string // 用户消息（指令或 prompt）
    Sender   string // 不透明发言人 id（单人自用 v1 不用）
    EventID  string // 去重用（渠道原生事件 id）
    DirectAt bool   // 该不该触发：私聊 true / 群里 @bot true / 群里闲聊 false
}

type Reply struct {
    Text string // v1 纯文本（由 frontend 自行分片）；未来：卡片/流式
}
```

- **收/发分离**（§1.3）：`Run` 推 inbound、`Send` 发回复，不假设一条双工连接。
- **`ConvID` 带 frontend 前缀**：绑定表、串行队列全以 `ConvID` 为键，天然跨渠道不撞、且未来同一项目可从多个渠道绑。
- **平面持 `map[name]Frontend`**：`Send` 时按 `ConvID` 前缀找回对应 frontend。

#### D-C2 — 多项目路由 = 会话绑定（持久化 bindings 表，frontend-无关）

`ConvID → 项目绝对路径` 是**平面自己的运行态**，不是 seek 的 agent transcript → **允许持久化**（不违反"reconstruct from transcript, don't persist parallel state"：那条约束的是 agent/plan 状态）。

- 存储：`~/.seek/serve/bindings.json`（原子写：temp+rename）。每条 `{conv_id, project_abs_path, session_id, updated_at}`。
- 指令（平面解析 `Inbound.Text`，**所有 frontend 通用**）：`/bind <path>`（校验目录存在，session 留空）、`/projects`、`/switch <path>`（改绑+清 session）、`/new`（留项目+清 session）、`/cancel`（中断当前 run）。未绑会话收普通消息 → 提示先 `/bind`。

#### D-C3 — 直连执行器：抓 session id 续多轮

```
首条:  seek -p '<msg>' --json                 cmd.Dir=<项目目录>
后续:  seek -p '<msg>' --json -resume <sid>
```

- 用 `--json`（JSONL）拿**结构化**最终文本 + session id，不脆弱地 grep stderr。session id 在**末行 `agent_end` 事件的顶层 `session_id` 字段**（§6 已验证①：schema 稳定契约，无需 `--print-session` 补口）。
- 首条成功 → 回写 binding 的 session_id；后续带 `-resume`。`-resume` 续跑返回**同一 id**，且实测 prefix cache 命中（resume 经济性成立）。
- `-resume` 失败 → seek 直接 **exit 1、stdout 空**（不自愈）→ 平面检测非零退出后降级为首条重跑并回写新 id（自愈责任在平面，一次坏 id 不卡死会话）。mid-run error（stdout 有 `error` 行、无 `agent_end`）→ 拿不到 id，弃上下文按首条重开（已接受边界）。

#### D-C4 — 并发：同会话串行、跨会话并行

- **per-ConvID 串行队列**：同 session 同时只能一个 seek 子进程（防 transcript 写冲突 + resume 竞态）。
- **跨 ConvID 并行** + 全局并发上限（config，默认 ~4）。
- 子进程 ctx 可取消（`/cancel` 或超时 kill；v1 先做超时 kill + `/cancel`）。

#### D-C5 — 权限：v1 单人自用，per-binding yolo（默认关），卡片审批留 v2

`seek -p` 无 TUI 交互 y/N → 权限询问无法在远程渠道完成（v1）。print 模式初始 pref 即 `PrefDeny`（§6 已验证②）：危险动作返回 `ErrDenied` **工具结果**（agent 收到后自行调整、不挂起、run 正常结束），默认体验 = **无任何 shell、out-of-CWD 读写挡、in-CWD 写放行**；binding 可显式 `yolo:true`（exec 时加 `--yolo`，全放行——仅信任自己机器时）。**飞书互动卡片审批**是杀手锏但依赖 print 模式可中断等外部审批输入，留 v2（§8）。

### 飞书 frontend（D-F）

#### D-F1 — 依赖边界：frontend SDK 隔离，核心二进制不变重

飞书长连接是 gateway+protobuf 私有协议，自实现成本高 → 用官方 `github.com/larksuite/oapi-sdk-go/v3`（`larkws` 收 + `larkim` 发）。但违反"stdlib first / 单二进制"诉求 → **严格隔离**：把飞书 frontend 编进 **build tag**（`//go:build feishu`）或独立 binary `cmd/seek-serve/`，使**默认 `seek` 构建产物不含任何 frontend SDK、体积不变**。`pkg/deepseek` 零-dep 铁律绝对不破。**每个 frontend 的依赖只随它自己的 build tag 进来**——平面核心仅 stdlib。

#### D-F2 — 出站必须自建应用 + IM API（实现 `Send`）

| | 自定义机器人 webhook（现状） | 自建应用 IM API（飞书 frontend） |
|---|---|---|
| 寻址 | 固定群、单向播报 | 任意 `chat_id`，回私聊/话题 |
| 鉴权 | URL 内嵌 token | `tenant_access_token`（缓存+过期前刷新） |
| 返回 | 无 | `message_id`（卡片增强的基础） |

→ `Send` = `im/v1/messages` POST，`receive_id`=入站 `chat_id`，`msg_type=text`（未来 `post` 富文本）。token 内存缓存带 TTL，过期前主动刷新。

#### D-F3 — 飞书投递语义：去重 + ACK + 触发条件（实现 `Run`）

- 飞书 **at-least-once**：handler 按 `event.Header.EventID` LRU 去重（内存即可）。
- WS handler **快速返回**（重活丢进平面队列异步做）即可——**SDK 自动 ACK**（handler 返回即回写 200/500；返回 error 或 panic 不回 ACK、触发服务端重推，§6 已验证③）。分片消息 SDK 自动合并。
- 触发归一化成 `Inbound.DirectAt`：私聊 = true、群里 @bot = true、群里闲聊 = false。

---

## 4. 架构与数据流

```
 ┌─ frontends（可插拔，各自实现 Frontend 端口；各自的 SDK 随 build tag 进来）─┐
 │                                                                         │
 │  ┌────────────────┐   ┌─ ─ ─ ─ ─ ─ ─┐  ┌─ ─ ─ ─ ─ ─┐  ┌─ ─ ─ ─ ─ ─┐    │
 │  │ feishu (v1)    │   │ telegram     │  │  web      │  │  voice    │    │
 │  │ larkws收/IM发  │   │  (未来)       │  │ (未来)    │  │ (未来)    │    │
 │  └───────┬────────┘   └─ ─ ─ ─ ─ ─ ─┘  └─ ─ ─ ─ ─ ─┘  └─ ─ ─ ─ ─ ─┘    │
 └──────────┼──────────────────────────────────────────────────────────────┘
            │ Run() 推 Inbound        ▲ Send(conv, reply)
            ▼  (扇入到一个 channel)    │
 ┌──────────────────────── 远程控制平面核心（frontend-无关）──────────────────┐
 │  去重(EventID) → DirectAt 过滤 → 指令/路由(ConvID→binding) → per-Conv 串行 │
 │                                                              │           │
 │                                                  exec, cmd.Dir=项目目录    │
 │   ┌──────────────────────────────────────────────────────────▼────────┐  │
 │   │     seek -p '<msg>' --json [-resume <sid>]                         │  │
 │   └──────────────────────────────┬─────────────────────────────────────┘  │
 │   JSONL: 最终文本 + session id     │ 回写 binding.session_id                │
 │   最终文本 → Reply → 按 ConvID 前缀找 frontend → frontend.Send()  ──────────┘
 └────────────────────────────────────────────────────────────────────────┘
   持久化:  ~/.seek/serve/bindings.json   (ConvID ↔ project ↔ session)
            ~/.seek/serve/env             (各 frontend 的密钥，如 FEISHU_APP_*)
```

**一条消息的生命周期**：frontend `Run` 收事件 → 归一化 `Inbound` 扇入 → 去重 → `DirectAt` 过滤 → 平面解析指令或查 `ConvID` 绑定（未绑→提示 `/bind`）→ per-Conv 队列 → 执行器 `exec seek ... -resume <sid>`（`cmd.Dir=项目`）→ 解析 JSONL → 回写 binding → `frontend.Send(conv, reply)` → ACK。

---

## 5. 实现计划

| 里程碑 | 内容 | 层级 | 依赖 | 估时 |
|---|---|---|---|---|
| **M1 平面骨架** | `internal/controlplane/`：`Frontend`/`Inbound`/`Reply`/`ConvID` 定义；config（候选项目/全局并发/默认 yolo）；`paths` 加 `~/.seek/serve/`；bindings store（原子写+重载+损坏修复） | 平面 | — | 1d |
| **M2 路由 + 指令** | `ConvID→binding` 解析、`/bind` `/projects` `/switch` `/new` `/cancel`、未绑提示、`DirectAt` 过滤 | 平面 | M1 | 1d |
| **M3 执行器** | `exec seek -p --json [-resume]`、`cmd.Dir`、JSONL 解析（文本+session id）、per-Conv 串行+跨 Conv 并行+全局上限、超时 kill、`-resume` 自愈 | 平面 | M2 | 1.5d |
| **M4 飞书 frontend·入站** | `larkws` `Run`（内建自动重连：默认无限次、首连抖动≤30s+固定间隔，`Start` 返回终态 error 时平面重建 Client）、`im.message.receive_v1`→`Inbound`、`EventID` 去重、`DirectAt`（ACK 由 SDK 自动完成） | frontend | M1 | 1.5d |
| **M5 飞书 frontend·出站** | `Send` via `larkim`、`tenant_access_token` 缓存+刷新、分片、best-effort+重试一次 | frontend | M4 | 1d |
| **M6 serve 生命周期** | `seek serve --frontend=feishu`、多 frontend 扇入、优雅退出（drain）、build-tag 依赖隔离、launchd/systemd 示例 | 两层 | M2–M5 | 0.5d |
| **M7 测试 + 文档** | §6；`docs/guide-serve.md` + 飞书侧配置清单（§9）+ **纸面验证第二个 frontend 的接缝**（§6 末） | all | all | 1.5d |

> 平面层（M1–M3）与飞书 frontend（M4–M5）**可分头并行**——这正是抽象的红利：核心可用 fake frontend 先跑通，飞书联调不阻塞核心。

---

## 6. 测试 bar（CLAUDE.md 失败路径优先）

平面核心用 **fake Frontend**（注入 `Inbound`、录制 `Send`）+ **fake seek 子进程**（脚本桩吐固定 JSONL）跑，**完全不碰飞书 SDK**——这本身就验证了 frontend-无关：

- **路由**：未绑→提示；`/bind` 后普通消息→进对应项目；`/switch`→换项目清 session；`/new`→留项目清 session。
- **多轮**：首条无 `-resume` 且回写 session id；次条带 `-resume`；`-resume` 失败→自愈重建+回写新 id。
- **并发**（`-race`）：同 Conv 两条快消息→串行（断言第二条在第一条退出后才起）；不同 Conv→并行；超全局上限→排队。
- **去重**：同 `EventID` 重投→只执行一次。
- **DirectAt**：群里闲聊（false）→不触发；私聊/@→触发。
- **malformed**：坏事件 / 缺 `Conv` / 缺文本→WARN 跳过不崩。
- **持久化 round-trip + 恢复**：bindings 写→重载一致；损坏文件→修复重建（参考 `session.Repair`）。
- **取消**：执行器 `ctx.Done`→kill 子进程不泄漏。

飞书 frontend 单测（`//go:build feishu`）：WS 重连退避；token 过期→刷新重发；长文本分片；4xx 不重试 / 5xx 重试一次。

**纸面验证第二个 frontend（不写代码，只证接缝够用）**：在 §7 附一段"Telegram frontend 怎么落"——`Run` 用 long-poll、`Send` 用 sendMessage、`ConvID="telegram:<chat>"`、`DirectAt` = 私聊/群 @。**若这段写不顺，说明端口抽错了**——这是抽象的验收测试。→ **已落（M7a）**：[`guide-serve.md`](../guide-serve.md) §5——写得顺，接缝成立；沉淀两条归一化约定（提及剥离归 frontend、去重统一在平面层），无需改端口。

**已验证项（2026-11-12 实测坐实，方法：worktree 构建二进制 + `SEEK_HOME` 隔离临时目录跑真 API + 读 larkws SDK v3.12.2 源码）**：

**① session id 字段 —— 已存在，`--print-session` 不需要**。`seek -p --json` 的 stdout JSONL 末行 `agent_end` 事件带顶层字段 `session_id`（`cmd/seek/main.go` `jsonLine.SessionID`，schema 注释声明为稳定契约）；人类可读诊断全在 stderr，stdout 保持 parse-clean。实测四条路径：happy path 正常带出；`-resume <sid>` 返回**同一 id**（session 文件复用，且 prefix cache 命中——resume 跑 cache_hit_tokens 12160/12339）；**坏 id**：exit 1、stdout 完全为空、错误只在 stderr（→ 自愈必须由平面自己做：检测非零退出后降级为首条重跑）；**mid-run error**（如 API 认证失败）：stdout 有 `agent_start`/`turn_start`/`error` 三类行但**无 `agent_end`**——若此前已有完成 turn，session 文件已落盘但 id 不上 stdout，平面拿不到 id 只能弃上下文重开（可接受，记为 D-C3 已知边界）。

**② print 模式权限行为 —— 默认 `deny`（非 ask、非被迫 yolo），不挂起**。`-p`/`--json` 模式初始 pref 就是 `PrefDeny`（`main.go` L920 起，注释明说"can't realistically interrupt to ask"），askFn 只在 TUI 路径注册，print 模式下为 nil → `prefGate` 对危险动作直接返回 `ErrDenied` **工具结果**（非致命）：实测 `pwd` 级 bash 也被挡，agent 收到带 hint 的拒绝后自行调整继续完成任务，exit 0、`agent_end` 照常带 session_id。精确挡面（`prefGate`）：**bash 全挡**（ReadOnly 白名单除外）、out-of-CWD 读写挡、memory/skill install 挡；**in-CWD write/edit 默认放行**（不属 dangerous）；`ask_user` → `ErrNoCallback` 工具错误。`--yolo` 实测全放行（bash 正常执行）。→ D-C5 的"默认非 yolo"成立，且远程默认体验 = "能读能写项目目录、不能跑任何 shell"；`/cancel`-级交互能力在 v1 默认模式下天然安全。**实测还撞出一个与平面无关的既有 bug**：相对路径 write 在符号链接 CWD（macOS `/tmp`→`/private/tmp`）下被 `isWithin` 误判 outside（`resolveClosest(".")` 原样返回 + `filepath.Abs` 不解析符号链接，而 root 侧走 `EvalSymlinks`）；模型改绝对路径一次重试即自愈，不阻塞平面，另报主会话处置。

**③ larkws ACK/重连形状（SDK `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`）—— ACK 全自动，重连内建**。ACK：**没有手动 ACK API**——SDK 在事件 handler **同步返回后**自动回写 Response 帧（handler 返回 nil → 200、返回 error → 500 触发服务端重推、panic 被 recover 但**不回 ACK**）→ D-F3 的"handler 快速返回"正确姿势就是 handler 内只做入队即刻 return；分片消息（headers `sum>1`）SDK 自动合并。重连：`autoReconnect` 默认开、次数默认无限（`reconnectCount=-1`）、首连失败随机抖动 ≤30s、此后**固定间隔**（默认 2min，服务端可在连接期下发 `ClientConfig` 覆盖全部四参数）——**不是指数退避**；仅非 `ClientError`（≈4xx）可重试，耗尽/不可重试 → `Start(ctx)` **阻塞返回 error** 且 Client 进入 terminal 状态**不可复用**（平面需重建 Client）。入口形状 `Start(ctx) error` 阻塞 + ctx 取消干净退出 + OnReady/OnReconnecting/OnReconnected/OnDisconnected/OnError 回调——与 `Frontend.Run` 端口一一对应。事件注册：`dispatcher.NewEventDispatcher("", "")`（长连接无需 verification token）+ `OnP2MessageReceiveV1(func(ctx, *larkim.P2MessageReceiveV1) error)`；去重键 `event.Header.EventID`。→ D-F3"按 SDK 要求 ACK"措辞修正为"SDK 自动 ACK"；M4"退避重连"修正为"内建抖动+固定间隔重连（默认即够用），平面仅在 `Start` 返回终态 error 时重建 Client"。

---

## 7. 风险表

| 风险 | 影响 | 缓解 |
|---|---|---|
| **过度抽象 / 为零 frontend 造插件系统** | 镀金、YAGNI、徒增复杂度 | 抽端口但 v1 **只实现飞书一个**；第二个 frontend 仅纸面验证接缝（§6 末）；端口保持最小（3 方法 + 2 结构体） |
| **常驻进程违背零-daemon 立场** | 架构纯洁性争议 | D-C1：隔离在 `seek serve`，核心 agent loop + cron 一字不动；明确标"有意、隔离的偏离" |
| **frontend SDK 拖重核心二进制** | "真·单二进制"卖点受损 | D-F1：build-tag / 独立 binary，默认构建不含任何 SDK；平面核心仅 stdlib |
| **"更像中枢"被误读为"更强大"** | 定位失真 | §1.1：平面是**触达+在场**，非新权能；pitch 措辞守住这条 |
| **print 模式无法交互审批** | 危险动作要么被挡要么被迫 yolo | D-C5：v1 默认非 yolo + per-binding 显式授权；卡片审批 v2 |
| **多用户冒名驱动我机器上的 seek** | 任意群成员能在你机器跑命令 | v1 单人自用、私聊优先；未来：`Sender` allowlist + per-用户 session 隔离 + 危险动作强制卡片审批 |
| **`ConvID` 跨 frontend 撞键** | 串话 | 前缀 `<frontend>:` 强制隔离 |
| **session id 抓取依赖未定 schema** | D-C3 落空 | **已解除**（§6 已验证①）：`agent_end.session_id` 是稳定契约字段，无需 `--print-session`；剩余边界 = mid-run error 无 `agent_end`（弃上下文重开） |
| **token/密钥落盘泄露** | 应用被盗用 | 存 `~/.seek/serve/env`（0600，沿用 cron env overlay 姿态），不进 config.json、不进日志 |

> **与 `vision.md` 的对齐张力（值得记下，不改 v1）**：vision.md 主张"系统应用与 CLI 层**通过 MCP 通信**"。本平面 v1 走 `exec seek` + 共享 `~/.seek/`，**站在 MCP 栈旁边而非栈里**（图简单）。长期，远程控制平面或某个 frontend 可能本身是 MCP-fronted 的——届时 `Frontend` 端口的存在让这种替换是"加一个实现"，而非重写。另：vision.md 称 CLI 为"中枢神经"靠的是确定性+可审计+穿透 SSH/容器；本平面**让 CLI 继续当大脑**（只 exec 它），自己只做远程触手——不违背该底牌。

---

## 8. 未来增强（非 v1）

- **更多 frontend**（端口已就位，各自一个 build tag）：Telegram / Slack / 企业微信 / 网页（WS+SSE）/ 短信 / 语音。
- **飞书互动卡片审批**（D-C5 v2）：把权限 ask 渲染成卡片按钮，点按回传喂回 seek——远程协作的杀手锏。
- **流式更新**：边收 `--json` 事件边更新同一条消息/卡片（"思考中…"→逐步成文）。
- **团队多人**：`Sender` allowlist + per-用户/per-会话 session 隔离 + 审计。
- **富回复**：代码块/diff 用富文本或文件卡片；长输出转附件。
- **反向通知收敛**：cron/trigger 的出站通知（现 `webhook.go` 群播）改走平面的 `Send`，回到"发起该任务的那条会话"。

---

## 9. 飞书开放平台配置清单（用户侧一次性）

1. 创建**企业自建应用**，拿 `App ID` / `App Secret`。
2. 开启**机器人**能力。
3. **事件订阅**：方式选 **"使用长连接接收事件"**（非回调 URL——本机无需公网）；订阅 `im.message.receive_v1`。
4. **权限**：`im:message`（收）+ `im:message:send_as_bot` / `im:message:p2p_msg`（发）。
5. 把机器人**加进协作群**（或私聊机器人）。
6. 本机 `~/.seek/serve/env` 写 `FEISHU_APP_ID` / `FEISHU_APP_SECRET`（0600），`seek serve --frontend=feishu` 启动。
