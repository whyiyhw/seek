# 第 23 章：v6 五柱 — 单点工具补齐（I · J · K · L · M）

> **对应版本**：v0.7.x
> **对应代码**：`internal/askuser/`、`internal/tools/askuser/`、`internal/skill/builtin/code-review.md`、`internal/tui/commands.go`（`/code-review`）、`internal/bgjob/`、`internal/tools/bash/bg_*.go`、`internal/tools/monitor/`、`internal/lspclient/`、`internal/tools/references/`、`internal/routines/webhook.go`、`internal/config/config.go`（`push_webhooks`）
> **PRD**：[`docs/prd/v6.md`](../prd/v6.md) · `docs/prd/feature-{askuser-v2,code-review,bash-monitor,lsp,mobile-push}.md`
> **验收**：5/5 柱全部落地；每柱有独立测试套 `-race` 绿
> **起点**：第 22 章（v5 柱 H 时序触发 Routines）。v5 关闭了 seek 与 Claude Code 之间最后三个架构级 P0 缺口，剩余的差距收敛为 5 项互不耦合的**单点工具**——每个都是"一天到一周"的量级，没有共享基础设施，可以独立 ship。

---

## 23.1 从架构缺口到单点补齐

走完 v5 后，[对比文档](../comparison.md)的差距汇总变成了这样：

```
🟢 已交付 (v0.4–v0.6): 8 项 — checkpoint · undo-redo · hooks · Tab · 键位 · subagent · worktree · routines
🟡 P1:              5 项 — LSP · Monitor · AskUserQuestion v2 · 复合 review · 移动 push
🟠 P2:              3 项 — TUI MCP 重启 · NotebookEdit · WebSearch
🔵 P3:              3 项 — Fast 模式 · Devcontainer · 语音输入
—— 范围外:           3 项 — SDK · 企业管理 · 多端
```

P0 已经清零。留下的 P1 全是**独立可补的单点**——任意一项 ship 后立刻可用，不需要等其他项做完。这与 v5 完全不同：v5 的子代理 + 调度 + worktree 必须一起设计（共享调度面 / 权限传播 / 成本归集），v6 的 5 项**没有任何共享基础设施**。

v6 因此不是传统的 umbrella，而是 5 个"横向打包在一起便于跟踪"的独立工程。每柱写成一个"我加 1 个工具 + 我 1 套测试"的形态，回滚就是 revert 一个 commit。

两条约束钉死了这个定位：

1. **不变更核心架构**——任一柱不应触发 `pkg/agent` / `pkg/deepseek` / `internal/permission` 的接口变更。如果某柱实施时发现需要改这些，停下来评估——它可能根本不是"单点工具"。
2. **每项独立可回滚**——柱 J 不能 import 柱 I 的代码、柱 K 不能依赖柱 L 的 LSP 启动。

---

## 23.2 柱 I — AskUserQuestion v2：多题 stack + preview 侧栏

### 23.2.1 v1 的遗产

`ask_user` 工具在 v0.3.x 阶段已经是一个成熟的 TUI 选择器——支持单题选择、multi-select、"Other"自由文本自动追加。但它有一个硬伤：一次只能问一道题。

当模型需要做一组相关决策时（"用什么框架？什么状态管理？什么 styling？"），要么分多次 `ask_user` 调用（每次一个往返，打乱对话节奏），要么塞到一个问题里（"请选择框架、状态管理和 styling"——无法给每项各自的一组选项）。

### 23.2.2 schema 演化：不用 version 字段的多态设计

v2 的核心加法只有两个：

1. **`questions` 数组**——一次调用来 1–4 题，TUI 渲染为纵向堆叠的选择器栈
2. **`preview` 字段（每选项）**——侧栏渲染 mockup / 代码片段 / ASCII diagram，解决"光看 label 和 description 不够判断"的问题（比如选颜色主题、布局方案）

关键的设计决策是**保持 v1 schema 兼容**，不引入 version 字段。做法很直接：v1 是顶层 `question + options`，v2 是顶层 `questions: [{question, options}, ...]`。Go 端的 `AskUser` 解析器检测到 `questions` 数组就走 v2，否则走 v1。一个工具，两个 shape，zero breaking change。

