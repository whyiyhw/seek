# 第 10 章：M5.2 — `/branch` 与 `/compact`

> 对应代码：`internal/session/session.go` 的 `Fork`、`internal/tui/commands.go` 的 `cmdBranch` / `cmdCompact`、`internal/tui/update.go` 的 `handleCompactDone`、`pkg/agent/agent.go` 的 `Summarise`。
> 起点：第 9 章实现了"一条线"的会话——开始、增长、保存、加载。
> 终点：这条线变成一棵树。`/branch` 让用户在不污染主线的前提下探索分支；`/compact` 把已经很长的历史压成一段摘要，同时把完整历史以 snapshot 形式保留下来。

---

第 9 章把会话持久化做完了。但只要你认真用 seek 一段时间，会撞上两类问题：

1. **想试一条不同的路**：当前对话已经走了 20 轮，你想"如果上一步换个问法会怎样"——但不想丢掉现在这条线。
2. **历史太长，context 快满了**：60 轮之后，每次 prompt 都重发完整历史，token 成本和延迟都开始让人不爽，缓存命中率也在掉。

两个问题表面无关，底层共用一个原语：**Fork**——从一个会话派生出另一个，父保留不动，子是独立的副本。`/branch` 直接调用 Fork；`/compact` 调用 Fork 之后再把子会话的历史替换成一段摘要。

这一章按"原语 → /branch → /compact"的顺序展开。

---

## 10.1 数据模型：一个 ParentID 字段，承载整棵树

第 9 章的 `Session` 结构里有这么一行：

```go
ParentID string `json:"parent_id,omitempty"`
```

整章没展开过它。现在用上：

```
root (无 ParentID)
 ├── child A (ParentID = root.ID)
 │     └── grandchild (ParentID = child A.ID)
 └── child B (ParentID = root.ID)
```

没有 children 字段。**只有 parent 指针，子从父派生**——一棵反向链接的树。

为什么不存 children？因为：
- **写代价低**：每个会话只在创建时写一次 ParentID，之后不动
- **改孩子不需要改父亲**：fork 一次 = 写一个新文件，旧文件零修改
- **遍历方向永远是"从叶子往根"**：用户操作的总是当前会话，需要时往上回溯一两步，从来不需要"从根往下找所有后代"

如果将来有 UI 想可视化整棵树，可以遍历 `--list` 的所有 SessionInfo 在内存里反向构建——一次性 O(N) 操作，N 通常 < 100，可忽略。预存 children 是过度设计。

---

## 10.2 Fork：深拷贝的边界

```go
func (s *Session) Fork() *Session {
    now := time.Now().UTC()
    msgs := make([]deepseek.Message, len(s.Messages))
    for i, m := range s.Messages {
        if len(m.ToolCalls) > 0 {
            m.ToolCalls = append([]deepseek.ToolCall(nil), m.ToolCalls...)
        }
        msgs[i] = m
    }
    return &Session{
        SchemaVersion: CurrentSchemaVersion,
        ID:            generateID(now),
        CreatedAt:     now,
        UpdatedAt:     now,
        Model:         s.Model,
        Yolo:          s.Yolo,
        CWD:           s.CWD,
        SystemPrompt:  s.SystemPrompt,
        Messages:      msgs,
        ParentID:      s.ID,
    }
}
```

四行代码做的事情看起来直白，但每一步都有理由：

**新 ID + 新时间戳**：子会话是一个独立的实体，不是"父的别名"。从 `--list` 的角度，它就是另一个会话。

**`Messages` 用 `make` 新切片 + 逐元素 copy**：而不是 `msgs := s.Messages`（共享底层数组）或者 `msgs := append([]deepseek.Message(nil), s.Messages...)`（一次性 copy）。第一种是显然不对的——后续向子追加消息会改到父；第二种**几乎正确**但在 `ToolCalls` 嵌套切片上失效，所以我们手写循环。

