# 第 15 章：M7 与发布

> 对应代码：`cmd/seek/main.go` 的 `runJSON` 与 `stdinIsPiped`、`internal/tui/banner.go` 的 `VersionString` / `formatVersion`、`go.mod`、`.github/workflows/ci.yml`。
> 起点：前 14 章构建出了一个能跑、能跨 provider、能持久化、能扩展的 Agent。
> 终点：让外部世界能用上这个二进制——脚本能消费它的输出、`go install` 一行装好、运行时知道自己是哪个版本、三大平台都正确。

---

最后一章。M7 不是一次大的功能跃迁，是一组让"工程产品"变成"可被别人使用的产品"的小事：

- **JSON 输出模式**：让脚本能解析 seek 的输出
- **自举**：用 seek 开发 seek，把自己当作真实用户
- **`go install` + 版本号**：单二进制发布的最小可工作姿势
- **跨平台细节**：windows/macOS/linux 的差异点

每件事各自都不大，但加起来决定了"这工具能不能让别人用上"。

---

## 15.1 JSON 输出模式：让脚本能用 seek

第 8-13 章讲的一切都建立在"用户坐在终端前面看 TUI"这个假设上。但 seek 的最自然演化方向之一是：**让它跑在脚本里**。比如：

- CI 上跑 "seek -p 'review this diff' --provider=deepseek" 然后把输出拿去标记 PR
- crontab 里跑 "seek -p '今天有 N 条新 issue，挑出最紧急的几条'" 然后发到 Slack
- 一个更上层的 agent 框架把 seek 当作 building block，需要按事件粒度消费它的输出

人读的 TUI 输出对这些场景都不友好——你要从 Markdown + 颜色 + 进度条里 grep 出"模型说了什么 / 调了哪些工具"，正则会越写越脆。

M7 加了 `-json` 标志（commit `dd5db84`）：**一行一个 JSON 对象的 JSONL 流到 stdout**。

```bash
$ seek -p '读 README.md 然后告诉我这个项目是干嘛的' -json
{"type":"agent_start"}
{"type":"turn_start","index":0}
{"type":"text_delta","delta":"我来读"}
{"type":"text_delta","delta": "一下 README"}
{"type":"tool_start","id":"call_1","name":"read","args":"{\"path\":\"README.md\"}"}
{"type":"tool_end","id":"call_1","name":"read","result":"...","bytes":2547}
{"type":"turn_end","index":0,"prompt_tokens":1832,"completion_tokens":124,"cache_hit_tokens":1600,"tool_calls":1}
{"type":"turn_start","index":1}
{"type":"text_delta","delta":"这是一个"}
...
{"type":"agent_end","turns":3,"prompt_tokens":7341,"completion_tokens":482,"cache_hit_tokens":6400,"tool_calls":2,"session_id":"20260121-..."}
```

每行一个独立 JSON 对象，`jq` 一条管道处理：

```bash
seek -p 'find all TODOs' -json \
    | jq -c 'select(.type == "tool_end" and .name == "grep")'
```

### 事件类型是稳定契约

```go
// cmd/seek/main.go
//
// Type values (stable contract — breaking changes = major version bump):
//
//  agent_start      — one per run
//  turn_start       — one per LLM call; index is 0-based
//  text_delta       — incremental assistant text; delta is the new chunk
//  reasoning_delta  — incremental CoT text from deepseek-reasoner
//  tool_start       — a tool call is about to execute; id/name/args set
//  tool_delta       — intermediate output from a streaming tool (think)
//  tool_end         — tool finished; result set on success, error on failure
//  turn_end         — LLM call settled; token counts + tool_calls count
//  agent_end        — run complete; cumulative stats; session_id if saved
//  error            — fatal error; message is the error string
```

明确写在源码注释里：**事件类型字符串是 stable contract**。新增类型 = minor version；删除或改义 = major version。

这跟普通的"我们改个字段名应该没事吧"心态是相反的——一旦 JSON 输出有外部消费者，每一个字段就是 API 的一部分。把它当 API 对待，文档化得早，破坏性变更才会被有意识地管理。

### `jsonLine`：一个 envelope，多种 type

