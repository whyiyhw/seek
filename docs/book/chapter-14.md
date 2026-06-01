# 第 14 章：M6 — 多 Provider 支持

> 对应代码：`pkg/llm/`（通用接口）、`pkg/llm/provider/{anthropic,openai,gemini}/`、`pkg/llm/compatible/`、`pkg/agent/translate.go`、`.github/workflows/ci.yml`（layer-lint）。
> 起点：第 13 章之前，seek 只能跟 DeepSeek 说话。
> 终点：`--provider=anthropic` / `openai` / `gemini` / `compatible` 让 seek 跑在四类后端上，Agent 循环、TUI、Skill、会话持久化全部不动。

---

加多 Provider 是写 LLM 工具最容易写歪的地方之一。常见的歪法：

- **抽象出"通用 LLM 接口"**，所有 provider 都实现，包括最常用的那个。结果：那个最常用的 provider 独有的能力被抽象掉了，所有用户都"享受"通用化的最低公分母。
- **每个 provider 单独写一份完整的 Agent 路径**，复制粘贴 80% 代码。结果：修一个 bug 要在四处改，时间长了行为偷偷分叉。
- **不写抽象，直接 if-else 分发**。结果：100 行 `switch provider { ... }` 散落在 cmd/ 各处，加新 provider 就是去翻所有 if-else。

seek 选了第四条路：**第一公民 + 第二公民两层**。这一章讲怎么做到的。

---

## 14.1 设计前提：`pkg/deepseek` 是第一公民，不参与抽象

第 1 章一开始就放过这条规则：

> `pkg/deepseek` 不能 import `pkg/llm`。CI lint 强制执行。

`.github/workflows/ci.yml`：

```yaml
layer-lint:
  name: layer-lint (pkg/deepseek must not import pkg/llm)
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - name: forbid pkg/deepseek -> pkg/llm imports
      run: |
        if grep -rnE 'github\.com/whyiyhw/seek/pkg/llm' pkg/deepseek; then
          echo "::error::pkg/deepseek must not import pkg/llm (see PRD §4.1)"
          exit 1
        fi
```

一条 `grep`，PR 里只要 `pkg/deepseek/*.go` 出现任何对 `pkg/llm` 的 import，CI 立刻失败。

为什么这条规则要硬执行？

如果允许 `pkg/deepseek` 用 `pkg/llm` 的类型，**未来某一天**有人会觉得"既然两个都是 LLM client，何不让 deepseek 也实现 `llm.Provider`"——一旦实现，DeepSeek 的独有字段（`PromptCacheHitTokens`、`reasoning_content`、FIM 端点）就有压力被抽象进 `llm.Provider` 或者被丢弃。两种结果都让 seek 失去对 DeepSeek 的精确控制——而精确控制 DeepSeek 是这个项目存在的理由（前缀缓存、reasoner、FIM 都是 DeepSeek 独有）。

**CI lint 不是检查代码风格，是把架构决策物化成一道闸门**。重看第 1 章的那个分层图就清楚了：

```
+----------------+  +-------------------------+
|  pkg/deepseek  |  |  pkg/llm                |
| (first-class)  |  |    provider/anthropic   |
|                |  |    provider/openai      |
|                |  |    provider/gemini      |
|                |  |    compatible           |
+----------------+  +-------------------------+
        ^                       ^
        |                       |
        +----  pkg/agent  ------+
              (type switch)
```

两个并排的孤岛，由 `pkg/agent` 用 type switch 接到一起。`pkg/deepseek` 完全不知道 `pkg/llm` 存在；`pkg/llm` 完全不知道 `pkg/deepseek` 存在。

CI lint 把这条理论上的边界变成了实践上的保护。一行 grep，零运行时开销，永远 lint。

---

## 14.2 `pkg/llm` 接口：故意瘦的中间件