```
// v1 形态（不变）
{
  "question": "Which framework?",
  "options": [...]
}

// v2 形态（新增）
{
  "questions": [
    { "question": "Framework?", "options": [...] },
    { "question": "State mgmt?", "options": [...] }
  ]
}
```

### 23.2.3 Preview 字段的 TUI 渲染

`preview` 是每个选项的附加字段——一段 ~12 行 × 80 列的纯文本，在终端宽度够时渲染为选项旁的侧栏。它的价值在比较视觉/结构替代方案时最明显：两个 layout 方案，光看 "bento grid" vs "card list" label 不够，但旁边展开各自的结构示意图就清楚了。

TUI 端做了**自适应截断**：preview 文本在窄终端下完全隐藏，不影响核心操作。

### 23.2.4 验收

两阶段落地：Phase 1 schema + API（commit `cb8ed16`）→ Phase 2a TUI stack（`5b775fe`）→ Phase 2b preview panel（`323c00e`）。测试覆盖了多题 stack 渲染、preview 截断、v1 向后兼容路径。

---

## 23.3 柱 J — 复合 Code-Review Skill

### 23.3.1 从 /review 到 /code-review

`/review` 是早期就有的 slash 命令，但它做的事情很简单：把 diff 喂给模型说"review this"。没有 effort 分级、没有 fix 模式、没有分支对比。

柱 J 的定位是把它从一个"单薄 slash 命令"升级成**结构化代码审查能力**——四档 effort framing（quick / medium / high / max）+ `--fix` 自动修复 + `--comment` 输出。为了容纳这个复杂度，审查方法论写成了**内置 skill**（`internal/skill/builtin/code-review.md`），slash 命令负责参数解析和 diff 采集，审查框架由 skill body 提供。

### 23.3.2 职责切分：命令 vs skill Body

这是一个关于"什么放在命令参数里，什么放在 skill prompt 里"的设计案例。

- **命令层**（`/code-review`）处理：effort 级别、`--fix`/`--comment` flag、目标分支
- **Skill body** 包含：审查方法论、effort 四档的语义定义、关注点分类表

为什么要这样切？因为 `Skill` 工具的 JSON schema 只有 `{name}` 一个参数——effort 和 flag 传不进去。而且 schema 被 prefix-cache 钉死了，不能轻易加字段。所以 effort 在命令层解析，然后通过 prompt 文本传递给模型。

`/review` 被收敛为 `/code-review quick` 的别名——普通用户打 `/review` 得到 quick 级审查，进阶用户用 `/code-review high main` 做分支对比。

### 23.3.3 --fix 的 propose 复用

`--fix` 模式是柱 J 最有趣的部分：审查发现 bug 后不只是一份报告，而是通过 **propose 路径**逐个修复。每个 bug 成为一个 propose step，用户 approve 后再执行。这意味着 `--fix` 复用了 plan-mode v2 的 workflow——不是新写的修复引擎，而是把"审查发现问题"和"plan-mode 执行步骤"连起来了。

修复粒度受 effort 控制：`quick --fix` 只修阻塞 bug，`max --fix` 修所有可自动修复的问题。

### 23.3.4 验收

commit `e24b2f9`。测试覆盖了 effort 参数解析、分支参数传递、`--fix`/`--comment` 分支、以及内置 skill 的加载。eval cases 验证了 effort `low` 和 `max` 的行为差异。

---

## 23.4 柱 K — 后台 Bash + Monitor

### 23.4.1 问题：长任务卡死 turn

seek 的 `bash` 工具一直是同步阻塞的——等命令跑完再返回。这对 `go build ./...`（30 秒）、完整测试套件（几分钟）、dev server（永不退出）来说是个灾难。它们要么撞超时被杀，要么让整个 turn 卡住十几秒，用户和模型都干等。

柱 K 的回答是：**`bash run_in_background` + `monitor`**——把长任务丢到后台立即拿回控制权，再随时查进度。

