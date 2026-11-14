# seek serve — 远程控制平面指南 / Remote control plane guide

`seek serve` 起一个**常驻控制平面进程**，把「远程触达 + 常驻在场 + 跨项目调度」做成 frontend-无关的核心：任何聊天/消息渠道只要实现一个 `Frontend` 端口（`Run` 收 + `Send` 发）就能驱动你机器上的 seek。**web 是第一个已交付的 frontend**（内置浏览器聊天页：SSE 流式 + token 认证，`--frontend=web` 即起）；§5 用 Telegram 在纸面上验证了聊天渠道能原样落进同一端口（飞书 adapter 2026-11-13 撤销排期——端口不绑定任何具体渠道）。每条会话可 `/bind` 到一个项目目录，平面在目录里 `exec seek -p … --json` 跑真会话、抓 session id 续多轮，把最终回复送回原会话。

> `seek serve` starts one long-running control-plane process that makes seek reachable from any chat channel and routes each conversation to a bound project directory. Any channel implementing the `Frontend` port (receive via `Run`, reply via `Send`) can drive seek; the bundled web frontend (browser chat over SSE + token auth) is the first shipped implementation, and §5 paper-verifies a Telegram adapter against the same port (the Feishu adapter was descoped 2026-11-13 — the port binds to no specific channel). The plane exec's the real `seek -p … --json` inside the bound directory, captures the session id for multi-turn resume, and ships the final reply back to the conversation it came from.

- 架构与全部设计决策（D-C1…D-F3）：[`docs/prd/feature-remote-control-plane.md`](./prd/feature-remote-control-plane.md)
- Web frontend 设计（MVP 已交付）：[`docs/prd/feature-web-frontend.md`](./prd/feature-web-frontend.md)
- 本文随装配线落地逐步坐实：M1–M3、M6a、M7a 已交付进 main；M4/M5（飞书 adapter）2026-11-13 撤销排期。

---

## 1. 架构 / Architecture

```
 聊天渠道（各自实现 Frontend 端口；SDK 随各自 build tag 进来）
   web (HTTP 收 / SSE 发, 已交付)     feishu / telegram (§5 纸面)     voice (未来)
        │ Run() 推 Inbound（扇入同一 channel）        ▲ Send(conv, reply)
        ▼                                            │
 ┌─────────────────── seek serve 平面核心（frontend-无关，仅 stdlib）──────────────────┐
 │  去重(EventID) → DirectAt 过滤 → 指令/路由(ConvID→binding) → per-Conv 串行队列        │
 │                                       │                                             │
 │        exec: seek -p '<msg>' --json --no-cron-tick [-resume <sid>] [--yolo]          │
 │              cmd.Dir = 绑定的项目目录                                                 │
 │                                       │  JSONL: 最终文本 + 末行 agent_end.session_id │
 │        回写 binding.session_id ──► Reply ──► 按 ConvID 前缀找回 frontend → Send() ───┘
 └─────────────────────────────────────────────────────────────────────────────────────┘
   持久化 ~/.seek/serve/bindings.json（ConvID ↔ 项目 ↔ session，原子写+损坏自愈）
          ~/.seek/serve/env（各 frontend 密钥，0600）
```

**一条消息的生命周期**：frontend `Run` 收到事件 → 归一化成 `Inbound` 扇入 → 平面按 `EventID` 去重 → `DirectAt=false`（群里闲聊）丢弃 → 指令走路由层、普通消息查 `ConvID` 绑定（未绑 → 提示 `/bind`）→ 进 per-Conv 串行队列 → 执行器在绑定目录里 `exec seek` → 解析 JSONL 拿最终文本 + session id → 回写 binding → `frontend.Send()` 回原会话。

平面核心**只跟 `Frontend` 端口对话**；核心 `seek`（agent loop / cron）一字不动，常驻只活在显式启动的 `seek serve` 里——对零-daemon 铁律的一次有意、隔离的偏离。

## 2. 命令 / Commands

### 2.1 启动