```go
type Provider interface {
    ChatStream(ctx context.Context, req ChatRequest) (<-chan Event, error)
    Name() string
}

type ChatRequest struct {
    Model    string
    Messages []Message
    Tools    []ToolDef
}
```

两个方法。`ChatRequest` 三个字段。**没有非流式 `Chat`，没有 `FIM`，没有 cache 元数据**。

为什么这么瘦？

**非流式 chat 不在接口里**：所有 provider 都支持流式，所有 seek 路径都用流式。如果有"非流式 summary"这种特殊场景，可以在流式 channel 上自己 drain 成完整字符串——`pkg/agent/agent.go:summariseLLM` 就这么做的：

```go
stream, _ := a.cfg.Provider.ChatStream(ctx, req)
var sb strings.Builder
for ev := range stream {
    switch e := ev.(type) {
    case llm.TextDelta:
        sb.WriteString(e.Delta)
    case llm.TurnDone:
        // ...
    }
}
return sb.String(), ...
```

少一个接口方法 = 每个 provider 少实现一份 = 整体复杂度下降。

**Usage 字段被刻意压扁**：

```go
type TurnDone struct {
    FinishReason string
    InputTokens  int
    OutputTokens int
}
```

`InputTokens` / `OutputTokens` 是所有 provider 都有的最小公倍数。**没有 `PromptCacheHitTokens` / `PromptCacheMissTokens`**——那是 DeepSeek 独有的字段，OpenAI/Anthropic/Gemini 都没有对应概念。

这正是"抽象不要稀释 DeepSeek"的体现：通用接口拿不到缓存命中数据。当 seek 跑在 OpenAI 上时，`tracker.Cumulative().PromptCacheHitTokens` 永远是 0——这是诚实的，缓存命中信息真的没有；用 DeepSeek 时，第 13 章那条"省了 280k tokens" 还是能显示。

### Event 是 sum type，不是 channel of strings

```go
type Event interface{ llmEvent() }

type TextDelta     struct{ Delta string }
type ToolCallDone  struct{ ID, Name, Arguments string }
type TurnDone      struct{ FinishReason string; InputTokens, OutputTokens int }
type ErrorEvent    struct{ Err error }

func (TextDelta)    llmEvent() {}
func (ToolCallDone) llmEvent() {}
func (TurnDone)     llmEvent() {}
func (ErrorEvent)   llmEvent() {}
```

`Event` 是个空方法签名的接口，用 marker method（`llmEvent()`）做 sum type——只有声明过 `llmEvent()` 的类型才能进 channel，外部包不能伪造一个新事件类型混进来。

这是 Go 里"sum type emulation"的标准做法。比"用 string 类型字段 + 大杂烩 struct" 干净——consumer 用 type switch 完整列出所有 case，编译期能看出有没有漏。

`ToolCallDone` 和 DeepSeek 流式协议的关键差异：**`ToolCallDone` 在工具调用完整组装好之后才发出一次**，而不是像 DeepSeek 那样按 index 流式 delta。理由：

- 三家 second-tier 各自的流式工具调用格式都不一样（OpenAI 跟 DeepSeek 几乎相同、Anthropic 用 content block events、Gemini 一次性给）
- TUI 看不见 tool_call 的"流式打字"——工具调用通常很快就完整，单次 event 足够
- 把"组装碎片"的复杂度封装在 provider 实现内部，外部接口干净

`TextDelta` 还是流式的——这是用户能看到的部分（assistant 文本逐字出现），不能合并。

---

## 14.3 `translate.go`：第一公民 ↔ 第二公民的边界

Agent 内部用 `deepseek.Message` 作为 canonical 类型。当走 LLM Provider 路径时，需要翻译一次：

```go
// pkg/agent/translate.go
func msgsToLLM(msgs []deepseek.Message) []llm.Message {
    // deepseek.Message → llm.Message
}

func toolsToLLM(reg *tools.Registry) []llm.ToolDef {
    // 把工具注册表翻成 llm.ToolDef[]
}
```

