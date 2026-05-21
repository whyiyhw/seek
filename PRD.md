# seek PRD：DeepSeek-first Go Coding Agent Harness

> 状态：v0.4（项目改名 + 兼容其他 Provider）
> 项目名：**`seek`**（旧名 `pi-go`，目录待重命名）
> 目标读者：项目发起人 / 早期贡献者
> 架构参考：https://github.com/earendil-works/pi （TypeScript，MIT，~52K stars）

## 一句话定位

**为 DeepSeek 深度优化、同时兼容 Claude / GPT / Gemini 的单二进制 Go Coding Agent Harness。** DeepSeek 是一等公民（缓存优化、Reasoner、FIM、错峰计价），其他 Provider 是 best-effort 兼容——你想用 Claude 也跑得起来，但拿不到 DeepSeek 那些专属优化。

## 决策记录（已确认）

| 决策项 | 选定方案 | 备注 |
|---|---|---|
| **项目名** | `seek` | 「DeepSeek」+「seek answers」双关，4 字母好打 |
| **核心定位** | DeepSeek-first，其他 Provider 兼容（§1.5、§4.1） | 不是 DeepSeek-only |
| **Provider 一等公民** | DeepSeek 官方 + DeepSeek-兼容端点（vLLM / Ollama / SiliconFlow） | 享受全部专属优化 |
| **Provider 二等兼容** | Anthropic、OpenAI、Google Gemini | 仅基础 chat + 工具调用，无缓存优化 / FIM / Reasoner 模式 |
| TUI 框架 | `charmbracelet/bubbletea` + `lipgloss` + `glamour` | 不自研 |
| 扩展机制 | MCP（语言无关协议） | 不嵌入 JS 引擎 |
| Skill 机制 | Markdown + frontmatter；兼容识别 `.claude/skills/` 与 `~/.claude/skills/`（见 §4.6） | 方便从 Claude Code 迁移 |
| Reasoner 集成深度 | 基础 `/think` 命令 + 完整「reasoner-then-chat」双模型协作内置 skill | v1.0 全做 |
| OAuth | 不做（v1.0 仅 API key） | |
| Batch API | 不做（延后 v1.1+） | |
| 目标用户 | 个人 / 团队（中文开发者优先） | 不做官网、安装器服务 |
| 核心动机 | 单二进制 + 干净供应链 + DeepSeek 性价比最大化 | |

---

## 1. 背景与动机

### 1.1 上游 pi 是什么

`earendil-works/pi` 是 TS 实现的通用 AI Coding Agent 工具链，支持 ~25 个 LLM Provider，4 个包（pi-ai / pi-agent-core / pi-coding-agent / pi-tui）。它是一个**架构参考**——我们借鉴它的事件流模型、工具调用循环、Skill 文件格式、TUI 思路，但**不复制它的多 Provider 抽象层**。

### 1.2 为什么 DeepSeek-first 而非 DeepSeek-only

最初考虑做纯 DeepSeek-only，但兼容其他 Provider 的边际成本不高（约 2 周）、却显著降低 lock-in 风险。最终路线：

- **一等公民（DeepSeek）**：所有 DeepSeek 专属优化（见 §1.5）全部启用
- **二等公民（Anthropic / OpenAI / Gemini）**：基础 chat + 工具调用 + 流式可用，但不享受缓存优化、FIM 快路径、Reasoner 路径
- 在 UI 和文档里**明确标注差异**，避免用户误以为「换个 Provider 体验一样」
- 这样既能保住 DeepSeek 的差异化，又给用户留一条退路

### 1.3 为什么 Go

1. **分发**：单二进制 + 零运行时依赖，对终端用户安装体验远好于 `npm i -g`
2. **供应链**：Go module + `go.sum`，攻击面远小于 npm
3. **并发**：goroutine + channel 天然契合 Agent 的流式事件 + 并行工具执行模型
4. **冷启动快**：对 RPC 模式（子进程被宿主反复拉起）友好
5. **生态对齐**：目标用户的项目大多本就是 Go

### 1.4 为什么 DeepSeek

- **性价比**：相同任务 token 成本约为 Claude/GPT-4 的 1/10–1/20，缓存命中后再降 10x
- **中文友好**：对中文代码注释、中文 prompt 表现更好
- **`deepseek-reasoner`**：开源/对手价位上少见的强推理模型，可用于关键决策点
- **API 稳定**：OpenAI 兼容接口为主，迁移成本低

### 1.5 DeepSeek 专用 harness 能做、通用 harness 不会做的事

