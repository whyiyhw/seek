# Feature: 移动端通知 webhook 桥（v6 柱 M）

**所属版本**：seek v0.7.0 · v6 柱 M 第三项（柱 I AskUserQuestion v2、柱 J code-review 已 ship）
**前置阅读**：[`v6.md`](v6.md) §3.5 草稿、[`internal/routines/notify.go`](../../internal/routines/notify.go)（`Notifier` 原语）、[`internal/routines/tick.go`](../../internal/routines/tick.go)（`TickOptions` + 派发点 L523）、[`internal/routines/trigger.go`](../../internal/routines/trigger.go)（trigger 派发点 L245）、[`internal/config/config.go`](../../internal/config/config.go)（`Config` 结构）、[`internal/tools/webfetch/validate.go`](../../internal/tools/webfetch/validate.go)（SSRF gate，**对照而非复用**）、[`docs/prd/feature-routines.md`](feature-routines.md)（v5 柱 H）
**状态**：🚀 已交付（v0.7.0 · v6 柱 M）。`internal/routines/webhook.go`（`WebhookDispatcher` sibling + 4 format + 5xx 重试一次 + scheme-only 校验放行私网）+ `TickOptions.Webhook` 接线两派发点 + `config.PushWebhook` + `seek cron config check [--probe]` + `routinescli.WebhookDispatcherFromConfig`（三调用点共用）。webhook + config-check 测试全过，`internal/{routines,routinescli,config}` 全 `-race` 绿，全 repo `go test ./...` 绿。文档见 [`guide-cron.md §4`](../guide-cron.md)。
**预估工作量**：~2 天（与 v6 §3.5 估时一致）

---

## 1. 真实差距（已校 v6 §3.5 的假设）

v6 §3.5 草稿方向正确（webhook 桥、ntfy/slack/discord/raw、不做 native push），但读 `internal/routines` 后发现**三条假设与 ground truth 冲突**，必须先纠正：

| v6 草稿假设 | ground truth | 影响 |
|---|---|---|
| 「`internal/routines/notify.go` 加 webhook dispatcher（与 osascript / notify-send 并列）」 | `Notifier` 是 `func(title, body string) error`（`notify.go:15`）。但 config 的 `events: ["cron.failed",…]` 过滤**需要事件/状态**，而 `(title, body)` 不带。扩 `Notifier` 签名会破坏全部平台实现 + `TickOptions.Notifier` + 所有 stub 测试 | webhook **不能**塞进 `Notifier`。走 CLAUDE.md "可选 sibling" 模式：新 dispatcher 带 `(event, title, body)`，与 OS Notifier **并列**派发（见 D1） |
| 「失败处理：连续失败 5 次后改 DEBUG 级别」 | routines 是**零 daemon**——每次 `seek cron tick` 是**独立进程**（`main.go:1349`）。跨 tick 的「连续失败计数器」需要持久化状态，违背 v5「编排状态 transcript-外存、不加 parallel state」原则 | 运行时节流**跨 tick 无效**。改用 **`seek cron config check` 预检**（配置时验证可达），把错误挡在「依赖它之前」，而非每 tick 静默失败（见 D5） |
| 「拒绝 file:// / **私网 IP** 通过用户配置渗透进来」（套 webfetch SSRF 策略） | webfetch 的 `ValidateIP`（`validate.go:145`）拒私网是防**模型驱动**的 SSRF（模型选 URL → 打内网）。webhook URL 是**用户在自己的 `~/.seek/config.json` 里静态配的**，且 v6 同段把**「自托管」**列为用例（homelab / LAN endpoint） | 对用户配置的 outbound URL 套私网拦截会**误伤合法 LAN push**。应**显式偏离** webfetch gate：只校验 scheme（http/https），**允许**私网/loopback（见 D3） |