这是双层架构的接口黏合层。`pkg/llm` 不知道 `deepseek` 类型，`pkg/deepseek` 不知道 `llm` 类型，**`pkg/agent` 是唯一同时知道两边的地方**——它做翻译。

为什么不让 `pkg/llm` 提供一个 `FromDeepSeek(deepseek.Message) llm.Message` 函数？那样会让 `pkg/llm` 反向依赖 `pkg/deepseek`，违反"两个孤岛"的设计。**翻译方向永远是从 deepseek 往 llm**（Agent 持有 deepseek.Message 作为 source of truth），翻译动作发生在 `pkg/agent` 里。

类似地 `Summarise` 的两条路径：

```go
func (a *Agent) Summarise(ctx context.Context) (string, deepseek.Usage, error) {
    if a.cfg.Provider != nil {
        return a.summariseLLM(ctx)    // 第二公民路径
    }
    // 第一公民路径
}
```

`a.cfg.Client` 和 `a.cfg.Provider` 永远只有一个非 nil。`if Provider != nil` 是 type switch 的简化版（只有两种状态时不需要写真正的 switch）。

---

## 14.4 Anthropic：tool_result 必须合并

每家 provider 都有自己的协议怪癖。先看 Anthropic。

OpenAI / DeepSeek 的协议是这样的：每个 `tool_call` 对应一条 `role="tool"` 消息，多个 tool_call 就排几条 tool 消息：

```jsonc
[
  {role: "assistant", tool_calls: [{id: "1", ...}, {id: "2", ...}]},
  {role: "tool", tool_call_id: "1", content: "..."},
  {role: "tool", tool_call_id: "2", content: "..."},
  {role: "user", content: "..."}
]
```

Anthropic 不接受这种结构。它要求所有 `tool_result` block 必须**塞进同一条 `role="user"` 消息**：

```jsonc
[
  {role: "assistant", content: [{type: "text", ...}, {type: "tool_use", id: "1", ...}, {type: "tool_use", id: "2", ...}]},
  {role: "user", content: [
    {type: "tool_result", tool_use_id: "1", content: "..."},
    {type: "tool_result", tool_use_id: "2", content: "..."}
  ]},
  // ...
]
```

发不合并的请求会得到 400：`tool_result blocks must all appear in the same user message`。

`pkg/llm/provider/anthropic/client.go:buildRequest` 就做这件事：

```go
i := 0
for i < len(msgs) {
    m := msgs[i]

    // Group consecutive tool-result messages into one user message.
    if m.Role == "tool" {
        var blocks []contentBlock
        for i < len(msgs) && msgs[i].Role == "tool" {
            blocks = append(blocks, contentBlock{
                Type:      "tool_result",
                ToolUseID: msgs[i].ToolCallID,
                Content:   msgs[i].Content,
            })
            i++
        }
        ar.Messages = append(ar.Messages, anthropicMessage{
            Role: "user", Content: blocks,
        })
        continue
    }
    // ...
}
```

双重循环——外层走全部消息，内层"扫一段连续的 tool 消息"打包成一条 user。注意 **i 在两层之间共享**，所以内层走完外层不会重复处理同一段。

这是把"协议形状不一致"硬编码成翻译规则的典型做法。每家协议怪癖在 buildRequest 里集中处理，对外（Agent 循环）完全不可见。

**System message 也得搬家**：

```go
if len(msgs) > 0 && msgs[0].Role == "system" {
    ar.System = msgs[0].Content
    msgs = msgs[1:]
}
```

Anthropic 把 system prompt 放在 request 的**顶层 `system` 字段**，不在 messages 数组里。OpenAI / DeepSeek 是把 system 当成 messages[0]。两种风格都没错，Anthropic 选了显式分开——翻译时第一条消息单独搬出来。

---

## 14.5 Gemini：没有 call ID + `systemInstruction` 单独字段

Gemini 比 Anthropic 多一个坑：**它的函数调用流里完全没有 `id` 字段**。

