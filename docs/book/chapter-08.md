# 第 8 章：M4.5 — TUI 稳定化

> 对应代码：`internal/tui/`、`internal/permission/`
> 起点：第 7 章结束时，TUI 可以运行，能流式渲染，能滚动。但只要你真的拿它写一会儿代码，就会撞到一组"用之前没意识到、用之后非要解决不可"的问题。
> 终点：Esc 中断不再污染会话，危险工具有当面审批，`/` 和 `@` 有补全，↑/↓ 翻历史，状态栏看得见花了多少、还剩多少。

---

M4.5 不是一个"新功能"里程碑，是一个"稳定化"里程碑。它由 8 个零散的 commit 组成，分散在大约两周的真实使用里——每一个 commit 都是"我自己用 seek 写代码时被某个边角问题烦到，停下来去修"。

这一章不像前面那样按"功能/工具"组织，而是按"用户的痛点"组织。每一节都对应一个真实发生过的"为什么会这样"的瞬间。

---

## 8.1 一个稳定化里程碑的形状

回头看 M4 完成时的 seek，它是这样的：

- 能输入，能流式输出，能滚动 ✅
- 用户按 Esc 想中断 → 会话被破坏，下一次输入直接被 API 拒绝 ❌
- 模型想 `bash rm xxx` → 直接执行，没有任何确认 ❌
- 用户敲 `/help` → 被当成普通文本发给模型，浪费一次推理 ❌
- 用户敲 `@internal/server/...` → 要逐字符敲完整路径 ❌
- 用户想重发上一条 → 没办法，得手动复制 ❌
- 用户想知道当前对话花了多少 token → 看不见 ❌

这些事情在 demo 里看不出来。Demo 里你只问一个问题，看到模型答完，就觉得"很好"。但凡你拿它干 30 分钟的活，每一个都会变成日常摩擦。

M4.5 的目标就是把这些日常摩擦一个一个磨平。

---

## 8.2 Esc 中断的三层闭环

这是 M4.5 里最重要的一条，也是上一章和第 4 章都埋过的伏笔——现在我们把它彻底关掉。

### 闭环的三层

第 4 章讲过 Agent 层的两层防护：
- **第一层**：`runTurn` 流结束后显式检查 `ctx.Err()`，把 ctx 取消转化成 error 返回
- **第二层**：`Prompt` 在追加 assistant 消息之前做不变量检查（`tool_calls` 非空 ↔ `finish == "tool_calls"`），违反就拒绝提交

但这两层都是 Agent 内部的事情。Esc 中断真正的起点在 TUI——是 TUI 持有那个 cancel 函数，并决定什么时候调用它。所以闭环的第三层在 TUI 这一侧：

```go
// internal/tui/update.go
func (m Model) submit(text string) (tea.Model, tea.Cmd) {
    // ...
    ctx, cancel := context.WithCancel(m.opts.Ctx)
    m.cancelStream = cancel

    ch := m.opts.Agent.Prompt(ctx, text)
    m.stream = ch
    // ...
}
```

每次 `submit` 都从 `opts.Ctx`（绑定到进程级 SIGINT 的根 ctx）派生一个**子 ctx**，把它的 cancel 存进 Model。这样：

- 用户按 Ctrl+C → SIGINT → 根 ctx 取消 → 子 ctx 自动取消 → Agent 停止
- 用户按 Esc → 我们手动 `m.cancelStream()` → 子 ctx 取消 → Agent 停止；**根 ctx 没动**，TUI 继续活着

按 Esc 之后整个 TUI 还活着，下一条消息可以照常发——这是和 Ctrl+C 的关键区别。

### 一个小但容易写错的细节：何时清 `cancelStream`

```go
case tea.KeyEsc:
    if m.streaming && m.cancelStream != nil {
        m.userCanceled = true
        m.cancelStream()
        // Don't clear m.cancelStream here — streamEndMsg will do
        // it after the stream channel actually drains, otherwise
        // the next Esc within the same race window double-cancels.
    }
```

