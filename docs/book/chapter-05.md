# 第 5 章：M2 — 工具系统设计

> 对应代码：`internal/tools/`、`internal/permission/`
> 起点：能运行 Agent 循环，但没有任何工具。终点：read / write / edit / bash / grep 全部可用，带权限管控。

---

工具是 Agent 的手。没有工具，模型只能说话，不能做事。

这一章分两部分：先设计工具系统的基础架构（接口、注册表、JSON Schema 的正确姿势），再深入几个具体工具里的关键决策和踩过的坑。

---

## 5.1 工具接口：三个方法，一个约定

每个工具实现这个接口：

```go
type Tool interface {
    Name()        string
    Description() string
    Schema()      json.RawMessage
    Execute(ctx context.Context, raw json.RawMessage) (string, error)
}
```

**`Name()`**：工具名，全小写，下划线分隔（`read`、`list_dir`、`fim_complete`）。这个名字会出现在模型的工具调用里。

**`Description()`**：自然语言描述，给模型看的。这是影响模型"什么时候调用这个工具"行为最直接的地方。描述写得好，模型就会在正确的时机调用；描述含糊，模型就会猜错，浪费一轮推理。

**`Schema()`**：JSON Schema 格式的参数定义，告诉模型这个工具接受什么参数、哪些是必填的。

**`Execute()`**：接收原始 JSON 参数，返回字符串结果（或错误）。返回类型是字符串不是结构体，因为工具结果最终是放进对话历史里给模型读的。

---

## 5.2 JSON Schema 必须是常量

这是全书最违反直觉的一个约定，但它对性能的影响很大。

前面讲过，DeepSeek 会缓存 prompt 的前缀部分。System prompt 加工具 Schema 通常有 2-3 KB，如果每次请求都能命中缓存，这部分的 token 成本降至约 1/31（V4-Flash 价格：命中 $0.014/M，未命中 $0.44/M，2026-08-16 起的峰谷价表）。

命中缓存的前提：**前缀字节必须完全一致**。

看这两种写法：

```go
// 错误的写法：每次调用可能产生不同字节
func (t Tool) Schema() json.RawMessage {
    schema := map[string]any{
        "type": "object",
        "properties": map[string]any{
            "path":  map[string]any{"type": "string"},
            "limit": map[string]any{"type": "integer"},
        },
    }
    b, _ := json.Marshal(schema)  // map 迭代顺序是随机的！
    return b
}

// 正确的写法：package-level 常量，字节永远相同
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":  {"type": "string"},
    "limit": {"type": "integer"}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

func (Tool) Schema() json.RawMessage { return schemaBytes }
```

Go 的 `map` 的迭代顺序是随机的——同样的代码，两次运行 `json.Marshal(map[string]any{...})` 可能产生不同的字节顺序。空格、字段顺序哪怕有一个字节的差异，都会让 DeepSeek 的缓存键不匹配，命中率归零。

所以 `Schema()` 方法返回的是 package-level 的 `[]byte`，这些字节在程序整个生命周期里是一个固定的内存地址，内容永不改变。

---

## 5.3 工具注册表

注册表把多个工具统一管理，向 Agent 暴露一个接口：

```go
type Registry struct {
    tools []Tool
    index map[string]Tool
}

func (r *Registry) Add(t Tool) *Registry {
    r.tools = append(r.tools, t)
    r.index[t.Name()] = t
    return r
}

// Wire 把注册的工具转换成 API 请求里的 tools 数组
func (r *Registry) Wire() []deepseek.Tool {
    result := make([]deepseek.Tool, len(r.tools))
    for i, t := range r.tools {
        result[i] = deepseek.Tool{
            Type: "function",
            Function: deepseek.ToolFunction{
                Name:        t.Name(),
                Description: t.Description(),
                Parameters:  t.Schema(),
            },
        }
    }
    return result
}
```

在 `cmd/seek/main.go` 里，所有工具在启动时注册一次：