OpenAI / Anthropic 都给每个 tool call 分配一个 `id`，后续 tool_result 通过 `id` 配对。Gemini 的 `functionCall` part 只有 `name` 和 `args`——它假设调用方按"位置"配对（第 N 个 functionCall 对应第 N 个 functionResponse）。

但 seek 的 Agent 内部需要 ID 来配对（`tool_call_id` 在 `deepseek.Message` 里是必填）。怎么办？**Gemini provider 自己生成 ID**：

```go
// pkg/llm/provider/gemini/client.go SSE 解析
var toolIdx int
for {
    // ...解析一个 functionCall part...
    out <- llm.ToolCallDone{
        ID:        fmt.Sprintf("gemini_%d", toolIdx),
        Name:      fc.Name,
        Arguments: argsJSON,
    }
    toolIdx++
}
```

`gemini_0`、`gemini_1`、`gemini_2`……单调递增。回填到 deepseek.Message 里，下次发送时再翻译回 Gemini 时**丢掉 ID**（Gemini 不要也不读），按位置配对成立。

这是双向翻译的小聪明：**对 Agent 假装 Gemini 有 ID，对 Gemini 假装 Agent 不发 ID**。翻译层吸收掉差异，Agent 循环完全不用关心。

**System message 也单独字段**：

```go
type geminiRequest struct {
    // ...
    SystemInstruction *geminiContent `json:"systemInstruction,omitempty"`
    Contents          []geminiContent
    // ...
}

// buildRequest 里
if msgs[0].Role == "system" {
    gr.SystemInstruction = &geminiContent{Parts: sysParts}
    msgs = msgs[1:]
}
```

跟 Anthropic 一个套路，字段名不一样而已。Gemini 叫 `systemInstruction`，Anthropic 叫 `system`，OpenAI/DeepSeek 把它当 messages[0]。三家三种风格，每家在翻译层处理一次。

---

## 14.6 OpenAI：必须显式打开 streaming usage

OpenAI 的流式响应**默认不带 usage 信息**。直接发流式请求，TurnDone 拿到的 `InputTokens` / `OutputTokens` 永远是 0。

解法在请求体里加一个 `stream_options`：

```go
type streamOptions struct {
    IncludeUsage bool `json:"include_usage"`
}

type openAIRequest struct {
    // ...
    Stream        bool            `json:"stream"`
    StreamOptions *streamOptions  `json:"stream_options,omitempty"`
}

// ChatStream 里
body := openAIRequest{
    // ...
    Stream:        true,
    StreamOptions: &streamOptions{IncludeUsage: true},
}
```

加上之后，流的最后会多一个 chunk，`choices` 是空数组但带 `usage` 字段——provider 解析它发出 `TurnDone{InputTokens: ..., OutputTokens: ...}`。

这是 OpenAI 的一个"向后兼容"设计：以前的流式不带 usage，怕老客户端打不开 usage 字段处理逻辑会乱，所以加了一个开关。不知道这个开关的人会以为 OpenAI 流式不支持 token 统计——文档里有但不显眼。

`pkg/llm/compatible` 也用 OpenAI 协议（vLLM / Ollama / SiliconFlow / 各种自部署 OpenAI-compatible 服务），同样要这个 flag。

---

## 14.7 `compatible`：vLLM / Ollama / SiliconFlow 的薄包装

```go
// cmd/seek/main.go
case "compatible":
    apiKey := os.Getenv("OPENAI_API_KEY")
    if apiKey == "" {
        apiKey = os.Getenv("DEEPSEEK_API_KEY")
    }
    if baseURLFlag == "" {
        return nil, nil, "", "", fmt.Errorf("--base-url is required for --provider=compatible")
    }
    return compatible.New(apiKey, baseURLFlag, provName), nil, provName, "", nil
```

`compatible` 不是一个新协议，是**复用 OpenAI 协议、换 URL**。一行命令就能跑：

