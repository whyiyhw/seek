# 第 6 章：M3 — 推理增强

> 对应代码：`internal/tools/think/`、`internal/tools/fimcomplete/`
> 起点：基础工具可用。终点：模型可以在复杂问题上调用 reasoner，可以用 FIM 做代码填空。

---

到目前为止，seek 的模型调用是单一的：一个 Chat 模型，一次请求，一个响应。M3 引入了第一种"模型协作"：主模型发现一个问题需要深度推理，它调用 `think` 工具，这个工具在内部运行 reasoner 并把推理结果返回——主模型拿到这个结果，继续工作。

这一章还会介绍 FIM（Fill-in-the-Middle）：一种专为代码填空设计的接口，比 chat 便宜，延迟更低，适合小范围的精确编辑。

---

## 6.1 为什么要"工具化"reasoner

> **时间线脚注**：DeepSeek 一度有一个独立的 `deepseek-reasoner` 模型——要拿到推理链，你得发请求到它的端点。V4（2026-01 发布）把推理能力折叠成了普通模型上的一个开关：`Thinking.Type = "enabled"`。从此 reasoner 不再是一个"模型"，而是一种"模式"。本章后面的代码示例已经是 V4 写法（`ModelV4Flash` + `Thinking.Type=enabled` + `ReasoningEffort=high`，commit `aa5e532`）。如果你在仓库历史里看到对 `deepseek-reasoner` 模型常量的引用，那是 V3 时代的遗物——学习要点（隔离历史、`reasoning_content` 不能回传、`thinking` 与 `tools` 不能同时用）在两个时代是完全一样的，本章不需要因为 V4 推翻任何结论。

DeepSeek 的 reasoner 能力（`thinking` 参数）有一个重要的约束：**开启 `thinking` 的请求不支持 `tools` 字段**。

这意味着你不能这样用：

```
主模型（有 tools）→ 直接开启 thinking → 仍然能调用工具
```

如果你在主模型的请求里同时设置 `Thinking.Type = "enabled"` 和 `Tools = [...]`，API 会返回错误。

所以有两个选择：

**选择 A：主模型开 thinking，不支持工具调用**
- 推理能力强，但失去了工具调用能力。模型没法读文件、修代码——那就不是编程智能体了。

**选择 B：主模型正常模式（有工具），需要深度推理时调用一个独立的 reasoner**
- 主模型保留工具调用能力，在遇到复杂问题时主动选择"调用 think 工具"
- `think` 工具在内部向 reasoner 模型发出一个完全独立的请求，返回推理结果

seek 选择 B，把 reasoner 包装成一个普通工具。

---

## 6.2 think 工具的关键设计：完全隔离历史

```go
type Tool struct {
    client     *deepseek.Client
    modelFunc  func() string  // 调用方当前选用的模型；运行时解析
    effortFunc func() string  // 会话当前 /effort 设置；运行时解析
}

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
    sys, userMsg, err := parseArgs(raw)
    if err != nil { return "", err }

    // 注意：这里发起的是一个全新的 Chat 调用
    // 没有历史消息，没有工具声明，只有 [system, user]
    resp, err := t.client.Chat(ctx, t.buildRequest(sys, userMsg))
    // ...
}

func (t Tool) buildRequest(sys, userMsg string) *deepseek.ChatRequest {
    return &deepseek.ChatRequest{
        Model: t.modelName(),  // ← 跟随调用方的 /model 选择
        Messages: []deepseek.Message{
            {Role: deepseek.RoleSystem, Content: sys},
            {Role: deepseek.RoleUser,   Content: userMsg},
        },
        Thinking:        &deepseek.ThinkingMode{Type: "enabled"},
        ReasoningEffort: t.bumpEffort(),  // ← 跟随 /effort，一步提升
    }
}

// bumpEffort 返回 think 工具应该使用的 reasoning_effort，
// 总是比会话当前的 /effort 高一级：
//
//	""     → "high"  （默认/off：think 比 chat 高一档）
//	"high" → "max"   （会话 high → think max）
//	"max"  → "max"   （已在最高级，不变）
func (t Tool) bumpEffort() string {
	var sessionEffort string
	if t.effortFunc != nil {
		sessionEffort = t.effortFunc()
	}
	switch sessionEffort {
	case "max":
		return "max"
	case "high":
		return "max"
	default: // ""（off）或其他未知值
		return "high"
	}
}

// modelName 在执行时解析当前 /model；返回空时回退到 V4-Flash。
func (t Tool) modelName() string {
    if t.modelFunc != nil {
        if m := t.modelFunc(); m != "" {
            return m
        }
    }
    return deepseek.ModelV4Flash
}
```