**`ToolCalls` 嵌套切片单独 copy**：这是最容易被忽略的一处。`deepseek.Message` 是值类型，整个结构体可以按值拷贝；**但它内部有一个 `[]ToolCall` 字段**。值拷贝结构体的时候，切片头被复制，**但底层数组共享**。如果父子之后都基于自己的 Messages 继续修改这些 ToolCall，会互相影响——尤其是 `Function.Arguments` 这种字符串字段如果被覆盖。

`append([]deepseek.ToolCall(nil), m.ToolCalls...)` 显式 allocate 一个新切片，把元素逐个 copy 过去。`ToolCall` 内部还有字段吗？目前没有更深的切片，所以两层够。

**计数器/Usage 不继承**：注意返回的结构里 `Turns / ToolCalls / Usage` 都是零值——子会话从零开始计数。这跟"消息历史是继承的"形成对比：消息是事实，计数是该子会话自己的产出。

> **坑 #13：值拷贝带 slice 的 struct，slice 的底层数组是共享的**
>
> Go 的"按值拷贝"对结构体是按位拷贝。结构体里的 slice 字段会被复制——但 slice 头复制不等于底层数组复制，两个 slice 头指向同一段内存。
>
> 后果取决于使用方式：如果父子都只追加（`append` 触发扩容时会换新数组），可能很长时间发现不了问题；一旦有一方原地写 `msgs[i].ToolCalls[j].Function.Arguments = ...`，另一方立刻被改到。
>
> 规则：**包含 slice / map / 指针 / 函数的 struct，深拷贝要手写每一层**。`reflect.DeepCopy` 不存在；第三方库（`copystructure` 之类）能跑但慢、行为不可预测。手写两层循环更短、更快、更可读。

### 为什么不在 Save 父之前 Fork

`Fork` 是纯内存操作——不写文件。调用方决定什么时候把子写到磁盘，以及什么时候把父刷新到磁盘。这种"操作和持久化解耦"的设计让 `Fork` 本身可测试（不需要文件系统），也让 `/branch` 和 `/compact` 各自决定写入时机。

下面就看到不同的时机选择。

---

## 10.3 `/branch`：用户层面 + 实现细节

用户视角：

```
> /branch
branched: 20260121-103045-a1b2c3 → 20260122-091203-f7e8d9 (continuing on new branch; parent intact at ~/.config/seek/sessions)
```

当前对话继续往下走，但**新消息只追加到 `f7e8d9`**；`a1b2c3` 永久停在分叉点，可以用 `--resume a1b2c3` 找回。

实现：

```go
func cmdBranch(m *Model, _ string) cmdResult {
    if m.opts.Session == nil || m.opts.Store == nil {
        return cmdResult{text: styleMuted.Render("/branch unavailable — session persistence is off (--no-save)")}
    }
    if m.streaming {
        return cmdResult{text: styleMuted.Render("/branch: wait for the current turn to finish")}
    }

    parent := m.opts.Session

    // 1. 先把父会话刷新到磁盘——分叉点必须准确
    m.persistSession()

    // 2. Fork（纯内存）
    child := parent.Fork()

    // 3. 把子会话写到磁盘
    m.opts.Store.Save(child)

    // 4. 切换当前会话引用，重置子的计数器
    m.opts.Session = child
    m.turns = 0
    m.toolCalls = 0

    return cmdResult{text: ...}
}
```

四个动作的顺序不能换：

- **先刷父再 Fork**：如果反过来，Fork 时父的 in-memory 状态可能比磁盘上的新——子继承的是 in-memory 版本，但磁盘上的父还是旧的。下次启动用 `--resume parent.ID` 会看到一个比 fork 点旧的父。**`Fork` 不写父，所以调用方必须先 `persistSession` 保证父的盘上版本和 in-memory 一致**。

- **Save 子在 Fork 之后**：Fork 只创建对象，Save 才落盘。如果 Save 失败（磁盘满、权限错），用户可以选择放弃这次 branch，主线状态完全没动。