### 23.4.2 设计：会话级的进程组管理

后台任务的生命周期绑定到当前 seek 会话：

```
bash(command="go build ./...", run_in_background=true)
    → 立即返回 "[bg: started bg-1] $ go build ./..."
    → 进程在后台跑，不阻塞 turn

monitor(job="bg-1", action=poll)
    → "running · 12s · out: building cmd/..."
monitor(job="bg-1", action=wait, until_regex="PASS")
    → 阻塞直到输出匹配 "PASS"，或超时
monitor(job="bg-1", action=kill)
    → 杀进程组
```

关键设计约束是**不复用前台 timeout/deny 逻辑**：后台模式下 `timeout_ms` 被忽略（任务一直跑到自己退出或被 kill）。进程组管理通过 `Setsid` + 进程组 kill 实现，确保 kill 时不留 orphan。

`monitor` 支持三种 action：
- **poll**：返回自从上次 poll 后的输出 + 状态（running / exited / killed）
- **wait**：阻塞直到任务退出、`until_regex` 匹配新输出、或超时
- **kill**：杀掉整个进程组

### 23.4.3 适配层：Windows 降级

Windows 没有 POSIX `Setsid`，也不能用 `/bin/sh -c`。柱 K 在 Windows 上走 `cmd.exe /C` 加进程组 handle 跟踪，功能等价但实现不同。

### 23.4.4 验收

commit `b640199`。测试套覆盖了 bg 启动 + poll + wait + kill 全流程，`-race` 验证了并发安全。`bgjob/ring.go` 的环形缓冲区经过了容量溢出和并发读写测试。

---

## 23.5 柱 L — LSP References 语义找引用

### 23.5.1 最贵的 P1

五柱中估时最长的（~7 天），原因是 LSP 集成需要在一个完全不相关的工具框架里塞进**语言服务器客户端**——启动、会话管理、JSON-RPC 2.0 通信、crash 重启、降级策略。不是"写个工具"那么简单。

最终以瘦身版落地（~4 天），因为经 ROI 评估后砍掉了 `definition` / `hover` / `symbols` 三个能力：

| 能力 | ROI 评估 | 决策 |
|------|----------|------|
| `references` | Go 中 grep 不能替代（别名导入、接口实现、跨包引用） | ✅ 做 |
| `definition` | Go 中 `grep type Foo struct` + `go doc` 已覆盖 | ❌ 不做 |
| `hover` | Go 中 `grep` + 读代码已覆盖 | ❌ 不做 |
| `symbols` | `grep -rn '^func\|^type\|^var'` 已覆盖 | ❌ 不做 |

### 23.5.2 客户端架构

`internal/lspclient/` 包含：

| 文件 | 职责 |
|------|------|
| `client.go` | LSP 客户端核心：stdio 通信、JSON-RPC 2.0 发送/接收、method dispatch |
| `manager.go` | 会话级生命周期管理：懒启动、crash 重启、超时重置 |
| `framing.go` | Content-Length 帧的读写（LSP 标准传输层） |
| `types.go` | LSP 类型定义（最小子集：只有 references 请求/响应） |

`manager.go` 是整个集成的关键：它不是每次调用都启动新 server，而是**
会话级复用**一个 server 实例。server crash 时自动重启（最多 3 次），不可用时降级回 `grep`。

### 23.5.3 工具层的适配

`references` 工具是位置驱动的：需要 `file` + 1-based `line` + 符号名（或列号）。典型的调用模式是**先 grep 找到声明位置，再 references 找所有引用**——这正是 AGENTS.md 里强调的 workflow。

工具签名：

```
references(file="internal/lspclient/client.go", line=42, symbol="NewClient")
    → 返回所有引用的位置列表（file:line:col + 上下文代码行）
```

### 23.5.4 降级策略

语言服务器不可用（没装 / crash / 超时）时，`references` 返回一个带 `[references: gopls not found in PATH; fall back to grep]` 的提示消息——不是 error，是 tool result。模型看到这个提示后可以用 `grep` 替代。这种"失败告知但不中断"的模式在 seek 中反复出现（OCR、permission 等）。