这是项目的**差异化护城河**，每一条都对应 v1.0 的具体功能：

| DeepSeek 特性 | 通用 harness 做法 | seek 做法 |
|---|---|---|
| **Context Caching**（前缀缓存自动命中、命中后价格 1/10） | 不感知 | 主动把 system prompt + 工具 schema + 历史消息组织成稳定前缀；TUI 显示每轮命中率；提供 `/cache-stats` 命令 |
| **`deepseek-reasoner`** 模型（不支持 tool call，`reasoning_content` 必须从下一轮剥离） | 当普通模型处理，踩坑 | 独立代码路径；提供「双模型协作模式」：`deepseek-chat` 主跑工具调用，关键节点切到 reasoner 反思后再回来 |
| **FIM 端点**（fill-in-the-middle 补全，比 chat 便宜 5–10x） | 不用 | 内置 `edit` 工具在小范围补丁时优先走 FIM；大范围改动 fallback 到 chat |
| **错峰计价**（北京时间 00:30–08:30 折扣 50–75%） | 不知道 | TUI 角标显示当前价格档位；`/compact` 等批量动作可建议错峰跑 |
| **JSON 严格模式** | 弱依赖 | 工具调用强制走 JSON 模式，降低解析失败率 |
| **中文优势** | 系统 prompt 全英文 | 系统 prompt、工具描述、`/help` 等双语；中文 codebase 注释/变量名识别更稳 |

### 1.6 主要风险

| 风险 | 缓解 |
|---|---|
| DeepSeek 服务可用性 / API 变更 / 价格政策变动 | 三层退路：DeepSeek-兼容端点（vLLM / Ollama / SiliconFlow）→ Anthropic → OpenAI |
| 用户也想跑 Claude / GPT 时怎么办 | v1.0 已支持，但是二等兼容，不享受 DeepSeek 专属优化 |
| 上游 pi 频繁迭代 | 只借架构思路，不追特性，没有同步压力 |
| 多 Provider 抽象「腐蚀」DeepSeek 优化 | `pkg/deepseek` 与 `pkg/llm/provider/*` 严格分层，DeepSeek 专属优化绝不下沉到通用接口 |

---

## 2. 目标与非目标

### 2.1 目标（v1.0）

- **G1**：CLI `seek` 单二进制，覆盖上游 `pi-coding-agent` 的核心交互场景：interactive、print、JSON 输出、RPC（stdio + JSON-RPC）
- **G2**：内置 4 个核心工具：`read` / `write` / `edit` / `bash`，行为与上游对齐（含 patch 风格 edit）
- **G3a（一等公民）**：DeepSeek 全面接入
  - DeepSeek 官方：`deepseek-chat`、`deepseek-reasoner`、FIM 端点
  - DeepSeek-兼容端点：vLLM 自托管、Ollama（DeepSeek-V3 蒸馏版）、SiliconFlow 等
- **G3b（二等兼容）**：Anthropic Claude / OpenAI GPT / Google Gemini
  - 仅基础 chat + 工具调用 + 流式
  - 明确不支持：缓存命中率统计、FIM 快路径、Reasoner-then-Chat 双模型模式（这些是 DeepSeek 专属）
- **G7（DeepSeek 专属）**：缓存友好的上下文组织 + 缓存命中率可视化
- **G8（DeepSeek 专属）**：`deepseek-reasoner` 独立代码路径，**v1.0 全做**：基础 `/think` 命令 + 完整「reasoner 反思 + chat 执行」双模型协作内置 skill
- **G9（DeepSeek 专属）**：`edit` 工具支持 FIM 快路径
- **G10（DeepSeek 专属）**：错峰计价感知
- **G4**：会话管理：本地持久化、`/branch` 分支、`/compact` 压缩
- **G5**：Skill 机制（Markdown + frontmatter，与上游 skill 文件格式兼容，可直接复用上游 skill 仓库的 Markdown 部分；详见 §4.7）
- **G6**：可作为 Go 库被嵌入（`pkg/agent`、`pkg/llm` 公共 API）

### 2.2 非目标（v1.0 不做）

- ❌ 与上游 TS Extension 二进制级兼容
- ❌ Web UI / Slack bot / vLLM pods
- ❌ 图像生成与图像输入（DeepSeek 多模态尚不成熟；其他 Provider 也不做以保持一致性）
- ❌ OAuth
- ❌ **DeepSeek Batch API**（v1.1+）
- ❌ 把 DeepSeek 专属优化反向「降级」到 Claude/GPT/Gemini —— 它们无法享受这些
- ❌ Mistral / Groq / xAI / Cerebras 等额外 Provider —— v1.0 只接 4 家（DeepSeek + Anthropic + OpenAI + Gemini）
- ❌ 公开 OSS 大规模分发的配套：官网、安装脚本服务、文档站、Discord 等

