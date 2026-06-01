# 第 12 章：M5.4 — MCP client

> 对应代码：`pkg/mcp/`（协议客户端）、`internal/mcpconfig/`（配置加载）、`internal/tools/mcptool/`（注册桥接）
> 起点：M5.3 之后，模型有了 read/grep/edit/bash 这些 seek 自带工具，加上 Skill 工具拉项目级说明书。但工具集是**封闭的**——想加一个 GitHub API、一个数据库查询、一个 Jira 写工单，必须改 seek 源码、重新编译、重新发布。
> 终点：本地或团队部署的 MCP server 进程可以即插即用——`mcp.json` 配好一条，重启 seek，里面的工具就出现在 Agent 的工具表里，行为和内置工具完全一致。

---

MCP（Model Context Protocol）是 Anthropic 在 2024 年底推出的标准，目的是把"模型能调用的工具/资源"从"绑死在 host 应用里"解耦成"任何符合协议的 server 都能挂上来"。

到本章写的时候，社区已经积累了相当多的 MCP server：`filesystem` 让模型访问任意目录、`postgres` 让模型查数据库、`github` 让模型读 issue 和 PR、各种公司内部的 server 让模型查公司知识库。如果 seek 不支持 MCP，每加一类工具都要改 seek 源码——这显然不是可持续的扩展方向。

这一章按"传输 → 协议 → 桥接 → 集成"四层讲。

---

## 12.1 MCP 是一个"瘦协议 + 子进程模型"

朴素的"扩展机制"设计有几种常见形态：

- **Plugin DSO**：编译成动态库，host 进程 dlopen。性能最好，但 ABI 兼容噩梦、调试地狱、跨语言痛苦。
- **HTTP API**：插件起个 HTTP server，host 调用。简单，但每个插件都要管端口、起服务，运维复杂；本地工具不该带 socket 监听。
- **stdin/stdout JSON-RPC**：host 把插件作为**子进程**启动，通过 pipe 收发 JSON 消息。零运维，进程隔离，跨语言天然支持，崩了就重启。

MCP 选了第三种。具体来说：

- **传输**：`stdio`（host spawn 子进程，pipe stdin/stdout 通信）
- **编码**：JSON-RPC 2.0（一个非常瘦的请求/响应协议）
- **应用层**：`initialize` → `tools/list` → `tools/call`（外加 `resources/list`、`prompts/list` 等可选能力，本章先不展开）

为什么是子进程而不是 socket？
- **生命周期清晰**：host 退出 = 子进程死，没有孤儿
- **权限继承**：子进程继承 host 的 user/group，不需要单独 auth
- **零监听端口**：本地工具的安全模型最干净
- **进程隔离**：server 崩了不会拖死 host

代价：通信只能一对一，不能多个 host 共享一个 server 实例。对 seek（单用户本地工具）这正是想要的——每个 seek 进程有自己的 server 实例，互不影响。

---

## 12.2 JSON-RPC 2.0：一个很瘦的协议

JSON-RPC 2.0 整个规范五页纸。核心三类消息：

```jsonc
// 请求（有 id，期望响应）
{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}

// 响应（id 对应请求）
{"jsonrpc": "2.0", "id": 1, "result": {"tools": [...]}}
// 或
{"jsonrpc": "2.0", "id": 1, "error": {"code": -32601, "message": "method not found"}}

// 通知（无 id，不期望响应）
{"jsonrpc": "2.0", "method": "notifications/initialized"}
```

`pkg/mcp/types.go` 把这些翻成 Go 结构：

```go
type request struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int64           `json:"id,omitempty"`   // 0 = notification
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int64           `json:"id,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *rpcError       `json:"error,omitempty"`
}
```

注意 `omitempty` 在 `ID` 上的用法——**JSON-RPC 用"字段是否存在"区分请求和通知**。请求必须有 `id`，通知不能有 `id`。Go 端用 `int64` + `omitempty` 巧妙地把"等于 0" 映射到"不出现在 wire 上"，发出去的字节就是合法的通知。

这是一个 Go 类型系统和 wire 格式的小巧合：JSON-RPC 的 id 可以是 string 或 number（规范允许），seek 选了 `int64` + 顺序生成。0 永远不会被分配（`c.seq++` 在使用前先自增，第一个 id 是 1），所以"零值 = 字段不出现 = 通知" 这一约定是安全的。

### 序列化协调：在 wire 层走单一队列

```go
type Client struct {
    mu  sync.Mutex
    enc *json.Encoder
    dec *json.Decoder
    seq int64
    // ...
}

