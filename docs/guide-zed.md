# 在 Zed 里用 seek — ACP 编辑器集成指南 / Using seek inside Zed (ACP)

seek 实现了 **Agent Client Protocol (ACP)**，可作为一个**自定义 agent** 嵌进 [Zed](https://zed.dev)：在 Agent 面板里直接跟 seek 对话，它的工具调用（read / grep / edit / git…）会渲染成 Zed 原生的步骤卡片。

> seek speaks the Agent Client Protocol, so it plugs into Zed as a **custom agent** — chat with seek in Zed's Agent Panel; its tool calls render as native step cards.

> **定位**：这是 **MVP**。握手 + 流式回复 + 工具调用映射已跑通（真 Zed 验证），但还简陋——审批门、session resume、slash 命令等见 §5 的优化清单。设计见 [`docs/prd/feature-acp.md`](./prd/feature-acp.md)。

---

## 1. 前置 / Prerequisites

1. **一个支持 `acp` 的 seek 二进制**。`seek acp` 是较新的命令——老版本没有。自检：

   ```bash
   printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}' \
     | seek acp 2>/dev/null | head -1
   ```

   应打印 `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{...}}}`。
   如果它反而**像在回答这行文字**（把 JSON 当成 prompt 了），说明你的 `seek` 是不带 ACP 的旧版——重新构建/安装：`go build -o /tmp/seek ./cmd/seek && sudo cp /tmp/seek $(which seek)`。

2. **配好 provider / API key**（`~/.seek/config.json`，或 `DEEPSEEK_API_KEY`）。`seek acp` 跑的是完整 agent，跟 TUI 一样需要 key。

3. **知道二进制的绝对路径**：`which seek`。macOS 的 GUI 应用通常**不继承你 shell 的 `PATH`**，所以 Zed 配置里要写**全路径**，不能只写 `seek`。

---

## 2. 配置 / Configure

⚠️ **不在 Extensions、也不在 ACP Registry 里找 seek**——那两个是**已发布 agent**的市场（OpenCode 之类）。seek 是**自定义本地 agent**，只通过 `settings.json` 注册。

打开 Zed 设置（`cmd-,` 或命令面板 → `zed: open settings`），加：

```json
{
  "agent_servers": {
    "seek": {
      "command": "/usr/local/bin/seek",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

把 `command` 换成你 `which seek` 的输出。Zed 的设置是 **JSONC**，尾随逗号、`//` 注释都允许，所以解析不会因为这些挂掉。

---

## 3. 使用 / Use it

seek 出现在 **Agent 面板**里，不在 Extensions：

1. 打开 Agent 面板：右下角状态栏的 assistant 图标，或命令面板 → **`agent: new thread`**。
2. 面板顶部 **`+ New Thread`** 旁边的**下拉箭头**点开 → **External Agents** 区能看到 **seek**（默认有个 `⌘N` 快捷键）。选它。
3. 正常发消息。read/grep/list_dir/git/edit 等会以步骤卡片流式展示，回复逐字流出。

> Open the Agent Panel → the `+ New Thread` dropdown → **External Agents → seek**.

---

## 4. 能做什么 / What works today

真 Zed（1.4.x）端到端验证过：

- ✅ **握手 + 会话**：`initialize` → `session/new`（cwd = 你打开的项目）→ `session/prompt`。
- ✅ **流式回复**：文本逐 chunk 推送（reasoning 不外泄给客户端）。
- ✅ **工具调用渲染**：`read` / `grep` / `list_dir` / `git` / `edit` 等映射成 Zed 的 `tool_call` 卡片，含 in_progress → completed/failed 状态。
- ✅ **真实编辑工作区**：seek 直接改你打开的项目文件（实测在 `.gitignore` 加了一行成功）。
- ✅ **取消**：Zed 中断一轮 → seek 收到 `session/cancel`，干净停。

---

## 5. 已知限制 + 后续优化 / Known limits & optimization backlog

这是 MVP，**简陋**，以下是真实的边界与后续可优化项（欢迎按需推进）：

| 项 | 现状 | 后续优化方向 |
|---|---|---|
| **审批门 / approval** | ACP 路径**没有 per-action 审批 UI**——seek 直接操作工作区（实质接近无门）。 | 实现 ACP `session/request_permission`，把工具审批弹到 Zed 原生权限 UI（"允许 seek 编辑 X?"）。 |
| **session resume** | `agentCapabilities.loadSession=false`，不能恢复历史会话。 | 实现 `session/load`，对接 seek 的 JSONL session 持久化。 |
| **会话数** | 一个 `seek` 进程 = 一个会话（MVP）。 | 支持多 `session/new` 复用同进程。 |
| **slash 命令** | `/plan`、`/model` 等 **TUI-only**，ACP 下无效。 | 设计 ACP 下可表达的子集（如 plan 模式经 capability 暴露）。 |
| **capabilities** | 只报最简 `agentCapabilities`；`initialize` 仅回显 `protocolVersion`。 | 加 `promptCapabilities`（图片/embedded context）、做真正的版本协商。 |
| **per-session cwd** | `session/new` 的 `cwd` 被忽略，用进程启动目录。 | 按 session 切工作目录。 |
| **MCP 透传** | Zed 在 `session/new` 发 `mcpServers`，seek 暂未透传。 | 把 Zed 给的 MCP server 接进 seek 的 MCP 客户端。 |

---

## 6. 调试 / Debugging

连不上、或选了 seek 没反应：

1. **Zed 日志**：命令面板 → `dev: open logs`，找 ACP / agent 握手报错。
2. **seek 协议 trace**（最直接）：在配置的 `env` 里加 `SEEK_ACP_LOG`，seek 会把收发的原始 JSON-RPC 记到文件——`<<` 是 Zed 发来的，`>>` 是 seek 回的：

   ```json
   "env": { "SEEK_ACP_LOG": "/tmp/seek-acp.log" }
   ```

   重连一次，然后看 `/tmp/seek-acp.log`：
   - 有 `>> …"result":…"end_turn"` → 连上了、跑通了。
   - 卡在 `initialize`（Zed 发的版本/字段和 seek 回的对不上）→ 见 §5 capabilities 优化项。

   > `SEEK_ACP_LOG` is opt-in and off by default — zero overhead when unset. The real stdio stream is untouched; only a tagged copy is logged.

3. **dropdown 里没有 seek** → `settings.json` 没被加载：检查 JSON 结构（`agent_servers` 拼写、大括号配对），或 Zed 日志里的 settings parse 错误。
