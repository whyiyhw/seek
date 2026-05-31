# seek 推送通知 — Webhook 桥 / Push notifications via webhooks

seek 可以把 cron 任务完成、autopilot 结果、长时间交互回合的结束通知**推送到你的手机**——通过 webhook 桥接你已有的通知渠道。

> seek can push job completions, autopilot results, and long-turn end notifications to your phone — via a webhook bridge to your preferred channel.

设计决策见 [`docs/prd/feature-mobile-push.md`](prd/feature-mobile-push.md)。

---

## 1. 配置渠道 / Supported channels

支持的 webhook 格式（在 `~/.seek/config.json` 的 `push_webhooks` 数组中配置）：

| Format | 用途 | 示例服务 |
|--------|------|---------|
| `"ntfy"` | ntfy.sh 或自托管 ntfy | [ntfy.sh](https://ntfy.sh) |
| `"slack"` | Slack Webhook | Slack Incoming Webhook |
| `"discord"` | Discord Webhook | Discord Channel Webhook |
| `"feishu"` | 飞书自定义机器人 | Feishu / Lark Bot |
| `"feishu-flow"` | 飞书 Flow 触发器 | Feishu Flow |
| `"template"` | 自定义 JSON 模板 | 任何支持 JSON POST 的 webhook |
| `"raw"`（默认） | 纯 JSON 负载 | 通用 |

---

## 2. 配置方法 / Configuration

编辑 `~/.seek/config.json`（不存在则创建）：

```json
{
  "push_webhooks": [
    {
      "url": "https://ntfy.sh/your-topic",
      "format": "ntfy"
    },
    {
      "url": "https://hooks.slack.com/services/T00/B00/xxxxx",
      "format": "slack"
    },
    {
      "url": "https://discord.com/api/webhooks/xxx/yyy",
      "format": "discord"
    }
  ]
}
```

### 配置项详解

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `url` | string | ✅ | POST 目标 URL。http/https 均可，**允许内网地址**（自托管 ntfy 等场景） |
| `format` | string | ❌ | 负载格式。默认 `"raw"` |
| `template` | string | ❌ | 仅 `format: "template"` 时使用。自定义 JSON 负载，支持 `{{title}}`、`{{body}}`、`{{event}}` 占位符 |

### 占位符

所有 `format` 使用以下标准字段：
- `title`：通知标题（如 "cron: ci-watch"）
- `body`：正文内容（如 "3/4 done, 1 failed: …"）
- `event`：事件类型（如 `cron.completed`、`autopilot.done`、`session.completed`）

`template` 格式允许自定义 JSON，用占位符替换：

```json
{
  "push_webhooks": [
    {
      "url": "https://your-server.com/webhook",
      "format": "template",
      "template": "{\"msg\":\"{{title}}: {{body}}\",\"type\":\"{{event}}\"}"
    }
  ]
}
```

---

## 3. 触发时机 / When notifications fire

### a) Cron / Routines 完成时

每个 cron 任务运行结束后，若配置了 `push_webhooks`，自动推送：

```
cron: ci-watch (completed)
  ✓ 3 个检查全部通过
  → event: cron.completed
```

### b) Autopilot 完成时

autopilot run 结束后推送摘要：

```
autopilot: 重构 User 模型 (done)
  ✓ 4/4 tasks succeeded
  → event: autopilot.done
```

### c) 长时间交互回合结束时（TUI）

当一次 TUI 交互回合运行超过 **60 秒**（可配置）并且配置了 webhook 订阅 `session.completed` 事件时，回合结束后推送通知——比如你等着 build 完成时切出去看别处，build 完成后手机震一下。

配置：

```json
{
  "session_notify_seconds": 60,
  "push_webhooks": [
    {
      "url": "https://ntfy.sh/my-topic",
      "format": "ntfy"
    }
  ]
}
```

- `session_notify_seconds`：阈值秒数。默认 60。**设为 0 关闭**交互通知
- 交互通知的事件类型是 `session.completed`

---

## 4. 验证配置 / Verify configuration

```bash
# 检查配置是否正确
seek cron config check

# 手动发送测试通知（需要配置了 push_webhooks）
# 触发一次 cron 任务来验证
seek cron run ci-watch
```

---

## 5. 与 seek 架构的关系 / Architecture note

与 Claude Code 的云原生 push 不同，seek 走 **webhook 桥**路径：

- seek 不维护云服务、不注册 device token、不要求你开防火墙端口
- 通知通道完全由你控制——自托管 ntfy、公司 Slack、任意 HTTP endpoint
- **故意不做** native 云 push（违背零 daemon / 本地隐私的架构立场）
- 与 `cron tick` 同进程生命周期——tick 结束时一次性 POST，不常驻

---

## 6. 示例：手机收到通知 / Example: receiving on your phone

### ntfy（推荐）

1. 手机安装 [ntfy](https://ntfy.sh/) app
2. 订阅一个主题
3. 配置 seek：

```json
{
  "push_webhooks": [
    {
      "url": "https://ntfy.sh/你的主题",
      "format": "ntfy"
    }
  ]
}
```

### Slack

1. 在 Slack 中创建 Incoming Webhook
2. 复制 webhook URL
3. 配置 seek：

```json
{
  "push_webhooks": [
    {
      "url": "https://hooks.slack.com/services/T00/B00/xxxxx",
      "format": "slack"
    }
  ]
}
```

---

> **下一步**：了解 autopilot 如何集成 webhook → [`guide-autopilot.md`](guide-autopilot.md)