```go
type jsonLine struct {
    Type  string `json:"type"`
    Index int    `json:"index,omitempty"`
    // text_delta / reasoning_delta / tool_delta
    Delta     string `json:"delta,omitempty"`
    Reasoning bool   `json:"reasoning,omitempty"`
    // tool_start / tool_delta / tool_end
    ID     string `json:"id,omitempty"`
    Name   string `json:"name,omitempty"`
    Args   string `json:"args,omitempty"`
    Result string `json:"result,omitempty"`
    Bytes  int    `json:"bytes,omitempty"`
    // error / tool_end error
    Error string `json:"error,omitempty"`
    // turn_end / agent_end token accounting
    PromptTokens     int `json:"prompt_tokens,omitempty"`
    CompletionTokens int `json:"completion_tokens,omitempty"`
    CacheHitTokens   int `json:"cache_hit_tokens,omitempty"`
    ToolCalls        int `json:"tool_calls,omitempty"`
    // agent_end only
    Turns     int    `json:"turns,omitempty"`
    SessionID string `json:"session_id,omitempty"`
}
```

**一个 struct 承载所有事件类型**，`omitempty` 让不相关字段消失。代价：消费者必须知道哪些字段属于哪个 type。收益：

- Go 端不需要维护 10 个 struct + 一个 sum type interface（虽然第 14 章的 `llm.Event` 里那样做了——但 wire 格式是另一回事）
- JSON 编码/解码代码极其简单
- 消费者用 `jq` 时不需要 union 各种类型

这是"interface vs envelope"经典权衡里偏向 envelope 的选择。本来想做 polymorphic encoding，权衡之后 envelope 在脚本场景更友好（jq 不擅长 union）。

### 路由：`-json` 隐含 print 模式

```go
// cmd/seek/main.go
if *jsonOut || *prompt != "" || stdinIsPiped() {
    // print/json mode 路径——不进 TUI
}

// runJSON 内部
emit := func(line jsonLine) {
    _ = enc.Encode(line)   // json.Encoder always writes a trailing \n
}
```

三种触发"非 TUI 模式"的条件之一就行：
1. `-json` 显式选
2. `-p '...'` 给了 prompt
3. stdin 被管道喂数据（`stdinIsPiped()`）

`stdinIsPiped` 检查 stdin 是不是终端：

```go
func stdinIsPiped() bool {
    stat, err := os.Stdin.Stat()
    if err != nil { return false }
    return stat.Mode()&os.ModeCharDevice == 0
}
```

`os.ModeCharDevice` 在 stdin 是 TTY 时被置位，重定向或管道时清零。这是 Unix 工具检测"我是在管道里还是交互式"的标准做法。`cat file.txt | seek` 自动进 print 模式，不需要用户再加 `-p`。

`-json` 用 `json.Encoder.Encode`——这又一次是第 9 章讲过的 JSONL 原语。手写 `Marshal + "\n"` 就有点不对了。

### stdout vs stderr 的洁净分离

```go
// runJSON
emit := func(line jsonLine) {
    _ = enc.Encode(line)   // stdout
}
// ...
fmt.Fprintf(os.Stderr, "warning: failed to save session ...")  // stderr
```

JSON 流走 **stdout**，诊断 / 警告 / 错误 / 提示走 **stderr**。这是 unix 工具的标准合同——**stdout 必须解析得动**，任何"给人看的"信息走 stderr。

如果 stderr 不分开走，脚本 `seek -json | jq` 在出现 warning 时立刻 jq 解析失败。分开之后，warning 印到终端不影响管道里的 JSON 流。

```bash
# stdout 进 jq，stderr 进终端
seek -p '...' -json 2>warnings.log | jq '...'
```

这是个非常便宜的纪律 ——`fmt.Println` 写到 stdout，`fmt.Fprintln(os.Stderr, ...)` 写到 stderr。代价为零，收益是"工具能被嵌进任何脚本里"。

---

## 15.2 自举：用 seek 写 seek

这是 PRD §6 验收标准里的一条：

> v1.0 之前：用 seek 完成至少一个非平凡的 seek 自身修改。

不是"能跑通"的验收，是**写代码**的验收。`README.md` 这次同步到 M6-done state 的版本——是 seek 自己跑出来的（commit `1467be8` "docs(README): sync to M6-done state via first self-hosted seek run"）。M7 polish 的三个 commit（§15.6 讲的浮动 help、`/new`、`--theme`）也是——作者描述需求，seek 跑代码。**这本书的第 8-15 章不在这个统计里**——它们走的是另一条协作链路（Claude / Anthropic），详见前言"关于写作过程"。

为什么这个测试重要？

### 工具用自己测自己时，所有粗糙都会冒出来

写一个 Agent 框架，跑跑 demo 用例都很顺利。一旦让它自己干"实质性"的活——读你的代码、改你的代码、修你的 bug——一切边角问题瞬间显形：