注意注释。直觉的写法是按完 Esc 立刻 `m.cancelStream = nil`，但这样有一个 race：从你调用 cancel 到流真正排空，期间还会有若干个 `agentEventMsg` 在 channel 里。如果用户在这个窗口里又按了一次 Esc，第二次会看到 `cancelStream == nil`（被清掉了）而误以为没在流——什么都不做。结果用户看到屏幕还在滚，按 Esc 没用。

正确的做法是**让 `streamEndMsg` 来清**——也就是流真正排空、Agent goroutine 退出之后才清。在那之前，`cancelStream` 一直非 nil，重复按 Esc 是幂等的（cancel 函数本身就允许多次调用）。

### `userCanceled` 这个标志的存在意义

按了 Esc 之后，Agent goroutine 还会在它的 ctx 上继续走一小段——`runTurn` 要从 stream 里 drain 完最后几个 chunk，`Prompt` 要走 cancel 检测路径。这段时间里它仍然会发 `agentEventMsg` 到 TUI。如果 TUI 不知道"这次是用户主动取消的"，它会把这些尾巴 event 当成正常输出渲染——用户会看到屏幕上还在长出新字。

`m.userCanceled = true` 给 TUI 一个明确的信号：流即将结束，且这次结束是用户主动触发的，结束时**打印一行 `↰ interrupted`** 而不是按正常完成处理。

> **坑 #10：cancel 后到 stream 真正排空之间的"尾巴 event"会被 TUI 当成正常输出**
>
> Go 的 ctx 取消是协作式的——你调 cancel，下游需要在某个 `select`/`ctx.Err()` 检查点才会真正退出。这之间，已经发出的 channel event 仍然会送达消费者。如果消费者不知道"这次取消是用户触发的"，它会把这些 event 当正常事件处理，用户会觉得 Esc 没生效。
>
> 经验法则：任何"用户触发的取消"都需要一个**显式的标志**，而不是只靠"ctx 已经取消"这一个隐式信号——因为后者对消费者来说有几十毫秒的延迟可见性。

---

## 8.3 per-call 审批：policy 三态 + inline y/N/a

第 5 章讲过 Permission Policy 的三种模式（`ModeDeny` / `ModeAsk` / `ModeYolo`）和"拒绝是工具结果不是错误"。M4.5 把第三种模式——`ModeAsk`——真正变成一个可用的交互。

### 整条链路

```
[edit / write / bash 工具]
     |
     | policy.Check(Action{Kind, Path, Cmd, Diff})
     v
[Policy.askFn] -- send --> approvalCh  (buffered, host -> TUI)
     |                       |
     | recv reply            v
     v                  [TUI: pendingApproval = req,
[wait for user]          input box switches to y/N/a]
                              |
                              v
                         req.Reply <- bool  (buffered, TUI -> host)
                              |
                              v
                         [askFn returns]
```

四个组件、两个 channel，一边是 host（cmd/seek 里启动时拼好的 closure），一边是 TUI。

### Host 端：把 askFn 翻译成 channel 操作

```go
// cmd/seek/main.go
approvalCh := make(chan permission.ApprovalRequest, 4)
policy.SetAskFn(func(a permission.Action) bool {
    resp := make(chan bool, 1)
    select {
    case approvalCh <- permission.ApprovalRequest{Action: a, Reply: resp}:
    case <-ctx.Done():
        return false
    }
    select {
    case ok := <-resp:
        return ok
    case <-ctx.Done():
        return false
    }
})
```

第 5 章预告过的那个"两端都要 ctx-aware select"的坑——就在这里。两次 `select` 各自带 `<-ctx.Done()` 逃生通道，缺一个都会在某种取消路径下死锁。

`approvalCh` 用容量 4 的 buffered channel，理由很朴素：Agent 循环是顺序的，并发审批基本不会发生，但 buffer 4 是免费保险，能消除"TUI 一瞬间没准备好读"的极短窗口。`Reply` 用容量 1 是必须的——如果 TUI 一直按 Esc 想关 TUI（而 host 还没看到），buffered 1 让 TUI 写完就能走人，不会因为没人读而死锁。