```bash
seek serve --frontend=web             # 拉起平面 + web 聊天页（默认只听 127.0.0.1，token 首启生成）
seek serve --frontend=web --frontend=<chat>        # 多 frontend 扇入同一平面
```

聊天渠道密钥写在 `~/.seek/serve/env`（0600），如 `TELEGRAM_BOT_TOKEN`；web frontend 的换票 token 首启自动生成于 `~/.seek/serve/web.token`（0600）。都不进 config.json、不进日志。

### 2.2 会话内指令（所有 frontend 通用，平面解析）

| 指令 | 作用 |
|---|---|
| `/bind <项目路径>` | 把当前会话绑到该目录（校验存在；session 清空） |
| `/projects` | 列出候选项目与当前绑定 |
| `/switch <项目路径>` | 改绑 + 清 session |
| `/new` | 留项目 + 清 session（开新话题） |
| `/cancel` | 中断当前 run（kill 子进程） |

未绑定的会话发普通消息 → 回提示先 `/bind`。

**触发规则（`DirectAt`）**：私聊永远触发；群里只有 @bot（或 `/命令@bot`）触发；群里闲聊不触发（平面丢弃，不回话）。

## 3. 绑定模型 / The binding model

- **`ConvID = "<frontend>:<native-id>"`**（如 `web:1f3a…`、`telegram:-1001234`）：绑定表、串行队列全以它为键，跨渠道天然不撞；native id 自身可含 `:`（首分隔符切分）。
- **存储 `~/.seek/serve/bindings.json`**，每条 `{conv_id, project_abs_path, session_id, yolo, updated_at}`。原子写（temp + rename）；损坏自愈：整文件坏 → 重建为空，单条坏 → 跳过，重复 conv_id → 后写赢。
- **多轮续接**：首条不带 `-resume`，从末行 `agent_end` 事件的顶层 `session_id` 字段抓 id 回写 binding（stdout 保持 parse-clean，诊断全在 stderr）；后续每条带 `-resume <sid>`，返回同一 id 且 prefix cache 命中（远程续聊不亏钱）。
- **自愈**（责任在平面，坏 id 不卡死会话）：`-resume` 失败 → seek 非零退出、stdout 空 → 降级为首条重跑、回写新 id；mid-run error（有输出行但无 `agent_end`）→ 拿不到 id，弃上下文按首条重开（已知边界）。
- **并发**：同一 Conv 串行（防 transcript 写冲突），跨 Conv 并行，全局并发上限默认 4；单次尝试超时默认 10 分钟，超时/`/cancel` kill 整个进程组不泄漏。

## 4. 安全模型 / Security model

- **v1 单人自用**：平面不鉴权发言人——只把机器人拉进**你自己的私聊/可信小群**。多用户（`Sender` allowlist、per-用户隔离）是未来增强；在此之前，能进那个会话的人就能驱动你的机器。
- **执行面 = `seek -p --json`（print 模式）**：初始权限即 `deny` 且不挂起——危险动作返回**拒绝工具结果**（agent 收到后自行调整、run 正常结束），不是死等输入。默认体验：能读、能写**项目目录内**文件；**不能跑任何 shell**（只读白名单除外）、挡 CWD 外读写、挡 memory/skill install。
- **per-binding `yolo:true`**：该会话的子进程加 `--yolo` 全放行。仅在你完全信任触发渠道（私聊、只有你）时开。
- **密钥**：`~/.seek/serve/env`（0600），沿用 cron env overlay 姿态；不进 config.json、不进日志。
- **重投/乱序**：渠道 at-least-once 由平面统一按 `EventID` 去重（frontend 不用各自扫门口，见 §5）。
- **核心零改动**：平面只 `exec seek`，`pkg/agent` / `pkg/deepseek` / permission 一字不动；frontend SDK 全部隔离在各自 build tag 后面，默认构建产物仍是纯 stdlib 单二进制。

## 5. 接第二个 frontend：纸面验证 Telegram / Paper-verifying a second frontend