- 工具描述写得不够准，模型猜错调用时机 → 浪费几轮
- 错误信息不够 actionable，模型陷入"试了三次都不行就放弃"
- 流式 UI 在长任务里抖动、滚动、丢字符
- 会话太长导致 context 溢出，但提示来得太晚
- 工具的输出格式让模型读不懂自己的输出
- `/compact` 之后续作时丢了关键约束

每一个都是用户哪天用 seek 时也会遇到的——但**只有当工具的用户是工具的作者本人**，作者才会立刻动手修而不是写进 backlog。

### 自举的副产品：一份 papercut 清单

按时间线，事情大致这样发生的：

1. **M0–M5 期间**：作者自己手写代码、手写 commit message、手写 README、手写本书第 1–7 章
2. **M6 完成后**，作者开始把 seek 真正放进自己的日常工作流——不是 demo，是"今天的活就用它干"
3. **第一份自举产物**：`README.md` 同步到 M6-done state（commit `1467be8`）。大致对了，作者过一遍 commit。**这是 seek 第一次帮自己更新自己的 README**
4. **接下来**：`docs/pitfalls.md` 回填 4 条（commit `5b23e55`），同样是 seek 跑出草稿、作者校对
5. **再接下来**：作者一边用 seek 干非保密的小活，一边发现 seek 自身的几个 papercut：
   - "auto-continue 偶尔会在任务完成后多跑一轮无意义迭代" → commit `aff27da` 先 revert / `0d847b4` 改成 opt-in flag
   - "MaxTurns 默认 32 不够长链调用" → commit `50f7c0e` 提到 200
   - "read 默认 20 行太少" → commit `ad0cf28` 提到 50
   - "/help 滚屏冲掉对话" → commit `b479187` 改成浮动 overlay（§15.6 讲过）
   - "/reset 静默丢历史" → commit `393d6b7` 改成 `/new` 自动保存
   - "light 终端状态栏看不清" → commit `56b9df8` 加 `--theme` flag

每一个修复都来自"我用 seek 的过程中被这件事卡住了"。这种反馈循环是测试套件给不了的——测试验证"代码做了它应该做的"，自举验证"做的事是用户实际想要的"。

### 一个澄清：什么是 seek 自举，什么不是

要诚实划一下边界——这本书反复讲"诚实大于神秘"，自己的写作过程不能撒谎。

- **是 seek 自举**：上面 5 条列出的 README 同步、pitfalls 回填、6 个代码 papercut 的修复——作者描述需求，seek（DeepSeek 后端）跑代码并写 commit message，作者审阅 / 改 / 合并
- **不是 seek 自举**：你正在读的第 8–15 章和附录 A。它们是另一条 LLM 协作链路的产物（Claude / Anthropic API），不在 "用 seek 写 seek" 的统计里。**前言"关于写作过程"一节**把这件事讲清楚

这条区分重要——如果把"作者请另一个 LLM 帮忙写书"也算成"seek 自举"，自举的论据就稀释了：那是任何带 API 的 LLM 工具都能做到的事，不是 seek 本身可信度的证据。**seek 自举的证据**是 seek 改它自己的代码这一段，**这一段是货真价实的**——M7 polish 的三个 commit + auto-continue / MaxTurns / read 三个旧 papercut 修复都有具体 hash 摆在 git log 里。

### 自举的负面教训

也不是没有发现"不该做"的事情：

- **某些代码改动让 seek 一气写完会让风格漂浮**——比如新加的工具如果不约束 prompt，seek 会随机选择"verbose 还是 terse"。**对策**：作者过 diff，把"AI 标记"洗掉再 commit
- **commit message 的 "why not what" 原则 seek 容易丢**——seek 倾向写"做了 X、改了 Y"，而 PRD 里规定 commit body 要解释"为什么"。**对策**：作者改写 message 再提交，或者把 "why" 在描述需求时就明确说出来
- **seek 不会主动质疑前提**——你说"加一个 `/foo` 命令"，它就加；不会说"等等，`/foo` 跟现有 `/bar` 其实可以合并"

自举不是"让 seek 全自动接管"，是"让 seek 加速大段重复劳动，作者做 judgement"。

这条不在 PRD 里，但写在这里，是这本书的最后一条主旨：**自动化的工具最有价值的应用是放大作者，不是替代作者**。

---

## 15.3 `go install`：单二进制发布的最小工作姿势

```bash
go install github.com/whyiyhw/seek/cmd/seek@latest
```

一行命令，从 Github 拉源码，编译，把 `seek` 二进制装到 `$GOBIN`。用户不需要安装 Python、Node、npm、Docker；不需要管依赖；不需要"先 clone 再 build"。