func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    // ...写请求，读响应...
}
```

整个 `call` 持锁。**同一时刻只有一个请求 in-flight**。这是有意的简化：

- seek 的 Agent 循环是**顺序**调度工具的——一个 tool_call 处理完才处理下一个
- 如果走"并发请求 + 按 id 匹配响应"那一套，需要维护待响应 map、读 goroutine、超时清理——多 100 行代码、3 个 race 风险点
- MCP server 的吞吐瓶颈通常在 server 本身（执行工具的代价），不在 wire 上多发几条请求

简单串行化对当前场景足够。**未来如果 Agent 改成并发分发工具**（PRD 里 Post-v1.0 的 "parallel tool calls"），需要回头重写这一段为多路复用。届时的代价是已知的、可隔离的——`pkg/mcp` 内部改，外部接口不变。

### 跳过遇到的 notification

```go
for {
    if err := ctx.Err(); err != nil { return nil, err }
    var resp response
    if err := c.dec.Decode(&resp); err != nil { return nil, fmt.Errorf(...) }
    // Notifications carry no ID (decodes as 0); skip them.
    if resp.ID == 0 { continue }
    if resp.ID != id { continue }  // stale，串行模式下也不该出现
    // ...
}
```

MCP server 可以**主动发通知**给 client（比如 `tools/listChanged` 表示工具列表变了）。当前 seek 不订阅这些通知——但 wire 上还是会收到。`call` 在等响应的循环里看到 `ID == 0` 直接 continue 掉，不当成错误。

这条"宽容地丢弃不认识的消息"是一个有意的稳健性策略：协议会演进，server 可能比 client 新，client 看不懂的 frame 不应该让整个连接死掉。

---

## 12.3 子进程 spawn：`exec.CommandContext` + pipe

```go
func StartServer(ctx context.Context, cfg ServerConfig) (*Client, error) {
    srvCtx, cancel := context.WithCancel(ctx)
    cmd := exec.CommandContext(srvCtx, cfg.Command, cfg.Args...)
    if len(cfg.Env) > 0 {
        cmd.Env = append(os.Environ(), cfg.Env...)
    }
    cmd.Stderr = io.Discard

    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    return &Client{
        enc:    json.NewEncoder(stdin),
        dec:    json.NewDecoder(bufio.NewReader(stdout)),
        cmd:    cmd,
        cancel: cancel,
    }, nil
}
```

几个值得展开的细节：

**`exec.CommandContext` 而非 `exec.Command`**：前者在 ctx 取消时**自动 SIGKILL 子进程**。这是 server 进程生命周期的关键——seek 退出（ctx 取消传到 srvCtx）时所有 MCP server 一起退出。没有孤儿进程。

**Env 是 append 不是替换**：`os.Environ() + cfg.Env`——seek 的环境变量（PATH、HOME 等）全继承，server 配置的额外变量（如 `GITHUB_TOKEN`）追加在后面。`os/exec` 的语义里后面的 KEY=VALUE 覆盖前面的，所以用户配置赢。

**`cmd.Stderr = io.Discard`**：MCP server 按规范把诊断信息写 stderr（stdout 留给 JSON-RPC frame 干净使用）。host 不需要这些 stderr 输出——它们对调试有用，但混进 seek 自己的输出会让 TUI 渲染乱掉。Discard 是简单选择；将来如果加 `--mcp-debug` 标志，把 stderr 转到一个文件就行。

**`bufio.NewReader(stdout)` 包一层**：`json.Decoder` 自己有内部缓冲，但 `bufio.Reader` 在它前面再加一层，确保**部分读**（一个 frame 跨多次 syscall 到来）不会让 decoder 卡住。这是和管道通信打交道的标准做法。

### Close 的语义

```go
func (c *Client) Close() error {
    if c.cancel != nil {
        c.cancel()       // 取消 srvCtx → CommandContext 发 SIGKILL
    }
    if c.cmd != nil {
        return c.cmd.Wait()  // 收尸，避免僵尸进程
    }
    return nil
}
```

`cancel()` 加 `Wait()` 是 unix 进程清理的固定姿势——杀掉 + 回收 PID。少了 `Wait` 就有 zombie 进程；少了 `cancel` 就只是 host 不再读它，子进程在 `os.Exit(0)` 之前不会主动退出。

---

## 12.4 三步握手：initialize → tools/list → 准备就绪

```go
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
    p, _ := json.Marshal(initializeParams{
        ProtocolVersion: ProtocolVersion,           // "2024-11-05"
        Capabilities:    clientCapabilities{},      // 空——seek 当前不需要任何 client 能力
        ClientInfo:      clientInfo{Name: "seek", Version: "0.1"},
    })
    raw, err := c.call(ctx, "initialize", p)
    // ...
    // 必须的后续动作：通知 server 初始化完成
    c.notify(ctx, "notifications/initialized", nil)
    // ...
}
```

MCP 的握手有一个**容易忽略的礼貌动作**：客户端发 `initialize` 请求 → server 回 `initializeResult` → **客户端必须发一条 `notifications/initialized` 通知** 表示"我准备好了"。在这之前，server 不应该认为初始化完成。

第一次写的时候很容易漏这一步——`initialize` 请求拿到响应，看起来一切正常，然后调 `tools/list` 就挂住或者报"未初始化"错误。规范的细节，但写错代价直接（server 行为不一致）。

之后 `ListTools(ctx)` 把整个工具表拉过来：

```go
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
    raw, err := c.call(ctx, "tools/list", json.RawMessage(`{}`))
    // ...
    return result.Tools, nil
}