### TUI 端：approval 模式接管键盘

当 `pendingApproval` 非 nil 时，**所有键盘事件**都路由进 `handleApprovalKey`，textarea 不再接收输入：

```go
switch msg.Type {
case tea.KeyEnter:
    allow = true
case tea.KeyEsc:
    allow = false
case tea.KeyCtrlC:
    // Reply deny, then quit. Without this the agent goroutine
    // would block forever on the reply channel.
    m.replyApproval(false)
    return m, tea.Quit
default:
    switch strings.ToLower(msg.String()) {
    case "y": allow = true
    case "n": allow = false
    case "a": allow, always = true, true   // 一直允许：升级到 Yolo
    }
}
```

注意 `KeyCtrlC` 那条路径。直觉上 Ctrl+C 就是 `tea.Quit`，但如果你直接 quit，Agent goroutine 还在 `<-resp` 上阻塞——它永远不会收到回复，永远不会退出 goroutine。所以**先 `replyApproval(false)`，再 quit**，保证 Agent 能从 askFn 解开往下走，最终在自己的 ctx 取消路径上干净退出。

这正是第 7 章 8.6 节"一个 KeyMsg 只能去一个消费者"那条规则的另一面：当 approval 模式打开时，KeyMsg 只去 `handleApprovalKey`；textarea 完全脱离循环。

### Action 携带的 Diff：审批要看到具体改了什么

```go
type Action struct {
    Kind ActionKind
    Path string
    Cmd  string
    // Diff is optional: edit tools compute it once via internal/diff
    // before it calls Check. When non-empty the TUI renders it alongside
    // the y/N approval prompt so the user can see exactly what will change.
    Diff string
}
```

`Diff` 这个字段是 M5 的 `edit` 工具加上去的（commit `74e1a61`），但和 per-call 审批是同一个故事——审批要有意义，用户必须看到**具体改了什么**。所以 `edit` 工具在调用 `policy.Check` 之前就先用 `internal/diff` 算好 unified diff（最多 8 个 hunk，超出就截断），把它塞进 `Action.Diff`。TUI 在 y/N 输入框上方就能渲染出来。

设计选择上有一个微妙的地方：diff 是**审批的输入**，不是**审批通过之后才看的东西**。如果你倒过来——先批，再 diff，再写——你其实给用户的是一个"无条件信任"的按钮。先 diff 再批，用户的"y"才有实际信息含量。

---

## 8.4 slash 命令补全菜单

用户敲 `/`，弹出菜单；继续敲字符，按前缀过滤；Tab 接受，Enter 执行。

实现上看起来是琐事，但有一个 Go 特有的小陷阱值得记一笔。

### init cycle：top-level var 和它列出的 func 互相引用

最自然的写法是这样：

```go
// 错误写法：会触发 init cycle
var commands = []command{
    {names: []string{"help"},   handler: cmdHelp},
    {names: []string{"yolo"},   handler: cmdYolo},
    // ...
}

func cmdHelp(m *Model, _ string) tea.Cmd {
    // ...想列出所有命令...
    for _, c := range commands {  // ← 这里读 commands
        fmt.Println(c.names[0])
    }
    return nil
}
```

`commands` 的初始化引用了 `cmdHelp` 函数（作为函数值），而 `cmdHelp` 又在函数体里读了 `commands`。Go 的初始化器无法把两者排出一个合法顺序，`go vet` 会直接报 `initialization cycle for commands`。

修复非常简单：把 `commands` 从 `var` 降级为 `func`：

```go
// 正确：懒构造，调用时才装配
func allCommands() []command {
    return []command{
        {names: []string{"help"},   handler: cmdHelp},
        {names: []string{"yolo"},   handler: cmdYolo},
        // ...
    }
}
```

函数被调用时才执行，所以 `cmdHelp` 引用 `allCommands()` 不会触发 init 阶段的环。