**已就绪、可直接复用**（不重造）：
- `Notifier` best-effort 契约（`notify.go:9-14`）+ 派发点失败处理（`tick.go:518-528`：失败 WARN 到 stderr，绝不回滚 `MarkRun`）——webhook 沿用同一姿态。
- `TickOptions` 依赖注入模式（`tick.go:112`，nil → 默认）——加一个可选 `Webhook` 字段，nil = 无 webhook，零破坏。
- 两个派发点已构造好 `(title, status, body)`：cron `tick.go:524`（`"seek cron: <name> (<status>)"`）、trigger `trigger.go:245`（`"seek trigger: <id> (<status>)"`）。
- `config.Config` 顶层字段向后兼容（`config.go:38-40` 注释明确：旧 binary 忽略未知 key）。
- `net/http` stdlib（"stdlib first"，与 webfetch 同源），无新依赖。

## 2. 目标与非目标

### 2.1 目标

1. 通知派发时（cron 终态 / trigger 终态）**额外**把 `(title, body)` 经 HTTPS POST 推到用户配置的 webhook URL；**不替代** OS 桌面通知。
2. 支持 4 种 format：`ntfy`（推荐）/ `slack` / `discord` / `raw`，各自正确的 payload + header 形态。
3. 每个 webhook 可配 `events` 过滤（默认全部）；webhook 触发**独立于** `job.Notify`（见 D4 理由）。
4. best-effort：webhook 失败 → WARN 到 stderr，**绝不**阻塞或回滚 cron run（继承 `Notifier` 契约）。
5. `seek cron config check`：配置时验证 webhook（scheme + URL parse + 可选可达探测），把错误挡在用户依赖它之前。
6. 零破坏：`Notifier` 签名、OS 通知路径、现有 routines 测试全部不动。

### 2.2 非目标

- **不做** native iOS/Android push（要 Apple/Google 推送注册 + 后端中转 + app 发布；月级成本，反 seek 零 daemon/隐私立场）。v6 §3.5 anti-goal。
- **不做** SMS / 邮件（用户可自接 webhook → SMS gateway）。
- **不做** inbound webhook server（这是 outbound only；inbound 走已有的 `triggers/` 文件桥）。
- **不扩 `Notifier` 签名 / 不触 `pkg/agent`·`pkg/deepseek`·`permission`**（v6 §2 跨柱约束）——webhook 是 routines 内的叠加层。
- **不套 webfetch 的 SSRF gate**（D3）——那是模型驱动防护，不适用用户静态配置的 outbound。
- **不做**跨 tick 持久化的失败节流（零 daemon 决定，见 D5）。

## 3. 关键设计决策

### D1 — webhook 走可选 sibling dispatcher，不扩 `Notifier`

`Notifier func(title, body) error` 是被 4 个平台实现 + `TickOptions` + 多个 stub 测试共享的主 contract。`events` 过滤要事件/状态，但 OS 通知不需要。按 CLAUDE.md "Sink interfaces: don't break the main contract — add OPTIONAL sibling"：

```go
// WebhookDispatcher fans (event, title, body) out to all configured
// webhooks (each internally filtered by its events list). Best-effort,
// like Notifier. nil = no webhooks configured.
type WebhookDispatcher func(event, title, body string)
```

`TickOptions` 加可选字段 `Webhook WebhookDispatcher`（nil = skip）。两个派发点在 OS notify **之后**追加一次 webhook 调用：

```go
// tick.go cron 派发点（L523 区域）
title := fmt.Sprintf("seek cron: %s (%s)", job.Name, terminalStatus)
if notify != nil && shouldNotify(job, terminalStatus) {
    if err := notify(title, terminalNote); err != nil { /* WARN, unchanged */ }
}
if webhook != nil {
    webhook("cron."+terminalStatus, title, terminalNote) // 内部按 events 过滤 + best-effort
}
```

trigger.go 同理，event 前缀 `"trigger."`。`Notifier` 一行不改。

### D2 — dispatcher 在 cmd/seek 从 config 构造，经 TickOptions 注入

接线点 `main.go:1349` 现在传 `TickOptions{}`（空 → DefaultNotifier）。改为：读 `config.Load()` 的 `PushWebhooks`，构造 `WebhookDispatcher`（内部持有 `*http.Client` + 解析好的 webhook 列表），塞进 `TickOptions{Webhook: …}`。无配置 → 不构造，字段留 nil。dispatcher 实现放新包 `internal/routines/webhook.go`（与 notify.go 并列，同包，复用 best-effort 措辞）。

