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

不是"能跑通"的验收，是**写代码**的验收。`README.md` 这次同步到 M6-done state 的版本——是 seek 自己跑出来的（commit `1467be8` "docs(README): sync to M6-done state via first self-hosted seek run"）。这本书的第 8-15 章——也是 seek 自己写的。

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

### 自举测试的副产品：本书

按时间线，事情大致这样发生的：

1. M0-M5 期间，author 自己手写代码、手写 commit message、手写 README
2. M6 完成后，author 决定"今天起，凡是不需要保密的写作，先让 seek 试一遍"
3. seek 跑出来的第一份产物是 `README.md` 的 M6-done 状态同步——大概对了，作者改一改 commit
4. 接下来是 `docs/pitfalls.md` 的回填（commit `5b23e55` "backfill 4 entries from M6 provider work"）
5. 再接下来是本书第 8 章往后所有章节——边写边发现 seek 的不足，比如：
   - "auto-continue 偶尔会在任务完成后多跑一轮无意义的迭代" → commit `aff27da` revert / `0d847b4` restore as opt-in
   - "MaxTurns 默认 32 不够长链调用" → commit `50f7c0e` 提到 200
   - "read 默认 20 行太少" → commit `ad0cf28` 提到 50

每一个修复都来自"我用 seek 的过程中被这件事卡住了"。这种反馈循环是测试套件给不了的——测试验证"代码做了它应该做的"，自举验证"做的事是用户实际想要的"。

### 自举的负面教训

也不是没有发现"不该做"的事情：

- **某些章节让 seek 一气写完会让风格漂浮**——节奏、举例密度、callout 的频率会偏向 LLM 的平均偏好，而不是这本书前几章建立的语气。**对策**：作者每章都过一遍重写关键段，把"AI 标记"洗掉
- **代码风格细节 seek 不擅长保持一致**——比如 commit message 的"why not what"原则在长 PR description 里容易丢
- **seek 不会主动质疑前提**——你告诉它"写第 X 章"，它就写；不会说"等等，第 X 章和第 Y 章其实可以合并"

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

第 7 章讲过的 OSC 11 探针、WindowSizeMsg 合成、KeyMsg 路由——这些都在 macOS Terminal / iTerm2 / GNOME Terminal / Alacritty 上完全一致工作。**Windows 上的 TUI 体验是最弱的**：

- 老的 cmd.exe / PowerShell 不全支持 ANSI 转义
- Windows Terminal 是好的，但用户不一定装了
- 颜色支持 / 鼠标行为 / scrollback 的细节都有偏差

seek 在 Windows 上能跑（CI 验证），但建议用户用 Windows Terminal 或 WSL。这条写在 README 里，比试图把所有 Windows 终端都 polish 到 macOS 体验更现实。

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

## 15.6 v1.0 验收：每条都对得上

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
| **M7** | JSON 输出模式 | ✅ M7（dd5db84） |
| | 自举：用 seek 改 seek | ✅ 进行中 |
| | `go install` + 版本号 | ✅ 当前 |
| **跨平台** | 三 OS CI 全绿 | ✅ 全程 |

`v1.0` 不远了。剩下的是文档（包括这本书）+ 一次正式 tag。

---

## 15.7 这本书是怎么停的

你读到这里，意味着你已经走完了 seek 从一行 Go 代码到 v1.0 候选发布的整个过程。15 章 + 前言 + 附录，每一章对应至少一个真实的 commit、一段真实的 bug、一组真实的取舍。

我希望你带走的不是任何一个具体的 API 选择（那些都会随着 DeepSeek / Anthropic / OpenAI 协议演进而过时），而是**做这件事时的工作姿态**：

- **每一个看起来神奇的能力，背后都是几十个具体决定的累积**。Claude Code 不是魔法，seek 也不是。它就是 SSE 解析 + 工具循环 + 不变量检查 + 缓存优化 + UI 细节，每件做对一点点，加起来变成"挺好用"。
- **测试针对状态形状，不是触发原因**。bug 修一次，测试覆盖 N 种产生同样症状的路径，未来回归就不需要再被同一类问题咬。
- **跨边界的契约要硬执行**。`pkg/deepseek` 不 import `pkg/llm` 用一行 CI grep 物化；不可印字符用 `\uXXXX` 转义不用复制粘贴；状态栏数字必须追到唯一数据源——边界一旦软，复杂度就会从那里渗进来。
- **诚实大于神秘**。状态栏给真实的成本，banner 公开降级，错误信息附带恢复方法——工具的可信度由"你看到的数是真的"累积出来。
- **简单先于灵活**。在所有可能的"加抽象 / 等需求出现再做 / 一次性写死"中间，永远选最简单那个，直到你已经看到三个具体场景同时需要它。提前做的抽象总是错的。
- **自举是终极的测试**。把你的工具用在它自己的开发上——还在意的所有细节都会浮上来，作者会立刻动手修。

代码会过时。架构决策会被新场景挑战。但"如何思考软件"这件事——稳定得多。

---

## 本章小结

- `-json` 模式让 seek 能跑在脚本里；事件类型字符串是 stable contract
- 一个 `jsonLine` envelope 承载所有事件类型 + omitempty——比 polymorphic encoding 对 `jq` 更友好
- stdout 走 JSON，stderr 走 warning——unix 工具基本合同
- 自举（用 seek 写 seek）是终极测试。写测试发现不了的边角问题，让 seek 自己干活时全冒出来
- `go install` + 单二进制是从第 1 章开始就埋伏的承诺——零外部依赖、零 cgo、`go:embed` 自带资源、`FixedZone` 不要 tzdata，加起来兑现
- 版本号用 `runtime/debug.BuildInfo`，自动捕获 git revision + dirty 标记；把读 BuildInfo 和格式化字符串分开，纯函数可测
- 跨平台细节：windows 用 `APPDATA`、unix 用 XDG、`time.FixedZone` 不依赖 tzdata、`TrimRight(\r)` 是按行处理的肌肉记忆
- v1.0 验收清单几乎全绿；剩下的是文档收口 + 正式 tag

---

*这本书到此结束。对应 commit：`dd5db84`（M7 JSON 模式）、`1467be8`（首次自举：README 同步）、`5b23e55`（pitfalls 回填）。整个项目的最新进度：`git log --oneline | head -20`。*
