# 第 3 章：M0 — 最小可用 API 客户端

> 对应代码：`pkg/deepseek/`
> 起点：空仓库。终点：能发出一次流式对话请求并正确解析响应。

---

这是本书第一段真正的代码。M0 只做一件事：把 Go 程序和 DeepSeek API 连通，支持流式输出。

没有 TUI，没有工具调用，没有 Agent 循环。只是一个能发请求、能读 SSE 流、能把 token 一个个打出来的客户端。

这个最小化目标是刻意的。你需要在加入任何复杂性之前，先把通信层做对。通信层的 bug 最难调试，因为你不知道问题在客户端还是在服务端、在 JSON 解析还是在 HTTP 连接、在你的代码还是在 API 文档。

---

## 3.1 零外部依赖的设计决策

`pkg/deepseek` 的 `go.mod` 里没有任何第三方依赖，将来也不会有。

这是一个主动的设计选择，不是因为懒得找库。原因：

**1. 行为可预测性**。标准库的每个函数你都能 `go doc` 查到精确语义。第三方 HTTP 库可能有自己的重试逻辑、连接池策略、超时处理——在调试"为什么流式输出卡住了"的时候，你不想同时怀疑你的代码和库的代码。

**2. 零 CVE 面**。`pkg/deepseek` 不依赖任何包，就不会因为依赖链里某个库爆洞而被扫到。

**3. DeepSeek 的 API 格式非常标准**。它是 OpenAI Chat Completions 的严格超集，HTTP + JSON + SSE，Go 标准库完全覆盖。不需要 SDK。

### 包结构

```
pkg/deepseek/
  client.go   HTTP 客户端 + Chat/ChatStream/FIM 方法
  types.go    所有请求/响应结构体
  stream.go   SSE 解析器 + 事件类型
  fim.go      FIM 端点（fill-in-the-middle）
```

---

## 3.2 Client 的结构

```go
type Client struct {
    apiKey  string
    baseURL string
    http    *http.Client
}
```

三个字段，没有全局状态。`baseURL` 可以被测试覆盖（第 3.5 节会看到这很重要），`http.Client` 可以注入自定义传输层。

构造函数用 functional options 模式：

```go
func New(opts ...Option) *Client {
    c := &Client{
        baseURL: DefaultBaseURL,
        http:    &http.Client{Timeout: 5 * time.Minute},
    }
    for _, o := range opts {
        o(c)
    }
    return c
}
```

`Timeout: 5 * time.Minute` 是为流式请求设的。非流式 chat 通常几秒钟完成，但 reasoner 的深度思考可以跑几分钟——5 分钟是一个合理的上限，同时能防止连接泄漏。

### do：所有 HTTP 请求的统一入口

```go
func (c *Client) do(ctx context.Context, path string, payload any) (*http.Response, error) {
    if c.apiKey == "" {
        return nil, errors.New("deepseek: missing api key (set DEEPSEEK_API_KEY)")
    }

    buf, err := json.Marshal(payload)
    if err != nil {
        return nil, fmt.Errorf("deepseek: encode request: %w", err)
    }

    req, err := http.NewRequestWithContext(ctx, http.MethodPost,
        c.baseURL+path, bytes.NewReader(buf))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    req.Header.Set("Accept", "application/json")

    resp, err := c.http.Do(req)
    if err != nil {
        return nil, fmt.Errorf("deepseek: http: %w", err)
    }
    return resp, nil
}
```

注意 `bytes.NewReader(buf)` 而不是 `bytes.NewBuffer(buf)`。两者都实现 `io.Reader`，但 `NewReader` 是只读的，不会意外被消费；`NewBuffer` 的底层 `*bytes.Buffer` 可以被写入，在并发场景下容易出问题。这里是小细节，但写 Go 就是这些小细节的积累。

---

## 3.3 类型系统：把 API 文档翻译成 Go

`types.go` 是 DeepSeek API 的 Go 映射。几个需要注意的地方：

### Message：携带 ReasoningContent 的坑

```go
type Message struct {
    Role             string     `json:"role"`
    Content          string     `json:"content,omitempty"`
    ToolCallID       string     `json:"tool_call_id,omitempty"`
    ToolCalls        []ToolCall `json:"tool_calls,omitempty"`

    // ReasoningContent is populated by deepseek-reasoner responses.
    // It MUST be stripped before sending the message back to the API —
    // DeepSeek rejects requests that include prior reasoning_content fields.
    ReasoningContent string `json:"reasoning_content,omitempty"`
}
```