> **为什么 `modelFunc` 而不是 `model string`？**
>
> 早期版本把模型名直接写死成 `ModelV4Flash`(commit 早于 `cc73860`)——理由是"反正 think 内部用什么模型对用户透明"。后来用户报告:跑 V4-Pro 的会话里, think 工具出来的推理质量明显比主模型差;同时定价表上 think 算的钱是 V4-Flash, 实际计费应该是 V4-Pro——状态栏的成本数偷偷少算。
>
> 修法(commit `cc73860`):构造时传入一个 `func() string`,Execute 时再调用。这样 `/model` 切到 V4-Pro 之后, 后续 think 调用就跟着切。**回调而不是固定值**是因为 Tool 对象在 Agent 启动时构造一次, 但 `/model` 可以会话中任意切换——必须运行时解析。
>
> 还有一个相关的改动(commit `a2b095a`):**reasoning 模型上 `Thinking.Type=enabled` 由 `pkg/deepseek` 自动加**。调用方不再需要手动在请求里写 thinking 开关——只要选了 reasoner 系的模型, 自动开。这跟 think 工具的取舍是一致的:**减少调用方需要记的事**。
>
> **`effortFunc`：跟随 `/effort` 的推理深度**
>
> 与 `modelFunc` 同源的设计模式：`think` 工具的推理深度应该跟随会话当前设置的 `/effort`。如果用户在会话中把 effort 从 `high` 调到 `max`，之后的 think 调用也应该更深——再加一级，在更高一档的水平上运行。
>
> 规则（`bumpEffort`）：`"" → "high"`（off 或者未设置时用 high 作为最低档）、`"high" → "max"`（会话已经主动选了 high，think 就上到 max）、`"max" → "max"`（已经在顶，不能再升）。这个"高一档"的设计动机是：用户调用 think 就是为了获得比默认更深的推理，所以即使会话是 off，think 仍然跑 high；会话已经在 high，think 就上到 max。
>
> 默认会话 effort 是 `"max"`（`cmd/seek/main.go`），所以开箱即用时 think 也是 max——`bumpEffort("max") = "max"`。用户可以通过 `/effort off` 或 `/effort high` 把会话调到更低档来节约花费。

**为什么必须是全新的调用，而不是把历史一起发过去？**

两个原因：

**1. ReasoningContent 不能回传**。如果你之前用过 think，历史里的 assistant 消息会有 `reasoning_content` 字段。DeepSeek 的 reasoner API 会拒绝包含这个字段的请求。你当然可以调用 `StripReasoningContent` 清理，但这个清理会丢失推理上下文，意义不大。

**2. 历史对推理质量的影响**。主对话的历史里充满了工具调用、文件内容、代码片段。把这些内容都发给 reasoner，可能让它在大量无关信息里迷失。一个干净的"只有任务描述"的请求，比一个带着完整对话历史的请求，通常会得到更专注的推理结果。

调用方可以把相关上下文作为 `context` 参数传入：

```go
think(
    task="分析这段代码的时间复杂度并给出优化建议",
    context="func bubbleSort(arr []int) {\n    for i := ...",
    reflect=false
)
```

这样就保留了所有相关信息，同时避免了不相关历史的干扰。

---

## 6.3 流式化 think：当工具也有"输出过程"

reasoner 的一次调用通常需要几秒到十几秒——因为它要"先想，再回答"，思考过程本身就是输出的一部分。在 TUI 里，如果这段时间屏幕什么都不显示，用户会以为程序卡死了。

解决方案：让 `think` 工具也支持流式输出，推理链 token 实时推送到 TUI。

这需要一个新的接口：

```go
type StreamingTool interface {
    Tool
    ExecuteStream(
        ctx context.Context,
        raw json.RawMessage,
        push func(StreamDelta) error,
    ) (string, error)
}

type StreamDelta struct {
    Delta     string
    Reasoning bool  // true = 推理链，false = 最终回答
}
```

`ExecuteStream` 和 `Execute` 返回完全相同的字符串结果，但 `ExecuteStream` 在过程中通过 `push` 回调实时发送每个 delta。Agent 在分发工具时，会检测工具是否实现了 `StreamingTool`：

```go
if st, ok := tool.(tools.StreamingTool); ok {
    result, err = st.ExecuteStream(ctx, raw, func(d tools.StreamDelta) error {
        // 推送到 TUI 的 ToolDelta 事件
        return sendEvent(out, ToolDelta{Name: tc.Function.Name, Delta: d.Delta, Reasoning: d.Reasoning})
    })
} else {
    result, err = tool.Execute(ctx, raw)
}
```

`think` 工具实现了这两个路径——`Execute` 用于测试和不支持流式的调用方，`ExecuteStream` 用于 TUI：