### 2.3 明确延后（v1.1+）

- DeepSeek Batch API（50% 折扣的离线批处理）
- 更多 Provider（Mistral / Groq / xAI / Cerebras …）
- Theme 系统
- Pi Packages 风格的分发机制
- 图像输入 / 图像生成
- OAuth（Anthropic Pro/Max、Codex、Copilot）

---

## 3. 范围与模块拆分

Go 仓库布局（monorepo，单 `go.mod`，module path 建议 `github.com/whyiyhw/seek`）：

```
seek/
├── cmd/seek/                # CLI 入口（cobra），二进制名 seek
├── internal/
│   ├── tui/                 # TUI（bubbletea）
│   ├── session/             # 会话存储 / 分支 / 压缩
│   ├── tools/               # read / write / edit / bash
│   ├── skill/               # Skill 加载（兼容 .claude/skills/）
│   ├── mcp/                 # MCP client
│   ├── cache/               # 缓存友好上下文组织 + 命中率统计（DeepSeek 专属）
│   ├── pricing/             # 错峰计价表 + 当前档位（DeepSeek 专属）
│   └── rpc/                 # stdio JSON-RPC 模式
├── pkg/
│   ├── deepseek/            # 一等公民：chat / reasoner / FIM / 缓存元数据 / JSON 模式
│   ├── llm/                 # 通用接口（Stream / Complete / Tool）
│   │   ├── provider/
│   │   │   ├── anthropic/   # 二等兼容
│   │   │   ├── openai/      # 二等兼容
│   │   │   └── gemini/      # 二等兼容
│   │   └── compatible/      # DeepSeek-兼容端点（vLLM / Ollama / SiliconFlow）
│   └── agent/               # Agent 运行时（事件流、工具调用、Provider 路由）
└── examples/                # SDK 嵌入示例
```

**架构分层原则**（重要）：

- `pkg/deepseek` **不**实现 `pkg/llm.Provider` 接口，而是被 `pkg/agent` 直接持有的一等公民
- `pkg/llm/provider/*` 实现统一接口，被 `pkg/agent` 通过接口持有
- DeepSeek 专属优化（缓存统计、FIM、Reasoner 路径）只在「Provider 是 DeepSeek」分支启用，**绝不下沉到通用接口**——防止「为了对齐所有 Provider 而削弱 DeepSeek 体验」
- 用 Provider 二等的体验不到这些优化，TUI 里相关组件直接隐藏 / 灰显

### 3.1 估时（单人，含测试）

| 模块 | 估时 |
|---|---|
| `pkg/deepseek` chat + reasoner + FIM + JSON 模式 + 缓存元数据 | 1.5 周 |
| `pkg/llm` 通用接口 + Anthropic Provider | 1 周 |
| `pkg/llm` OpenAI Provider | 0.5 周 |
| `pkg/llm` Gemini Provider | 0.7 周 |
| `pkg/llm/compatible` DeepSeek-兼容端点适配 | 0.3 周 |
| `pkg/agent` 运行时 + 事件流 + 并行工具执行 + Provider 路由 | 1.5 周 |
| `internal/cache` 缓存友好上下文组织 + 命中率统计 | 0.5 周 |
| `internal/pricing` 错峰计价表 + UI 集成 | 0.3 周 |
| `internal/tools` 4 个工具（`edit` 含 FIM 快路径） | 1 周 |
| `internal/tui` bubbletea + 状态栏（含缓存/错峰角标） | 1 周 |
| `internal/session` + `internal/skill`（含 `.claude/skills/` 兼容） | 1.2 周 |
| `internal/mcp` MCP client | 0.8 周 |
| 双模型协作内置 skill + `/think` 命令 | 0.5 周 |
| `cmd/seek` interactive / print / json / rpc 4 种模式 | 0.7 周 |
| 集成 / 端到端测试 / 文档 | 1 周 |
| **合计 MVP** | **~12.5 周 ≈ 3 个月** |

> 相比 v0.3（10 周）多出的 2.5 周来自：
> - +2.2 周：补 Anthropic / OpenAI / Gemini 三个 Provider
> - +0.5 周：完整双模型协作 skill（不只是基础 `/think`）
> - +0.3 周：`.claude/skills/` 路径兼容（实际开销很小，主要是文档与测试）

---

