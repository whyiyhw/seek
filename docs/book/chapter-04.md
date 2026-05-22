# 第 4 章：M1 — Agent 循环

> 对应代码：`pkg/agent/`
> 起点：M0 的流式客户端。终点：能自主执行多步工具调用的 Agent。

---

有了流式客户端，下一步是把它组装成真正的 Agent：接收用户消息，调用模型，执行工具，把结果返回给模型，直到任务完成。

这一章有两条并行的叙述线。一条是"正常流程"——如何把这个循环写得简洁、可扩展；另一条是"什么会出错"——一个中断路径的 bug，直到上线后才被发现，修起来比预期复杂得多。

---

## 4.1 消息历史的数据结构

Agent 的状态只有一样东西：消息列表。

```go
type Agent struct {
    cfg      Config
    messages []deepseek.Message
}
```

`messages` 就是每次发给 DeepSeek 的那个列表，从第一条 system 消息到最新的工具结果。没有单独的"当前轮"或"上下文窗口"——整个历史就是上下文。

初始化时，系统提示词放在最前面：

```go
func New(cfg Config) (*Agent, error) {
    a := &Agent{cfg: cfg}
    if cfg.SystemPrompt != "" {
        a.messages = append(a.messages, deepseek.Message{
            Role:    deepseek.RoleSystem,
            Content: cfg.SystemPrompt,
        })
    }
    // 如果是恢复会话，追加历史消息（跳过旧的 system 消息）
    for _, m := range cfg.InitialMessages {
        if m.Role == deepseek.RoleSystem { continue }
        a.messages = append(a.messages, m)
    }
    return a, nil
}
```

恢复会话时跳过旧的 system 消息，是因为系统提示词可能已经变了（用户切换了模型、开关了 `--yolo`，或者 CWD 变了）。只保留对话内容，用新的 system 提示词重新开始。

---

## 4.2 Agent 循环的状态机

`Prompt` 方法是 Agent 循环的入口。它立刻启动一个 goroutine，在 goroutine 里跑循环，通过 channel 推送事件给调用方：

```go
func (a *Agent) Prompt(ctx context.Context, userText string) <-chan Event {
    out := make(chan Event, 32)

    go func() {
        defer close(out)

        a.messages = append(a.messages, deepseek.Message{
            Role: deepseek.RoleUser, Content: userText,
        })
        out <- AgentStart{}

        for turn := 0; turn < a.cfg.MaxTurns; turn++ {
            // 1. 调用模型，流式接收
            assistant, usage, finish, err := a.runTurn(ctx, out)

            // 2. 错误处理（包括用户取消）
            if err != nil { ... }

            // 3. 不变量检查：tool_calls 必须配对
            if len(assistant.ToolCalls) > 0 && finish != "tool_calls" { ... }

            // 4. 把 assistant 消息追加到历史
            a.messages = append(a.messages, assistant)

            // 5. 如果没有工具调用，任务完成，退出循环
            if len(assistant.ToolCalls) == 0 { break }

            // 6. 执行所有工具，把结果追加到历史
            for _, tc := range assistant.ToolCalls {
                result, _ := a.dispatchTool(ctx, tc, out)
                a.messages = append(a.messages, deepseek.Message{
                    Role: deepseek.RoleTool, ToolCallID: tc.ID, Content: result,
                })
            }
            // 7. 回到步骤 1，模型看到工具结果，继续推理
        }

        out <- AgentEnd{...}
    }()

    return out
}
```

这个循环的终止条件有三种：
- 模型的 `finish_reason` 是 `"stop"`（任务完成）
- 达到 `MaxTurns` 上限（防止无限循环）
- `ctx` 被取消（用户按 Esc 或 Ctrl+C）

---

## 4.3 工具调用 delta 的组装

`runTurn` 负责流式接收模型输出并组装成完整的 assistant 消息。文本 token 很简单（直接拼接），工具调用需要按 `index` 组装：

