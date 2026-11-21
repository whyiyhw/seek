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

- ✅ **握手 + 会话**：`initialize` → `session/new`（cwd = 你打开的项目，**生效**：工具相对路径、bash/git 工作目录都锚到它）→ `session/prompt`。协议版本是**真协商**（P1-3）：响应带的是「客户端请求的版本」与「seek 支持的版本」的交集里最高的那个，不是回显——客户端报 2 会拿到 1，降级在响应里申报，seek 侧另在 stderr 记一行（§5）。
- ✅ **图片输入（P1-3）**：Zed 里给 seek 线程附图，seek 收下 ACP 的 image content block（`type:"image"` + base64 `data` + `mimeType`），**视觉模型原生看图**；非视觉模型则该图不发送、行内注一行「当前模型不支持图片输入，用 /model 选一个支持视觉的模型」，回合照常完成。坏 base64 / 非图 mimeType 静默跳过那一块，不崩（§5.3）。
- ✅ **流式回复**：文本逐 chunk 推送；**思考流走 `agent_thought_chunk` 独立通道**（2026-11-19 用户拍板开放，客户端渲染为可折叠思考区——主模型的 CoT 与 `think` 工具的实时输出都会出现；代价：CoT 会进客户端侧日志）。
- ✅ **工具调用渲染**：`read` / `grep` / `list_dir` / `git` / `edit` 等映射成 ACP 的 `tool_call` 卡片，含 in_progress → completed/failed 状态，且走**全协议字段**（2026-11-18）：`name` + `kind`（客户端图标/呈现分类）、`locations`（绝对文件路径 + 行号，客户端 follow-along）、`rawInput`/`rawOutput`（可折叠原始输入/输出，16KB 截断）、`content`——`edit` 发协议 diff 块（old/new 即改即视），其余工具发结果文本块（4KB 截断）。
- ✅ **真实编辑工作区**：seek 直接改你打开的项目文件（实测在 `.gitignore` 加了一行成功）。
- ✅ **取消**：Zed 中断一轮 → seek 收到 `session/cancel`，干净停。

---

## 5. 已知限制 + 后续优化 / Known limits & optimization backlog

这是 MVP，**简陋**，以下是真实的边界与后续可优化项（欢迎按需推进）：

| 项 | 现状 | 后续优化方向 |
|---|---|---|
| **审批门 / approval** | **已修（P0-2）**：非 yolo 的 ACP 车道权限姿态为 `Ask`，危险动作经 `session/request_permission` 弹到客户端原生权限 UI（"允许 seek 运行 X?"），三选项 allow_once / allow_always / reject_once；`allow_always` 即 TUI 的「[a] always: \<kind\>」语义（同 Kind 本会话不再问）。**无应答默认拒**：客户端断线、超时（默认 10 分钟，`SEEK_ACP_PERMISSION_TIMEOUT` 秒数可调）、或无头 driver 回 `-32601`，一律拒绝并照常完成回合——不会卡死。无头无人值守场景仍用 `SEEK_ACP_YOLO=1` 直接放行（§5.1，优先级高于审批门）。 | Zed 客户端面板较旧、暂不渲染权限弹窗的实机验证；`reject_always`（协议有、Policy 无 per-Kind 拒绝清单）未接——目前不提供也不接受该选项。 |
| **session resume** | **已修**：`agentCapabilities.loadSession=true`（有持久化时），`session/load` 会读出那份 JSONL transcript、重新锚定 cwd、恢复会话模型车道、把整段历史按 `user_message_chunk` / `agent_message_chunk` / `tool_call(_update)` 回放给客户端，并在回放**之后**才回响应。`session/new` 返回的 `sessionId` 现在是**持久 transcript id**（客户端存下来才能继续；这也是 paseo 存 native session id 的依据）。paseo 据此开启 session.persistence。`--no-save` 在 ACP 车道够不到（flag 停在 `acp` 位置参数），所以实际部署里 `loadSession` 恒为 true；无持久化的兜底路径会退回项目名并申报 false。 | 唯一缺口：`sessionCapabilities.list`（paseo 的"列出历史会话"）仍未申报。 |
| **会话数** | 一个 `seek` 进程 = 一个会话（MVP）；`session/new` 第二次调用是重锚定已有会话，不新开。 | 支持多 `session/new` 复用同进程。 |
| **slash 命令** | `/plan`、`/model` 等 **TUI-only**，ACP 下无效。 | 设计 ACP 下可表达的子集（如 plan 模式经 capability 暴露）。模型选择已有协议通道替代（§5.2）。 |
| **capabilities** | **已修（P1-3）**：`initialize` 申报 `promptCapabilities.image=true` + `agentCapabilities.loadSession`（按持久化能力申报），并做**真正的版本协商**——响应带「客户端请求 ∩ seek 支持」里的最高版（现状只有 1），请求更高版则降级申报（响应即申报，stderr 另记一行 `acp: initialize negotiated requested=N answered=1 (downgrade)`）。embedded context 仍未申报（seek 不处理该块类型）。 | embedded-context 块（编辑器选中的代码片段）若要支持，另加 capability + 解析分支。 |
| **per-session cwd** | **已修**：`session/new` 的 `cwd` 生效——进程与权限策略一起切到该目录（Zed 传的就是启动目录，行为不变）。 | 多 session 各持独立栈（见 backlog ③）。 |
| **MCP 透传** | Zed 在 `session/new` 发 `mcpServers`，seek 暂未透传。 | 把 Zed 给的 MCP server 接进 seek 的 MCP 客户端。 |