> **坑 #11：top-level `var` slice 列出 func，再让 func 读这个 slice，会 init cycle**
>
> 出现在"我想做个命令表，再做个 `help` 命令把表打出来"这种再常见不过的需求里。Go 的 init 顺序对 `var slice = [...]func{...}` 的处理比直觉严格：任何被 slice 引用的函数 + 这个函数对 slice 的反向引用 = 环。
>
> 规则：**当 top-level `var` 和它列出的 func 互相引用，把 var 降级为 func**。代价只是多一次构造，可忽略；好处是省掉一类 Go 特有的初始化谜题。

### 动态过滤：每次按键都重算

当 `m.commandMenuOpen == true`：

```go
// 用户按方向键 → 在菜单里移动
case tea.KeyUp:
    if m.commandMenuSelected > 0 { m.commandMenuSelected-- }
case tea.KeyDown:
    if m.commandMenuSelected < len(m.commandMenuFiltered)-1 {
        m.commandMenuSelected++
    }
case tea.KeyTab:
    // 接受当前选中
    name := m.commandMenuFiltered[m.commandMenuSelected].names[0]
    m.input.SetValue("/" + name + " ")
    m.commandMenuOpen = false
```

打开/关闭/过滤都在 `updateCommandMenu()` 里集中处理——每次 textarea 内容变化时调一次。这种"每次重算"的模式在 TUI 里非常便宜，因为命令总数十几条，过滤代价可忽略；换来的是状态机简单（不需要维护 incremental filter），bug 少。

---

## 8.5 `@` 路径补全

`@` 路径补全和 `/` 命令菜单是同一套机制的另一个实例：用户敲 `@`，开 picker；继续敲字符，按当前目录下的文件名过滤。

实现上的差异主要在数据源——`@` 的候选不是常量列表，而是 `os.ReadDir(cwd)` 的结果。但 UX 表现一致：方向键导航、Enter 接受、Esc 取消。

设计选择上有一个值得提的：**`@` 补全只看当前目录，不递归**。原因是递归遍历对大目录（`node_modules`、`.git`、构建产物）会快速变得不可用——你打个 `@src` 期望补全到 `src/` 目录，结果 picker 要先 stat 完几千个文件。当前目录的文件 + 子目录列表通常二三十条，过滤体感是瞬间的，深入子目录通过"接受当前选择 → 再按 `/`"来分步完成。

这是一个"功能上的克制"——不递归，看起来缺一个能力，但实际用起来快得多。如果你以后做 IDE-style 的工具，会反复看到这种"局部展开 + 多次交互"vs"一次性全量索引"的权衡。

---

## 8.6 prompt 历史：上下方向键翻

```go
case tea.KeyUp:
    if m.tryHistoryUp() { return m, nil }
case tea.KeyDown:
    if m.tryHistoryDown() { return m, nil }
```

`tryHistoryUp`/`Down` 在两种情况下生效：
- textarea 是空的（防止和多行草稿的光标上移冲突）
- 已经在翻历史的过程中

`promptHistory` 就是一个普通的 `[]string`，`historyIdx` 表示当前光标。`-1` 表示"不在翻历史"。

```go
func (m *Model) tryHistoryUp() bool {
    if len(m.promptHistory) == 0 { return false }
    if m.historyIdx == -1 {
        m.savedDraft = m.input.Value()                // 翻历史前保存当前草稿
        m.historyIdx = len(m.promptHistory) - 1
        m.input.SetValue(m.promptHistory[m.historyIdx])
        return true
    }
    if m.historyIdx > 0 {
        m.historyIdx--
        m.input.SetValue(m.promptHistory[m.historyIdx])
    }
    return true
}
```

注意 `savedDraft`——用户可能在 textarea 里打了一半字突然想"等等我刚才发过类似的"，按上看历史。如果不存草稿，他翻完历史按下，半截草稿就消失了。存一下，翻到最底端时还回去。