```go
pending := map[int]*deepseek.ToolCall{}
maxIdx  := -1

for ev := range stream {
    switch ev.Type {
    case deepseek.EventDelta:
        assistant.Content += ev.Delta
        out <- MessageDelta{Delta: ev.Delta}

    case deepseek.EventToolCallDelta:
        tc := ev.ToolCall
        cur, ok := pending[tc.Index]
        if !ok {
            cur = &deepseek.ToolCall{Index: tc.Index, Type: "function"}
            pending[tc.Index] = cur
            if tc.Index > maxIdx { maxIdx = tc.Index }
        }
        if tc.ID != ""               { cur.ID = tc.ID }
        if tc.Function.Name != ""    { cur.Function.Name = tc.Function.Name }
        if tc.Function.Arguments != "" {
            cur.Function.Arguments += tc.Function.Arguments  // 累积字符串
        }

    case deepseek.EventDone:
        usage  = ev.Usage
        finish = ev.FinishReason
    }
}

// 流结束后，按 index 顺序组装 ToolCalls 列表
if maxIdx >= 0 {
    assistant.ToolCalls = make([]deepseek.ToolCall, 0, maxIdx+1)
    for i := 0; i <= maxIdx; i++ {
        if p, ok := pending[i]; ok {
            assistant.ToolCalls = append(assistant.ToolCalls, *p)
        }
    }
}
```

`Function.Arguments` 是一个字符串形式的 JSON，在流式过程中是分片到达的。不需要尝试在中途解析，等流结束后一次性 `json.Unmarshal` 即可。

---

## 4.4 孤儿 tool_calls：一个让会话损坏的 bug

在第一个能运行的版本里有一个潜在的 bug，在开发阶段完全看不出来，只有在用户按下 Esc 的时候才会触发。

### 问题的发现

用户在一次工具调用正在流式传输时按了 Esc。下一次发消息，界面立刻报错：

```
An assistant message with 'tool_calls' must be followed by tool messages
responding to each 'tool_call_id'.
```

而且这个错误是永久性的——每次发消息都报这个错，会话被完全锁死，必须重开。

### 根因分析

循环里的 ctx 取消路径没有被仔细处理。原始代码大概是：

```go
// 错误的版本
assistant, usage, finish, err := a.runTurn(ctx, out)
if err != nil {
    // 忘记检查 ctx 是不是被取消了
    out <- ErrorEvent{Err: err}
    return
}
a.messages = append(a.messages, assistant)  // ← 这里是问题所在
```

当用户按 Esc，`ctx` 被取消，`runTurn` 的 SSE 流提前关闭。此时 `assistant` 消息已经开始组装（收到了工具调用的 `id` 和 `name`），但 `finish_reason` 还没有到达（因为流被切断了），所以 `finish` 是空字符串。

`err` 是 `nil`——因为 Go 的 HTTP 客户端在 ctx 取消时，`sc.Scan()` 会停止扫描，`sc.Err()` 返回 `nil`（ctx 错误在 `ctx.Err()` 里）。

于是程序走到了 `a.messages = append(a.messages, assistant)`，把一个半组装的 assistant 消息追加进历史。这条消息带着 `tool_calls` 字段（调用 ID 存在），但后面没有对应的 `tool` 结果消息。DeepSeek 的 API 规定这个配对是必须的，一旦违反，后续每次请求都会被拒绝。

### 修复：两层防护

**第一层：检测 ctx 取消，丢弃整个不完整的轮次**

```go
assistant, usage, finish, err := a.runTurn(ctx, out)
if err != nil {
    if errors.Is(err, context.Canceled) || ctx.Err() != nil {
        // 用户主动取消：不追加任何消息，保持历史干净
        out <- AgentEnd{Usage: totalUsage, Turns: turns - 1}
        return
    }
    out <- ErrorEvent{Err: err}
    return
}
```

`runTurn` 在流结束后需要明确检查 `ctx.Err()` 并返回它：

```go
// runTurn 的末尾
if err := ctx.Err(); err != nil {
    return deepseek.Message{}, deepseek.Usage{}, "", err
}
```

**第二层：不变量检查，拒绝提交不一致的状态**

ctx 取消只是触发孤儿 `tool_calls` 的其中一条路径。服务端在发 `[DONE]` 之前断开连接、SSE 解析出错、服务端错误地在 `tool_calls` 的 assistant 消息上附了 `finish_reason="stop"`——这些路径都会产生相同的症状：`assistant.ToolCalls` 非空，但 `finish` 不是 `"tool_calls"`。

