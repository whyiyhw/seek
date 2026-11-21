# 在 Zed 里用 seek — ACP 编辑器集成指南 / Using seek inside Zed (ACP)

seek 实现了 **Agent Client Protocol (ACP)**，可作为一个**自定义 agent** 嵌进 [Zed](https://zed.dev)：在 Agent 面板里直接跟 seek 对话，它的工具调用（read / grep / edit / git…）会渲染成 Zed 原生的步骤卡片。

> seek speaks the Agent Client Protocol, so it plugs into Zed as a **custom agent** — chat with seek in Zed's Agent Panel; its tool calls render as native step cards.

> **定位**：这是 **MVP**。握手 + 流式回复 + 工具调用映射 + **session resume** 已跑通（真 Zed 验证 / 真 stdio e2e），但还简陋——审批门、slash 命令等见 §5 的优化清单。设计见 [`docs/prd/archive/feature-acp.md`](./prd/archive/feature-acp.md)。

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

- ✅ **握手 + 会话**：`initialize` → `session/new`（cwd = 你打开的项目，**生效**：工具相对路径、bash/git 工作目录都锚到它）→ `session/prompt`。
- ✅ **流式回复**：文本逐 chunk 推送；**思考流走 `agent_thought_chunk` 独立通道**（2026-11-19 用户拍板开放，客户端渲染为可折叠思考区——主模型的 CoT 与 `think` 工具的实时输出都会出现；代价：CoT 会进客户端侧日志）。
- ✅ **工具调用渲染**：`read` / `grep` / `list_dir` / `git` / `edit` 等映射成 ACP 的 `tool_call` 卡片，含 in_progress → completed/failed 状态，且走**全协议字段**（2026-11-18）：`name` + `kind`（客户端图标/呈现分类）、`locations`（绝对文件路径 + 行号，客户端 follow-along）、`rawInput`/`rawOutput`（可折叠原始输入/输出，16KB 截断）、`content`——`edit` 发协议 diff 块（old/new 即改即视），其余工具发结果文本块（4KB 截断）。
- ✅ **真实编辑工作区**：seek 直接改你打开的项目文件（实测在 `.gitignore` 加了一行成功）。
- ✅ **取消**：Zed 中断一轮 → seek 收到 `session/cancel`，干净停。
- ✅ **上下文余量上报（2026-11-21，卡 c_uzlczr）**：每个完成的模型回合通过 `session/update` 发一帧 ACP 的 `usage_update`，客户端据此显示余量条/百分比。字段是**协议自己的**（[RFC/RFD "Session Context Size and Cost"](https://agentclientprotocol.com/rfds/session-usage)）：`used`（当前上下文占用量）+ `size`（该模型窗口大小），余量与百分比由客户端自己算（`size - used` / `used/size`）。seek 额外把 `percent` / `severity` 放在协议预留的 `_meta.seek.ctx` 里，近阈值那一帧再多 `alert` + 一句 `notice`（详见 §5.3）。数字口径与 TUI 状态栏 `formatBudget`、Web 顶栏 ctxmeter **同源**：占用 = **末个回合**的 prompt+completion（累计值会随历史逐轮重发二次膨胀），窗口 = `internal/budget.Limit(model)`，阈值 = `budget.Classify` 的 0.60/0.75。零新数据源、零新表。

---

## 5. 已知限制 + 后续优化 / Known limits & optimization backlog

这是 MVP，**简陋**，以下是真实的边界与后续可优化项（欢迎按需推进）：

| 项 | 现状 | 后续优化方向 |
|---|---|---|
| **审批门 / approval** | ACP 路径**没有 per-action 审批 UI**，且权限姿态钉死 `Deny`（flag 够不到，见 §5.1）：cwd 内写/编辑直接放行、**bash 全拒**（`go vet` 类 read-only 白名单除外）。无头场景可用 `SEEK_ACP_YOLO=1` 放开（§5.1）。 | 实现 ACP `session/request_permission`，把工具审批弹到 Zed 原生权限 UI（"允许 seek 编辑 X?"）。 |
| **session resume** | **已修**：`agentCapabilities.loadSession=true`（有持久化时），`session/load` 会读出那份 JSONL transcript、重新锚定 cwd、恢复会话模型车道、把整段历史按 `user_message_chunk` / `agent_message_chunk` / `tool_call(_update)` 回放给客户端，并在回放**之后**才回响应。`session/new` 返回的 `sessionId` 现在是**持久 transcript id**（客户端存下来才能继续；这也是 paseo 存 native session id 的依据）。paseo 据此开启 session.persistence。`--no-save` 在 ACP 车道够不到（flag 停在 `acp` 位置参数），所以实际部署里 `loadSession` 恒为 true；无持久化的兜底路径会退回项目名并申报 false。 | 唯一缺口：`sessionCapabilities.list`（paseo 的"列出历史会话"）仍未申报。 |
| **会话数** | 一个 `seek` 进程 = 一个会话（MVP）；`session/new` 第二次调用是重锚定已有会话，不新开。 | 支持多 `session/new` 复用同进程。 |
| **slash 命令** | `/plan`、`/model` 等 **TUI-only**，ACP 下无效。 | 设计 ACP 下可表达的子集（如 plan 模式经 capability 暴露）。模型选择已有协议通道替代（§5.2）。 |
| **capabilities** | 只报最简 `agentCapabilities`；`initialize` 仅回显 `protocolVersion`。 | 加 `promptCapabilities`（图片/embedded context）、做真正的版本协商。 |
| **per-session cwd** | **已修**：`session/new` 的 `cwd` 生效——进程与权限策略一起切到该目录（Zed 传的就是启动目录，行为不变）。 | 多 session 各持独立栈（见 backlog ③）。 |
| **MCP 透传** | Zed 在 `session/new` 发 `mcpServers`，seek 暂未透传。 | 把 Zed 给的 MCP server 接进 seek 的 MCP 客户端。 |

### 5.1 无头权限开关 `SEEK_ACP_YOLO` / Headless permission switch

`seek acp` 以 `os.Args[1]=="acp"` 短路启动，而 `flag.Parse` 在第一个位置参数处就停止——**一个命令行 flag 都不消费**，`--yolo` 根本表达不到这条路。权限姿态因此默认钉死 `Deny`：cwd 内写/编辑放行，**bash 全拒**（read-only 白名单如 `go vet` 除外）。交互场景（Zed）这是合理默认；**无头场景**（paseo / ACP driver 派单，没有人回答 y/N）会直接卡死——`git commit`、`go test` 都跑不了。

开关：给 ACP 进程注入环境变量（真值：`1`/`true`/`yes`/`on`，大小写不敏感；其它值一律视为关）：

```json
"env": { "SEEK_ACP_YOLO": "1" }
```

seek 在 ACP 车道**初始化时**就以 `PrefYolo` 起栈（不是事后翻转——系统提示词里的 Mode 标签和权限门必须一致），bash / cwd 外写全部放行，与 `--yolo` 同姿态。先例：`seek goal run`、autopilot 的"无人值守 → yolo"。

yolo 放开的只是**本地**操作：与 goal / autopilot 同款 no-remote 守卫同时挂上（主会话与 subagent 的 bash 都过 `IsRemoteMutating` 检查），`git push`、`gh pr create` 等 push/PR 类远程变更照旧被拒——无人值守车道能改本地、不能动远端。

> ⚠️ **仅受信本机 headless 场景使用**（paseo provider 的 `env` 块就是典型用法）。`PrefYolo` 放开的是本机 bash 与写文件（远程变更仍被 no-remote 守卫拦下）；env 会被原样继承给子进程，不要在不受信、共享或远程可达的环境里设置。默认关（unset = `Deny`）；只在 `seek acp` 车道生效——shell 里残留的 `SEEK_ACP_YOLO` 对 TUI / print 模式**无**影响。

设计出处：[`docs/prd/archive/feature-paseo-provider.md`](./prd/archive/feature-paseo-provider.md) §3.3（缺口 D-2）。

### 5.2 模型选择 / Model selection（configOptions + env 钉定）

`session/new` 现在会返回 `configOptions`（一个 `id:"model"` 的 select 型下拉）：凡 key 可解析的 provider（deepseek / anthropic / openai / gemini / glm / opencode）各带自己的型号入列。**ACP 客户端（paseo）据此渲染模型下拉**；选择后客户端发 `session/set_config_option`，seek 在**回合之间**把整条车道（provider 客户端 + model）一次换掉——下一个 `session/prompt` 就用新模型。Zed 侧暂不渲染 configOptions 下拉（其 ACP 面较旧），但换挡机制对任何实现了该协议的客户端一致。

限制（都刻意的）：

- **回合中切换会被拒**（`-32000`，"switch models between turns"）——换挡是回合间操作，中途换会与正在跑的回合竞争。
- **启动型号不在 curated 清单时不出下拉**（如 freeform `SEEK_ACP_MODEL=deepseek-v4-pro`）：下拉的 current 必须在自己列出的选项里，否则 UI 在撒谎。
- **subagent 车道与 memory 不跟车**：它们按启动时的路由各走各的 key（deepseek 缺 key 时才落 glm），主会话切 glm 不影响它们。
- **TUI `-resume` 一张 ACP 会话**：会话记录的是裸型号名（如 `glm-5.3`），TUI 按现有 key 自动探测 provider——本机有 DeepSeek key 时会拿 deepseek 客户端配 glm 型号名，首轮报错。显式 `seek -resume <id> --provider glm` 即可。

**env 钉定**（启动车道，flags 够不到这条路——同 §5.1 的理由）：

```json
"env": { "SEEK_ACP_PROVIDER": "glm", "SEEK_ACP_MODEL": "glm-5.3" }
```

两个都可选、可单用；`SEEK_ACP_PROVIDER` 同时压掉首启向导（无头进程不该进交互式贴 key 提示）。只影响 `seek acp` 车道，shell 里残留的值对 TUI / print 模式无影响。paseo 侧典型用法：复制一个 provider 条目（如 `seek-glm`）写上 env，同一台机器 DeepSeek / GLM 两条入口并存——与 zcode 桥的 `ZCODE_PROVIDER`/`ZCODE_MODEL` 钉法同款。

wire 形状与参照实现出处：[`docs/prd/archive/feature-paseo-provider.md`](./prd/archive/feature-paseo-provider.md) §3.4（C 落地记录）。

### 5.3 上下文余量 `usage_update` / Context-window reporting

**每一轮的形状**（走 `session/update`，不是新 method）：

```json
{"jsonrpc":"2.0","method":"session/update","params":{
  "sessionId":"20261121-160406-…",
  "update":{
    "sessionUpdate":"usage_update",
    "used":800012,
    "size":1000000,
    "_meta":{"seek":{"ctx":{"percent":80,"severity":"critical"}}}
  }}}
```

- `used` / `size` 是 **ACP 协议字段**（RFD "Session Context Size and Cost"，ACP 1.2 起进 schema；paseo 的 `zUsageUpdate` 正是 `z.object({used, size})`）。协议里**没有** percent 字段——客户端自己除。所以 seek 不自造字段名，把补充信息放进协议预留的 `_meta`（"Implementations MUST NOT make assumptions about values at these keys"）。
- `_meta.seek.ctx.percent`：取整的占用百分比，与 TUI 状态栏那个 `ctx NN%` 是同一个数。
- `_meta.seek.ctx.severity`：`ok` / `warn` / `critical`，三档对应 `budget.Classify`。
- **告警帧**：只在**跨过阈值的那一帧**额外发一帧（同一 used/size），多带 `alert`（= 该档 severity）与 `notice`（`warn` → `"ctx 70%"`，`critical` → `"ctx 80% — /compact soon"`，措辞照抄 `formatBudget`）。为什么不把 alert 挂在每一帧上：每次 arrival 都会被客户端渲染成一次仪表更新，每轮重复告警是噪声；只报"刚跨过"才是一条真正的信号。落回阈值以下、或 `session/load` 换进来一个会话，跨越记忆会重置。
- **开关**：`SEEK_ACP_CTX_ALERT=0`（或 `false`/`no`/`off`，大小写不敏感）关掉告警帧，普通 `usage_update` 照常每轮发——余量条不会因此消失。默认**开**。ACP 车道不吃任何 flag（同 §5.1 的理由），所以只能走 env。
- **阈值的边界**：这里用 `budget.Classify`（编译期 0.60 / 0.75），与 Web 顶栏 ctxmeter 的色阶一致。TUI 的 `formatBudget` 在**配了 `auto_compact`** 时会把红色点挪到触发点的 80%（`ClassifyAt`）——ACP 不跟这个会话级配置走：余量条是客户端自己的 UI，阈值该是协议面上稳定的常量，会话级自定义属于客户端侧。
- **不发的情形**：窗口大小不可用（`budget.Limit` ≤ 0）。RFD 明确没有 `size: null` 这种状态，协议自己也说"无法给出有意义的窗口就干脆不要发"。
- **`session/load` 之后**：回放的是历史消息帧（`user_message_chunk` / `agent_message_chunk` / `tool_call*`），**不含** `usage_update`——transcript 里只有累计用量，没有"上一回合的 prompt+completion"这个口径值，硬凑一个就是新造数据源。所以恢复会话后余量条从**下一回合**开始显示（客户端在收到第一帧前不渲染这个部件即可）。

验证：`bash scripts/acp-smoke-test.sh` 用真二进制跑两态——低用量（1000/1M → `percent=0 severity=ok` 单帧）与近满（800000/1M → `percent=80 severity=critical` 且恰好多一帧告警、`notice` 带 `/compact`）——断言在解析后的 JSON 上做（`used`/`size` 必须是 int、`size` 必须等于 `budget.Limit("glm-5.3")`=1,000,000），不是字符串 grep。设计说明与 wire 形状出处写在 `internal/acp/usage.go`。

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