- **切换 `m.opts.Session = child` 在最后**：保证如果上面任何一步失败，TUI 仍指向原会话，状态干净。

### Streaming 中不能 /branch

```go
if m.streaming {
    return cmdResult{text: styleMuted.Render("/branch: wait for the current turn to finish")}
}
```

为什么？因为 streaming 时 agent 的 message history 是**正在变化的**——`runTurn` 正在 append 工具调用和工具结果。如果在这中间 fork，子会话可能继承一个"半截的轮"（assistant 已经声明了 tool_calls，但 tool 结果还没追加），加载时被 Repair 砍掉，用户会一脸懵：我刚 branch 的内容呢？

简单的规则——**等本轮结束**——比"试图判断什么时候 fork 安全"鲁棒得多。代价是用户偶尔需要再按一次。值得。

### `/branch` 在 `--no-save` 下直接拒绝

这是第 9 章预告过的："`--no-save` 隐含 `/branch /compact` 不可用"。第一行就 guard，给一个清楚的解释而不是 nil panic。

---

## 10.4 `/compact`：保留完整历史的 fork-snapshot 设计

这是 M5.2 里最有意思的一个决定。

### 朴素做法（最初的实现）

直接把当前会话的 message 历史替换成一段摘要：

```go
// 朴素版：直接覆盖原会话
summary, _, _ := agent.Summarise(ctx)
agent.Reset([]deepseek.Message{
    {Role: "user",      Content: "Here's a summary: " + summary},
    {Role: "assistant", Content: "Understood, ready to continue."},
})
persistSession()  // 同一个 session ID 被覆盖
```

跑起来没毛病：context 占用降下来了，对话能接着进行。然后某天用户问："我上周那个会话里讨论过 `internal/server/server.go` 的具体改动方案，你能帮我找回原文吗？"——回答是不能，被 compact 抹掉了。摘要捕获了"决策"，但具体的代码片段、错误日志、详细推理过程都已经不在磁盘上了。

### Fork-snapshot 设计（commit `c3082ec`）

正确做法是 fork：

```
compact 之前：[parent session]  ← 60 轮历史

compact 之后：[parent session]  ← 60 轮历史，原封不动（snapshot）
                  │
                  └─→ [child session]  ← [summary user, summary assistant]
                                            ↑ 用户在这里继续对话
```

代码：

```go
func (m *Model) handleCompactDone(msg compactDoneMsg) []tea.Cmd {
    // ...

    if m.opts.Session != nil && m.opts.Store != nil {
        // 1. 把完整历史落到当前 session ID（snapshot 角色）
        m.persistSession()
        snapshotID = m.opts.Session.ID

        // 2. Fork 一个子会话，ParentID 指向 snapshot
        child := m.opts.Session.Fork()
        m.opts.Session = child
        m.turns = 0
        m.toolCalls = 0
    }

    // 3. 把 agent 的内存历史替换成 summary pair
    m.opts.Agent.Reset([]deepseek.Message{
        {Role: deepseek.RoleUser,      Content: "Here is a summary of our earlier conversation. Continue from this context:\n\n" + msg.summary},
        {Role: deepseek.RoleAssistant, Content: "Understood — I have the context. Ready to continue."},
    })

    // 4. 写子会话（只含 summary pair）
    m.persistSession()
    // ...
}
```

`/branch` 和 `/compact` 在数据流上**几乎完全相同**：都是"先 persist 当前作为 snapshot，再 fork 出子"。差异只在 step 3——`/branch` 让 agent 的 in-memory 历史保持不变（子继承完整历史），`/compact` 把 agent 历史 reset 成 summary pair。

整个链通过 `ParentID` 串起来。`--list` 看到的 child 会显示它的 parent，用户随时 `--resume <snapshot-id>` 找回完整原历史。