type ToolDef struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"inputSchema"`
}
```

注意 `InputSchema` 是 `json.RawMessage`——**不解析进 Go 结构**。原因：每个 MCP server 的 schema 长得各不相同（filesystem 的 `read_file` 和 postgres 的 `query` 输入参数完全不同），不存在一个"通用"的 Go 结构能覆盖它们。直接把字节当作不透明的 schema，转交给 LLM 用。

这是 `json.RawMessage` 的典型用例——**当一个 JSON 字段你不需要解析、只是要原样转发时，用它**。免去结构定义的开销，免去往返序列化的成本（字节就是字节）。

---

## 12.5 Bridge：把 MCP 工具假装成 seek 工具

Agent 循环只认识 `tools.Tool` 接口——它不知道某个工具是 "in-process Go 函数" 还是 "JSON-RPC 经 stdio 调一个 Python 子进程"。`Bridge` 就是这个伪装层：

```go
// internal/tools/mcptool/bridge.go
type Bridge struct {
    client     *mcp.Client
    serverName string
    effectName string         // 注册时用的名字，可能加了前缀
    def        mcp.ToolDef
}

func (b *Bridge) Name() string            { return b.effectName }
func (b *Bridge) Description() string     { return b.def.Description }
func (b *Bridge) Schema() json.RawMessage { return b.def.InputSchema }

func (b *Bridge) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
    var args map[string]any
    if err := json.Unmarshal(raw, &args); err != nil {
        return "", fmt.Errorf("mcptool %s: bad arguments: %v", b.effectName, err)
    }
    result, err := b.client.CallTool(ctx, b.def.Name, args)
    if err != nil {
        return "", fmt.Errorf("mcptool %s: %w", b.effectName, err)
    }
    text := result.TextContent()
    if result.IsError {
        return "", fmt.Errorf("mcptool %s: tool error: %s", b.effectName, text)
    }
    if text == "" {
        return fmt.Sprintf("mcptool %s: (empty response)", b.effectName), nil
    }
    return text, nil
}
```

`Execute` 做的事情：
1. **解析参数**：MCP 协议要求 `arguments` 是 JSON 对象，不是 schema-validated 的结构——`map[string]any` 是最忠实的映射。
2. **调用 server**：`client.CallTool` 把这次调用包装成 `tools/call` JSON-RPC 请求发过去，阻塞等响应。
3. **提取文本**：MCP 的 `CallToolResult` 有一个 `Content []ContentBlock` 字段，每个 block 有 type（"text" / "image" / 等）。`TextContent()` 只取 text block 拼起来。seek 当前不处理 image 等富类型——LLM 拿到的全是字符串。
4. **错误传递**：`IsError = true` 是 MCP 用来表达"工具执行了，但语义上是错误"（比如 "file not found"）。Bridge 把这种情况翻译成 Go error，让 Agent 走"工具结果是错误消息"的路径（第 5 章那条"权限拒绝是工具结果不是 fatal error"的延伸）。

### 不带 `UnmarshalStrict`：为什么不约束未知字段

第 5 章里所有 seek 自带工具都用 `UnmarshalStrict` 拒绝未知字段。Bridge 这里却用普通 `json.Unmarshal`。

原因：**schema 来自 server，不来自 seek**。如果 server 后续往 schema 里加了字段、模型在新字段上发了参数——MCP server 自己会处理。seek 不应该夹在中间检查"这个字段是不是 schema 里声明的"。Bridge 是个透明的转发层；它的工作是**忠实传递**，不是验证。

这是不同抽象层的不同纪律——seek 自带工具的契约由 seek 控制（严格），MCP 工具的契约由 server 控制（透明转发）。

---

## 12.6 名称冲突：动态前缀