`ReasoningContent` 是 reasoner 专有的字段，注释特别说明了：把这个字段的消息发回 API 会报错。为什么这样设计？DeepSeek 认为推理过程是单次的——你问一次，它思考一次，思考结果不应该成为"历史"的一部分被反复回传。第 7 章的 `think` 工具会专门处理这个问题。

### Usage：暴露缓存元数据

```go
type Usage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`

    PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
    PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

func (u Usage) HitRatio() float64 {
    total := u.PromptCacheHitTokens + u.PromptCacheMissTokens
    if total == 0 { return 0 }
    return float64(u.PromptCacheHitTokens) / float64(total)
}
```

`PromptCacheHitTokens` 和 `PromptCacheMissTokens` 是 DeepSeek 独有的字段，OpenAI 没有。这就是 `pkg/deepseek` 不能被通用接口抽象掉的原因之一——一旦抽象，这两个字段就消失了，你就失去了优化缓存命中率的数据基础。

---

## 3.4 实现 SSE 解析器

流式请求的 SSE 解析在 `stream.go` 里。先看事件类型的设计：

```go
type StreamEventType string

const (
    EventDelta          StreamEventType = "delta"           // 普通文本 token
    EventReasoningDelta StreamEventType = "reasoning_delta" // 推理链 token
    EventToolCallDelta  StreamEventType = "tool_call_delta" // 工具调用片段
    EventDone           StreamEventType = "done"            // 流结束
)

type StreamEvent struct {
    Type         StreamEventType
    Delta        string    // EventDelta / EventReasoningDelta
    ToolCall     *ToolCall // EventToolCallDelta
    FinishReason string    // EventDone
    Usage        Usage     // EventDone
}
```

为什么用 channel 而不是 callback？

两种方案都常见：channel 让调用方在自己的 goroutine 里处理事件（`for ev := range stream`），callback 在解析 goroutine 里调用（`parser.OnDelta(func(s string) {})`）。

seek 选了 channel，因为 Agent 循环需要在处理流事件的同时做状态管理（组装工具调用、维护消息历史），`for range` 循环比嵌套 callback 更容易读，而且 `ctx.Done()` 的检查可以统一放在 `for range` 之外。

### ChatStream 的实现

```go
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamEvent, error) {
    r := *req       // 复制请求，不修改调用方的原始结构体
    r.Stream = true
    if r.StreamOptions == nil {
        r.StreamOptions = &StreamOptions{IncludeUsage: true}
    }

    resp, err := c.do(ctx, endpointChat, &r)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode/100 != 2 {
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        return nil, parseAPIError(resp.StatusCode, body)
    }

    out := make(chan StreamEvent, 16)
    go func() {
        defer close(out)
        defer resp.Body.Close()
        // ... 解析循环 ...
    }()

    return out, nil
}
```

几个细节：

**`r := *req` 的复制**：调用方传进来的 `ChatRequest` 指针可能被复用，我们要修改 `Stream = true`，不应该污染调用方的原始结构。

**`IncludeUsage: true`**：默认不开启这个选项时，流式响应不包含 usage 统计。我们强制开启，这样每次流式请求都能得到缓存命中率数据。

**`make(chan StreamEvent, 16)` 的缓冲**：解析 goroutine 和消费 goroutine 是解耦的。16 的缓冲意味着解析 goroutine 最多可以领先消费者 16 个事件——在消费者做工具执行这样的 IO 操作时，不需要每个 token 都等待。

### SSE 解析循环

```go
sc := bufio.NewScanner(resp.Body)
sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)  // 允许单行最大 1 MiB

