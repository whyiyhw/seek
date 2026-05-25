# 附录 A：踩坑录索引

这本书正文里编号到 **#1–#16** 的"坑"是 callout 形式的方框，标出最值得记的一类细节——一个 LLM 协议层面或 Go 语言层面的、做对要靠经验、做错要靠运气的事情。

仓库根的 [`docs/pitfalls.md`](../pitfalls.md) 是更全的版本——所有在开发中浮现过的非显然问题都记一条，截至本书写作时大约 35 条。两份内容**重叠但不相等**：

- **书中编号的 16 条**：每条都有教学价值（能从中提炼出一条普适规则），且通常出现在前后两个章节里互相引用
- **pitfalls.md 的其余条目**：包括很多"修了一次就不再讨论"的具体 bug、tooling 怪癖、依赖问题——重要但不教学性

这份附录做两件事：

1. **§A.1**：把书中 #1–#16 ↔ pitfalls.md 的精确条目对上
2. **§A.2**：列出书中**讨论过但未编号**的内容在 pitfalls.md 里的位置——方便顺藤摸瓜读到更多

---

## A.1 书中编号坑 ↔ pitfalls.md

| 书 # | 章节 | 书中标题（简） | pitfalls.md 对应条目 |
|---|---|---|---|
| #1 | ch2 §2.2 | SSE 流里的 `[DONE]` 是字符串，不是 JSON | _（无独立条目——协议层一般知识）_ |
| #2 | ch2 §2.3 | 工具调用 arguments 是 JSON 字符串，不是 JSON 对象 | _（无独立条目；与下条紧邻）_ |
| #3 | ch2 §2.4 | FIM 端点不是 `/chat/completions` | "FIM endpoint is at `/beta/completions` with legacy OpenAI shape" |
| #4 | ch3 §3.4 | `bufio.Scanner` 默认缓冲区 64 KiB 装不下 tool_call | _（无独立条目——预防式设计）_ |
| #5 | ch4 §4.4 | 测试要针对"状态形状"，而不是"触发原因" | "Followup: ctx-cancel was one of FOUR paths to the same orphan state" |
| #6 | ch5 §5.4 | askFn 在发送和接收两端都要 ctx-aware select | "Approval callback that blocks on a channel needs ctx-aware select on BOTH ends" |
| #7 | ch5 §5.5 | `json.Unmarshal` 静默丢弃未知字段 | "`json.Unmarshal` silently drops unknown fields — LLM typos produce useless errors" |
| #8 | ch7 §7.2 | `tea.WithAltScreen()` 对聊天工具是路径依赖陷阱 | "Alt-screen mode breaks scrollback, copy, and content persistence" |
| #9 | ch7 §7.8 | `for j, r := range string` 的 j 是字节偏移 | "`for j, r := range string` gives BYTE indices, not rune indices" |
| #10 | ch8 §8.2 | cancel 后到 stream 真正排空之间的"尾巴 event" | _（与 "Esc mid-stream poisoned the session..." 相关但角度不同：那条讲 agent 端的不变量，#10 讲 TUI 端的 `userCanceled` 标志）_ |
| #11 | ch8 §8.4 | top-level `var` slice + func 互相引用 = init cycle | "Top-level `var` slice and `func` that reference each other → init cycle" |
| #12 | ch9 §9.2 | `omitempty` 在 slice 上"既 omit nil 也 omit 空切片" | "`omitempty` on a slice field omits both nil AND empty (`[]T{}`)" |
| #13 | ch10 §10.2 | 值拷贝带 slice 的 struct，底层数组共享 | _（无独立条目——设计纪律。Fork 测试覆盖）_ |
| #14 | ch11 §11.2 | 源码里字面 UTF-8 BOM 是 Go 编译错误 | "Literal UTF-8 BOM in a Go string literal is a compile error" |
| #15 | ch13 §13.2 | multi-turn 的"累计 prompt tokens"不是 context 预算信号 | "Cumulative prompt tokens are meaningless as a context-limit signal" |
| #16 | ch13 §13.5 | 协议里非 `stop` 完成原因必须显式处理 | "`finish_reason=\"length\"` looks like a normal stop" |

**16 条里有 11 条在 pitfalls.md 有一比一对应**，5 条没有独立条目——它们要么是协议层一般知识（#1、#2），要么是 seek 在设计阶段就规避了的预防式工程（#4、#13），要么是某个 pitfall 条目的另一面（#10）。

---

## A.2 书中提到但未编号 — pitfalls.md 里的延伸阅读

这些坑在书里出现过（讲修法、讲历史、或顺手提一句），但**没用方框 callout**——通常因为正文已经把它讲透，再加一个方框反而冗余。如果你想看更紧凑、按"症状 / 原因 / 修复 / 教训"四段的版本，去 pitfalls.md 找。