```go
// internal/tools/mcptool/bridge.go
for _, def := range defs {
    effectName := def.Name
    if existing[effectName] {
        effectName = serverName + "__" + def.Name
    }
    bridges = append(bridges, New(client, serverName, def, effectName))
}
```

一个真实问题：`filesystem` MCP server 提供一个叫 `read_file` 的工具——名字跟 seek 自带的 `read` 工具不冲突。但如果某个 MCP server 提供了一个也叫 `read` 的工具呢？

注册到 `tools.Registry` 时会覆盖（或 panic，取决于实现）。Bridge 的处理是**只在冲突时**前缀：

- 没冲突：直接用 server 给的名字 `read_file`
- 冲突：前缀成 `<server>__<tool>`，如 `myserver__read`

为什么不**永远**加前缀？因为大多数 MCP server 的工具名字本来就独特（`read_file` / `query` / `gh_create_issue`），强制前缀会让 LLM 看到的工具名字变长、变难记。"够独特就保持原名，冲突才加前缀" 是一个 just-enough 策略。

副作用：**注册顺序影响最终名字**。如果 server A 先注册了 `foo`，server B 的 `foo` 就变成 `B__foo`；反过来注册顺序就翻转。当前实现按 `mcp.json` 里 `mcpServers` map 的 Go 迭代顺序（**不确定**），所以严格说同一份配置启动两次可能给出不同的最终名字。

这是一个**已知缺陷**——目前还没修，因为冲突在实际使用中极少发生。一旦真发生，修法是显然的：按 server name 字典序遍历 `mcpServers`，让顺序确定下来。坑录里没记，但应该记——埋一下。

---

## 12.7 `mcp.json`：兼容 Claude Code / Cursor 的格式

```jsonc
// ~/.config/seek/mcp.json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/Users/whyiyhw/code"]
    },
    "github": {
      "command": "docker",
      "args": ["run", "--rm", "-i", "mcp/github"],
      "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_xxx"}
    }
  }
}
```

这个格式**和 Claude Code / Cursor 用的一致**。理由跟第 11 章兼容 `.claude/skills/` 一样——已经在用其他工具的用户，配置文件直接复用，零迁移成本。

`internal/mcpconfig/config.go` 解析：

```go
type ServerEntry struct {
    Command string            `json:"command"`
    Args    []string          `json:"args"`
    Env     map[string]string `json:"env"`
}

type Config struct {
    MCPServers map[string]ServerEntry `json:"mcpServers"`
}
```

文件不存在 = 空 config，**不是错误**：

```go
func LoadFrom(path string) (Config, error) {
    data, err := os.ReadFile(path)
    if os.IsNotExist(err) {
        return Config{}, nil      // ← 显式无错返回
    }
    // ...
}
```

绝大多数用户启动 seek 时没有这个文件——开箱即用比"必须配置"重要。

平台路径：
- macOS/Linux: `$XDG_CONFIG_HOME/seek/mcp.json`（默认 `~/.config/seek/mcp.json`）
- Windows: `%APPDATA%/seek/mcp.json`

这跟 Claude Code 也对齐。一个看起来琐碎的细节，但是用户从 Claude Code 切到 seek 时"我的 server 不见了"的烦恼能不能避免，就在这一行的细节上。

---

## 12.8 集成：启动时一次加载、错误不致命

```go
// cmd/seek/main.go
if mcpCfg, err := mcpconfig.Load(); err != nil {
    fmt.Fprintln(os.Stderr, "mcp config:", err)
} else if len(mcpCfg.MCPServers) > 0 {
    servers := /* mcpconfig.ServerEntry → mcptool.ServerConfig */
    existing := /* 当前 reg 里已有工具名的集合 */
    lr := mcptool.LoadServers(ctx, servers, existing)
    for _, e := range lr.Errors {
        fmt.Fprintln(os.Stderr, "mcp:", e)
    }
    for _, b := range lr.Bridges {
        reg.Add(b)
    }
    if len(lr.Bridges) > 0 {
        fmt.Fprintf(os.Stderr, "mcp: loaded %d tool(s)\n", len(lr.Bridges))
    }
}
```

加载有几条纪律：

**配置错误不致命**：`mcp.json` JSON 解析失败 → 打到 stderr，seek 照常启动（无 MCP 工具）。比"启动失败让用户面对一个不能跑的 seek"友好。

**某 server 启动失败不影响其他**：`LoadServers` 在内部对每个 server 独立 try/recover——`filesystem` 跑起来了、`github` 因为 docker 没装挂了，结果是 `filesystem` 的工具注册成功，`github` 错误写到 stderr，seek 启动后能用 `filesystem`。