### 23.5.5 验收

commit `ab97105`。测试覆盖了 client 连接、manager 懒启动、crash 重启、framing 解析、以及降级路径。

---

## 23.6 柱 M — 移动端通知 Webhook 桥

### 23.6.1 问题

v5 柱 H 已经实现了 OS 原生通知——用户坐在电脑前能收到 seek 的弹窗。但"电脑前"这个前提很脆弱：cron 任务半夜跑完、autopilot 早晨完工、长时间 build 结束时你在看别处——这些场景需要的是**手机也能收到通知**。

### 23.6.2 为什么不是原生 Push

Claude Code 的 push 走云服务：注册 device token → 云推送 → 手机收到。但这与 seek 的"零 daemon、零云服务、本地隐私优先"的架构立场冲突。

柱 M 的答案是 **webhook 桥**——不在 seek 内部实现推送，而是把通知 POST 到用户配置的 webhook URL，由用户自选的渠道转发到手机：

```
seek cron tick (完成) → OS 通知（桌面）
                     → Webhook POST（ntfy / Slack / Discord / 自建）
                        → 你的手机
```

支持的 webhook 格式：`ntfy`、`slack`、`discord`、`feishu`（飞书）、`feishu-flow`、`template`（自定义 JSON）、`raw`。

### 23.6.3 实现：可选的 Notifier 兄弟

柱 M 没扩展 `internal/routines.Notifier` 接口——Notifier 只接受 `(title, body)` 两个参数，装不下 `events` 过滤。正确的做法是加一个**可选的 sibling 接口** `WebhookDispatcher(event, title, body)`：

```go
type WebhookDispatcher func(ctx context.Context, event, title, body string)
```

与 `Notifier` 正交——TickOptions 同时携带 Notifier 和 Webhook，两者独立工作。用户只配了 Notifier 就只桌面通知，只配了 webhook 就只手机推送，都配就两路都走。

### 23.6.4 SessionNotifySeconds：长交互回合的推送

除了 cron/autopilot 终态通知，柱 M 还加了一个**交互通知**：当一次 TUI 回合运行超过 60 秒（默认）时，回合结束时会触发 `session.completed` 事件推送。适用场景：你跑了一个 3 分钟的 `go test ./...`，切出去刷网页，测试跑完手机震一下。

`session_notify_seconds` 可配置，设为 0 关闭。

### 23.6.5 私网放行

通常 seek 的 HTTP 工具（如 `webfetch`）会阻止向私有 IP 地址发请求（SSRF 防御）。但 webhook 面对的是用户**主动配置**的 URL——自托管 ntfy、内网 relay 是有意为之的用例。所以柱 M 的 webhook dispatcher 放行私网地址，这是显式偏离 SSRF gate 的设计决策。

### 23.6.6 验收

commit `2d53a4d`（柱 M 核心）+ `997cd8a`（交互通知扩展）。测试覆盖了四种 format 的 payload 生成、构造时 URL 校验、以及 `session_notify` 的时长门控。

---

## 23.7 一个观察：从"推土机"到"扳手"的换挡

v0–v5 的每一版都在做"架构级补齐"——推土机式的工程，一次改动波及十几个包，PRD 讨论的是设计约束和状态机。

v6 的 5 根柱子的共同特征是**小而专注**：

- 每柱增加或修改 1–2 个工具 + 对应的测试
- 没有一柱触发 `pkg/agent` 或 `internal/permission` 的接口变更
- 最大的一柱（LSP）也只是一个 `internal/lspclient/` 新目录 + `internal/tools/references/` 一个工具
- 回滚就是 `git revert <commit>`，没有级联依赖

这种从"推土机"到"扳手"的换挡是项目成熟度的标志。并非所有功能都需要全新架构——很多时候，一个好工具就是最好的架构。v6 证明了 seek 的工具框架足够稳定和可扩展，以至于新能力的加入可以像"在工具箱里加一把扳手"一样直接。

---

> **下一章**：v7 四柱——从追赶 Reasonix 到深化护城河。