```go
reg := tools.New().
    Add(read.New()).
    Add(grep.New()).
    Add(listdir.New()).
    Add(write.New(policy)).
    Add(edit.New(policy)).
    Add(bash.New(policy)).
    Add(fimcomplete.New(client, model)).
    Add(think.New(client)).
    Add(skilltool.New(skills))
```

注意 `write`、`edit`、`bash` 接收 `policy` 参数——只有这三个工具需要权限管控，因为它们会修改文件系统或执行命令。

---

## 5.4 Permission Policy：拒绝是工具结果，不是崩溃

权限系统有三种模式：

```go
const (
    ModeDeny  Mode = "deny"   // 默认：所有危险操作都询问用户
    ModeAsk   Mode = "ask"    // 询问模式（TUI 里用）
    ModeAllow Mode = "allow"  // --yolo：全部允许
)
```

当工具调用被拒绝时，返回的是一个**工具结果**，而不是一个错误：

```go
func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
    var a Args
    // ...解析参数...

    if err := t.policy.Check(permission.Action{
        Kind: permission.KindWrite, Path: a.Path,
    }); err != nil {
        return "", err  // 这里的 err 实际上是 ErrDenied
    }
    // ...执行写入...
}
```

在 Agent 循环里，工具执行的错误被转换成工具结果消息：

```go
result, terr := a.dispatchTool(ctx, tc, out)
toolMsg := deepseek.Message{
    Role:       deepseek.RoleTool,
    ToolCallID: tc.ID,
    Content:    result,
}
if terr != nil {
    toolMsg.Content = fmt.Sprintf("tool error: %v", terr)
}
a.messages = append(a.messages, toolMsg)
```

这个设计的意图：**模型应该知道权限被拒绝，这样它才能向用户解释或者换一个方案**。如果把拒绝当成 fatal error 直接结束对话，模型就没有机会优雅地处理这种情况。

### 并发安全：Permission 是同步原语

Policy 里的 `mode` 字段会被两个 goroutine 访问：TUI goroutine（用户按 `/yolo` 切换模式）和 Agent goroutine（工具执行时调用 `Check`）。这是一个数据竞争。

```go
type Policy struct {
    mu    sync.RWMutex
    mode  Mode
    askFn func(ApprovalRequest) bool
    cwd   string
}

func (p *Policy) Check(action Action) error {
    // 在 RLock 下读取，释放锁之后再调用 askFn（askFn 会阻塞等用户响应）
    p.mu.RLock()
    mode := p.mode
    askFn := p.askFn
    cwd   := p.cwd
    p.mu.RUnlock()

    if mode == ModeAllow { return nil }
    if mode == ModeDeny  { return ErrDenied }
    // mode == ModeAsk
    if askFn != nil && askFn(ApprovalRequest{Action: action, CWD: cwd}) {
        return nil
    }
    return ErrDenied
}
```

关键点：`askFn` 调用需要放在锁外，因为 `askFn` 会阻塞（等待用户在 TUI 里点 y/n）。如果在锁内调用，持有 `RLock` 的时间可能是几秒甚至更长，导致 `SetMode` 调用（需要 `Lock`）一直阻塞。

> **坑 #6：Channel 里的 askFn 需要在发送和接收两端都做 ctx-aware select**
>
> ```go
> // 错误的写法：两个阻塞点，Ctrl+C 时都可能死锁
> askFn = func(req ApprovalRequest) bool {
>     ch <- req        // ← 如果没人读 ch，永久阻塞
>     return <-resp    // ← 如果另一端退出，永久阻塞
> }
>
> // 正确的写法：每个阻塞点都有 ctx 逃生通道
> askFn = func(req ApprovalRequest) bool {
>     select {
>     case ch <- req:
>     case <-ctx.Done(): return false  // 取消 = 拒绝
>     }
>     select {
>     case r := <-resp: return r
>     case <-ctx.Done(): return false
>     }
> }
> ```
>
> 用户按 Ctrl+C 时，ctx 被取消，askFn 立刻返回 false（拒绝），不会永远等待。

