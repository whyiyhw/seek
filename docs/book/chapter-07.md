# 第 7 章：M4 — 终端用户界面

> 对应代码：`internal/tui/`
> 起点：命令行可以运行，但没有交互界面。终点：流式渲染的 TUI，带品牌 Logo、状态栏、工具调用可视化。

---

前六章的代码都是"后端"——网络、协议、工具执行。这一章进入终端 UI。

TUI 有一种特殊的复杂性：它同时处理用户输入、流式输出、状态展示，这三件事都在实时发生，都需要响应，都在同一个终端里渲染。做好了，用户感觉像在用一个精心设计的工具；做差了，会有输入卡顿、渲染抖动、文字乱码。

---

## 7.1 为什么选 bubbletea

Go 有几个 TUI 框架，seek 选了 [bubbletea](https://github.com/charmbracelet/bubbletea)（charm 团队的），原因如下：

> 版本现状：seek 运行在 Bubble Tea **v2**（`charm.land/bubbletea/v2`，v2.0.x 起）上，配套 `charm.land/bubbles/v2`（textarea/spinner）与 `charm.land/lipgloss/v2`。v2 的声明式 `View() tea.View`、独立的 `PasteMsg`、键盘增强协议让 v1 时代需要 workaround 的输入怪癖（见 `docs/pitfalls.md`）大部分原生消解；渲染器（Cursed Renderer）也更高效。

**Elm 架构**：bubbletea 的编程模型是 Model-Update-View。所有状态在 Model 里，Update 函数处理消息（键盘事件、定时器、goroutine 发来的数据），View 函数渲染当前状态。这是一个纯函数式的架构——没有全局状态，没有回调地狱，状态变化可追踪。

**inline 模式天然支持**：bubbletea 既支持 alt-screen（全屏接管终端），也支持 inline 模式（在当前光标位置下方渲染，不清屏）。这个区别比看起来重要得多——我们在 M4 做了一个关键决策。

---

## 7.2 inline 模式 vs alt-screen：一个影响深远的决策

alt-screen 是很多 TUI 工具的默认选择（vim、less、htop 都用）。它把终端切换到一个"副缓冲区"，在里面全屏渲染，退出时切回来。

对于编程智能体，这是一个错误的选择。

**alt-screen 的问题**：

1. **退出后内容消失**。用户和 AI 的对话、AI 的思考、写了哪些文件——退出 seek 之后全没了，终端 scrollback 里看不到任何东西。用户没法用 `cat` 或鼠标拖选复制 AI 的输出。

2. **鼠标选择失效**。进入 alt-screen 后，终端的原生文本选择通常被禁用（TUI 接管了鼠标事件）。

3. **OSC 11 探针的时序问题**。这在第 7.3 节展开。

inline 模式的工作方式：bubbletea 在终端光标当前位置下方渲染 TUI，"已完成"的内容（用户消息、AI 的回复、工具执行结果）通过 `tea.Println` 写入标准输出，永久保留在 scrollback 里；"活跃"的内容（正在流式输出的 AI 消息、当前工具的执行状态、输入框）在 bubbletea 的 live region 里实时更新。

这个模式完全符合用户对聊天工具的期望：对话历史在 scrollback 里，可以无限往上翻；输入框始终在底部。gh CLI、gemini CLI 与 seek 行为一致——它们也是真 inline（历史进终端原生 scrollback）。**Claude Code 是例外**：它走的是"伪全屏"的第三条路——全屏接管终端、历史留在应用内存、滚动由应用内虚拟列表负责、退出时再把整份会话 dump 到 scrollback。见下文"Claude Code 的第三条路"。

> **坑 #8：`tea.WithAltScreen()` 对聊天类工具是一个路径依赖的陷阱**
>
> alt-screen 很容易加（一行代码），但一旦加上，很多特性就建立在它之上了：viewport 的大小假设、Println 的路由、scrollback 的不存在。切换到 inline 模式需要重写大量渲染逻辑，不是"改一行"能解决的。
>
> 开始做 TUI 时就要想清楚这个问题。聊天/REPL 类工具用 inline；全屏编辑器用 alt-screen。

### Claude Code 的第三条路：伪全屏

上文说"inline 模式与 Claude Code 行为一致"——这个说法不准确，需要修正。Claude Code 既不是 alt-screen，也不是 bubbletea 意义上的 inline，它是第三种模型：

1. **全屏接管**。启动时清屏，整个可视区域归应用所有。运行期间终端 scrollback 里看不到会话——历史全部在应用内存里。
2. **滚动是应用内虚拟列表**。滚轮 / PgUp / PgDn 走应用（需要鼠标捕获），原生文本选择被压制（要 Shift / Option 修饰键绕过）。
3. **输入框贴底是布局坐标**。屏幕 = flex column：内容区（flex: 1）+ 底部输入框。窗口尺寸已知，resize 时整树重排——"底部"是画布坐标，不存在"终端 scrollback 累积了多少行"这个变量，所以从结构上就不会有 seek M3 时代的漂移问题。
4. **退出时 dump**。离开全屏后把整份会话重新渲染打印到终端，留在 scrollback 里——这是应用打印的副本，不是运行期间的原生历史。
5. **渲染层**：React + Ink（公开报道；后续版本因虚拟滚动性能部分替换为自研渲染）。

Claude Code 能"完美贴底"，正是因为它选了 seek 明确放弃的那条路：全屏接管。seek 选择真 inline（终端原生滚动 / 原生选择 / 低内存，历史由 JSONL 会话文件持久化），代价是放弃"贴绝对底部"的执念——live region 随内容增长，状态栏锚定 live region 末尾（也就是终端底部），输入框紧贴状态栏上方，高度变化交给渲染器原生处理（见上文 7.2 的 CRITICAL 注释与坑 #8 的后续）。两种架构的取舍不同，不是实现水平差异。

> 修正记录：初版此书断言"Claude Code 也用 inline 模式"，不准确。gh CLI、gemini CLI 才是真 inline；Claude Code 是伪全屏 + 退出时 dump。与之对应的 `docs/pitfalls.md` "Alt-screen mode breaks scrollback, copy, and content persistence" 条目已同步修正。另注：早期版本曾用"常驻 9 行 reserved zone"稳定输入框位置（菜单开关时不跳动），代价是输入框上方永远悬着空白、看起来像悬浮；该设计后来被废弃——输入框回到紧贴状态栏的位置，菜单打开时输入框随内容自然下移（与 streaming 内容增长是同一行为）。期间还尝试过把状态栏移到输入框上方让输入框当最后一行，结果状态栏在 inline 模式下悬在屏幕中间（inline 没有固定顶部）——状态栏必须保持屏幕最底部。详见 `docs/pitfalls.md` "Reserved popup zone was the same mistake as floor-pin padding, wearing a different hat"。

---

## 7.3 OSC 11 探针：在进入 TUI 前完成终端检测

seek 需要根据终端背景色（深色/浅色）选择代码高亮主题（glamour 渲染 Markdown）。标准做法是发一个 OSC 11 查询——一个特殊的 ANSI escape sequence，终端会回复当前背景色的 RGB 值。

这个查询必须在进入 bubbletea 之前完成。

原因：bubbletea 启动后会接管 stdin。OSC 11 的响应是终端写回到 stdin 的，如果此时 bubbletea 在读 stdin，响应就会被当成键盘输入——用户会看到输入框里莫名出现了 `]11;rgb:fae0/fae0/fae0`。

修复方法：在 `main()` 里，在调用 `tui.Run()` 之前，完成探针查询：

```go
// cmd/seek/main.go

style := detectGlamourStyle()  // 在这里完成 OSC 11 查询
// ...
tui.Run(tui.Options{GlamourStyle: style, ...})
```

```go
func detectGlamourStyle() string {
    // termenv.HasDarkBackground() 在内部发送 OSC 11 并同步等待响应
    // 在 bubbletea 启动之前调用，stdin 还归我们管
    if termenv.NewOutput(os.Stdout).HasDarkBackground() {
        return "dark"
    }
    return "light"
}
```

---

## 7.4 WindowSizeMsg 的不可靠性

bubbletea 承诺在启动时发送一个 `WindowSizeMsg`，告诉程序终端的宽高。大多数 TUI 用这个消息做初始布局。

问题是，在某些终端 / tmux 配置 / `go run` 启动方式下，这个消息可能延迟几秒甚至永远不到。`relayout()` 依赖窗口尺寸才能工作，没有尺寸就没有布局，界面卡在初始状态。

修复：在 `Init()` 方法里主动查询一次终端尺寸，作为合成的初始消息：

```go
func (m Model) Init() tea.Cmd {
    return func() tea.Msg {
        w, h, err := term.GetSize(int(os.Stdout.Fd()))
        if err != nil || w == 0 {
            w, h = 80, 24  // 合理的默认值
        }
        return tea.WindowSizeMsg{Width: w, Height: h}
    }
}
```

真实的 `WindowSizeMsg` 如果随后到达，会触发相同的 `relayout()`，是幂等的。

---

## 7.5 Elm 架构在 seek TUI 里的应用

Model 持有所有 UI 状态：

```go
type Model struct {
    // 布局
    width, height int
    ready         bool

    // 内容
    viewport  viewport.Model   // 滚动区域（历史消息）
    input     textarea.Model   // 输入框
    activeTools []activeTool   // 正在执行的工具

    // Agent 状态
    agent      *agent.Agent
    streaming  bool
    turns      int

    // 功能面板
    commandMenuOpen bool
    pathPicker      pathPickerState
}
```

Update 函数是唯一修改 Model 的地方：

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        return m.relayout(msg.Width, msg.Height)
    case tea.KeyMsg:
        return m.handleKey(msg)
    case agentEventMsg:
        return m.handleAgentEvent(msg.Event)
    // ...
    }
}
```

Agent 事件通过一个包装类型 `agentEventMsg` 从后台 goroutine 发到 bubbletea 的消息循环：

```go
type agentEventMsg struct{ Event agent.Event }