for sc.Scan() {
    line := sc.Bytes()
    if len(line) == 0 { continue }
    if !bytes.HasPrefix(line, []byte("data:")) { continue }

    payload := bytes.TrimSpace(line[len("data:"):])
    if len(payload) == 0 { continue }
    if string(payload) == "[DONE]" { break }

    var chunk streamChunk
    if err := json.Unmarshal(payload, &chunk); err != nil {
        // 解析失败：通过 FinishReason 传递错误信息
        out <- StreamEvent{
            Type:         EventDone,
            FinishReason: "decode_error:" + truncate(err.Error(), 80),
        }
        return
    }
    // 处理 chunk ...
}
```

> **坑 #4：`bufio.Scanner` 默认缓冲区是 64 KiB，工具调用 arguments 可能超这个限制**
>
> 当模型要写一段很长的代码时，它可能把整段代码作为工具调用的参数。一个 200 行的 Go 函数轻松超过 64 KiB 的 SSE 单行限制。如果不扩大缓冲区，`sc.Scan()` 会静默失败（返回 false，`sc.Err()` 里才有错误）。
>
> `sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)` 把上限设为 1 MiB，覆盖绝大多数情况。

### `[DONE]` 的特殊处理

```go
if string(payload) == "[DONE]" { break }
```

`[DONE]` 不是合法的 JSON，不能放进 `json.Unmarshal`。如果你先 Unmarshal 再判断，会得到一个解析错误，然后你需要判断这个错误是"真正的错误"还是"正常的流结束"——那是一场噩梦。提前检查，直接跳出。

---

## 3.5 测试策略：用 httptest 模拟 DeepSeek

`pkg/deepseek` 的测试不需要真实的 API key，用 `net/http/httptest` 搭一个本地 HTTP 服务器来模拟 DeepSeek 的响应。

```go
func newFakeServer(t *testing.T, responses []string) *httptest.Server {
    t.Helper()
    i := 0
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        for _, chunk := range responses[i] {
            fmt.Fprintf(w, "data: %s\n\n", chunk)
        }
        fmt.Fprint(w, "data: [DONE]\n\n")
        i++
    }))
}
```

测试用例可以精确控制服务端发什么，包括模拟各种边界情况：

```go
func TestChatStream_PartialJSON(t *testing.T) {
    // 模拟一个损坏的 SSE 流：第二个 chunk 是非法 JSON
    srv := newFakeServer(t, []string{
        `{"choices":[{"delta":{"content":"hello"}}]}`,
        `{"not valid json`,  // 损坏的 chunk
    })
    // 验证客户端优雅处理，不 panic
}
```

这个测试策略让测试套件可以在 CI 里没有任何外部依赖地运行。真实 API 只在 smoke test 里用，不进 CI。

---

## 3.6 验收：一次完整的流式对话

M0 完成后，你可以运行这样的程序：

```go
client := deepseek.New(deepseek.WithAPIKey(os.Getenv("DEEPSEEK_API_KEY")))

stream, err := client.ChatStream(ctx, &deepseek.ChatRequest{
    Model: deepseek.ModelV4Flash,
    Messages: []deepseek.Message{
        {Role: deepseek.RoleUser, Content: "用一句话解释什么是递归"},
    },
})
if err != nil { log.Fatal(err) }

for ev := range stream {
    if ev.Type == deepseek.EventDelta {
        fmt.Print(ev.Delta)
    }
    if ev.Type == deepseek.EventDone {
        fmt.Printf("\n[usage: %+v, cache hit: %.1f%%]\n",
            ev.Usage, ev.Usage.HitRatio()*100)
    }
}
```

输出大概是：

```
递归是函数调用自身来解决问题的方法，每次调用都使问题规模缩小，直到到达基本情况为止。
[usage: {PromptTokens:15 CompletionTokens:42 ...}, cache hit: 0.0%]
```

第一次调用缓存命中率是 0%，这是正常的——缓存是"读时命中，写时填充"，第一次请求在建立缓存。同样的系统提示词发第二次请求，命中率会提升。

---

## 本章小结

- `pkg/deepseek` 是零外部依赖的纯 stdlib 实现，让通信层行为可预测
- SSE 解析的两个关键坑：`[DONE]` 不是 JSON；单行缓冲区需要扩大到 1 MiB
- `Usage.HitRatio()` 是缓存优化的数据基础，后续章节会反复用到
- 测试用 `httptest` 模拟服务端，CI 不需要真实 API key

下一章，我们在这个客户端之上构建 Agent 循环——消息历史的管理、工具调用 delta 的组装、以及那个让会话损坏的孤儿 `tool_calls` 问题。

---

*对应 commit：`pkg/deepseek` 的初始实现。运行 `go test ./pkg/deepseek/...` 验证。*