### 来自 TUI / 终端

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch7 §7.4 WindowSizeMsg | "'starting seek …' placeholder stuck for seconds (or forever)" |
| ch7 §7.3 OSC 11 探针 | "Garbage `]11;rgb:fae0/fae0/fae0\[1;1R` in the input box" |
| ch7 §7.6 KeyMsg 路由 | "Spacebar scrolled the conversation a page" |
| ch7 §7.7 自动滚动 | "Streaming kept yanking the viewport to the bottom" |
| ch7（隐含） | "Mouse drag-to-select did nothing"、"`View()` rendered 'starting seek …' for a frame" |
| ch15 §15.6 浮动 help | "New overlay panel consumed Ctrl+C — user couldn't quit while help was open" |

### 来自 Agent loop

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch4 §4.4 + ch8 §8.2 + ch9 §9.6 三处共同讲的 | "Esc mid-stream poisoned the session: orphan `tool_calls` rejected on every subsequent turn" |
| ch5 §5.4 提到 `sync.RWMutex` | "`Policy.mode` was raced by /yolo flips against concurrent `Check`" |
| ch5 §5.6 提了一句"已知限制" | "Symlinks inside CWD let `write`/`edit` escape the CWD gate" |

### 来自 DeepSeek API

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch3 §3.3 + ch6 反复讲 | "Reasoner rejects requests that retain prior `reasoning_content`" |
| ch6 §6.1 + ch14 §14.1 | "Reasoner doesn't support `tools`, `temperature`, `top_p`, etc." |
| ch5 §5.2 缓存命中条件 | "Prefix cache hits are best-effort and short prompts don't trigger" |
| ch2 §2.3 工具调用 delta 按 index 合并 | "Streamed tool calls arrive as deltas keyed by `index`" |

### 来自会话持久化 / `--no-save`

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch9 §9.2 + §9.3 | "`json.Encoder.Encode()` always appends `\n` — the JSONL primitive" |
| ch9 §9.7 + ch10 §10.4 | "`/compact` panicked with nil Session on `--no-save` path" |

### 来自 M6 多 Provider

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch14 §14.4 | "Anthropic requires consecutive tool results merged into ONE user message" |
| ch14 §14.5 | "Gemini omits tool call IDs; system message is `systemInstruction`, not a messages-array entry" |
| ch14 §14.6 | "OpenAI streaming token counts require `stream_options: {include_usage: true}`" |

### 来自 Go 语言细节（书中未单独编号但都被 ch5 / ch7 / ch11 / ch13 顺带提到）

- "Backticks in raw string literals close the string"
- "Shadowing the `cap` builtin in a local helper"
- "Go's constant float→int conversion isn't auto-applied"

### 来自工具链 / 环境（书中基本未提，留作运行手册）

- "Auto-mode classifier blocked inline API keys on the command line"
- "`glamour@v1.0.0` required a specific `lipgloss` pre-release commit"
- "`go run ./cmd/seek` is slow enough to feel broken"

### 来自 Plan Mode / 交互工具（Ch 18）

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch18 §18.3 propose | "Propose tool's `expected_replacements` requires exact match count"（示例：propose 工具的参数校验模式类似） |
| ch18 §18.4 git 工具 | "`git` tool refused flags that could mutate refs (--delete, -D, --force)" |
| ch18 §18.5 ask_user | "ask_user: cancelled=true 时模型必须做最佳猜测而非重问" |

### 来自 M8 Skill 安装扩展（Ch 17 §17.9）

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch17 §17.9 | "`skill_fetch` → `skill_commit` scope 参数必需用户选择, 不能默认" |
| ch17 §17.9 | "`t.Cleanup(chdir)` after `t.TempDir()` breaks Windows TempDir removal" |

### 来自 Hook / Memory 维护（Ch 16 补充）

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch16 §16.3.2 | "OnSessionStart must reset snapshot state for --resume correctness" |
| ch16 §16.4.3 | "M 不是 bug tracker: `memory_remember` 工具描述误导模型用 M 记 bug" |

### 来自 DeepSeek API 更新

| 书中位置 | pitfalls.md 条目 |
|---|---|
| ch13 §13.5（延伸） | "DeepSeek HTTP 5xx and empty SSE bodies are transient — retry once before failing" |
| ch18（隐含，无缝继续） | "`/lang` only updated the display and session variable, not the live Agent → no effect until `/new`"（per-message injection 模式） |

---

## A.3 怎么读 pitfalls.md

[`docs/pitfalls.md`](../pitfalls.md) 顶部有一个 "Reading order for newcomers" 段落——按它列的三步走：

1. **DeepSeek API** 一节先读完。这是 seek 优化的目标面，所有"为什么这么设计"的原因在这里
2. **TUI / terminal**——用户可见的 polish 大部分在这里
3. **Go / tooling**——撞到才看，提前看记不住

每条都按统一格式（**Saw / Why / Fix / Lesson / Refs**），符号短而可扫。建议每次写一个新模块之前花 3 分钟扫一遍——很多 footgun 已经替你试过了。

---

*pitfalls.md 是这本书的"原料库"。书是结构化的叙述，pitfalls 是 append-only 的事实记录。任何一处不一致，以 pitfalls.md 为准——它由 `Pitfall:` commit trailer 自动生成校验信号（见 `scripts/extract-pitfalls.sh` 和 [`AGENTS.md`](../../AGENTS.md) 的 pitfall 记录规则）。*