历史目前**不跨会话**——重启后清空。是否要持久化是一个开放问题，目前的判断是：跨会话历史在概念上属于"用户的命令记忆"，归 shell 的 `history` 管更自然，加进 seek 反而是越界。

---

## 8.7 状态栏：streaming 计时 + token 估算

状态栏是 M4.5 里最容易被忽略的一项工作，但用户长期凝视它。它的内容（精简版）：

```
  ● 4.2s · ↓~312tok    ctx 28%    model: deepseek-chat    /yolo off
```

`● 4.2s` 是 streaming 已运行的实时秒数。`↓~312tok` 是输出 token 估算（streaming 期间真实数还没回来，按 ~4 chars/token 估）。`ctx 28%` 是上下文占用百分比。后面是当前 model、yolo 开关、provider banner（M6 才出现）。

### 计时器：`statusTickMsg`

bubbletea 的消息循环是事件驱动的——没有事件就不重绘。但状态栏的"4.2s"是连续变化的，不重绘就停在那。所以我们派发一个 `tea.Tick`：

```go
type statusTickMsg time.Time

func statusTick() tea.Cmd {
    return tea.Tick(time.Second, func(t time.Time) tea.Msg {
        return statusTickMsg(t)
    })
}
```

每收到一个 tick，update 一次状态栏，然后再派发下一个 tick。这是 bubbletea 里"自维持的周期任务"的标准模式。

### 关于 ctx%：一个等到第 13 章才解释清楚的反直觉

`ctx%` 这个字段比看起来复杂。一开始它是这么算的：

```go
ctxPct = 100 * tracker.Cumulative().PromptTokens / contextLimit
```

直观、好理解、**完全错误**。

错误的原因要等到第 13 章成本与上下文预算才能讲清楚——简单说：每一轮 prompt 都会重发完整历史，cumulative tokens 在 O(turns²) 增长，跑 55 轮后会显示超过 100%，但**实际**上下文窗口（最近一轮的 prompt token 数）可能只占 30%。

现在状态栏用的是 `tracker.Last().PromptTokens`——上一轮 prompt 的 token 数，这才是"窗口现在多满"的正确信号。

这里先埋个伏笔。第 13 章会把整个 `cache.Tracker` / `budget.Classify` / 60-75% 阈值的故事讲完。

---

## 本章小结

- Esc 中断的闭环有三层：TUI 持有子 ctx 的 cancel，Agent 在 ctx.Err 上 bail，加载时 Repair 兜底。`userCanceled` 标志让 TUI 能正确处理"取消后到流真正结束"之间的尾巴 event
- per-call 审批是一对 channel：host 把 askFn 翻译成 `approvalCh` 投递，TUI 切换到 approval 键盘模式，回复经 `Reply` 写回。两端 select 都必须带 `ctx.Done`
- `Action.Diff` 让审批携带"具体改了什么"——审批是 diff 的输入，不是 diff 的旁观者
- top-level `var` slice + 互相引用的 func = init cycle。把 var 降级为 func 是最干净的解
- `/` 命令菜单和 `@` 路径补全用同一套打开/过滤/接受的模式，差异只在数据源
- 状态栏的"4.2s"靠 `tea.Tick` 自维持周期；`ctx%` 必须用 `Last()` 而非 `Cumulative()`，原因第 13 章讲

下一章进入 M5.1：会话持久化。我们会看到为什么单文件 JSON 是错的、为什么 JSONL 是对的、schema_version 怎么把旧文件透明迁移过去，以及 `loadMeta` 这个"只读第一行"的小优化为什么让 `--list` 子命令的延迟从感知得到变成感知不到。

---

*对应 commit：`a38bfd0` (Esc + tool 计时)、`7c96bd7` (per-call 审批)、`5f6b316` (slash 菜单)、`edf443c` (@ 路径补全)、`d038455` (历史 + token 告警)、`08449cd` (init cycle 修复)、`73c5f3d` (Policy race fix)。运行 `go test -race ./internal/tui/... ./internal/permission/...` 验证。*