这是一个对"破坏性操作"的通用模式：**永远不要原地覆盖；fork 出一个 snapshot，让原状态变成不可达但仍可达**（绕一步就到）。

### `--no-save` 路径的退化

```go
if m.opts.Session != nil && m.opts.Store != nil {
    // fork-snapshot 路径
} else {
    // --no-save：没有磁盘可放 snapshot，只能原地替换 in-memory 历史
}
```

`--no-save` 下，根本就没有磁盘——也没有所谓 "保留" 的可能。Agent 的内存历史被 Reset，用户对此知情（毕竟他选了 `--no-save`）。两条路径汇合到 `agent.Reset()` 那一行，前面是"是否有持久化"的分支。

第 9 章那条 nil-pointer panic（commit `c3082ec`）就是这一段 guard 的来历：早期版本没有 `if m.opts.Session != nil`，`--no-save` 直接读 `m.opts.Session.ID` 立刻崩溃。

---

## 10.5 摘要的 prompt 设计

`Agent.Summarise` 在原历史末尾追加一个 user 消息，发请求拿回摘要：

```go
const summariserPrompt = `Summarise the conversation so far into a compact briefing that lets a fresh assistant pick up where we left off:
- The user's overall goal and any constraints they mentioned.
- Key files, commands, or decisions made — name them explicitly.
- Outstanding questions or next steps.

Keep it under ~400 words. Bullet points where they help. Do not narrate that you are summarising; output only the briefing.`
```

几个有意思的选择：

**~400 词的硬限制**：太短丢信息，太长起不到 compact 的作用。400 词 ≈ 500-700 token，加上 system prompt 还能舒服地装进 DeepSeek 的缓存前缀（~64 token 起命中，~2KB 是典型最大值）——这意味着 compact 之后的下一轮请求**前缀缓存命中率立刻拉起来**，token 成本断崖式下降。这是设计上有意的——compact 不是只为了"省 context 容量"，也是为了"重启缓存命中"。

**列出固定关注点**：goal / constraints / 关键文件和命令 / 决策 / 待办——这些是工程对话里真正承载状态的东西。LLM 自己摘要不带这种引导，容易写成抽象的"我们讨论了 X，分析了 Y"，下次接上还是不知道具体走到哪。

**"Do not narrate that you are summarising"**：没这一句，模型会写 "Here is a summary of our conversation so far: ..."——这一句对下次接上没用，纯属浪费 token。

**不带工具**：

```go
req := &deepseek.ChatRequest{
    Model:    a.cfg.Model,
    Messages: deepseek.StripReasoningContent(history),
    // 注意：没有 Tools 字段
}
```

摘要是 prose 任务，不要让模型调用 read/grep 之类。`Tools` 不传 = 模型只能给文本回答。

**`StripReasoningContent` 一定要调**：第 6 章讲过的那条 DeepSeek 约束——历史里如果有 `reasoning_content` 字段直接回传会被 API 拒绝。摘要走的是普通 chat 端点（不是 thinking-enabled），所以这条约束适用。

**60 秒 timeout**：

```go
ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
```

摘要是非流式 chat 调用，正常 5-15 秒。60 秒是保守上限，防止网络/服务端卡死把用户卡在"compacting..."字样上。用户按 Ctrl+C 通过父 ctx 取消同样能逃出去。

---

## 10.6 user + assistant 双消息引导：为什么不是单 user 或单 system

`handleCompactDone` 把摘要装进了一对消息：

```go
m.opts.Agent.Reset([]deepseek.Message{
    {Role: RoleUser,      Content: "Here is a summary of our earlier conversation. Continue from this context:\n\n" + summary},
    {Role: RoleAssistant, Content: "Understood — I have the context. Ready to continue."},
})
```

为什么不更简单：单条 system？或者单条 user？

**单条 system 不行**：seek 已经有一条 system prompt（工具描述 + 项目说明）。摘要和它是不同的内容、不同生命周期（system prompt 启动时定下，summary 是 runtime 产物）。塞进 system 会污染缓存键——下次重启可能 system prompt 完全变了。