func listenToAgent(events <-chan agent.Event) tea.Cmd {
    return func() tea.Msg {
        return agentEventMsg{Event: <-events}
    }
}
```

每次处理一个 `agentEventMsg`，就返回一个新的 `listenToAgent` 命令，监听下一个事件——这是 bubbletea 里处理长期 channel 的标准模式。

---

## 7.6 键盘路由：不要把 KeyMsg 发给两个消费者

这是一个在开发初期必然会踩的坑。

bubbletea 把所有按键都以 `tea.KeyMsg` 的形式发进 Update。如果你把这个消息同时转发给 viewport（滚动控件）和 textarea（输入控件），两者都会处理它。

viewport 的默认键盘映射：
- 空格 → 向下翻页
- `b` → 向上翻页
- `j` / `k` → 上下滚动

这些和正常打字完全冲突。用户打 "build"，`b` 会触发向上翻页，输入框里只有 "uild"。

解决：在 `handleKey` 里做明确的路由，而不是广播：

```go
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch msg.Type {
    case tea.KeyCtrlC, tea.KeyEsc:
        return m.handleInterrupt()
    case tea.KeyPgUp, tea.KeyCtrlU:
        m.viewport, _ = m.viewport.Update(msg)  // 只发给 viewport
        return m, nil
    case tea.KeyPgDown, tea.KeyCtrlD:
        m.viewport, _ = m.viewport.Update(msg)  // 只发给 viewport
        return m, nil
    case tea.KeyEnter:
        if !msg.Alt { return m.handleSubmit() }
        // Alt+Enter 发给 textarea（换行）
    }

    // 其他所有按键只发给 textarea
    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)
    return m, cmd
}
```

关键原则：**一个 KeyMsg 只能去一个消费者**。

---

## 7.7 自动滚动：尊重用户的滚动意图

streaming 期间，模型一直在输出新 token，viewport 应该跟着滚到底部——但如果用户向上滚动去看历史消息，我们不应该强制把他们拽回来。

实现：在每次更新 viewport 之前，记住用户是否在底部：

```go
func (m Model) handleAgentEvent(ev agent.Event) (tea.Model, tea.Cmd) {
    wasAtBottom := m.viewport.AtBottom()

    // 更新内容
    m.viewport.SetContent(m.renderContent())

    // 只在用户已经在底部时才自动滚动
    if wasAtBottom {
        m.viewport.GotoBottom()
    }

    return m, listenToAgent(m.eventChan)
}
```

用户主动向上滚动时，`wasAtBottom` 变成 `false`，后续更新不再自动跳底。用户滚回底部后，`wasAtBottom` 再次变成 `true`，恢复自动跟随。

---

## 7.8 品牌 Logo：像素字体、渐变色、逐字母动画

TUI 的欢迎屏幕显示一个"SEEK"的像素艺术字标。这个设计的实现经历了两个值得记录的 bug。

### 渐变色的实现

5×7 的像素点阵，用三层青色（颜色 117 → 80 → 38）表示深度渐变：

```go
var seekRows = []seekRow{
    {" ████ █████ █████ █   █", 0},  // 第0层（最亮）
    {"█     █     █     █  █ ", 0},
    {"█     █     █     █ █  ", 0},
    {" ███  ████  ████  ██   ", 1},  // 第1层（中间）
    {"    █ █     █     █ █  ", 2},  // 第2层（最深）
    {"    █ █     █     █  █ ", 2},
    {"████  █████ █████ █   █", 2},
}
var gradientCyan = [3]lipgloss.Color{"117", "80", "38"}
```

### 逐字母动画的 UTF-8 陷阱

动画效果是"字母逐个亮起"：启动时，SEEK 四个字母一个接一个地显示。实现方法是根据时间参数 `n` 决定显示到第几个字母：

```go
// 每个字母结束的列号（以字符位置计）
var letterEndCols = [4]int{6, 12, 18, 24}

