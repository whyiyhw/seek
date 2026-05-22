# 第 2 章：大语言模型的工具调用协议

在写第一行 Go 代码之前，我们需要理解底层协议。这一章是"读文档"章节——但不是那种你读一遍就忘的文档，而是会在后续每一个章节里反复用到的基础知识。

---

## 2.1 Chat Completions API 的消息结构

DeepSeek（以及 OpenAI 兼容的 API）使用一个统一的消息列表来表示对话历史。每条消息有三个字段：`role`、`content`、以及若干可选字段。

```json
[
  {"role": "system",    "content": "You are a coding agent..."},
  {"role": "user",      "content": "Fix the nil pointer in server.go"},
  {"role": "assistant", "content": "Let me read the file first.",
                        "tool_calls": [{"id": "call_1", "function": {"name": "read", "arguments": "{\"path\": \"server.go\"}"}}]},
  {"role": "tool",      "content": "1\tpackage server\n2\t...", "tool_call_id": "call_1"},
  {"role": "assistant", "content": "Found the issue at line 47..."}
]
```

几个关键点：

**角色（role）有四种**：
- `system`：系统提示词，定义模型的行为和可用工具。通常放在消息列表的最前面，且只有一条。
- `user`：用户的输入
- `assistant`：模型的输出。当模型调用工具时，这条消息包含 `tool_calls` 字段，`content` 可以为空或为一段解释性文字
- `tool`：工具的执行结果。每条 `tool` 消息必须对应一个 `tool_call_id`，且必须紧跟在包含对应 `tool_calls` 的 `assistant` 消息后面

**`tool_calls` 和 `tool` 消息是配对的**。如果一个 `assistant` 消息声明了 3 个 tool_call，那么接下来必须有 3 条 `tool` 消息，各自对应一个 `tool_call_id`。这个配对关系是 API 强制执行的——如果你把一个有 `tool_calls` 的 `assistant` 消息推进历史，但没有配套的 `tool` 结果，下一次 API 调用就会报错。

这是整本书里最重要的不变量，后面会反复提到。

### 工具的声明方式

工具在请求体的 `tools` 字段里声明，每个工具包含名字、描述、和 JSON Schema 格式的参数定义：

```json
{
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "read",
        "description": "Read a file from the local filesystem...",
        "parameters": {
          "type": "object",
          "properties": {
            "path":   {"type": "string"},
            "offset": {"type": "integer"},
            "limit":  {"type": "integer"}
          },
          "required": ["path"],
          "additionalProperties": false
        }
      }
    }
  ]
}
```

模型不执行工具，它只是在输出里声明"我想调用这个工具，参数是这些"。实际执行由你的程序负责。

---

## 2.2 Server-Sent Events：流式输出的底层机制

如果你用非流式 API，请求发出去，等一段时间，收到完整的响应。这对聊天 UI 体验很差——用户盯着空屏幕等几秒钟。

流式 API 的工作方式：模型每生成一个 token，就立刻发过来，不等整个响应完成。底层是 HTTP 长连接 + Server-Sent Events（SSE）格式。

SSE 的格式非常简单：

```
data: {"choices":[{"delta":{"content":"I'll"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":" read"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":" the file"},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

每行 `data: ` 后面是一个 JSON 对象（delta chunk），`[DONE]` 表示流结束。

解析 SSE 流的 Go 实现：

```go
scanner := bufio.NewScanner(resp.Body)
for scanner.Scan() {
    line := scanner.Text()
    if line == "data: [DONE]" {
        break
    }
    if !strings.HasPrefix(line, "data: ") {
        continue // 空行、注释行，忽略
    }
    payload := line[6:] // 去掉 "data: " 前缀
    var chunk ChatChunk
    if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
        return fmt.Errorf("parse chunk: %w", err)
    }
    // 处理 chunk...
}
```

看起来简单，但有几个坑。

> **坑 #1：SSE 流里的 `[DONE]` 是字符串，不是 JSON**
>
> 你不能把 `data: [DONE]` 直接 `json.Unmarshal`，因为 `[DONE]` 不是合法的 JSON。必须在 unmarshal 之前检查并跳过这一行。
>
> 更隐蔽的问题：有些 HTTP 代理（比如 Nginx 的某些配置）会把多行 SSE 合并成一行，或者把一行 SSE 拆成多行。生产环境里要做好防御。

---

## 2.3 流式工具调用的组装：delta 为什么要按 index 合并

工具调用的流式输出比文本更复杂。模型会把一个工具调用拆成多个 chunk 发送：

```
chunk 1: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":""}}]}}]}
chunk 2: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}
chunk 3: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\""}}]}}]}
chunk 4: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"server.go\"}"}}]}}]}
chunk 5: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}
```

注意 `index` 字段。当模型并行调用多个工具时，同一个流里会交替出现不同 index 的 chunk：

```
chunk 1: {index:0, id:"call_1", name:"read",    arguments: ""}
chunk 2: {index:1, id:"call_2", name:"grep",    arguments: ""}
chunk 3: {index:0, arguments: "{\"path\""}
chunk 4: {index:1, arguments: "{\"pattern\""}
chunk 5: {index:0, arguments: ":\"server.go\"}"}
chunk 6: {index:1, arguments: ":\"nil pointer\"}"}
```

正确的处理方式是按 `index` 维护一个 map，把每个工具调用的 arguments 字符串累积起来，直到收到 `finish_reason: "tool_calls"` 才最终解析 JSON：

```go
type partialToolCall struct {
    id        string
    name      string
    arguments strings.Builder
}