## 4. 关键设计决策（请确认）

### 4.1 LLM 接入分层

```
                    ┌─────────────────────────────────────┐
                    │           pkg/agent                  │
                    │  (Provider 路由 + 事件流 + 工具调用) │
                    └─────────┬──────────────────┬─────────┘
                              │                  │
        ┌─────────────────────┘                  └─────────────────────┐
        ↓                                                              ↓
┌───────────────────┐                              ┌──────────────────────────┐
│   pkg/deepseek    │  ← 一等公民                  │       pkg/llm            │
│  chat / reasoner  │     专属优化在这里启用       │  通用接口 Stream/Complete│
│  FIM / JSON / 缓存│                              └──────────────┬───────────┘
└───────────────────┘                                             │
                                  ┌───────────────────────────────┼───────────────────────────────┐
                                  ↓                               ↓                               ↓
                          ┌──────────────┐          ┌───────────────┐          ┌─────────────────────┐
                          │  anthropic   │          │    openai     │          │      gemini         │
                          │  (二等兼容)  │          │  (二等兼容)   │          │   (二等兼容)        │
                          └──────────────┘          └───────────────┘          └─────────────────────┘

         pkg/llm/compatible（兼容 DeepSeek-OpenAI 协议的端点：vLLM / Ollama / SiliconFlow）
         → 当 endpoint 配置 + 模型名以 deepseek/ 开头时，仍走 pkg/deepseek 客户端（享受全部优化）
         → 否则走 pkg/llm/provider/openai 通用路径
```

**关键设计**：

1. **`pkg/deepseek` 不实现 `llm.Provider` 接口**——它有自己的 method（`Chat`、`Reasoner`、`FIM`、`CacheStats`），暴露字段比通用接口多。强行套接口反而丢信息。
2. **`pkg/agent` 通过 `interface{}` + 类型断言**判断 Provider 类型：
   ```go
   switch p := agent.provider.(type) {
   case *deepseek.Client:
       // 启用 cache / FIM / reasoner 等 DeepSeek-only 路径
   case llm.Provider:
       // 走通用路径（Anthropic / OpenAI / Gemini）
   }
   ```
3. **为什么不直接用官方 SDK**：DeepSeek 有大量增强字段（`reasoning_content`、`prefix`、`prompt_cache_hit_tokens`、FIM endpoint）。Anthropic Go SDK / OpenAI Go SDK 的事件流格式也各异——统一手撸 HTTP 反而最干净（参考上游 pi-ai 经验）。
4. **Provider 选择**：
   - 默认：DeepSeek 官方（检测 `DEEPSEEK_API_KEY`）
   - 用户运行 `seek /login` 或编辑配置后切换
   - 二等 Provider 启用时 TUI 顶部显示 banner：「⚠️ 当前 Provider 是 Anthropic，FIM / 缓存优化 / Reasoner 模式已禁用」

### 4.2 工具调用与事件流

事件类型对齐上游（`agent_start` / `turn_start` / `message_start` / `message_update` / `tool_execution_start` / ...），用 Go channel 暴露：

```go
ch, err := agent.Prompt(ctx, "Hello!")
for ev := range ch {
    switch e := ev.(type) {
    case TextDelta:
        fmt.Print(e.Delta)
    case ToolExecutionEnd:
        ...
    }
}
```

工具并行执行用 `errgroup.WithContext`，与上游 `parallel` 模式语义一致。

### 4.3 TUI

**已选定：`charmbracelet/bubbletea` + `lipgloss`（样式） + `glamour`（Markdown 渲染）**。

- 编辑器：用 bubbletea 的 `textarea` 起步，必要时换 `charmbracelet/bubbles/textarea` 的扩展版
- Loader / Spinner：用 `bubbles/spinner`
- 文件路径 / 斜杠命令的自动补全：自行实现一个 `Model`，挂在 textarea 上层
- 不做：Kitty/iTerm2 内联图像协议（v1.0 不需要）、CSI 2026 同步输出（bubbletea 自带的方案够用）

### 4.4 扩展机制：MCP（已选定）

Extension 完全走 **MCP（Model Context Protocol）**：

- `~/.config/seek/mcp.json` 配置 MCP server 列表（stdio / SSE / streamable HTTP）
- 启动时按需拉起 server 子进程，把 server 暴露的 tools 合并进 Agent 的工具集
- MCP server 暴露的 `resources` / `prompts` 在 v1.0 暂不接入（v1.1+）
- 配置格式与 Claude Code / Cursor 的 `mcp.json` 完全兼容，方便用户迁移