这是从第 1 章就在埋伏的一个决定：**Go + 单二进制**。第 1 章讲过这个理由，到 M7 才真正兑现——所有支撑这个体验的工程决策（pkg/deepseek 零外部依赖、`go:embed` 内置 skill、`time.FixedZone` 不依赖 tzdata、零 cgo）累积起来确保 `go install` 出来的东西**真的能运行**而不是装好了报错。

测试这个最简单：

```bash
# 装到全新的 $GOBIN
GOBIN=/tmp/seekbin go install github.com/whyiyhw/seek/cmd/seek@latest

# 跑一下
DEEPSEEK_API_KEY=... /tmp/seekbin/seek -p 'hello'
```

跑得动 = 验收通过。**跑不动**意味着某处偷偷依赖了"我开发机上有但用户机器上没有"的东西——比如 cgo 链接的 libc、`time.LoadLocation` 要的 tzdata、`exec.LookPath` 找的某个 helper 程序。每次发版前应该新机器装一遍。

### 不靠 Makefile / shell 脚本 / 安装器

很多 Go 工具有 `Makefile`、`install.sh`、`brew` formula、`apt` 包……每加一种发布渠道，维护负担线性增长，且经常版本漂移（brew 上是 0.3，npm 上是 0.2，github release 是 0.4）。

seek 目前只支持 `go install`。**只一种**。对于读者来说：

- 还没装 Go → 装 Go。一次性，往后所有 Go 工具都受益
- 装了 Go → `go install`，到此为止

代价：用户必须装 Go。收益：每个用户、每个平台都走完全相同的路径——支持成本最低。

将来如果有需要，可以考虑加 `goreleaser` 出二进制 tarball + brew formula——但那是有人提出来再加，不是预先 over-engineering。

---

## 15.4 `runtime/debug.ReadBuildInfo`：版本号的正确姿势

很多 Go 项目这样写版本：

```go
// 错误：靠 ldflags 注入
var Version = "dev"

// build 命令：go build -ldflags "-X main.Version=v1.2.3" ./cmd/seek
```

问题：用户 `go install` 不会传 ldflags，所以装出来的二进制 `Version == "dev"`，永远显示开发版。

正确姿势用 `runtime/debug.BuildInfo`：

```go
// internal/tui/banner.go
func VersionString() string {
    info, ok := debug.ReadBuildInfo()
    if !ok { return "unknown" }
    return formatVersion(info)
}

func formatVersion(info *debug.BuildInfo) string {
    version := info.Main.Version
    switch {
    case version == "" || version == "(devel)":
        version = "dev"
    case strings.HasPrefix(version, "v0.0.0-"):
        version = "dev"
    }

    var rev string
    var modified bool
    for _, s := range info.Settings {
        switch s.Key {
        case "vcs.revision":
            if len(s.Value) >= 7 { rev = s.Value[:7] }
        case "vcs.modified":
            modified = s.Value == "true"
        }
    }

    if rev == "" { return version }
    suffix := ""
    if modified { suffix = "+" }
    return fmt.Sprintf("%s · %s%s", version, rev, suffix)
}
```

`debug.ReadBuildInfo` 是 Go 1.18+ 标准库特性，自动从二进制里读取：

- **`Main.Version`**：模块版本——`go install @v1.2.3` 时是 `"v1.2.3"`，`@latest` 解析后的 tag 是真实 tag，本地 `go build` 是 `"(devel)"`
- **`Settings["vcs.revision"]`**：完整 git commit hash（如果 `go build` 在 git 仓库里跑）
- **`Settings["vcs.modified"]`**：构建时工作目录是不是 dirty（有 uncommitted changes）

输出例子：

| 场景 | 输出 |
|---|---|
| `go install @v0.1.0` | `v0.1.0 · abc1234` |
| 本地 `go build`（clean） | `dev · abc1234` |
| 本地 `go build`（dirty） | `dev · abc1234+` |
| 老的 二进制没 vcs info | `dev` |
| 极端情况 ReadBuildInfo 失败 | `unknown` |

`+` 后缀很关键——它告诉用户"这是一个你机器上本地编译的、还没提交的版本"。出 bug 反馈时这个标记让你知道"这不是发布的 v0.1.0，是 v0.1.0+ 改了什么"。

### 收口在一个纯函数里

```go
func VersionString() string {              // 副作用边界
    info, ok := debug.ReadBuildInfo()
    if !ok { return "unknown" }
    return formatVersion(info)              // 纯函数
}

func formatVersion(info *debug.BuildInfo) string { ... }
```