```bash
seek --provider=compatible --base-url=http://localhost:11434/v1 --model=llama3.2 -p '...'
```

这是开发体验上的一个大改进：本地 Ollama / 公司私有的 vLLM 服务，只要它们声称 "OpenAI-compatible"，seek 就能用上。不需要为每个写一个 adapter。

`--provider-name` flag 让用户自定义 banner 文字（"Local Ollama"、"vLLM"），TUI 里显示"⚠ Provider: Local Ollama"。

代价是：`compatible` 默认不知道目标的 context limit（每个本地模型都不一样），所以 `--model` 也不强制——budget 模块的 `Default = 128_000` 兜底（第 13 章讲的保守回退）。

---

## 14.8 Provider 选择：标志 + env 自动探测

```go
func buildProvider(provFlag, baseURLFlag, provName string) (
    provider llm.Provider, dsClient *deepseek.Client,
    provLabel, modelDefault string, err error,
) {
    if provFlag == "" {
        switch {
        case os.Getenv("ANTHROPIC_API_KEY") != "" && os.Getenv("DEEPSEEK_API_KEY") == "":
            provFlag = "anthropic"
        case os.Getenv("OPENAI_API_KEY") != "" && os.Getenv("DEEPSEEK_API_KEY") == "":
            provFlag = "openai"
        case os.Getenv("GEMINI_API_KEY") != "" && os.Getenv("DEEPSEEK_API_KEY") == "":
            provFlag = "gemini"
        default:
            provFlag = "deepseek"
        }
    }
    // ...
}
```

选择规则：
- `--provider` 显式指定 → 用它
- `DEEPSEEK_API_KEY` 存在 → 用 DeepSeek（第一公民优先）
- 只设了某个第二公民的 key → 用对应 provider
- 都没设 → 报错（DeepSeek 路径自己处理）

`&& DEEPSEEK_API_KEY == ""` 那一行重要：**只要 DeepSeek 的 key 在，就不自动切第二公民**。即使用户同时设了 OpenAI key，默认还是用 DeepSeek——因为 seek 是为 DeepSeek 优化的。**明确想用别的 provider，要显式 `--provider=openai`**。这避免了"我装了多个工具的 SDK，结果 seek 偷偷跑去用 OpenAI"的意外。

返回值有意思：`provider` 和 `dsClient` 永远一个 nil 一个非 nil。Agent 用 `if Provider != nil` 路由——这种"两个互斥指针"的模式比"enum + switch"更轻量，但只在选项是 2 时才用，超过 2 个就退化成 type switch。

---

## 14.9 TUI Provider banner：能力降级的可视化

```go
// internal/tui/view.go
if m.opts.ProviderName != "" {
    banner := styleBannerWarn.
        Render("⚠ Provider: " + m.opts.ProviderName + " — FIM / cache stats / Reasoner disabled")
}
```

`ProviderName == ""` 是 DeepSeek（不显示 banner），非空是第二公民。banner 内容明确告诉用户**哪些能力不可用**：

- **FIM**：`fim_complete` 工具只在 DeepSeek 下注册（main.go 里 `if dsClient != nil { reg.Add(fimcomplete.New(...)) }`）
- **Cache stats**：状态栏的 "saved Xk" 在第二公民下永远 0，因为 `llm.TurnDone` 没有缓存字段
- **Reasoner**：`think` 工具同上，只在 DeepSeek 下注册

这一行 banner 是给用户的**明确预期管理**——而不是悄悄降级让用户以后才发现 `/think` 命令不存在。把降级公开化是"诚实 > 神秘"原则的延伸（第 13 章的状态栏诚实度）。

---

## 14.10 一个测试观察：每个 provider 都有 6-7 个 buildRequest 测试

```
pkg/llm/provider/anthropic/client_test.go   6 tests
pkg/llm/provider/openai/client_test.go      7 tests
pkg/llm/provider/gemini/client_test.go      6 tests
```