**单条 user 不够好**：history 以 user 消息结尾时，下一条必须是 assistant。用户按 Enter 发问，他的消息会变成历史里第二条连续的 user 消息——多数 provider 接受，但 OpenAI/Anthropic 在严格模式下会抱怨"角色不交替"。即使能跑，"上一条 user 消息我没回答就接着接受下一条 user 消息"的逻辑模型也让模型困惑。

**user + assistant 双消息**：自然 slot 进 chat 协议——历史以 assistant 结尾，下一条天然是 user。Claude Code 用的也是这个模式（社区里能看到）。

assistant 那句 "Understood — I have the context. Ready to continue." 看起来像样板，但它解决的是"角色交替"的硬要求。

---

## 10.7 一个细节：tracker.Record 在 compact 路径上

```go
if m.opts.Tracker != nil {
    m.opts.Tracker.Record(msg.usage)
}
```

`Summarise` 自己也是一次真实的 LLM 调用，有自己的 token 用量。如果不把这次 usage 录进 tracker，状态栏的 ctx% 和 cumulative 都会少算这次成本——用户的"我今天花了多少"就不准了。

这是"诚实显示成本"的小细节。第 13 章会把整个 tracker / pricing / budget 的故事讲完。

---

## 10.8 链可视化（半步）

`--list` 输出会显示 `ParentID`，让用户能看到关系：

```
20260121-103045-a1b2c3      60 turns, deepseek-v4-flash                  (root)
20260121-103045-a1b2c3      60 turns, deepseek-v4-flash            ← parent of:
20260122-091203-f7e8d9      8 turns,  deepseek-v4-flash
```

目前的展示停在"列出关系"层面，没有 ASCII 树渲染。原因是绝大多数用户的会话图是浅的——主线 + 偶尔分一两个支——线性列表加 parent 标注已经够用。

如果将来用户的图变深，可以基于现有 `SessionInfo` 在内存构建一棵树后渲染。但那是"等真实需求出现再做"的事情，不是预先设计。

---

## 本章小结

- 一个 `ParentID` 字段就能承载整棵会话树，反向指针让"fork 一次 = 写一个新文件，旧文件零修改"
- `Fork` 是纯内存操作，需要手写两层 copy（Messages 切片 + ToolCalls 嵌套切片），因为值拷贝结构体不会复制 slice 的底层数组（**坑 #13**）
- `/branch` 的四步顺序不能换：先刷父、再 Fork、再 Save 子、最后切引用
- streaming 中不允许 `/branch` `/compact`——简单规则比"判断什么时候安全"鲁棒
- `/compact` 用 fork-snapshot 模式：把当前会话保留为完整 snapshot，fork 出只含 summary pair 的子会话——历史不丢、链可遍历、`--list` 看得见
- 摘要 prompt 控制在 ~400 词不只是为了省 context，更是为了**让 compact 之后的下一轮立刻命中缓存前缀**
- summary 装进 user + assistant 双消息引导，自然 slot 进 chat 协议的角色交替规则
- compact 自身的 token 用量必须 Record 进 tracker，否则状态栏的成本会撒谎

下一章进入 M5.3——Skill 系统。我们会看到 Skill 的 Markdown frontmatter 格式、project / user / builtin 三层（实际 4+1 层）优先级扫描、为什么 system prompt 里只注入 manifest 而 body 等到模型调用 `Skill` 工具时才返回，以及 `AGENTS.md` 自动加载是怎么把"项目级指令"注入到每个 seek 会话的。

---

*对应 commit：`3a0b6bf`（`/branch` + `/compact` 初版）、`c3082ec`（fork-snapshot 保留完整历史 + `--no-save` nil guard）、`ba90a48`（Fork 深拷贝修复）。运行 `go test -race ./internal/session/... ./pkg/agent/...` 验证。*