> PRD §6 末规定的抽象验收测试：**不写代码**，把 Telegram Bot API 逐个接缝点落位；若写不顺 = 端口抽错了。结论先行：**写得顺**，四个接缝点一一落位，没有一处需要改 `Frontend` 端口；过程中沉淀两条**归一化约定**（写进下文，是约定不是改动）。

| 端口接缝 | Telegram 落点 |
|---|---|
| `Name()` | `"telegram"` |
| `Run`（收） | `getUpdates` long-poll（offset 推进，自管重连/退避） |
| `Send`（发） | `sendMessage`（`chat_id` 寻址，4096 分片） |
| `ConvID` | `NewConvID("telegram", itoa(chat.id))` → `telegram:<chat_id>` |
| `EventID` | `update.update_id` |
| `DirectAt` | 私聊（`chat.type=="private"`）true；群 @bot / `/cmd@bot` true；群闲聊 false |
| 密钥 | `TELEGRAM_BOT_TOKEN` → `~/.seek/serve/env` |

### 5.1 Run：getUpdates long-poll

- 主循环 `GET /bot<token>/getUpdates?offset=<o>&timeout=50`（HTTP client Timeout ≥ 60s，ctx.Done 时中断长轮询、`return nil`）。每个响应批量取 `update` 列表，归一化后推 `in`，再推进 `offset = update_id + 1`（内存即可，不持久化）。
- **归一化**：只认 `message` / `edited_message` 且带 `text` 的更新（照片/贴纸 WARN 跳过不崩——malformed 契约）；`EventID = itoa(update_id)`；`Conv`、`DirectAt` 按上表；**`Text` = 剥掉渠道寻址语法后的用户文本**（见约定①）。
- **offset 不持久化的后果**：进程重启后 Telegram 会重投最近一段更新——平面层 `EventID` 去重正着盖住。这**坐实了去重放平面层**（而非各 frontend 自扫门口）的切分是对的（见约定②）。
- **重连是 Run 自己的职责**（端口契约："owns its own transport including reconnects"）：429 读 `retry_after` 睡；5xx/网络错误指数退避重试；启动时若 webhook 占用 `getUpdates`（409 Conflict）先 `deleteWebhook` 一次再进循环；401（token 失效）视为**终态** → `return error`，平面标记 frontend down——与飞书「larkws `Start` 返回终态 error 时平面重建 Client」同形。
- **群 privacy mode 保持默认 ON**：bot 在群里只收得到 @ 它的消息和 `/` 命令，噪音天然小；平面的 `DirectAt` 过滤仍是兜底。顺带红利：`/bind` `/cancel` 天然是 Telegram 原生命令形态，还可用 BotFather `setMyCommands` 注册客户端自动补全。

### 5.2 Send：sendMessage

- `POST /bot<token>/sendMessage`：`chat_id = conv.Native()`、`text = r.Text`、**不带 `parse_mode`**（v1 纯文本，不吃 Markdown 转义坑）。
- 4096 字符上限 → frontend 分片多条发（`Reply` 注释明说 chunking 是 frontend 职责，与飞书同款）。
- best-effort + 失败重试一次（429 尊重 `retry_after`）；再失败丢弃该条、`return error`——绝不能 panic 执行器（端口注释原文）。

### 5.3 纸面结论与两条归一化约定

Telegram 与飞书的收发机制完全不同（long-poll+bot API vs WS+IM API），但四个接缝点全部一一落位、无需改端口——**抽象成立**。写的过程中沉淀两条约定，对任何「群里 @ 才触发」的渠道（含未来任何聊天渠道实现）同样适用：

1. **提及剥离是 frontend 的归一化职责**：`Inbound.Text` 必须是剥完渠道寻址语法（`@botname ` 前缀、`/cmd@botname` 后缀）之后的用户文本——否则路由层的指令前缀匹配会被 `"@mybot /bind …"` 卡住。端口签名不变，这是 `Inbound.Text` 语义的一句文档化澄清。
2. **去重统一在平面层**：渠道重投（Telegram 重启重投、飞书 ACK 丢失重推）由平面按 `EventID` 去重兜住，frontend 不各自建去重——Telegram 的重启重投恰好是这条切分的免费反例验证。