测试主要在两件事：
1. **buildRequest 的输出形状**：给一个固定 messages 输入，验证生成的请求 JSON 完全符合 provider 协议（tool_result 合并对了、systemInstruction 移到顶层了、stream_options 设了）
2. **SSE 解析的形状**：给一段固定的 SSE 输入字节流，验证 ChatStream 发出的事件序列符合预期（TextDelta → ToolCallDone → TurnDone）

这两类测试覆盖了所有"协议怪癖"——也就是上面讲的所有"必须这么做不然 400"的细节。如果某天 provider 改了 protocol，测试会立刻 fail，让你知道要去翻文档了。

第 9 章里诚实记过 session 包"loadMeta 没有语义测试"的空白。Provider 测试这块的形状测试做得反而完整——区别就在于"协议契约"是一个外部输入，**容易被遗忘但失败成本高**，所以测试得严；session 包的优化是内部，逻辑等价，所以容易跳过测试。两种倾向各自合理但要警惕。

---

### 相关踩坑

多 Provider 支持实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. Anthropic 要求 tool_result 合并到同一条 user 消息**

- **Saw**：将每个 `tool_result` 分别作为独立 `role="user"` 消息发送给 Anthropic API，返回 400。
- **Why**：Anthropic 要求同一轮中所有 `tool_result` 块合并到**一条** `role="user"` 消息中。
- **Fix**：在 `translate.go` 中累积同一批 tool result，合并为一条 user 消息发送。

**2. Gemini 没有 tool call ID——system message 是顶层字段**

- **Saw**：Gemini 的 functionCall 没有 ID 字段；system message 不是消息数组的一部分，而是顶层 `systemInstruction` 字段。
- **Fix**：Provider 层自生成 `gemini_N` 形式的 ID；system message 在构建请求时放到顶层。

**3. OpenAI streaming token 计数默认关闭**

- **Saw**：OpenAI 的流式响应中 token 用量不在 SSE 事件中返回。
- **Why**：需要显式设置 `stream_options: {include_usage: true}`。
- **Fix**：在 buildRequest 中对 OpenAI 路径设置此选项。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- 多 Provider 的核心架构：**第一公民（DeepSeek）+ 第二公民（pkg/llm）两层并排，pkg/agent 用 type switch 衔接**——CI lint 用一行 grep 把这条规则物化
- `pkg/llm` 接口刻意瘦：只 `ChatStream`，没有非流式；`TurnDone` 不带缓存字段——抽象不稀释 DeepSeek
- Event 是 sum type with marker method，让 consumer type-switch 编译期完整
- `translate.go` 是双层架构的唯一接口黏合层——翻译方向永远 deepseek → llm
- **Anthropic**：tool_result 必须合并进同一条 user 消息；system prompt 单独顶层字段
- **Gemini**：functionCall 无 ID，provider 自己生成 `gemini_N`；systemInstruction 单独顶层字段
- **OpenAI**：streaming usage 必须显式 `stream_options.include_usage=true` 才开
- `compatible` 复用 OpenAI 协议，只换 URL——本地 Ollama / 公司 vLLM 一行命令接入
- Provider 自动探测优先 DeepSeek；要用第二公民必须显式 `--provider=xxx`，避免意外切换
- TUI banner 明确公开降级（FIM / cache / Reasoner 不可用）——诚实 > 神秘
- 每个 provider 都有专门的"协议形状测试" + "SSE 解析测试"，捕获 buildRequest 和流解析的所有怪癖

下一章——最后一章——讲 M7 与发布：JSON 输出模式、自举（用 seek 开发 seek 的真实记录）、`go install` 体验、`runtime/debug.ReadBuildInfo` 拿版本号的正确姿势、跨平台差异。

---

*对应 commit：`a1fde05`（M6 三家 provider + compatible + 双路由）、`b01bc17`（compact 阈值 80/95 → 60/75，主要是 M6 之后 1M context 下的体感调整）。运行 `go test -race ./pkg/llm/...` 验证。*