### D3 — outbound URL 校验：只管 scheme，**放行私网**（显式偏离 webfetch）

| | webfetch（模型驱动 inbound 风险） | webhook（用户静态配置 outbound） |
|---|---|---|
| URL 来源 | 模型生成 → SSRF 风险 | 用户写进自己的 config.json |
| 私网/loopback | **拒**（防打内网） | **放行**（自托管 / homelab / LAN push 是用例） |
| scheme | https（http 需 opt-in） | http/https，**其余拒**（file://、gopher:// 等挡配置笔误/怪 scheme） |

**决策**：webhook URL 校验**只**做 (a) `url.Parse` 成功 (b) scheme ∈ {http, https}。**不**调 webfetch 的 `ValidateIP`。理由：SSRF gate 的威胁模型是「模型选 URL」，此处 URL 是用户在本机 config 里显式写的——拦私网只会误杀合法 LAN push，且用户本就能用 curl 打同样的地址。https 为推荐，http 放行（LAN 常无证书）。**这是与 webfetch 的故意分歧，PRD 钉死理由，防 reviewer 误以为漏了 SSRF 防护。**

### D4 — event 分类 + 过滤独立于 `job.Notify`

事件名 = `"<source>.<status>"`：
- cron：`cron.completed` / `cron.failed` / `cron.killed`（status 来自 `StatusCompleted/Failed/Killed`）
- trigger：`trigger.completed` / `trigger.failed`

webhook 的 `events`（默认 = 全部）过滤**独立于** `job.Notify`（always/on_failure/never）。理由：`job.Notify` 管**本地桌面弹窗**（「这个 job 要不要 banner」）；`webhook.events` 管**远端渠道**（「我手机这个频道要不要收」）。两者正交——用户可能 `Notify=never`（不要桌面 spam）但仍想失败推手机。所以 webhook 派发**不**包在 `if shouldNotify` 块里，由 dispatcher 内部按 `events` 自行过滤。

### D5 — 预检替代运行时节流（零 daemon 的必然）

v6 的「同 URL 连续失败 5 次降 DEBUG」假设有跨调用的进程内存——但 routines 零 daemon，每 tick 独立进程，计数器跨 tick 即丢。所以：

- **单 tick 内**：webhook 失败照常 WARN 到 stderr（一个 tick 内 due jobs 不多，不会刷屏）；launchd/systemd 捕获。
- **跨 tick**：不做持久化节流（拒绝加 parallel state 文件，v5 原则）。
- **真正的缓解 = 配置时预检**：`seek cron config check` 对每个 webhook 做 scheme 校验 + 可选一次 POST 探测（发一条 `seek webhook test` 消息），把「配错 URL」挡在用户依赖它**之前**，而非每分钟静默失败。**比运行时节流更对路**——错误在配置时暴露，不在凌晨 cron 时淹没日志。

### D6 — format 适配器

每 format 一个 `(title, body) → (httpMethod, headers, payload)` 构造器：

| format | 形态 |
|---|---|
| `ntfy` | POST，body = `body` 纯文本；header `Title: <title>`、`Priority`、可选 `Tags`（ntfy.sh API 形式，**非** JSON body） |
| `slack` | POST，JSON body `{"text": "<title>\n<body>"}`（incoming webhook） |
| `discord` | POST，JSON body `{"content": "**<title>**\n<body>"}` |
| `raw` | POST，JSON body `{"title": ..., "body": ..., "event": ...}` |

stdlib `net/http`，5s timeout，best-effort。

## 4. config schema（`~/.seek/config.json`）

```jsonc
{
  "push_webhooks": [
    {
      "url": "https://ntfy.sh/my-seek-topic",
      "format": "ntfy",                     // ntfy | slack | discord | raw（默认 raw）
      "events": ["cron.failed", "cron.killed", "trigger.failed"]  // 省略 = 全部
    }
  ]
}
```

`config.Config` 加：
```go
PushWebhooks []PushWebhook `json:"push_webhooks,omitempty"`
```
向后兼容（旧 binary 忽略未知 key，`config.go:38-40`）。`PushWebhook{URL, Format, Events []string}`。