**启动开销可见**：`mcp: loaded N tool(s)` 一行明确告诉用户 MCP 系统活着、加载了多少工具。如果加载了 0 个，这行不出来——不会污染用户的启动输出。

这个"启动时一次性 spawn 所有 server"的模型有个隐含成本：**N 个 server = N 个子进程，在 seek 进程生命周期内常驻**。即使用户这次只用其中一个，其他几个也在跑、占着内存。

对当前规模（典型用户 1-3 个 server），常驻成本几乎可忽略（每个 server 几十 MB）。如果将来 server 数量增长到 10+，可以改成"lazy spawn on first call"模式——首次模型调用某 MCP 工具时才启动那个 server。当前不优化是因为复杂度增加而收益对典型用户为零。

---

## 12.9 一个端到端验证

跑一个真实的 `filesystem` server：

```jsonc
// ~/.config/seek/mcp.json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }
  }
}
```

启动 seek，stderr 显示：

```
mcp: loaded 5 tool(s)
```

进入 TUI 后：

```
> 列出 /tmp 下的目录
```

模型会调用 `list_directory` 工具（filesystem server 提供的）——MCP server 收到 `tools/call`，返回目录内容，bridge 把文本结果当作 tool result 追加到对话历史。整个流程对用户来说和"调内置工具"无差别。

这是 M5.4 "工具表从封闭变成可扩展" 的最直接证据。同一个 seek 二进制，三天前没有 GitHub 工具，今天 `mcp.json` 加一条、重启，模型能查 PR 了——零代码修改。

---

### 相关踩坑

MCP 客户端实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. 子进程夺取 /dev/tty——Esc 无法中断 bash**

- **Saw**：MCP 子进程（如 filesystem server）继承终端控制权，主进程的 Esc 中断失效。
- **Why**：子进程的标准 I/O 如果连接到终端（而非 pipe），会夺取终端的控制权。
- **Fix**：确保子进程的 stdin/stdout/stderr 使用 pipe，不与主进程共享终端。

**2. Setsid + pipe stdout = 孤儿孙进程导致 Wait() 死锁**

- **Saw**：MCP 子进程通过 `setsid` 创建新 session 后，其孙进程成为孤儿。管道的写入端不能关闭，导致父进程 `Wait()` 永远阻塞。
- **Fix**：在上下文取消时强制 kill 进程组，确保所有子进程终止、管道关闭。

**3. Windows CRLF 编辑匹配问题**

- **Saw**：`edit` 工具的 `old_string` 在 Windows CRLF 文件上匹配失败。
- **Fix**：在匹配前规范化换行符。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- MCP = stdio + JSON-RPC 2.0 + `initialize` / `tools/list` / `tools/call` 三步握手。子进程模型让生命周期、权限、隔离都很清晰
- JSON-RPC 用"有无 `id` 字段"区分请求和通知；Go 端用 `int64` + `omitempty`，0 永不分配 → 安全的零值-即-通知映射
- 当前实现 wire 层全局锁串行化 = 简单 + 当前足够；并发分发要回头改这一段
- 跳过不认识的 `id`（含通知）是协议演进时的稳健性储备
- `exec.CommandContext` 让 ctx 取消自动 SIGKILL 子进程；`Close` 必须 `cancel + Wait` 才能彻底回收
- `initialize` 后必须发 `notifications/initialized` 通知——容易漏的协议礼仪
- `InputSchema` 用 `json.RawMessage` 不解析 = 透明转发，不假装通用契约
- Bridge **不**用 `UnmarshalStrict`——MCP 工具的 schema 契约归 server，seek 是透明传递层
- 名称冲突时按 `<server>__<tool>` 前缀；遍历顺序不确定是已知缺陷（按 server name 排序可修）
- `mcp.json` 格式兼容 Claude Code / Cursor——已有用户零迁移成本
- 错误隔离：单 server 失败不阻塞其他，启动失败不致命，文件缺失不当错误

下一章进入一个**新增的章节**（原 TOC 里没有）：**成本与上下文预算**。我们会看到 `cache.Tracker` 怎么算缓存命中率、`pricing` 怎么算钱、`budget` 怎么决定什么时候提示 `/compact`，以及第 8 章和第 9 章都埋伏过的那条反直觉——为什么 ctx% 必须用 `Last()` 而不是 `Cumulative()`。

---

*对应 commit：`45c71af`（MCP client + JSON-RPC + Bridge 初版）、`398448e`（失败路径与并发测试覆盖）。运行 `go test ./pkg/mcp/... ./internal/mcpconfig/... ./internal/tools/mcptool/...` 验证。*