`VersionString` 调 `ReadBuildInfo`（系统副作用），然后委托给 `formatVersion`（纯函数，输入 `*debug.BuildInfo` 出 string）。测试只测 `formatVersion`——构造合成的 `BuildInfo`，验证各种场景的输出。

```go
func TestFormatVersion_Dirty(t *testing.T) {
    info := &debug.BuildInfo{
        Main: debug.Module{Version: "v0.1.0"},
        Settings: []debug.BuildSetting{
            {Key: "vcs.revision", Value: "abc1234deadbeef"},
            {Key: "vcs.modified", Value: "true"},
        },
    }
    got := formatVersion(info)
    want := "v0.1.0 · abc1234+"
    // ...
}
```

把"读真实 build info"和"格式化字符串"分开，是测试可行的关键——副作用在外，纯逻辑在内。

---

## 15.5 跨平台：三条主要差异

CI 在 ubuntu / macOS / windows 三个 OS 上跑 `go test -race ./...`。Go 大部分代码跨平台天然工作，但有几条要小心：

### MCP 配置路径（第 12 章已展开）

```go
// internal/mcpconfig/config.go
switch runtime.GOOS {
case "windows":
    base = os.Getenv("APPDATA")
    if base == "" {
        return "", fmt.Errorf("mcpconfig: %%APPDATA%% is not set")
    }
default:
    home, _ := os.UserHomeDir()
    xdg := os.Getenv("XDG_CONFIG_HOME")
    if xdg != "" { base = xdg } else { base = filepath.Join(home, ".config") }
}
return filepath.Join(base, "seek", "mcp.json"), nil
```

unix-like 系统按 XDG 规范：`$XDG_CONFIG_HOME/seek/mcp.json` 默认 `~/.config/seek/mcp.json`。Windows 按惯例放 `%APPDATA%\seek\mcp.json`。

如果直接用 `filepath.Join(home, ".config", ...)` 在 windows 上也能工作——但放在用户 home 目录里跟 windows 用户的预期不符。每个平台尊重它自己的约定。

### `time.FixedZone` 而非 `LoadLocation`（第 13 章已展开）

```go
var Shanghai = time.FixedZone("CST", 8*60*60)
```

Alpine / distroless / 某些精简 docker image 不带 tzdata。`time.LoadLocation("Asia/Shanghai")` 在那些环境上返回错误，状态栏的离峰窗口显示就崩了。`FixedZone` 是纯计算，零文件系统依赖。

代价：不处理 DST。对中国时区无影响。对其它时区可能要加 DST 逻辑——目前不需要。

### TUI 与终端能力