## 5. 实施拆解与估时

| 任务 | 估时 |
|---|---|
| `internal/routines/webhook.go`：`WebhookDispatcher` 类型 + 构造器 + 4 个 format 适配器 + HTTPS POST（5s timeout、best-effort）+ URL 校验（scheme-only，D3） | ~1d |
| 接线：`TickOptions.Webhook` 字段 + 两个派发点追加调用（D1）+ `main.go` 从 config 构造注入（D2）+ `config.PushWebhook` 结构 | ~0.5d |
| `seek cron config check`（routinescli 新 verb，D5）+ 测试 + 文档（README + `guide-cron.md` 加 ntfy 安装指南） | ~0.5d |
| **合计** | **~2d**（对齐 v6 §3.5） |

## 6. 测试要求（CLAUDE.md "Testing (load-bearing)" 5 条 + v6 §6 柱 M）

| 标准 | 本柱适用点 |
|---|---|
| **失败/降级** | `httptest` server 返 5xx → dispatcher WARN 且**不**回滚 run（断言 MarkRun 不受影响）；server 不可达（拨号失败）同样不阻塞 |
| **malformed 输入** | 坏 config：URL 非 http(s)、`url.Parse` 失败、未知 format → `config check` 报错；运行时遇坏 URL → skip + WARN，不 panic |
| **cancellation** | dispatcher 的 http 请求挂 ctx（5s timeout）；tick ctx 取消时 in-flight POST 被取消，不泄漏 goroutine |
| **events 过滤** | table-test：`events=["cron.failed"]` 时 `cron.completed` 不发、`cron.failed` 发；空 events = 全发；过滤独立于 job.Notify（D4） |
| **format 正确性** | 4 个 format 各断言 method/header/payload（ntfy 的 `Title` header 在 header 不在 body；slack/discord JSON 形态；raw 带 event 字段） |
| **并发 -race** | 多 webhook 并发 POST（一个 tick 多 job）无共享可变态竞争；http.Client 并发安全 |
| **持久化 round-trip** | config 带 `push_webhooks` 写→读→dispatcher 构造一致；旧 config（无该字段）→ nil dispatcher，零行为变化 |
| **零破坏回归** | 现有 `notify` / tick / trigger 测试全绿（`Notifier` 签名未动） |

## 7. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 用户配错 URL → 每 tick 失败刷日志 | D5：`config check` 预检 + 单 tick 内 WARN 有限；不做无效的跨 tick 节流 |
| 把私网拦截漏了被当成 SSRF 缺陷 | D3：PRD 钉死「用户静态配置 outbound ≠ 模型驱动 SSRF」的理由，附测试注释 |
| webhook 慢 → 拖慢 cron tick | 5s timeout + best-effort；dispatcher 不阻塞 MarkRun（已先写 run record 再派发，`tick.go:516` 在前） |
| body 含敏感 cron 输出推到第三方 | 文档明示：webhook 把 (title, body) 发给用户**自选**的第三方；推荐 ntfy 自有 topic / 自托管；body 即 OS 通知的 body，不额外泄露 |

## 8. 与 v5 / 现有的关系 + 开放决策

- **叠加层**：新 `webhook.go`（routines 内）+ `TickOptions.Webhook` 可选字段 + 两派发点各 +1 行 + config 一个字段 + 一个 CLI verb。`Notifier`/OS 通知/`pkg/*`/permission 全不动。
- **直接挂 v5 柱 H 通知通道**（v6 §5）——routines 已 ship，webhook 是其 OS 通知的并列旁路。
- **回滚成本**：revert `webhook.go` + 两派发点的 +1 行 + config 字段，单 commit。

**开放决策（reviewer 拍板）**：
1. **D3 私网放行**：本 PRD 主张放行（自托管用例）。若安全姿态要求保守，可加 config 顶层开关 `push_allow_private: false`（默认放行）让谨慎用户自锁——但默认放行，否则 LAN push 开箱即坏。
2. **`config check` 是否发真探测 POST**：发一条 test 消息能真验可达，但会给用户频道推一条「测试」噪声。建议 `check` 默认只做 scheme/parse 校验，`--probe` flag 才发真 POST。