```go
func (t Tool) ExecuteStream(ctx context.Context, raw json.RawMessage, push func(tools.StreamDelta) error) (string, error) {
    sys, userMsg, err := parseArgs(raw)
    if err != nil { return "", err }

    stream, err := t.client.ChatStream(ctx, buildRequest(sys, userMsg))
    if err != nil { return "", err }

    var reasoning, content strings.Builder
    for ev := range stream {
        switch ev.Type {
        case deepseek.EventReasoningDelta:
            reasoning.WriteString(ev.Delta)
            if err := push(tools.StreamDelta{Delta: ev.Delta, Reasoning: true}); err != nil {
                return "", err
            }
        case deepseek.EventDelta:
            content.WriteString(ev.Delta)
            if err := push(tools.StreamDelta{Delta: ev.Delta, Reasoning: false}); err != nil {
                return "", err
            }
        }
    }

    return formatResult(reasoning.String(), content.String(), usage), nil
}
```

两条路径（`Execute` 和 `ExecuteStream`）产生的字符串结果是完全一致的——同样的 `formatResult` 函数，同样的输入内容。这意味着工具结果追加进对话历史的内容不受调用路径影响，保证了会话的一致性。

---

## 6.4 截断：防止推理结果撑爆 context

reasoner 的推理链可以很长——特别是把 `ReasoningEffort` 设为 `"high"` 的时候，它可能写出几千 token 的思考过程。如果把全部内容都追加进对话历史，后续请求的 context 会迅速膨胀。

seek 对推理链和回答内容各设了 4000 字符的上限：

```go
const (
    reasoningCap = 4000
    contentCap   = 4000
)

func clip(s string, limit int) string {
    if len(s) <= limit { return s }
    return s[:limit] + fmt.Sprintf(
        "\n…[truncated %d chars; re-call think with a narrower task for specifics]",
        len(s)-limit)
    )
}
```

截断时附上提示，让模型知道内容被截断了，以及如何获取完整内容。这比静默截断要好——模型不会误以为推理链就是那么短。

---

## 6.5 FIM：填空补全，不是全文重写

FIM（Fill-in-the-Middle）是一种专为代码补全设计的 API 模式。给出光标前后的代码，模型填中间的空白。

为什么这比 chat 更好？

当你用 chat 修改一行代码时，通常是这样的：
1. `read` 整个文件
2. `edit` 发送 old_string + new_string

对于大文件，`read` 会产生大量 context 占用。对于小修改，有时候你只需要"这里填什么"，不需要整个文件。

FIM 的请求：

```go
resp, err := t.client.FIM(ctx, &deepseek.FIMRequest{
    Model:     deepseek.ModelChat,
    Prompt:    content[:beforeEnd],   // 光标前的内容
    Suffix:    content[afterStart:],  // 光标后的内容
    MaxTokens: a.MaxTokens,
})
```

返回的 `resp.Choices[0].Text` 是填入的内容，不包含前后文。

### FIM 的端点问题

FIM 不在 `/chat/completions`，而在 `/beta/completions`，使用的是 OpenAI 的旧版 completions 格式：

```
请求格式：{model, prompt, suffix, max_tokens}
响应格式：{choices: [{text: "..."}]}
```

这和 chat 的 `{choices: [{message: {content: "..."}}]}` 完全不同。两者不能混用，需要各自独立的类型和端点。

FIM 工具本身是只读的——它返回补全文本，不直接应用到文件。调用方（通常是模型）拿到补全文本后，再调用 `edit` 工具把它写进文件。这保持了一致的权限故事：文件修改永远经过 `edit`，权限检查永远在 `edit` 里进行。

---

## 本章小结

- reasoner 被包装成工具而不是直接集成，因为 `thinking` 和 `tools` 参数不能同时使用
- `think` 工具发起完全隔离的 Chat 调用，不带历史，避免 `reasoning_content` 回传问题
- 模型选择是**运行时回调**——`modelFunc func() string` 让 `think` 跟随调用方的 `/model`，而不是写死 V4-Flash(commit `cc73860`)
- reasoning 系模型的 `Thinking.Type=enabled` 由 `pkg/deepseek` 自动加, 调用方不必显式写(commit `a2b095a`)
- `ReasoningEffort` 不再是固定值 `"high"`——`think` 工具通过 `effortFunc func() string` 跟随会话的 `/effort` 设置，并执行 `bumpEffort` 高一档规则。TUI 里敲 `/effort off|high|max` 可实时切换推理深度
- `StreamingTool` 接口让工具支持流式输出，两条路径（Execute / ExecuteStream）产生完全一致的字符串结果
- 推理结果需要截断，截断时附带提示让模型知道如何获取完整内容
- FIM 使用独立端点和独立格式，返回文本而不直接写文件，权限管控保持在 `edit` 里

下一章进入 TUI：一个可以实时渲染流式输出、响应用户交互、在终端里显示像素艺术的界面。我们会看到 bubbletea 的架构，以及为什么 alt-screen 对于聊天类工具是一个错误的选择。

---

*对应 commit：think 工具 + FIM 工具。运行 `go test -race ./internal/tools/think/ ./internal/tools/fimcomplete/` 验证。*