func bannerWithLettersRevealed(n int) string {
    cutoff := -1
    if n < 4 { cutoff = letterEndCols[n] }

    for _, row := range seekRows {
        runes := []rune(row.text)       // ← 关键：转换为 rune 切片
        for j, r := range runes {
            if cutoff >= 0 && j > cutoff {
                fmt.Fprint(&sb, " ")   // 未到达的字母显示空格
            } else {
                // 用对应颜色渲染 r
            }
        }
    }
}
```

`█` 是 UTF-8 的 3 字节字符（U+2588）。如果用 `for j, r := range row.text`，`j` 是字节偏移量，不是字符偏移量——第一个 `█` 在 j=0，第二个在 j=3，第三个在 j=6……而 `letterEndCols` 是按字符数定义的。结果：`j <= cutoff` 的比较在字节和字符之间混用，第一个字母刚好对，后面全错。

修复只需把字符串先转换成 `[]rune`，然后按索引迭代——此时 `j` 是字符索引，与 `letterEndCols` 的字符位置对应。

> **坑 #9：`for j, r := range string` 的 j 是字节偏移，不是字符索引**
>
> Go 里 `for j, r := range s` 中，`r` 是 rune，但 `j` 是 rune 开始的**字节**位置。对于纯 ASCII 字符串，字节位置等于字符位置，所以在大多数代码里不会出问题。一旦引入多字节字符（中文、特殊符号、图形字符），混用就会出错，而且在 ASCII 测试用例里完全看不出来。
>
> 规则：需要按字符位置做计算（切片、列对齐、动画 cutoff），先 `[]rune(s)` 转换。

---

### 相关踩坑

TUI 实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. Alt-screen 模式破坏 scrollback、复制和内容持久性**

- **Saw**：M4 版本使用 `tea.WithAltScreen()`。问题累积：终端 scrollback 消失（只有一个应用内 viewport），复制只能在可见区域内工作，退出 seek 后整个对话消失，OSC 查询响应无处可去。
- **Why**：alt-screen 切换到终端的备屏缓冲区。该缓冲区**设计上就没有 scrollback**——它是为全屏应用（vim、less）准备的，使用者接受"屏幕上有什么就是什么"。对于聊天式编码 agent，这个权衡是错的：用户希望任意回滚、复制任意历史消息、退出后对话仍然留在 shell 中。
- **Fix**：M4.5.1 切换到 inline 模式。移除 `tea.WithAltScreen()`，已提交的历史（用户 prompt、tool result、已完成的 assistant 消息）通过 `tea.Println` 发布到终端的原生 scrollback。bubbletea 的 live 区域只持有易失状态（活动工具、流式输出、输入框、状态栏）。
- **Lesson**：alt-screen 对任何"有历史记录的对话"都是错误默认。仅用于真正的全屏模态 UI（文件选择器、日志查看器）。Inline 模式 + `tea.Println` 提交内容是 gh CLI、gemini CLI 的选择——这不是巧合。Claude Code 是例外（伪全屏 + 退出时 dump），见上文 7.2 节"Claude Code 的第三条路"。
- **后续**：后来一次修复布局漂移的尝试把 seek 切回了 alt-screen，鼠标滚轮和复制再次损坏——同一个教训被学习了两次。参见 "Inline-mode drift"。

**2. `WindowSizeMsg` 不可靠——Init 中主动合成**

- **Saw**：bubbletea 的 `WindowSizeMsg` 在某些终端/tmux/`go run` 组合下会延迟或丢失。没有窗口尺寸时 `relayout()` 无法进行。
- **Fix**：在 `Init()` 中通过 `term.GetSize(os.Stdout.Fd())` 主动合成 `WindowSizeMsg`。如果真实事件后来到达，幂等的 relayout 只是重新执行一次。

**3. KeyMsg 不能发给两个消费者**

- **Saw**：Update 把每个 `KeyMsg` 同时发给 textarea 和 viewport。viewport 的默认键映射包含空格、b、f、j、k 用于滚动——这些也是普通文本输入。结果：打字时页面也跟着滚动。
- **Fix**：显式 per-key 路由——全局键由 handleKey 处理；PgUp/PgDn/Ctrl+U/Ctrl+D → viewport；其他所有 → textarea。

**4. 自动滚动应尊重用户意图**

- **Saw**：streaming 时每收到一个 token 都调用 `viewport.GotoBottom()`。用户滚动到上面阅读历史时会被拉回底部。
- **Fix**：在应用事件前捕获 `wasAtBottom := m.viewport.AtBottom()`，仅当为 true 时才 `GotoBottom()`。

**5. OSC 11 探针时序——bubbletea 启动前完成**

- **Saw**：`glamour.WithAutoStyle()` 通过 OSC 11 查询终端背景色。终端响应在 bubbletea 已进入 alt-screen 后才到达，被当成键盘输入，显示为 `]11;rgb:...` 乱码。
- **Fix**：在 `cmd/seek` 中，进入 bubbletea 的 alt-screen **之前**同步调用 `termenv.NewOutput(os.Stdout).HasDarkBackground()`，结果作为 `Options` 传入。

**6. `for range string` 的 byte 索引陷阱**

- **Saw**：`for j, r := range s` 中 j 是 rune 的**字节**起始位置，不是字符索引。涉及中文、特殊符号时切片错误破坏 UTF-8 序列。
- **Fix**：需要按字符位置做计算时先转 `[]rune(s)`。

**7. 鼠标捕获权衡：drag-to-select vs 滚轮**

- 两次反转：最初移除 `tea.WithMouseCellMotion()` 恢复原生选择→用户失去滚轮→重新启用→drag 失效。最终方案：alt-screen 模式下启用滚轮，并提供修饰键（Option）作为原生选择的逃逸口。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- inline 模式保留 scrollback，是聊天类工具的正确选择
- OSC 11 探针必须在 bubbletea 启动前完成，否则响应会被当成键盘输入
- `WindowSizeMsg` 不可靠，在 `Init()` 里主动合成一个
- 键盘路由必须明确：一个 KeyMsg 只能去一个消费者
- 自动滚动要尊重用户意图：`wasAtBottom` 决定是否跟随
- `for range string` 的 j 是字节偏移，涉及字符位置计算时先转 `[]rune`

下一章进入 M5：会话持久化。我们会看到如何在进程退出后保留对话历史，以及如何在重启时修复上一章那个孤儿 `tool_calls` 遗留下来的损坏会话。

---

*对应 commit：TUI 初始实现 + inline 模式切换 + 像素 Logo。运行 `go test ./internal/tui/...` 验证布局逻辑。*