不实现自家的 Extension 协议——MCP 生态已经比上游 `pi` 自家 Extension 大得多，没必要造重复轮子。

### 4.5 会话存储

- 格式：JSONL（每条消息一行），与上游 `~/.pi/sessions/*.jsonl` 兼容
- 目录：`~/.config/seek/sessions/`（macOS 走 XDG，Windows 走 `%APPDATA%`）
- 分支：会话 ID + parent_id 链
- 压缩：`/compact` 调用一次 LLM 摘要替换历史前缀（行为对齐上游）

### 4.6 Skill 加载机制（与 MCP 解耦）

**重要前提**：Skill ≠ MCP。
- **MCP** 解决「让模型多一些能力（tools/resources）」——通过协议接入外部进程
- **Skill** 解决「告诉模型在什么场景该用什么工作流」——本质是按需注入到 prompt 里的指令文档

两者并存，互不替代。Go 版的 Skill 设计与上游 `pi` / Claude Code 保持一致。

#### 4.6.1 Skill 文件格式（与上游兼容）

```markdown
---
name: go-test-runner
description: Use when the user asks to run, debug, or analyze Go tests. Triggers on phrases like "run tests", "go test", "fix failing test".
---

# Running Go tests

1. First, identify the package: `go list ./...`
2. Run with verbose output and race detector for the relevant package
3. If a test fails, read the failure, locate the source, propose a minimal fix
...
```

字段：
- `name`（必填）：kebab-case 唯一标识
- `description`（必填）：单行描述「何时该用」——这条是注入 system prompt 的，必须写清触发条件
- `body`：纯 Markdown，可任意长，包含步骤、注意事项、示例

#### 4.6.2 加载位置（按优先级合并，名称冲突时高优先级胜出）

| 优先级 | 路径 | 用途 |
|---|---|---|
| 1（最高） | `<project>/.seek/skills/*.md` | 项目内 Skill（团队共享，进 git） |
| 2 | `<project>/.claude/skills/*.md` | **兼容识别**——从 Claude Code 迁移过来的 skill 直接可用 |
| 3 | `~/.config/seek/skills/*.md` | 用户级 Skill |
| 4 | `~/.claude/skills/*.md` | **兼容识别**——Claude Code 的用户级 skill |
| 5 | `<embedded>` | 二进制内嵌的内置 Skill（go:embed） |

启动时按上述顺序扫描、合并，**同名 skill 高优先级胜出**（项目级 > 用户级 > 内置；同级别 `.seek` > `.claude`）。

启动日志会打印：
```
Loaded 12 skills (3 from .seek/skills, 5 from .claude/skills, 4 builtin)
```

`/skills` 命令显示完整列表，并标注来源。

#### 4.6.3 运行时如何让模型「用上」Skill

启动时扫描所有 Skill → 在 system prompt 末尾追加一段清单（**只列 name + description，不放 body**）：

```
# Available skills

The following skills are available. Each skill describes when it should be applied.
Invoke a skill by calling the `Skill` tool with the skill's name; the tool returns
the skill's body (instructions) which you should then follow.

- go-test-runner: Use when the user asks to run, debug, or analyze Go tests...
- migration-review: Use before merging any database migration PR...
- ...
```

同时注册一个内置工具：

```go
// 工具名：Skill
// 参数：{ "name": "go-test-runner" }
// 返回：该 skill 的 Markdown body 全文（作为 toolResult 注入下一轮上下文）
```

模型决定要用某个 skill 时，自己发起一次 `Skill(name="...")` 调用，body 通过 toolResult 进入上下文，后续轮次按 body 里的步骤执行。

**为什么这样设计**（不直接把所有 skill body 全塞进 system prompt）：
- skill 数量增长后 token 开销不可控（10 个 skill 可能就 10K+ token）
- 让模型自己选择「现在需不需要这个 skill」比起每次都全量注入更精准
- 与 Claude Code 的 Skill 机制完全对齐，用户心智模型一致

#### 4.6.4 与 MCP `prompts` 的关系

MCP 协议里有个 `prompts` 能力，看上去和 Skill 重叠。v1.0 的策略：
- **不**自动把 MCP `prompts` 转成 Skill
- MCP `prompts` 是「用户主动触发的模板」（通过 `/` 命令调用），与 Skill「模型自主决策何时使用」语义不同
- v1.1+ 再考虑是否把 MCP `prompts` 显式注册为斜杠命令

---

### 4.7 安全与权限