accumulator := make(map[int]*partialToolCall)

// 处理每个 delta
for _, tc := range delta.ToolCalls {
    if accumulator[tc.Index] == nil {
        accumulator[tc.Index] = &partialToolCall{}
    }
    p := accumulator[tc.Index]
    if tc.ID != "" { p.id = tc.ID }
    if tc.Function.Name != "" { p.name = tc.Function.Name }
    p.arguments.WriteString(tc.Function.Arguments)
}

// finish_reason == "tool_calls" 时，组装最终结果
for _, p := range accumulator {
    var args map[string]any
    json.Unmarshal([]byte(p.arguments.String()), &args)
    // 执行工具...
}
```

> **坑 #2：工具调用的 arguments 是 JSON 字符串，不是 JSON 对象**
>
> 注意上面的 `"arguments": "{\"path\": \"server.go\"}"` ——arguments 是一个 **字符串**，里面的内容是 JSON。你需要两次解析：先解析 chunk 的外层 JSON，再解析 arguments 字符串。
>
> 更糟糕的是：如果模型生成的 arguments JSON 不合法（这比你想象的更常见），第二次解析会失败。这时候要把错误作为工具结果返回给模型，而不是崩溃。

---

## 2.4 DeepSeek 的差异化能力

### 前缀缓存

DeepSeek 会缓存每个请求的 prompt 前缀。如果下一个请求和上一个请求的 prompt 前缀相同，这部分 token 的计算成本降至约 1/50（V4-Flash 价格：命中 $0.0028/M，未命中 $0.14/M）。

缓存命中的条件：
- 前缀至少约 64 个 token（太短的前缀不会被缓存）
- 前缀字节必须完全一致（一个空格的差异都会破坏命中）

对 seek 的设计影响：

**工具的 JSON Schema 必须是 `package-level []byte` 常量**，不能在运行时构建。如果每次请求都重新序列化 Schema，字段顺序可能不一样（Go 的 map 是随机顺序的），导致字节不一致，缓存失效。

```go
// 正确：package-level 常量，字节永远相同
var schemaBytes = []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)

// 错误：每次调用可能产生不同字节顺序
func (t Tool) Schema() json.RawMessage {
    return json.Marshal(map[string]any{
        "type": "object",
        "properties": map[string]any{"path": map[string]any{"type": "string"}},
    })
}
```

响应里会包含缓存命中情况：

```json
{
  "usage": {
    "prompt_tokens": 2048,
    "completion_tokens": 150,
    "prompt_cache_hit_tokens": 1800,  // 命中缓存的 token 数
    "prompt_cache_miss_tokens": 248    // 未命中的 token 数
  }
}
```

第 14 章会详细分析如何追踪和优化这个比率。

### Reasoner

DeepSeek V4 支持 `thinking` 模式，开启后模型会先做一段明确的推理（`reasoning_content`），再给出最终回答（`content`）：

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "reasoning_content": "Let me analyze this step by step...",
      "content": "The bug is at line 47."
    }
  }]
}
```

两个重要约束：

1. **`reasoning_content` 不能回传给模型**。在下一轮请求里，assistant 消息里不能包含 `reasoning_content` 字段，否则 API 会返回 400 错误。每次发请求前必须把历史里的 `reasoning_content` 清掉。

2. **开启 thinking 模式后不支持 `tools` 参数**。所以我们不能直接在主对话模型上开启 thinking，而是把 reasoner 包装成一个独立的工具（第 7 章的 `think` 工具），在需要的时候调用。

### FIM 端点

Fill-in-the-Middle 用于代码补全场景：

```
请求：
{
  "model": "deepseek-coder",
  "prompt": "func add(a, b int) int {\n\t",   // 光标前的代码
  "suffix": "\n}",                             // 光标后的代码
  "max_tokens": 50
}

响应：
{
  "choices": [{"text": "return a + b"}]
}
```

FIM 的端点是 `/beta/completions`，使用的是 OpenAI 的旧版 completions 格式（不是 chat completions），返回的是 `choices[0].text` 而不是 `choices[0].message.content`。

> **坑 #3：FIM 的端点不是 `/chat/completions`**
>
> DeepSeek 的大多数能力都在 `/chat/completions`，但 FIM 在 `/beta/completions`，而且用的是完全不同的请求/响应格式。如果你把 FIM 请求发到 chat 端点，会得到一个 400 错误，错误信息不够清晰。

---

## 本章小结

- 消息列表是对话的载体。`tool_calls` 消息和 `tool` 结果消息必须配对，这个不变量贯穿整本书
- SSE 是流式输出的底层格式。`[DONE]` 不是 JSON，要特殊处理
- 工具调用 delta 按 `index` 组装，arguments 是字符串形式的 JSON
- DeepSeek 的三个差异化能力：前缀缓存（Schema 必须字节恒定）、reasoner（reasoning_content 不能回传，不支持 tools）、FIM（独立端点，不同格式）

这些是接下来所有代码的基础。下一章，我们开始写第一个版本——一个能和 DeepSeek 通信的最小 API 客户端。

---

*对应代码：第 3 章开始。起点 commit：项目初始化。*