### 5.1 无头权限开关 `SEEK_ACP_YOLO` / Headless permission switch

`seek acp` 以 `os.Args[1]=="acp"` 短路启动，而 `flag.Parse` 在第一个位置参数处就停止——**一个命令行 flag 都不消费**，`--yolo` 根本表达不到这条路。默认姿态（P0-2 起）是 `Ask`：危险动作经 `session/request_permission` 交给客户端（§5 审批行），**没有应答就拒**——交互场景（Zed）弹原生权限 UI；**无头场景**（paseo / ACP driver 派单，没有人回答）每个 bash 都等满超时才拒，效率极差。无头无人值守车道应该用下面的开关直接放行。

开关：给 ACP 进程注入环境变量（真值：`1`/`true`/`yes`/`on`，大小写不敏感；其它值一律视为关）：

```json
"env": { "SEEK_ACP_YOLO": "1" }
```

seek 在 ACP 车道**初始化时**就以 `PrefYolo` 起栈（不是事后翻转——系统提示词里的 Mode 标签和权限门必须一致），bash / cwd 外写全部放行，与 `--yolo` 同姿态。先例：`seek goal run`、autopilot 的"无人值守 → yolo"。

yolo 放开的只是**本地**操作：与 goal / autopilot 同款 no-remote 守卫同时挂上（主会话与 subagent 的 bash 都过 `IsRemoteMutating` 检查），`git push`、`gh pr create` 等 push/PR 类远程变更照旧被拒——无人值守车道能改本地、不能动远端。

> ⚠️ **仅受信本机 headless 场景使用**（paseo provider 的 `env` 块就是典型用法）。`PrefYolo` 放开的是本机 bash 与写文件（远程变更仍被 no-remote 守卫拦下）；env 会被原样继承给子进程，不要在不受信、共享或远程可达的环境里设置。默认关（unset = `Ask` 审批门，见 §5 审批行）；只在 `seek acp` 车道生效——shell 里残留的 `SEEK_ACP_YOLO` 对 TUI / print 模式**无**影响。

审批门超时（可选）：`SEEK_ACP_PERMISSION_TIMEOUT=<秒>`（默认 600）。一个权限请求等不到应答时，到点默认拒并继续回合——既防「客户端活着但永不回答」的挂死，也方便无头车道把等待压到几秒。`SEEK_ACP_YOLO=1` 时整个审批门不安装，此变量无效。

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

### 5.3 图片输入 / Image input（P1-3）

`initialize` 申报 `promptCapabilities.image=true`，所以 Zed 肯把附图发给 seek。之后每张图是一个 ACP image content block（`type:"image"`，`data` 是 base64，`mimeType` 如 `image/png`），seek **按块序**处理：

- **视觉模型**（如默认的 `deepseek-flash` / `deepseek-v4.1-flash`、`gpt-4o`、`claude-sonnet-4`、`gemini-2.0-flash`）：图片字节落进项目资产库、以原生 image part 随消息发出，模型直接看图；行内留一行 `[image: pasted-image.png — attached natively · 2x2 · …]` 作为对话记录的自我描述。
- **非视觉模型**：**不发送图片字节**，改为行内注一行 `[image: … — 当前模型不支持图片输入，用 /model 选一个支持视觉的模型]`，回合照常完成（提示模型自己建议换模型，而不是让 API 报 400）。
- **块序保持**：文本 + 图 + 文本 的混合 prompt，那张图的上述标记落在两段文字**中间**，不挪到末尾——写明「这是第一张，这是第二张」的 prompt 才读得对。
- **降级不炸**：坏 base64、不是图片的 `mimeType`、超过单图 32 MiB、资产库不可用——该块静默跳过或降级成一行说明，进程不崩、其余块照常。

**纯图 prompt** 也成一回合（旧行为是 `text==""` 直接 `end_turn`、什么都不做）。

不申报的东西：`embeddedContext`（编辑器选中的代码上下文）——seek 不处理该块类型，客户端因此不会发。真 Zed 附图的人工实机验证仍待补（沙盒里能证的是"块被收下、标记按序进了请求"：`cmd/seek/testdata/acp-image/*.json` + `cmd/seek/acp_image_e2e_test.go` 驱动真二进制对假端点断言）。

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