工具调用前置 hook（`beforeToolCall`）实现：
- `bash` 默认询问；可通过 `--yolo` 或配置白名单跳过
- `write` / `edit` 默认询问对仓库外路径的写入
- 与 Claude Code 的 `settings.json` 权限模型对齐（便于用户迁移）

---

### 4.8 DeepSeek 专属优化（项目的差异化护城河）

#### 4.8.1 Context Caching 友好的上下文组织

DeepSeek API 自动做前缀缓存：每条请求会与历史请求做最长公共前缀匹配，命中部分按 `0.1×` 价格计费（截至 2026-05，input cache hit $0.014/M，cache miss $0.14/M）。要让命中率高，**整段上下文的前缀必须稳定**。

实现要点（在 `internal/cache` 与 `pkg/agent` 协作）：

1. **Prompt 结构固定顺序**：`system → 工具 schema → skill 清单 → 历史消息（按时间） → 当前用户消息`。任何节的顺序变化都会让缓存全部失效。
2. **工具 schema 一旦序列化就锁定**：用 `sync.Once` 缓存 JSON 序列化结果，避免 `map` 遍历顺序导致的字节级波动。
3. **避免「中间插入」**：MCP server 动态加载新工具时，新工具追加到列表末尾而不是插入中间。
4. **Skill body 走工具调用而非直接塞 prompt**（与 §4.6.3 一致），保持 system prompt 稳定。
5. **会话压缩（`/compact`）时**：摘要后的新 system prompt 会让缓存全部失效——只在 token 预算告警时触发，不要在小话题切换时触发。

TUI 集成：
- 状态栏显示「本轮 / 累计 缓存命中率」「累计节省 tokens」
- `/cache-stats` 命令查看详细命中分布
- 命中率 < 40% 时主动提示用户「你的 prompt 模式破坏了缓存，建议 …」

#### 4.8.2 `deepseek-reasoner` 集成（v1.0 全做：基础 + 完整双模型 skill）

Reasoner 限制（API 文档明确说的，不是猜的）：
- 不支持 `tools` / `tool_choice` / function calling
- 不支持 `temperature` / `top_p` / `presence_penalty` / `frequency_penalty`
- 返回 `reasoning_content` 字段（CoT 内容）
- **必须把上一轮 `reasoning_content` 从 messages 里删掉再发下一轮**，否则 API 报错

**Level 1：基础 `/think` 命令**

- `pkg/deepseek` 暴露 `Reasoner(ctx, messages)` 与 `Chat(ctx, messages, tools)` 两个独立函数
- 用户输入 `/think <问题>`：临时切到 reasoner，结果写回上下文（带 `<reasoning>...</reasoning>` 标记），然后继续 chat
- TUI 渲染 reasoning 段用淡色折叠显示，可 `Ctrl+R` 展开/收起

**Level 2：完整「reasoner-then-chat」双模型协作 skill（v1.0 必须做）**

内置 skill 文件 `dual-model.md`（go:embed），描述要点（注入到 system prompt 的清单部分）：
> Use this skill when the user gives a multi-step task that benefits from explicit planning. Process:
> 1. Call `Think(task)` — reasoner produces a step-by-step plan
> 2. Execute each step using `read` / `write` / `edit` / `bash`
> 3. Before submitting, call `Think(reflect=true, context=last_changes)` — reasoner reviews
> 4. Apply fixes and report

新增内置工具 `Think`（仅 DeepSeek Provider 可用，其他 Provider 隐藏）：

```go
// 工具：Think
// 参数：{ "task": "<待思考的问题>", "reflect": false, "context": "<可选：要反思的内容>" }
// 内部：调 deepseek-reasoner，返回 reasoning + answer
// 返回的 reasoning 写入 toolResult，下一轮 chat 拿到
```

Agent 实现层处理：
- 收到 `Think` tool call → 切到 reasoner endpoint
- reasoner 返回后，把 `reasoning_content` + `content` 都打包为 toolResult
- **下一轮调用 chat 时，从历史中剥离所有 `reasoning_content` 字段**（只保留 reasoner 给出的最终 `content`，外加 chat 自己的工具调用历史）
- 这一步在 `pkg/agent` 的 message 转换层做，对工具实现透明

TUI：
- reasoning 段背景色不同，标题「🧠 Reasoning（点击展开）」
- 状态栏临时显示「Reasoner 思考中…」并暂停 chat 模型的流式

#### 4.8.3 FIM（Fill-in-the-Middle）快路径

DeepSeek `/beta/completions` 接受 `prompt` + `suffix`，返回中间填充。比走 chat 便宜约 5–10x，延迟也低。

