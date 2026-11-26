# 在 Zed 里用 seek — ACP 编辑器集成指南 / Using seek inside Zed (ACP)

seek 实现了 **Agent Client Protocol (ACP)**，可作为一个**自定义 agent** 嵌进 [Zed](https://zed.dev)：在 Agent 面板里直接跟 seek 对话，它的工具调用（read / grep / edit / git…）会渲染成 Zed 原生的步骤卡片。

> seek speaks the Agent Client Protocol, so it plugs into Zed as a **custom agent** — chat with seek in Zed's Agent Panel; its tool calls render as native step cards.

> **定位**：这是 **MVP**。握手 + 流式回复 + 工具调用映射 + **session resume** + 审批门 + 模型选择 + **plan 模式（session modes）** + **多会话复用进程** 已跑通（真 Zed 验证 / 真 stdio e2e），但还简陋——多 session 的完善等见 §5 的优化清单（MCP 透传已于 c_oyt6ft 落地，见 §5.5）。设计见 [`docs/prd/archive/feature-acp.md`](./prd/archive/feature-acp.md)。

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
- ✅ **图片输入（P1-3）**：Zed 里给 seek 线程附图，seek 收下 ACP 的 image content block（`type:"image"` + base64 `data` + `mimeType`），**视觉模型原生看图**；非视觉模型则该图不发送、行内注一行「当前模型不支持图片输入，用 /model 选一个支持视觉的模型」，回合照常完成。坏 base64 / 非图 mimeType 静默跳过那一块，不崩（§5.4）。
- ✅ **流式回复**：文本逐 chunk 推送；**思考流走 `agent_thought_chunk` 独立通道**（2026-11-19 用户拍板开放，客户端渲染为可折叠思考区——主模型的 CoT 会出现；代价：CoT 会进客户端侧日志）。
- ✅ **工具调用渲染**：`read` / `grep` / `list_dir` / `git` / `edit` 等映射成 ACP 的 `tool_call` 卡片，含 in_progress → completed/failed 状态，且走**全协议字段**（2026-11-18）：`name` + `kind`（客户端图标/呈现分类）、`locations`（绝对文件路径 + 行号，客户端 follow-along）、`rawInput`/`rawOutput`（可折叠原始输入/输出，16KB 截断）、`content`——`edit` 发协议 diff 块（old/new 即改即视），其余工具发结果文本块（4KB 截断）。
- ✅ **真实编辑工作区**：seek 直接改你打开的项目文件（实测在 `.gitignore` 加了一行成功）。
- ✅ **取消**：Zed 中断一轮 → seek 收到 `session/cancel`，干净停。
- ✅ **终止可观测**（2026-11-21，c_vfgeam）：每轮 `session/prompt` 结束时，seek 在响应帧**之前**发一条结构化终止通知，`stopReason` 五值枚举 + 可读 detail + 原始 `finishReason`——断流/静默死亡不再无声，见 §4.1。
- ✅ **上下文余量上报（2026-11-21，卡 c_uzlczr）**：每个完成的模型回合通过 `session/update` 发一帧 ACP 的 `usage_update`，客户端据此显示余量条/百分比。字段是**协议自己的**（[RFC/RFD "Session Context Size and Cost"](https://agentclientprotocol.com/rfds/session-usage)）：`used`（当前上下文占用量）+ `size`（该模型窗口大小），余量与百分比由客户端自己算（`size - used` / `used/size`）。seek 额外把 `percent` / `severity` 放在协议预留的 `_meta.seek.ctx` 里，近阈值那一帧再多 `alert` + 一句 `notice`（详见 §5.6）。数字口径与 TUI 状态栏 `formatBudget`、Web 顶栏 ctxmeter **同源**：占用 = **末个回合**的 prompt+completion（累计值会随历史逐轮重发二次膨胀），窗口 = `internal/budget.Limit(model)`，阈值 = `budget.Classify` 的 0.60/0.75。零新数据源、零新表。

---

## 4.1 终止可观测 / Turn termination observability

一轮对话怎么结束的，现在是**协议面上一等公民**，不再是"没消息就是好消息"的猜谜。两层结构：

**① `session/prompt` 响应的 `stopReason`（ACP 规范枚举）**：`end_turn` / `max_tokens` / `refusal` / `cancelled`。以前 seek 对一切正常路径都答 `end_turn`、把 `finish_reason=length` 断流藏在一个泛型 JSON-RPC 错误里；现在长度断流答 **`max_tokens`**（规范自己的值，Zed 认识），拒答答 `refusal`。内部错误仍走 JSON-RPC 错误帧（-32000 + detail 文本）——那是规范的错误通道。

**② 终止通知帧 `_seek_turn_stop`（seek 扩展）**：每轮结束、响应帧**之前**，发一条：

```json
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"…","update":{
  "sessionUpdate":"_seek_turn_stop",
  "stopReason":"max_tokens",
  "detail":"agent: response truncated (finish_reason=length, max_tokens=8192) — use /compact to free context or ask me to continue",
  "finishReason":"length"
}}}
```

`stopReason` 枚举（五值 + 规范词表复用）：

| 值 | 含义 | 典型来源 |
|---|---|---|
| `end_turn` | 正常完成 | `finish_reason=stop`（含空补全自愈占位轮） |
| `max_tokens` | 长度断流 | `finish_reason=length`（DeepSeek/OpenAI 兼容/opencode 车道；Gemini 归一成 length）/ `max_tokens`（Anthropic 原样透传）——**c_qpcdtc 定性的 opencode 断流家族在 ACP 面就此可辨** |
| `refusal` | 模型拒答 | DeepSeek `refusal` / `content_filter` / Anthropic `refusal` |
| `silent_timeout` | 静默超时 | 上游停更、HTTP 5 分钟 deadline 兜底炸出（`context deadline exceeded` / net timeout）；或 driver 侧给回合设的期限先到 |
| `cancelled` | 客户端取消 | `session/cancel` / ctx cancel |
| `internal_error` | 内部错误 | API 报错、流解码失败、**已恢复的 panic**（栈在 detail 里） |

`detail` 永远人类可读；`finishReason` 是原始上游 finish_reason，机读不用解析 prose——paseo/butler 据此秒判死亡形态（c_w3zimi 检测卡的 agent 侧一半）。

**进程死亡判别规则（给无头 driver）**：终止帧**每轮恰一条、永远先于响应帧**。若 `session/prompt` 既无响应也无终止帧（stdin 还开着），唯一解释是 **seek 进程本身死了**（kill -9 / 未捕获的内核级崩溃）——进程死无法自报，帧的缺席就是信号，客户端应在自身超时上直接归因"agent 进程死亡"而非"上游慢"。已知边界：工具/主循环 panic 已在进程内恢复成 `internal_error`；网络层断供由 5 分钟 HTTP 超时兜成 `silent_timeout`。

**兼容性**：`_seek_turn_stop` 用 ACP v2 schema 为实现方扩展保留的下划线前缀命名。v2 客户端把它收进 `Other` 变体；**v1 schema 客户端（当前 Zed）会丢弃未知 tag 的通知并记一行日志**——ndJson 每行独立，丢一条不影响流，Zed 从响应 `stopReason` 拿同一信息（`max_tokens` 它本来就认）。paseo 的 JS 客户端 switch 不命中即忽略，随后按需消费。用 `SEEK_ACP_LOG`（§6）可以直接看到这些帧的原始 JSON。

设计出处：`internal/acp/stopreason.go`（枚举 + 分类器，含 wire-shape 测试）、`cmd/seek` `acpBackend.Prompt`（逐出口发帧 + 分类）、`pkg/agent`（`AgentEnd.Finish` 携带最终 finish_reason；panic 恢复成 ErrorEvent）。

---

## 5. 已知限制 + 后续优化 / Known limits & optimization backlog

这是 MVP，**简陋**，以下是真实的边界与后续可优化项（欢迎按需推进）：

| 项 | 现状 | 后续优化方向 |
|---|---|---|
| **审批门 / approval** | **已修（P0-2）**：非 yolo 的 ACP 车道权限姿态为 `Ask`，危险动作经 `session/request_permission` 弹到客户端原生权限 UI（"允许 seek 运行 X?"），三选项 allow_once / allow_always / reject_once；`allow_always` 即 TUI 的「[a] always: \<kind\>」语义（同 Kind 本会话不再问）。**无应答默认拒**：客户端断线、超时（默认 10 分钟，`SEEK_ACP_PERMISSION_TIMEOUT` 秒数可调）、或无头 driver 回 `-32601`，一律拒绝并照常完成回合——不会卡死。无头无人值守场景仍用 `SEEK_ACP_YOLO=1` 直接放行（§5.1，优先级高于审批门）。 | Zed 客户端面板较旧、暂不渲染权限弹窗的实机验证；`reject_always`（协议有、Policy 无 per-Kind 拒绝清单）未接——目前不提供也不接受该选项。 |
| **session resume** | **已修**：`agentCapabilities.loadSession=true`（有持久化时），`session/load` 会读出那份 JSONL transcript、重新锚定 cwd、恢复会话模型车道、把整段历史按 `user_message_chunk` / `agent_message_chunk` / `tool_call(_update)` 回放给客户端，并在回放**之后**才回响应。`session/new` 返回的 `sessionId` 现在是**持久 transcript id**（客户端存下来才能继续；这也是 paseo 存 native session id 的依据）。paseo 据此开启 session.persistence。`--no-save` 在 ACP 车道够不到（flag 停在 `acp` 位置参数），所以实际部署里 `loadSession` 恒为 true；无持久化的兜底路径会退回项目名并申报 false。 | **已修**（c_chzxgg，2026-11-21）：initialize 现在同时申报 `sessionCapabilities.list`（schema 形状是对象 `{}`，在场即"支持"，不是布尔；与 loadSession 同一道持久化闸），`session/list` 扫 transcript 存储返回 `{sessions:[{sessionId,cwd,title,updatedAt}]}`——数据源与 `session/load` 同一个 store（列出的 id 必可 load），标题取首条 user message 首行（80 rune 截断，与 serve `/rpc session/list` 同一派生），mtime 是 header 的 UpdatedAt（RFC3339）。空仓答 `sessions:[]`（数组恒在，不是 null）；`cwd` 过滤可选；不分页（单页全量，不回 nextCursor）。**未申报 `sessionCapabilities.delete`**——删 transcript 走 TUI/文件系统，不在协议上开口。 |
| **会话数** | **已修（P1-2，backlog ③）**：一个 `seek` 进程可同时服务 **N 个会话**——`session/new` 第二次调用**新开一会话**（新的持久 transcript id，首个完成的回合后落盘），不再重锚定已有会话；第一个会话仍延续启动时的 transcript（单会话行为不变）。**per-session 各自独立的是四根轴**：cwd 锚（每次该会话的回合把进程+权限策略锚到它的目录）、权限策略的会话轴（plan 模式 + `[a]` 常许清单，各会话互不影响）、历史栈（各写各的 transcript）、模型车道（一个会话换模型不动另一个）。实现是**回合级时间复用**：agent/工具注册表/进程 cwd 是一台共享机器，跨会话的回合在 turn 闸上排队串行（两会话并发 prompt 不串扰，但不同时跑）；`session/set_config_option` / `set_mode` / `session/load` 的「回合间才受理」窗口相应变为「任一会话无回合在跑时」。**仍是进程级（非 per-session）的**：系统提示/memory/skills/checkpoint 子代理挂载——都按启动目录焊死，与会话 cwd 无关（单会话时代即如此）。ACP v1 无 session/delete，会话状态随进程存亡。 | 并行回合（每会话独立 agent+注册表）需要把 1700 行启动接线按会话重建，收益是跨会话真并发——当前回合串行对 Zed/paseo 的人机节奏无感，若出现跨会话长回合互等的真实场景再立卡。 |
| **slash 命令** | **已修（P2，backlog ④）**：`/plan` 经 **session mode 通道**协议化——`session/new`/`session/load` 回 `modes` 能力（`currentModeId` + `availableModes: [default, plan]`），客户端用 `session/set_mode` 切换，成功后 `current_mode_update` 通知先于 `{}` 响应（§5.3）。**其余 slash 维持 TUI-only**（选型理由逐条列在 §5.3）。模型选择早已有协议通道（§5.2 configOptions）。 | 后续若接 propose 的人工审批门（plan 态 propose→approve 的协议承载），plan-execute 子态才有 ACP 形态；当前 ACP 下 plan 态 = 只读分析 + 计划文本，执行需切回 default。 |
| **capabilities** | **已修（P1-3）**：`initialize` 申报 `promptCapabilities.image=true` + `agentCapabilities.loadSession`（按持久化能力申报），并做**真正的版本协商**——响应带「客户端请求 ∩ seek 支持」里的最高版（现状只有 1），请求更高版则降级申报（响应即申报，stderr 另记一行 `acp: initialize negotiated requested=N answered=1 (downgrade)`）。embedded context 仍未申报（seek 不处理该块类型）。**读侧**：`initialize` 现在也解 `clientCapabilities`，只读 `elicitation.form`——它是 ask_user 车道能否可达的唯一开关（§5.7）。 | embedded-context 块（编辑器选中的代码片段）若要支持，另加 capability + 解析分支。 |
| **per-session cwd** | **已修**：`session/new` 的 `cwd` 生效——进程与权限策略一起切到该目录（Zed 传的就是启动目录，行为不变）。多会话下（P1-2）cwd 是**会话轴**：每个会话的回合把两半锚（进程 chdir + policy reanchor）切到自己的目录，回合间不漂移（见 §5 会话数行）。 | — |
| **MCP 透传** | **已修**：`session/new`（以及 `session/load`）的 `mcpServers` 现在生效——stdio 条目按声明起子进程、接进 seek 的 MCP 客户端栈（与 `~/.seek/mcp.json` 同一套 `pkg/mcp` 传输 + 握手 + `tools/list`），工具以 **`mcp__<server>__<tool>`** 进会话工具面（三段式，与 mcp.json 的 `<server>__<tool>` 可区分）；会话结束（stdin EOF / 进程退出）即断连回收子进程。http/sse/acp 变体跳过并在 stderr 说原因，**不**让 `session/new` 失败。不带 `mcpServers` 时零变化（见 §5.5）。 | 多 session 各自一套 roster（现多会话已落地但 roster 仍进程级共享，声明变化只告警不热换）。 |

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

`session/new` 现在会返回 `configOptions`（一个 `id:"model"` 的 select 型下拉）：凡 key 可解析的 provider（deepseek / anthropic / openai / gemini / glm / opencode / opendesign）各带自己的型号入列。**ACP 客户端（paseo）据此渲染模型下拉**；选择后客户端发 `session/set_config_option`，seek 在**回合之间**把整条车道（provider 客户端 + model）一次换掉——下一个 `session/prompt` 就用新模型。Zed 侧暂不渲染 configOptions 下拉（其 ACP 面较旧），但换挡机制对任何实现了该协议的客户端一致。

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

**模型 × 车道一致性闸**（2026-11-25，c_dxrjjl）：只钉 `SEEK_ACP_MODEL` 而不钉 provider 时，车道由 auto-detect 决定，而宿主传下来的可能是一条网关 wire id（如 `deepseek-v4.1-flash-eco`）。启动前会过 `internal/modelcap.LaneOf`：若该 id 只属于某条订阅网关车道（opencode / opendesign）而生效车道不是它，进程**启动即报错**并点名该钉哪条：

```
acp startup: model "deepseek-v4.1-flash-eco" belongs to lane "opendesign" (OpenDesign 网关)
but this session would start on lane "deepseek" — set SEEK_ACP_PROVIDER=opendesign (plus OPENDESIGN_API_KEY) …
```

这是刻意的：错配 id 打到官方 DeepSeek 会回 `invalid_request_error: The supported API model names are deepseek-flash, deepseek-v4-pro`，那个错误出现在对话中段，模型与用户都无从修正。闸门**放行**的情形——同一 id 另一条订阅车道也收录（两条网关共享 deepseek-v4.1-flash / deepseek-v4-pro / kimi-k2.7-code 等，判据是 `modelcap.Serves`，共享 id 走哪条车道都对）、`LaneOf` 判不出的自定义 id（`--provider=compatible` 自建端点）、以及与 provider 同厂前缀的第一梯队 id（`glm-5.3-flash` 之于 GLM）——它不猜别人的命名空间。

wire 形状与参照实现出处：[`docs/prd/archive/feature-paseo-provider.md`](./prd/archive/feature-paseo-provider.md) §3.4（C 落地记录）。

### 5.3 会话模式 / Session modes（/plan 的协议通道，P2）

`/plan` 不再是 TUI 专属：plan 模式经 ACP 规范自己的 **session modes** 语义暴露（agentclientprotocol.com/protocol/session-modes）——不是自创扩展。

**能力申报**（capability 半边）：`session/new` 与 `session/load` 的结果带 `modes` 字段：

```json
"modes": {
  "currentModeId": "default",
  "availableModes": [
    {"id": "default", "name": "Default", "description": "…"},
    {"id": "plan",    "name": "Plan",    "description": "…"}
  ]
}
```

客户端（paseo / 未来 Zed）据此渲染模式切换器；老客户端把未知字段当普通 JSON 忽略，无副作用。

**切换通道**（mode 半边）：`session/set_mode {sessionId, modeId}`——`modeId` 必须是 `availableModes` 里申报过的。seek 受理后先发一条 `session/update`（`current_mode_update` + 新 `currentModeId`）**再**回 `{}` 响应（响应本身不带 mode 信息，通知就是确认）；未知 mode 回 `-32000` 并点名可用值；**prompt 进行中回 `-32000` 拒绝**（与 §5.2 换模型同规则：模式翻转共享权限工作流状态，回合间才安全——规范允许任意时刻发起，seek 诚实地拒绝并让客户端回合作间重试）。

**语义映射**：`plan` == plan 模式的 analyze 子态（`WorkflowPlanAnalyze`，只读门：write/edit/bash 全拒，拒绝文案指向 propose）；`default` == 退出 plan（`WorkflowNone`）。切换同时更新**下一轮** user message 的 mode 提醒词（prefix-cache 安全，不改系统提示——TUI `/plan` 同款机制）。`plan-execute` 子态（propose 被人工批准后）**不是**第三个 wire mode：它的入口是 propose 审批 picker，ACP 车道暂无该承载（见上行 backlog），两态表面保持 default/plan 一进一出。

**边界（诚实清单）**：plan 态模型被提醒词指示走 `propose`，但 propose 的人工审批门（TUI picker 四选项）在 ACP 车道无协议承载——ACP 下 plan 态的现实用途是**只读分析 + 计划文本产出**，人审后切回 `default` 执行。plan 态两态行为差异有真二进制 e2e 钉死（`cmd/seek/acp_mode_e2e_test.go`：plan 态 write 被拒不落盘 + 提醒词在轮内；default 态同一 write 落盘 + 提醒词消失）。

**`ask_user` / `propose` 在 ACP 车道**（2026-11-25 更新：从「不存在」变成「客户端申报才存在」）。要点：**协议有自己的方法**——`elicitation/create`（form 模式），由 `initialize` 的 `clientCapabilities.elicitation.form` 门控。seek 现在就用它：客户端在握手里申报了 form elicitation，`askPolicy` 才在这一条车道上被点成 `ModeAsk`，`ask_user` / `propose` 的 2–4 选项（含 batch 多题、自由文本）渲染成客户端的表单；没申报的客户端**逐字节维持旧行为**（`ErrDisabled`）。§5.7 是完整说明。

为什么不再走 `session/request_permission`：那个方法的语义被权限钉死（三选项 allow_once / allow_always / reject_once，无 free-text，无多题），硬塞会把它渲染成「是否允许」而不是「你选哪个」。`elicitation/create` 是协议为此设计的请求型方法，形状（属性名 / enum / 多选数组）由 seek 的 schema 决定，客户端只负责渲染。

没申报 elicitation 的客户端（含 Paseo 0.17.3 的默认基线、以及所有现有测试的假客户端）行为是可预期的：`ask_user` / `propose` 返回 **`ErrDisabled`** ——「interactive question not available in this mode — rephrase as a plain-text question in your reply」，模型据此把问题改写进正文，回合照常完成。这条文案是有意的指令面（不是日志），2026-11-23 之前这里报的是 `ErrNoCallback`（"... (programming error)"），模型读到"程序错误"会重试同一个调用；现在 `askPolicy` 出生即 `ModeDisabled`，TUI 与申报了 elicitation 的 ACP 客户端各自在自己入口显式 `SetMode(ModeAsk)`，新加的车道默认继承正确姿态。钉死用例：`cmd/seek/askuser_lane_test.go`（print 与「不申报的 ACP」两条车道）+ `cmd/seek/acp_askuser_e2e_test.go`（申报的 ACP，四条 lane：答题 / 拒答 / 超时 / 不申报）。

**①选型：为什么首批只协议化 /plan、其余维持 TUI-only**：

- `/model` —— 已有协议通道（§5.2 configOptions 下拉 + `session/set_config_option`），不重复造。
- `/yolo` —— 车道姿态，启动期 env `SEEK_ACP_YOLO` 表达（§5.1）。不做会话中翻转：pref 与烤进系统提示的 Mode 标签必须一致（P0-2 的脱钩教训），会话中翻转会把「prompt 说的姿态」和「门实际姿态」拆开。
- `/compact`、`/reset`、`/new`、`/resume`、`/clear` —— 会话/上下文生命周期在 ACP 下归**客户端**管（`session/new`、`session/load` 就是客户端的动词），band 内再造一套等于一个会话两个主人。
- `/lang`、`/skill`、`/agents`、`/distill` 等 —— TUI 本地 UI/会话状态或人审门（skill 安装铁律），没有客户端可消费的协议形态；真需要时逐个立卡。
- 原则：**ACP 车道不做「prompt 里伪 slash 解析」**——模型不该看到 slash；凡是「模式/配置」类语义走协议方法（modes / configOptions），凡是「会话生命周期」类语义留在客户端。

### 5.4 图片输入 / Image input（P1-3）

`initialize` 申报 `promptCapabilities.image=true`，所以 Zed 肯把附图发给 seek。之后每张图是一个 ACP image content block（`type:"image"`，`data` 是 base64，`mimeType` 如 `image/png`），seek **按块序**处理：

- **视觉模型**（如默认的 `deepseek-flash` / `deepseek-v4.1-flash`、`gpt-4o`、`claude-sonnet-4`、`gemini-2.0-flash`）：图片字节落进项目资产库、以原生 image part 随消息发出，模型直接看图；行内留一行 `[image: pasted-image.png — attached natively · 2x2 · …]` 作为对话记录的自我描述。
- **非视觉模型**：**不发送图片字节**，改为行内注一行 `[image: … — 当前模型不支持图片输入，用 /model 选一个支持视觉的模型]`，回合照常完成（提示模型自己建议换模型，而不是让 API 报 400）。
- **块序保持**：文本 + 图 + 文本 的混合 prompt，那张图的上述标记落在两段文字**中间**，不挪到末尾——写明「这是第一张，这是第二张」的 prompt 才读得对。
- **降级不炸**：坏 base64、不是图片的 `mimeType`、超过单图 32 MiB、资产库不可用——该块静默跳过或降级成一行说明，进程不崩、其余块照常。

**纯图 prompt** 也成一回合（旧行为是 `text==""` 直接 `end_turn`、什么都不做）。

不申报的东西：`embeddedContext`（编辑器选中的代码上下文）——seek 不处理该块类型，客户端因此不会发。真 Zed 附图的人工实机验证仍待补（沙盒里能证的是"块被收下、标记按序进了请求"：`cmd/seek/testdata/acp-image/*.json` + `cmd/seek/acp_image_e2e_test.go` 驱动真二进制对假端点断言）。

### 5.5 MCP 透传 / MCP passthrough（session/new 的 `mcpServers`）

ACP 客户端可以在 `session/new`（和 `session/load`）里带一份 MCP roster，seek 会**按声明**把这些 server 接进会话——这是 backlog ⑦，也是"把任意工具面喂进 seek 会话"的通道（butler 卡面 MCP 这类跨系统组合）。

**声明 → 起连 → 进工具面 → 随会话清理**，四步各自落点：

| 步 | 做什么 | 代码 |
|---|---|---|
| 声明 | 解 `mcpServers` 数组；只认 `stdio`（`pkg/mcp` 就是 stdio 传输），`http`/`sse`/`acp` 跳过并在 stderr 说明 | `internal/acp/mcp.go` |
| 起连 | **复用 `~/.seek/mcp.json` 那一套客户端栈**：`mcptool.LoadServers` → `pkg/mcp.StartServer`（握手 + `tools/list`，无第二套实现） | `internal/tools/mcptool`、`cmd/seek/acp_mcp.go` |
| 进工具面 | 注册进同一个 registry，名字 **`mcp__<server>__<tool>`** | `cmd/seek/acp_mcp.go` |
| 随会话清理 | 会话结束（stdin EOF / 进程退出）→ 关客户端 → 子进程被 kill + 回收 | `cmd/seek` 的 `runACP` defer |

几点必须知道的边界：

- **名字是 `mcp__<server>__<tool>`，不是 `mcp.<server>.<tool>`。** 点号过不了模型 API 的函数名约束（`^[a-zA-Z0-9_-]{1,64}$`，DeepSeek / OpenAI / Anthropic 一致），首个请求就会 400。三段式把客户端注入的 server 与 `mcp.json` 的 `<server>__<tool>` 分开；server 名与工具名都会折进上面那个安全字母表（`card.read` → `mcp__butler__card_read`）。
- **声明里给的 `env` 是子进程拿到凭据的唯一途径。** seek 先按 `internal/childenv` 擦掉父环境里的凭据形状变量（`*_API_KEY`、`*_TOKEN`、`SEEK_*` …），再叠加声明里的 `env`——所以 server 需要 key 就明写在声明里。
- **坏声明不失败。** 起不来的 server / 不支持的传输只写 stderr，`session/new` 照常回 `sessionId`；会话照常能用，只是少了那批工具。
- **一个进程 = 一个 session = 一份 roster。** 重复的 `session/new` 带同一份声明是幂等 no-op（paseo 重锚时会重发）；带**不同**声明只告警、不热换（registry 无法遗忘已注册的工具，换 roster 请重开 `seek acp`）。
- **不进 system prompt。** `MCPSection` 在启动时组装并冻结（前缀缓存纪律），注入的 server 只出现在 tools 数组里。模型靠工具名与 description 认它。
- **roster 必须在会话首轮之前到。** tools 数组在首轮就冻结（`Registry.Wire`），此后再来的 roster 会被拒并打一行说明——那些工具模型根本看不到，起了子进程也没人调；重开 `seek acp` 即可。实践中客户端在 `session/new` 就带上，所以不会撞到。
- **不带 `mcpServers` 时零变化**：不起子进程、不动 registry、stderr 一行 `acp: mcp:` 都不会多出来（`scripts/acp-smoke-test.sh` 的两态断言之一）。

调试就看 stderr 的 `acp: mcp:` 行——连接/注册/回收各一行，工具名会原样列出：

```
acp: mcp: registered 2 tool(s) from 1 server(s): mcp__butler__card_read mcp__butler__echo
acp: mcp closed 1 server(s)
```

### 5.6 上下文余量 `usage_update` / Context-window reporting（P0-4）

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

---

### 5.7 `ask_user` / `propose` 的协议承载 `elicitation/create`

**一句话**：客户端在 `initialize` 里申报 `clientCapabilities.elicitation.form`，ask_user/propose 就在这条车道上可达；不申报就维持 `ErrDisabled`。门控在能力上，不在车道名上。

**握手（客户端 → seek）**：

```json
{"jsonrpc":"2.0","id":0,"method":"initialize","params":{
  "protocolVersion":1,
  "clientCapabilities":{
    "fs":{"readTextFile":false,"writeTextFile":false},
    "terminal":true,
    "elicitation":{"form":{}}
  },
  "clientInfo":{"name":"Zed","version":"…"}}}
```

`elicitation.form` 是**对象在场即支持**（`{}` 就是开），与 `sessionCapabilities.list` 同一套 schema 约定；`null` 与省略等价，都算不支持。seek 只读这一个字段（`internal/acp/elicitation.go` 的 `ClientCapabilities.SupportsElicitationForm`）。

**请求（seek → 客户端）**：

```json
{"jsonrpc":"2.0","id":"p1","method":"elicitation/create","params":{
  "sessionId":"20261125-…",
  "mode":"form",
  "message":"The assistant needs an answer before it can continue.",
  "requestedSchema":{
    "type":"object",
    "title":"seek asks",
    "properties":{
      "q1":{"type":"string","title":"Which scope?",
            "oneOf":[{"const":"user","title":"user","description":"private"},
                     {"const":"project","title":"project"}]},
      "q1_other":{"type":"string","title":"Which scope? (or type your own answer)"}
    }}}}
```

形状约定：

- **单选**用 `oneOf`（带 title → 客户端渲染标签而不是裸 id），不用 `enum`。
- **多选**（`multi_select:true`）用 `type:"array"` + `items:{type:"string",enum:[…]}`，`minItems:1` / `maxItems:len`。数组属性**没有** per-item title，所以选项标签 + 描述折进字段的 `description`（换行分隔）。
- **batch**（1–4 题）是**一张表单的多个字段**：第 i 题占 `q<i>`（枚举）与 `q<i>_other`（自由文本）。
- **`required` 恒为空**：`_other` 是枚举的替代品，强制任何一边都会让另一边无法作答。
- **`toolCallId` 省略**：askuser 触发点上没有 ACP 可见的 tool-call id，schema 里它是可选的。

**响应（客户端 → seek）**：

```json
{"jsonrpc":"2.0","id":"p1","result":{"action":"accept","content":{"q1":"project"}}}
```

`action` 三值：`accept`（带 content）/ `decline`（有意的拒绝）/ `cancel`（用户退出）。seek 把后两者都映射成 `Answer{Cancelled:true}` —— askuser 的 Answer 只有一位"没答"，但线上保留区别，客户端自己的 UX 可以不同。

映射细节（`cmd/seek/acp_askuser.go` 的 `answersFromElicitation`）：

- `q<i>` 的 id **必须**在提供的选项里；不在就是客户端 bug，丢弃该题（同 `ParsePermissionOutcome` 的"闭合菜单"姿态），不新造选项。
- 某题两个字段都空 → 该题 `Answer{Cancelled:true}`，**其余题不受影响**（per-index 契约）。
- **自由文本压过枚举选择**：`q<i>_other` 非空时 `q<i>` 被丢弃 —— 与 TUI picker 的互斥规则一致（选「Other」会清空已勾选行），因为 Answer 的回复 schema 是"二选一"。
- 多选里非字符串元素被丢弃而不是整题失败——部分可读仍比全丢有用。

**非应答收尾**（全部落到 `Cancelled`，绝不挂死回合）：

| 情形 | 结果 | stderr |
|---|---|---|
| 客户端回 `decline` / `cancel` | 整批 Cancelled | — |
| 超时（`SEEK_ACP_ASK_TIMEOUT` 秒，默认 600） | 整批 Cancelled | `acp: ask_user unanswered: acp: no reply from client (timeout)` |
| 客户端回 `-32601`（申报了但没实现） | 整批 Cancelled | 同上，带 JSON-RPC 错误文本 |
| 断线 / turn 被 `session/cancel` | 整批 Cancelled | `acp: ask_user unanswered: …` |
| 回合外被调用（不该发生） | 整批 Cancelled | `acp: ask_user not delivered: no active turn to ask in` |

**为什么不是 `session/request_permission`**：那个方法的语义被权限钉死（三选项 allow_once / allow_always / reject_once，无 free-text，无多题）。`elicitation/create` 是协议为此设计的请求型方法，语义由 seek 的 schema 决定。

**客户端现状（2026-11-25 实测，写下来免得下次重查）**：

- **Paseo 0.17.3 默认不申报**。它的客户端能力基线是写死的常量 `{fs:{readTextFile:false,writeTextFile:false},terminal:true}`（`app.asar` 里的 `BASE_ACP_CLIENT_CAPABILITIES`），`/tmp/seek-acp.log` 里 4552 次真实握手全部是这个形状。它的 provider 定义支持 `clientCapabilities` 覆盖（`providerOptions.clientCapabilities ?? this.clientCapabilities`），所以 `~/.paseo/config.json` 的 `agents.providers.seek` 可加：

  ```jsonc
  "seek": {
    "extends": "acp",
    "clientCapabilities": { "elicitation": { "form": {} } }
  }
  ```

  ⚠️ 但 Paseo 是否真的实现了 `elicitation/create` 的 handler **未实测**；如果它只声明不处理，请求会走上面的超时路径（工具结果仍是可读的 cancelled，回合不判死），表单不会出现。**先在编辑器里试一次**，用 `SEEK_ACP_LOG` 看 `>> "method":"elicitation/create"` 之后有没有 `<< "result"`。
- **zcode bridge 已实现同一条判断**（`zcode-acp-server/dist/server.js:1313` 的 `supportsElicitationForm()`，注释自述"When true, AskUserQuestion / ExitPlanMode can use the richer elicitation/create form UI; otherwise they fall back to session/request_permission"）——证明这条路在本机生态里是活的，不是纸面协议。

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