所以在追加消息之前，加一个明确的不变量检查：

```go
if len(assistant.ToolCalls) > 0 && finish != "tool_calls" {
    out <- ErrorEvent{Err: fmt.Errorf(
        "agent: refusing to commit turn — assistant emitted %d tool_call(s) " +
        "but stream ended with finish_reason=%q",
        len(assistant.ToolCalls), finish)}
    return
}
```

这个检查把"什么样的状态可以追加进历史"变成了一个显式的不变量，而不是散落在各处的隐式约定。任何违反这个不变量的路径都会被捕获，而不是静默地损坏会话。

> **坑 #5：修 bug 时，测试要针对"状态形状"，而不是"触发原因"**
>
> 第一版修复只覆盖了 ctx 取消这一条路径，测试也只测了"用户按 Esc 后历史是否干净"。然后发现还有三条路径会产生完全相同的症状：SSE 提前关闭、decode 错误、finish_reason 不匹配。
>
> 每条路径的根因不同，但症状是相同的：`len(ToolCalls) > 0 && finish != "tool_calls"`。正确的测试应该直接测这个形状，而不是测每个触发原因。覆盖了形状，所有产生这个形状的路径都自动被覆盖。

### 加载时的 Repair

即使修了循环里的 bug，用户可能已经有一些损坏的会话文件（在修复前保存的）。Session 加载时需要检测并修复：

```go
// session.Repair 检测并移除孤儿 tool_calls
func Repair(msgs []deepseek.Message) []deepseek.Message {
    // 找到没有配套 tool 结果的 assistant 消息，移除它
    // ...
}
```

这类"加载时修复"的模式在数据持久化场景里很常见：不能假设存储里的数据是干净的，特别是在程序有 bug 的时候写入的数据。

---

## 4.5 事件系统的设计

Agent 循环通过一个 channel 推送类型化的事件给调用方（TUI、测试等）：

```go
type AgentStart struct{}
type TurnStart   struct{ Index int }
type MessageStart struct{ Message deepseek.Message }
type MessageDelta struct{ Delta string; Reasoning bool }
type MessageEnd   struct{ Message deepseek.Message }
type TurnEnd      struct{ Index int; Usage deepseek.Usage; ToolCalls int }
type AgentEnd     struct{ Usage deepseek.Usage; Turns int; ToolCalls int }
type ErrorEvent   struct{ Err error }
type ToolDelta    struct{ Name string; Delta string; Reasoning bool }
```

`Reasoning bool` 区分普通文本和推理链文本，TUI 会用不同样式渲染它们。

事件系统让 Agent 核心和 UI 完全解耦：Agent 不知道也不在乎谁在消费事件，TUI 不知道 Agent 的内部状态。这个解耦在测试里特别有价值——测试用例可以把 channel 里的事件收集起来，不需要运行真实的 TUI。

```go
func TestAgent_SingleTurn(t *testing.T) {
    agent := setupTestAgent(t, fakeBackend)
    events := collectEvents(agent.Prompt(ctx, "hello"))

    // 检查事件序列
    assertEventOrder(t, events,
        AgentStart{},
        TurnStart{},
        MessageStart{},
        // MessageDelta...
        MessageEnd{},
        TurnEnd{},
        AgentEnd{},
    )
}
```

---

## 本章小结

- Agent 循环的核心是"发请求 → 组装 tool_calls → 执行工具 → 追加结果 → 继续"
- 工具调用 delta 按 `index` 累积，`Arguments` 是字符串拼接而非实时 JSON 解析
- 孤儿 `tool_calls` 是破坏会话的最危险的 bug，修复需要两层防护：ctx 检测 + 不变量检查
- 测试要针对"状态形状"而不是"触发原因"，这样一个测试能覆盖多条路径
- 事件系统把 Agent 和 TUI 解耦，让两者可以独立测试

下一章，我们在 Agent 之上构建工具系统：工具注册、权限管控、JSON Schema 的正确方式，以及为什么 `json.Unmarshal` 的默认行为会让 LLM 拼错字段名时陷入自我怀疑的循环。

---

*对应 commit：`pkg/agent` 的初始实现 + 孤儿 tool_calls 修复。运行 `go test -race ./pkg/agent/...` 验证。*