`edit` 工具实现：
- 输入是「目标文件 + 旧片段 + 新意图描述」
- 如果改动**范围小于 50 行 + 上下文小于 2K tokens + 用户没要求"解释"**：走 FIM
  - prompt = `<file before old fragment>`
  - suffix = `<file after old fragment>`
  - 模型补全 = 新片段
- 否则 fallback 到 chat 模型的常规 edit 流程
- TUI 会用图标区分两条路径（💨 = FIM 快路径，🐢 = chat 常规路径）

#### 4.8.4 错峰计价感知

DeepSeek 当前价目（2026-05，UTC+8）：
- 标准时段（08:30–00:30）：`deepseek-chat` 输入 $0.27/M（miss）/ $0.014/M（hit），输出 $1.10/M
- 错峰时段（00:30–08:30）：上述价格 50% 折扣

实现：
- `internal/pricing` 内置价目表（含时段、币种、模型）
- TUI 状态栏小角标显示「🌙 错峰 -50%」/「☀️ 标准价」
- `/compact`、`pi --batch <file>`（批量任务，v1.0 可不做但接口先留）等长任务，启动前提示「现在是标准价，2 小时后进入错峰，要等吗？」
- 价目表通过 `go:embed` 内嵌；价格变动时随版本更新，不远程拉取（避免引入外部依赖）

#### 4.8.5 JSON 严格模式 + 工具调用稳定性

DeepSeek 工具调用有时返回不完全合法的 JSON。对策：
- 调 API 时设置 `response_format={type: "json_object"}` + tool schema 同时下发
- 解析失败时不立刻报错，先做一次「容错修复」（补尾逗号、补引号），失败再回报
- 修复成功率统计入 `/cache-stats` 旁边的诊断面板

#### 4.8.6 中文友好

- 默认 system prompt 双语：核心指令英文（模型训练分布），用户可见的工具描述中文
- `--lang zh` / `--lang en` 开关
- 错误消息、`/help`、TUI 字符串走 i18n 表（v1.0 仅 zh / en）

---

## 5. 里程碑

| 里程碑 | 内容 | 周期 |
|---|---|---|
| **M0 骨架** | repo 改名 `seek`、CI、`pkg/deepseek` 跑通 chat 流式 + 工具调用 + 缓存元数据解析 | 第 1–2 周 |
| **M1 Agent loop** | `pkg/agent` 完成，事件流 + 并行工具执行 + Provider 路由（DeepSeek 一等路径）；print 模式可用 | 第 3 周 |
| **M2 工具 + FIM** | 4 个内置工具齐备；`edit` 实现 FIM 快路径与 chat fallback | 第 4 周 |
| **M3 Reasoner + 缓存** | `deepseek-reasoner` 独立路径；`Think` 工具；缓存友好 prompt 组织；命中率统计 | 第 5 周 |
| **M4 TUI** | bubbletea 基础；状态栏含缓存/错峰角标；`/think` 命令；reasoning 折叠；FIM 路径图标 | 第 6–7 周 |
| **M5 会话 + Skill + MCP** | 会话持久化 / 分支 / 压缩；Skill 加载（含 `.claude/skills/` 兼容）；MCP client；**双模型协作 skill** | 第 8–9 周 |
| **M6 二等 Provider** | Anthropic / OpenAI / Gemini 三家通过 `pkg/llm` 接入；`pkg/llm/compatible` 兼容端点；TUI 二等 banner | 第 10–11 周 |
| **M7 打磨** | RPC / JSON 模式；文档；自举测试；benchmark | 第 12 周 |
| **v1.0 发布** | | ≈ 3 个月 |

---

## 6. 验收标准

v1.0 发布前必须满足：

- [ ] `go install github.com/whyiyhw/seek/cmd/seek@latest` 一键装好，macOS / Linux / Windows 均可运行
- [ ] 仅有 `DEEPSEEK_API_KEY` 即开箱可用，无任何额外配置
- [ ] 自举测试：让 `seek` 自己读、改、运行 `seek` 仓库的 Go 测试
- [ ] **缓存命中率**：在自举测试场景下，缓存命中率 ≥ 60%（前 5 轮除外）
- [ ] **FIM 快路径**：小范围 `edit` 操作中走 FIM 的比例 ≥ 50%
- [ ] **Reasoner 基础**：`/think` 命令可用；reasoner 上下文剥离正确（不报 400）
- [ ] **双模型协作 skill**：内置 `dual-model` skill 可被模型自主调用；完整跑通「Think→执行→Think 反思」流程
- [ ] **错峰提示**：TUI 状态栏正确显示当前时段
- [ ] 至少 1 个真实 MCP server（filesystem）开箱可用
- [ ] Skill：内置 ≥ 3 个示例 skill（含 `dual-model`）；能加载 `.seek/skills/` `.claude/skills/` `~/.config/seek/skills/` `~/.claude/skills/`
- [ ] **二等 Provider 兼容**：DeepSeek 官方 + Anthropic + OpenAI + Gemini + 一个兼容端点（Ollama）至少各跑通一次「读文件、改一行、运行 `go test`」端到端用例
- [ ] **二等 Provider banner**：切换到 Anthropic / OpenAI / Gemini 时，TUI 顶部正确显示警告
- [ ] `pkg/deepseek` 与 `pkg/agent` 公共 API 单测覆盖 ≥ 70%