---

## 5.5 参数解析：让模型的拼写错误有意义

这是一个在上线前完全不会发现、上线后让人头痛的问题。

我们在真实使用中看到了这样的模型调用：

```json
{"directory": "/Users/whyiyhw/code", "depth": 1}
```

但 `list_dir` 工具的参数是 `path`，不是 `directory`。

Go 的 `json.Unmarshal` 默认行为是**静默丢弃未知字段**——`directory` 被忽略，`path` 保持零值，然后工具返回：

```
list_dir: path is required
```

模型看到这个错误一脸懵：我明明传了路径，为什么说没有？然后再试一次，还是同样的错误。循环卡死。

修复：用 `json.Decoder.DisallowUnknownFields()`，让未知字段产生明确的错误消息：

```go
func UnmarshalStrict(toolName string, raw json.RawMessage, v any, validFields ...string) error {
    dec := json.NewDecoder(bytes.NewReader(raw))
    dec.DisallowUnknownFields()
    if err := dec.Decode(v); err != nil {
        return fmt.Errorf("%s: bad arguments: %v. Got: %s. Valid fields: %s",
            toolName, err,
            truncateArgs(string(raw), 200),
            strings.Join(validFields, ", "))
    }
    return nil
}
```

错误变成了：

```
list_dir: bad arguments: json: unknown field "directory".
Got: {"directory":"/Users/whyiyhw/code","depth":1}.
Valid fields: path, depth, show_hidden
```

模型立刻知道问题所在，下一次调用就能纠正。从"循环卡死"变成"一次自我修正"。

所有工具的 `Execute` 方法的第一步都调用 `UnmarshalStrict`：

```go
func (Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
    var a Args
    if err := tools.UnmarshalStrict("list_dir", raw, &a,
        "path", "depth", "show_hidden"); err != nil {
        return "", err
    }
    // ...
}
```

> **坑 #7：`json.Unmarshal` 静默丢弃未知字段，这在 LLM 工具调用边界是致命的**
>
> 标准库的这个行为在普通业务场景里是合理的（向前兼容），但在"模型生成 JSON，你的代码解析，错误消息反馈给模型"这条链路上，静默丢弃让自我纠错变得不可能。凡是 LLM 直接生成的 JSON 落地的地方，都应该用 `DisallowUnknownFields`。

---

## 5.6 read 工具：简单背后的一个设计决策

`read` 工具的核心功能：读文件，返回带行号的内容，支持 `offset`/`limit` 分页。

但有一个有意思的细节：当 `path` 是目录时，`read` 不报错，而是自动做浅层 listing：

```go
if info.IsDir() {
    f.Close()
    return listDirShallow(clean)
}
```

为什么？因为模型经常做的事情是"先看看这个目录里有什么"，然后再决定读哪个文件。如果 `read(path="/some/dir")` 报错，模型要先纠错，再调用 `list_dir`，多一轮推理。

"对了就做，错了告诉你为什么"比"严格匹配，否则报错"在 Agent 场景里更有效率。这个原则在工具设计里会反复出现。

---

## 5.7 grep 工具：找位置，再精读

第 1 章提到，`read` 工具的问题是模型经常读整个文件，把大量无关内容塞进 context。`grep` 工具解决这个问题：

```
工作流程：
grep("func ServeHTTP", "internal/**/*.go", context_lines=3)
  → "internal/server/server.go 第 142 行"

read("internal/server/server.go", offset=138, limit=20)
  → 精准拿到 ServeHTTP 函数体
```

设计时一个值得记录的细节：`context_lines` 参数使用 `*int` 而不是 `int`：

```go
type Args struct {
    Pattern      string `json:"pattern"`
    Path         string `json:"path"`
    ContextLines *int   `json:"context_lines,omitempty"` // 指针
    // ...
}
```