第 7 章讲过的 OSC 11 探针、WindowSizeMsg 合成、KeyMsg 路由——这些都在 macOS Terminal / iTerm2 / GNOME Terminal / Alacritty 上完全一致工作。**Windows 上推荐 [Windows Terminal](https://github.com/microsoft/terminal)**：在 WT 里运行 PowerShell 或 CMD，TUI 流式输出、中文、键盘输入均正常。老式 conhost（蓝色 PowerShell 5.x 窗口）ANSI 支持弱，可能出现阶梯刷屏——换 WT 即可，详见 [`docs/guide-windows.md`](../guide-windows.md)。

其余 Windows 终端：Windows Terminal + PowerShell/CMD 为官方支持路径；Git Bash / 老式 conhost 不推荐用于 TUI。临时可用 `seek -p` print 模式。

### CRLF 不是 LF（一个没踩但要警惕的）

跨平台 Go 项目最经典的坑：windows 上 `\r\n` 换行，linux/macOS 上 `\n`。`bufio.Scanner` 默认按 `\n` split，但很多比较函数没考虑 `\r`：

```go
if line == "---" { ... }       // windows 下永远不匹配（实际是 "---\r"）
```

第 11 章 skill 解析里就写了：

```go
if strings.TrimRight(lines[i], "\r") == "---" { ... }
```

每次按行处理外部输入（用户写的文件、网络流），**养成 `TrimRight(line, "\r")`** 或者用 `strings.TrimRight(line, "\r\n")` 的肌肉记忆。一条便宜的纪律。

---

## 15.6 M7 polish：浮动 help、`/new`、`--theme`

JSON 模式（§15.1）是 M7 里"对外可见"的大功能。M7 还顺手做了三件**对内可见**的小事——分别解决一个具体的"用着觉得不对"——这一节按"被什么烦到 → 怎么修"的形式快速过一下。

### `/help` 从滚屏文本变成浮动 overlay

旧的 `/help` 把命令表打进 scrollback——20 行内容把刚才的对话冲到屏幕外。每次想查个键位绑定都要往上滚回来再翻回去。

M7 把它改成**浮动 overlay**（commit `b479187`）：

- `/help` 或者 `?` 热键（idle、input 为空、不在流式时）打开
- 居中浮窗，圆角边框，60% 终端宽度（clamp 在 [30, 80]）
- 命令列表 + 键位对照，两栏布局
- Esc / Enter / q / Q 任一键关闭，所有其它键被消费——**不会有幽灵输入串到 textarea 里**
- 关闭后底下的对话历史一字不动——浮窗只是"借用"屏幕空间，不污染 scrollback

`?` 热键是 vim / less 风格——idle 状态下按 `?` 看帮助是终端工具的肌肉记忆。

修这个 polish 时撞到一个意料外的小坑：

**新 overlay 偷了 Ctrl+C**。Overlay 打开时所有键都被 handle，但漏判了 Ctrl+C——用户没法在 help 打开时退出程序，只能 Esc 关闭 overlay 再 Ctrl+C。

```go
// 修复：Ctrl+C 在 overlay 路径里要早于 fallthrough 处理
case tea.KeyCtrlC:
    return m, tea.Quit
case tea.KeyEsc, tea.KeyEnter:
    m.helpOpen = false
    return m, nil
default:
    return m, nil  // 其它键全部 consume
```

这跟第 8 章 §8.3 那条 "Ctrl+C 在 approval 模式里必须先 reply 再 quit" 是同一类教训：**任何接管了键盘的临时 UI（modal、overlay、approval prompt）都要先把 Ctrl+C 的退出路径单独走通**，再写 `default: consume`。Ctrl+C 不是普通键，是用户唯一的逃生通道。

### `/reset` → `/new` + `/clear`

旧的 `/reset` 命令有两个问题：

1. 它**静默丢弃当前对话历史**——按下 `/reset`，agent 状态被清空，磁盘上的当前 session 也不再 touch，用户回头 `--list` 找不到刚才的工作
2. **名字和 `/clear` 混淆**——很多用户分不清 `/reset` 和 `/clear` 哪个是清屏哪个是清状态

修法（commit `393d6b7`）：

- **删掉 `/reset`**。`/clear` 保留原义（**只清屏**，agent 状态不动；终端 scrollback 由系统管，不动）
- **加 `/new`**：先 `persistSession()` 把当前会话刷到磁盘（用户不丢工作），再创建一个全新会话（新 ID，**没有 ParentID** ——不是 `/branch`，是真正的新对话），清 tracker 和 agent 状态，清屏
- `--no-save` 路径下跳过会话创建（保持 `m.opts.Session == nil` 的 ephemeral 形态）

这条修改是非常典型的"重命名 + 行为修正"双重变更——之前混在一起的两个语义被拆成两个命令，每个语义对应一个动词。`/clear` 处理屏幕，`/new` 处理对话。两个动词，两个责任，零歧义。

### `--theme dark|light|auto`

OSC 11 探针（第 7 章 §7.3）已经能用来让 glamour 选 dark / light 主题——但**只对 Markdown 渲染生效**。状态栏、菜单、banner 这些用 lipgloss 写的 UI chrome，颜色永远按 dark 终端调好，到 light 终端上对比度直接掉到看不清。

修法（commit `56b9df8`）：

- `--theme` 标志（`auto` / `dark` / `light`，默认 `auto`）
- 两套调色板：`darkPalette`（原来的）和 `lightPalette`（针对浅色背景重新调过）
- `SetTheme()` 在启动时一次性重建所有 package-level style 变量
- glamour 和 lipgloss **共享同一个解析后的主题**——避免"Markdown 是 light、状态栏是 dark"的撕裂

主题决策有三个来源，按优先级：

1. `--theme` flag 显式指定
2. `SEEK_STYLE` 环境变量（早期的逃生口，留作兼容）
3. termenv 检测终端背景色

三条路径都有 table-driven 测试覆盖——保证用户无论怎么配，最终拿到的 palette 是一致的。

### 共同点：M7 polish 是"自举驱动"的

这三个修改没一个是 PRD 里写的"v1.0 要做"的功能。它们都是这本书第 15.2 节讲的**自举副产品**——作者自己用 seek 写代码时，撞到了 `/help` 滚屏的烦躁、`/reset` 丢工作的恼火、light 终端看不清状态栏的眯眼——立刻动手修。

PRD 里"M7 polish"那一行原本是空的；这三个 commit 是把"polish"填满的具体动作。**自举不是验收测试，是产品需求来源**。

---

## 15.7 v1.0 验收：每条都对得上

回到 PRD §6——v1.0 之前要满足的验收标准。截至本章写完，状态大致：

| 类别 | 验收点 | 状态 |
|---|---|---|
| **API** | `pkg/deepseek` 流式 + cache 元数据 | ✅ M0 |
| | tools 数组 schema 字节稳定 | ✅ M2 |
| **Agent** | tool call loop + 多轮 | ✅ M1 |
| | Esc 中断不污染 history | ✅ M4.5 + Repair |
| | 不变量检查覆盖 4 条孤儿路径 | ✅ M5 |
| **工具** | read/write/edit/grep/bash/list_dir | ✅ M2 |
| | FIM + think | ✅ M3 |
| | per-call 审批 + diff 预览 | ✅ M4.5 / M5 |
| **TUI** | inline 模式 + bubbletea | ✅ M4 |
| | 自动滚动尊重用户意图 | ✅ M4 |
| | 流式计时 + token 预算 + 60/75 阈值 | ✅ M5 / M6 |
| **会话** | persistence + JSONL + Repair | ✅ M5.1 |
| | /branch + /compact 保留完整历史 | ✅ M5.2 |
| **Skill / 项目** | Skill 4+1 层 + 内置 builtin | ✅ M5.3 |
| | AGENTS.md 自动加载 | ✅ M5.3 |
| **MCP** | client + stdio + 动态注册 | ✅ M5.4 |
| **多 provider** | Anthropic / OpenAI / Gemini | ✅ M6 |
| | compatible 端点（vLLM / Ollama） | ✅ M6 |
| | CI lint 强制分层 | ✅ M6 |
| **M7** | JSON 输出模式 | ✅ M7（`dd5db84`） |
| | 自举：用 seek 改 seek | ✅ 进行中（`1467be8` 等） |
| | `/help` 浮动 overlay + `?` 热键 | ✅ M7 polish（`b479187`） |
| | `/new` 替代 `/reset`（自动保存） | ✅ M7 polish（`393d6b7`） |
| | `--theme` flag + light palette | ✅ M7 polish（`56b9df8`） |
| | `go install` + 版本号 | ✅ 当前 |
| **跨平台** | 三 OS CI 全绿 | ✅ 全程 |

`v1.0` 不远了。剩下的是文档（包括这本书）+ 一次正式 tag。

---

## 15.8 v1.0 之后:这本书没停, 只是换了个版本号

v1.0 不是终点, 是节奏的换挡。第 15.7 节那份验收清单全绿之后, seek 在不到半年里走完了两个独立的大版本:

- **v0.2.x — 三层认知记忆子系统**(M5.0–M5.8, PRD v1)。L/M/S 三层架构、`memory_recall` / `memory_remember` 工具、`/distill` 蒸馏、`seek -dream` 做梦、自动化的 S→M / M→L 流水线。**第 16 章**讲完整故事。
- **v0.3.x — Skill 生命周期管理**(M8.0–M8.7, PRD v2)。目录包对齐 Anthropic Agent Skills、`seek skill install/uninstall/update/list/status/stats/create` 子命令族、`.install.json` sidecar、调用统计 `.stats.jsonl`、TUI `/skill` 镜像。**第 17 章**讲完整故事。

这两条线没有任何一条出现在原 PRD v0 §6 的验收清单里——v1.0 验收只覆盖了"agent 能用"的最小集合。v0.2 和 v0.3 是 seek 自举到一定规模后, 作者自己用着觉得"差点意思", 才反推出来的。这跟 §15.6 讲过的"M7 polish 是自举驱动的"是同一回事, 只是规模放大了:**当工具够好用以后, 用户(包括作者自己)开始提的需求会换一个层级**——不再是"这个按键能不能改"或者"状态栏漏算了几分钱", 而是"我下次还要不要再说一遍我的代码风格偏好"、"我给同事推荐 skill 时, 让他抄哪几个文件、扔到哪个目录?"。

这两类需求的共同点是:**它们都是关于"工具如何延续"的, 不是关于"工具如何运行"的**。v0 解决了运行;v1/v2 解决了延续。

### 现在你读到这里, 意味着什么

走完了 seek 从一行 Go 代码到 v0.3.x 的整个过程。15 章原作 + 两章续作(16、17) + 前言 + 附录, 每一章对应至少一个真实的 commit、一段真实的 bug、一组真实的取舍。

我希望你带走的不是任何一个具体的 API 选择（那些都会随着 DeepSeek / Anthropic / OpenAI 协议演进而过时），而是**做这件事时的工作姿态**：

- **每一个看起来神奇的能力，背后都是几十个具体决定的累积**。Claude Code 不是魔法，seek 也不是。它就是 SSE 解析 + 工具循环 + 不变量检查 + 缓存优化 + UI 细节，每件做对一点点，加起来变成"挺好用"。
- **测试针对状态形状，不是触发原因**。bug 修一次，测试覆盖 N 种产生同样症状的路径，未来回归就不需要再被同一类问题咬。
- **跨边界的契约要硬执行**。`pkg/deepseek` 不 import `pkg/llm` 用一行 CI grep 物化；不可印字符用 `\uXXXX` 转义不用复制粘贴；状态栏数字必须追到唯一数据源——边界一旦软，复杂度就会从那里渗进来。
- **诚实大于神秘**。状态栏给真实的成本，banner 公开降级，错误信息附带恢复方法——工具的可信度由"你看到的数是真的"累积出来。
- **简单先于灵活**。在所有可能的"加抽象 / 等需求出现再做 / 一次性写死"中间，永远选最简单那个，直到你已经看到三个具体场景同时需要它。提前做的抽象总是错的。
- **自举是终极的测试**。把你的工具用在它自己的开发上——还在意的所有细节都会浮上来，作者会立刻动手修。

代码会过时。架构决策会被新场景挑战。但"如何思考软件"这件事——稳定得多。

---

### 相关踩坑

发布与跨平台中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. `VersionString()` 是格式化横幅，不是原始版本号**

- **Saw**：`seek -upgrade` 用 `VersionString()` 做版本比较，横幅包含 logo 装饰，比较逻辑错误。
- **Fix**：添加 `RawVersion()` 返回纯净语义版本号，两者职责分离。

**2. `seek -upgrade` 报 "permission denied" 而非 sudo 提示**

- **Saw**：用户执行 `seek -upgrade` 时看到原始的"permission denied"错误，不知道需要 sudo。
- **Fix**：检测到写 `/usr/local/bin/` 等系统路径失败时，给出明确的"需要 sudo 或指定自定义路径"提示。

**3. 原子自替换需要同文件系统临时文件**

- **Saw**：自升级的 `os.Rename(tmp, target)` 在不同挂载点间失败。
- **Fix**：在目标文件相同目录创建临时文件。

**4. `go run ./cmd/seek` 慢到感觉像坏了**

- **Saw**：`go run` 每次重新编译整个二进制，初次启动 >10 秒。
- **Lesson**：`go install` 后直接执行是正确用法，`go run` 仅用于开发调试。

**5. macOS bash 3.2 缺少 `mapfile`**

- **Saw**：构建脚本在 macOS 上失败，因为 bash 3.2 没有 `mapfile`/`readarray` 命令。
- **Fix**：使用兼容 POSIX 的循环替代。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- `-json` 模式让 seek 能跑在脚本里；事件类型字符串是 stable contract
- 一个 `jsonLine` envelope 承载所有事件类型 + omitempty——比 polymorphic encoding 对 `jq` 更友好
- stdout 走 JSON，stderr 走 warning——unix 工具基本合同
- 自举（用 seek 写 seek）是终极测试。写测试发现不了的边角问题，让 seek 自己干活时全冒出来
- `go install` + 单二进制是从第 1 章开始就埋伏的承诺——零外部依赖、零 cgo、`go:embed` 自带资源、`FixedZone` 不要 tzdata，加起来兑现
- 版本号用 `runtime/debug.BuildInfo`，自动捕获 git revision + dirty 标记；把读 BuildInfo 和格式化字符串分开，纯函数可测
- 跨平台细节：windows 用 `APPDATA`、unix 用 XDG、`time.FixedZone` 不依赖 tzdata、`TrimRight(\r)` 是按行处理的肌肉记忆
- M7 polish（浮动 help / `/new` / `--theme`）三件小事都源于自举撞坑——再次印证 §15.2 的论点：自举不是验收测试，是产品需求来源
- v1.0 验收清单几乎全绿；剩下的是文档收口 + 正式 tag

---

*第 1–15 章对应 commit:`dd5db84`(M7 JSON 模式)、`1467be8`(首次自举:README 同步)、`5b23e55`(pitfalls 回填)、`b479187`(`/help` overlay + `?` 热键)、`393d6b7`(`/new` 替代 `/reset`)、`56b9df8`(`--theme` flag)。第 16、17 章覆盖 v0.2.x / v0.3.x 的两套大版本——继续读下去, 整个项目的最新进度始终是 `git log --oneline | head -20` + `docs/prd/` 目录。*