---

## 7. 主要风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| **DeepSeek 服务可用性 / 价格政策变动** | 中 | 三层退路：DeepSeek-兼容端点 → Anthropic → OpenAI / Gemini；价目表 `go:embed`，版本随升级 |
| DeepSeek API 字段变更（如 `reasoning_content` 格式） | 中 | API 调用全部走 `pkg/deepseek`，集中一处维护；每月 dry-run smoke test |
| **多 Provider 抽象「腐蚀」DeepSeek 优化** | **高** | §3 + §4.1 的严格分层；CI 加 lint：`pkg/deepseek` 不允许 import `pkg/llm` |
| 缓存命中率优化反直觉，用户难理解 | 中 | TUI 内置 `/cache-stats` 教学面板；写一篇博客解释 |
| FIM 在边界情况下质量不如 chat | 中 | 自动 fallback 阈值（行数 / token 数）保守；用户可 `--no-fim` 关闭 |
| 双模型协作 skill 让 reasoner 频繁触发，token 成本反而上升 | 中 | skill description 强调「multi-step task that benefits from explicit planning」；TUI 显示本会话 reasoner 调用次数；可 `--no-think` 全局关闭 |
| 中文社区认知不足 | 低 | 中文 README、知乎/掘金/V2EX 起手；DeepSeek 官方/社区曝光 |
| 法律 | 低 | 上游 MIT，干净重写无问题；保留 NOTICE 注明灵感来源 |

---

## 8. 待办与下一步

所有关键决策已敲定（见文首决策记录表）。立项后第一周的具体动作：

1. **目录改名**：把 `/Users/whyiyhw/code/github/pi-go/` 重命名为 `seek/`（可手动 `mv`，下面命令仅作参考）
   ```bash
   mv /Users/whyiyhw/code/github/pi-go /Users/whyiyhw/code/github/seek
   ```
2. **初始化 `go.mod`**：`module github.com/whyiyhw/seek`
3. **搭目录骨架**（见 §3，含 `cmd/seek/`、`pkg/deepseek/`、`pkg/llm/`、`pkg/agent/`、`internal/{tui,session,tools,skill,mcp,cache,pricing,rpc}/`）
4. **CI**：GitHub Actions：`go test` + `golangci-lint` + 三平台构建产物 + **`pkg/deepseek` 不允许 import `pkg/llm` 的 lint 规则**（防止抽象腐蚀）
5. **M0 第一个可演示目标**：
   ```
   $ echo "读 README.md 总结一下" | seek
   [缓存 miss → 后续可命中]
   [流式输出]
   总结：...
   ```
   走通 `pkg/deepseek` 的 chat 流式 + 一次 `read` 工具调用 + 缓存元数据正确解析

---

## 9. 立项 checklist（确认即可开 M0）

- [x] 项目名：`seek`
- [x] 一等公民：DeepSeek（专属优化全启用）
- [x] 二等兼容：Anthropic / OpenAI / Gemini（基础 chat + 工具调用）
- [x] DeepSeek 退路：vLLM / Ollama / SiliconFlow 等兼容端点
- [x] TUI：bubbletea + lipgloss + glamour
- [x] 扩展：MCP
- [x] Skill：兼容 `.claude/skills/` 和 `~/.claude/skills/`
- [x] Reasoner：基础 `/think` + 完整双模型 skill（v1.0 全做）
- [x] OAuth：不做
- [x] Batch API：不做
- [x] 目标用户：个人 / 团队
- [ ] **Module path 最终确认**：`github.com/whyiyhw/seek`？（如果你的 GitHub 用户名是别的请告诉我）

确认 module path 后，下一步我可以直接开始 M0：建目录、写 `go.mod`、跑通 DeepSeek 的 hello-world + 一次 `read` 工具调用。