为什么用指针？因为 `context_lines=0` 是一个有效的值（不显示上下文行），但 Go 的 `json:"omitempty"` 在 `int` 类型下会把 `0` 当成"未设置"而省略掉。结果：用户明确传 `0`，但解析后得到的是默认值 `3`。用 `*int`，nil 是"未设置"，指向 `0` 的指针是"明确设为 0"，两者可以区分。

---

### 相关踩坑

工具系统实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. `json.Unmarshal` 静默丢弃未知字段——LLM 拼错字段名产生无用的错误信息**

- **Saw**：模型调用 `list_dir({"directory": "/path", "depth": 1})`（字段名错误）。错误信息是 `list_dir: path is required`——完全没有提到 `directory` 是未知字段，也没有提示有效字段。模型用一模一样参数重试，陷入自我怀疑循环。
- **Why**：Go 的 `json.Unmarshal` 默认静默丢弃未知字段。`directory` 被忽略，`path` 保持零值，后续的空值检查产生了一条没有任何诊断信息的通用错误。
- **Fix**：`internal/tools/tool.go` 引入 `UnmarshalStrict`（使用 `json.Decoder.DisallowUnknownFields`）和 `MissingField` 辅助函数。错误信息现在变成：`list_dir: bad arguments: json: unknown field "directory". Got: {"directory":...}. Valid: path, depth, ...`——模型当轮就能纠正。
- **Lesson**：任何 LLM 工具边界的 `json.Unmarshal` 目标都必须使用 `DisallowUnknownFields`。静默丢弃让自我纠正循环不可能——模型没有可操作的错误信息。

**2. `Policy.mode` 被 `/yolo` 切换与并发 `Check` 竞态**

- **Saw**：`/yolo` 切换 permission mode 时，并发的 `Check` 调用读到不一致的中间状态，导致权限判断错误。
- **Fix**：用 `sync.RWMutex` 保护 `mode` 字段的读写。`Check` 拿读锁，`SetMode` 拿写锁。

**3. 符号链接绕过 CWD 安全检查**

- **Saw**：工作目录内的符号链接可以指向目录外，`write`/`edit` 工具的路径检查只做了字符串前缀比较，没解析符号链接的目标。
- **Fix**：路径解析时调用 `filepath.EvalSymlinks` 解析所有符号链接后再做安全检查。

**4. 空工具结果 + `omitempty` → DeepSeek 拒绝消息**

- **Saw**：工具返回空字符串结果，序列化时 `omitempty` 导致 `content` 字段被省略。DeepSeek 拒绝此类缺少 `content` 的消息。
- **Fix**：确保工具结果为空时仍保留 `content` 字段（或返回占位符 "ok"）。

**5. 新增 permission mode 需要触及 7+ 包**

- **Lesson**：权限模式是横切关注点，新增 `Mode` 值的 checklis 包括：`permission.go` 的常量定义、`Check` 的 switch/case、TUI 状态栏渲染、plan-mode 子态判断等 7 个以上位置。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- 工具接口的四个方法：Name / Description / Schema / Execute
- Schema 必须是 package-level `[]byte` 常量，字节恒定是前缀缓存命中的前提
- 权限拒绝是工具结果，不是崩溃，模型需要知道被拒绝才能优雅处理
- `json.Unmarshal` 静默丢弃未知字段，LLM 场景必须用 `DisallowUnknownFields`
- 工具设计原则：输入有歧义时做有用的事（`read` 遇到目录），而不是立刻报错
- 可选整型参数如果 0 是有效值，用 `*int` 而不是 `int`

下一章，我们进入 M3——把 DeepSeek 的 reasoner 能力包装成一个工具，让聊天模型可以在需要时"外包"复杂推理，以及为什么这需要一个完全隔离历史的独立 Chat 调用。

---

*对应 commit：工具系统初始实现 + UnmarshalStrict 修复。运行 `go test -race ./internal/tools/...` 验证。*
