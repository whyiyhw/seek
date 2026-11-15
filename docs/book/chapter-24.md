# 第 24 章：v7 四柱 — 打擂台 Reasonix（N · O · P · Q）

> **对应版本**：v0.8.x
> **对应代码**：`internal/autopilot/`、`internal/sandbox/`、`internal/acp/`、`internal/ocr/`、`cmd/seek/main.go`（acp/autopilot dispatch）、`internal/tools/bash/`（sandbox integration）、`internal/subagent/`（buildSubagentRunner sandbox wiring）、`scripts/build-vision-ocr.sh`。§24.7 增补：`internal/controlplane/`（frontend / router / executor / serve / webfrontend）。
> **PRD**：[`docs/prd/v7.md`](../prd/v7.md) · `docs/prd/feature-{autopilot,sandbox,acp,image-ocr}.md` · [`docs/comparison.md`](../comparison.md) §R
> **验收**：四柱全部落地。N autopilot 真环境 e2e（修复两个隔离 bug）；O 双平台 seatbelt + landlock CI 验证；P ACP stdio e2e；Q 真二进制 OCR e2e
> **起点**：第 23 章（v6 五柱）。v6 把 P1 单点缺口全部补齐后，seek 与 Claude Code 的能力对标已经不再是主线任务——新的竞争对手出现在雷达上：Reasonix，一个同样基于 DeepSeek、同样用 Go 写的编程 agent。

---

## 24.1 战略转折：从追赶到防守

v0–v6 的每一版都有一个清晰的回答："我们还缺什么？Claude Code 有什么 we 还没有？"

v7 的回答变了。

读完 Reasonix 的 `main-v2` 源码后，一个事实浮出水面：Reasonix 和 seek 是**同论题孪生竞品**——都是 DeepSeek 原生的 Go 编程 agent、都支持 ACP、都有子代理、checkpoint、macOS 沙箱、后台任务。在 10 多项能力的逐项对比中，大部分是"≈ 对等"，少数是"Reasonix 赢"（规模、web 前端、语义索引），唯独两轴 seek 是**结构上领先**的：

1. **并行 worktree 子代理**：Reasonix 的子代理是**串行**且**无工作目录隔离**的（源码注释明确说 "keeps the parallel-dispatch path from running two sub-agents at once…writes race"）
2. **时序自治**：Reasonix 源码中没有任何 cron、wakeup、trigger、schedule 相关代码

v7 的战略是：**把这两轴焊成 Reasonix 做不出的成品**，再补上唯一明确落后的 IDE 集成（ACP），顺手加一个低成本的正交能力（OCR）。

```
v7 推力
  A. 深化护城河（把唯一领先焊成成品）
     柱 N Autopilot  —— 无人值守编排，Reasonix 没有
     柱 O Sandbox    —— 两平台内核级 jail，安全性反超
  B. 补触达（追平 IDE 集成）
     柱 P ACP        —— 标准协议，追平而非反超
  C. 扩能力（低风险高用处）
     柱 Q OCR        —— 离线本地文字识别，正交能力
```

---

## 24.2 柱 N — Autopilot：无人值守编排交付

### 24.2.1 Problem

v5 已经让 seek 可以 spawn 并行子代理（柱 G）和定时触发任务（柱 H）。但这两个能力是**分立**的——模型不能简单地说"重构 User 模型"然后让系统自己去拆解、分配、执行、汇总。

Autopilot 就是那个缺失的编排层：接收一个自然语言目标 → 自动分解 → 并行 worktree fleet → 聚合 → 本地 commit → 报告摘要。

### 24.2.2 架构

```
"重构 User 模型，添加邮箱验证"
     │
     ▼
  Decomposer（DeepSeek V4-Flash）
     │  将目标分解为具体子任务列表
     ▼
  Fleet（并行 worktree 子代理）
     ├─ worktree-1: 修改 User 结构体 + 迁移
     ├─ worktree-2: 添加 verifyEmail 方法
     ├─ worktree-3: 编写测试
     └─ worktree-4: 更新文档
     │
     ▼
  Aggregator → 报告（含每个任务的 commit SHA）
```

核心设计决策：**控制流在 Go，模型只做分解和干活**。autopilot 的分解/分发/聚合循环是确定性的 Go 代码，不是模型驱动的自由编排。在无人值守的场景下，没有人纠偏——控制流必须可复现。

### 24.2.3 安全：默认保守

Autopilot 的默认安全姿态是 v7 最重要的设计约束：

| 保护层 | 机制 |
|--------|------|
| Worktree 隔离 | 每个子代理在独立的 git worktree 中，与主树物理隔离 |
| No-remote 守卫 | 所有子代理的 bash 工具默认拒绝 `git push` / `git remote` 等远程操作 |
| OS 沙箱 | macOS seatbelt / Linux landlock 将子进程限制在 worktree 目录内 |
| Per-task commit | 每个子任务产生本地 commit，不自动 push |
| 无默认远程操作 | 不会自动开 PR，`--open-pr` 是显式 opt-in |

这些不是 launch 后才补的安全——它们是设计时就在代码里的。`buildSubagentRunner` 在构造每个子代理的 tool 实例时就注入了 no-remote guard 和 sandbox，而不是事后靠配置。

### 24.2.4 e2e 暴露的两个真 bug

Autopilot 的真环境 e2e 测试（让 autopilot 在真仓库上跑"append 一行到 README"）暴露了两个隔离 bug，是本书反复强调"测试要测真实路径"的完美范例：

1. **Worktree 隔离洞**——子代理的 `edit`/`write` 误写了**主树**的 README，尽管报告声称落在 worktree 内。根因是子代理复用了**父进程的 tool 实例**（内含父 policy，CWD 指向主树），`read`/`edit`/`write` 用 `filepath.Clean(a.Path)` 解析相对路径——完全不读 `policy.CWD()`。修复了两层：`Policy.Resolve(path)` 让相对路径锚定到 policy 的 CWD，`buildSubagentRunner` 用 child policy 重建 tool 实例。

2. **Dirty worktree**——fleet 中的子代理改完代码后未提交，导致主 repo 看到的是零改动。修复：fleet 在聚合前确定性 per-task 本地 commit（commit msg `autopilot: <title>`），报告显示每个任务的短 SHA。

### 24.2.5 CLI 与 cron 集成

```
seek autopilot run "重构 User 模型"    # 一次性运行

seek cron create --name nightly-refactor \
  --at @daily \
  --autopilot "清理过期 TODO 注释"       # 定时自动跑
```

cron 集成复用 v5 柱 H 的调度基础设施——autopilot 不是一个新调度器，只是 `seek cron tick` 拉起 `seek -p "goal"` 时的一个执行模式。

---

## 24.3 柱 O — OS 沙箱（Seatbelt / Landlock）

### 24.3.1 为什么是 OS 沙箱，不是容器

柱 O 和"用 Docker 隔离子进程"是完全不同的两件事。

Docker 要求 `dockerd` 常驻、要求用户有 docker 权限、要求 image pull——它把 seek 从"单二进制"变成了"需要容器运行时"。这违反了 seek 最核心的架构承诺。

柱 O 用的是**操作系统原生的内核 jail 机制**：

- **macOS**：`sandbox-exec`（Seatbelt），通过 SBPL（Seatbelt Profile Language）生成策略
- **Linux**：`Landlock`（Linux Security Module），通过 re-exec trampoline 实现

两者都是**零运行时依赖**——系统内核已经提供了这些能力，seek 只需要调用它们。单二进制不变。

```
容器化              vs      OS 沙箱
需要 dockerd                零 daemon
需要 docker 权限            内核能力，无需额外权限
image pull 几百 MB          单二进制不变
白名单路径                   内核级限制，不可绕过
```

### 24.3.2 macOS：Seatbelt

Seatbelt 是 macOS 的内核级强制访问控制（MAC），通过 `sandbox-exec` 命令使用。SBPL 策略文件描述了什么允许、什么禁止。

```
sandbox-exec -p <SBPL profile> /bin/sh -c "rm -rf /etc"
                                      │
                                      ▼
                                seatbelt 内核模块
                          read+exec ✅ | write: 仅 worktree + /tmp
                                        | network: ❌（默认）
```

柱 O 的 `sandbox.Argv` 生成 SBPL 策略并 prepend `sandbox-exec -p <profile>`。策略规则顺序很重要——allow-all 打底 → deny writes → re-allow write dirs → deny network。

Seatbelt 被 Apple 标为 deprecated，但功能完好且不需要 cgo（seek 是 `CGO_ENABLED=0`）。这是选择 shell out 而非 `sandbox_init` C API 的原因。

### 24.3.3 Linux：Landlock Trampoline

Landlock 是 Linux 从 5.13 开始提供的无特权沙箱机制。它比 seatbelt 更"安全"但也更受限——Landlock 只能限制当前进程，而且一旦应用就**不可逆转**（no_untrusted）。

柱 O 的实现是**re-exec trampoline**：

```
1. seek detect Landlock ABI ≥ 1
2. seek re-exec 自身，带特殊 argv（trampoline 标记）
3. trampoline 设置 no_new_privs
4. landlock_create_ruleset()
5. landlock_restrict_self()
   → 一旦成功，进程不可逆转
6. exec 目标命令（/bin/sh -c "rm -rf /etc"）
   → Landlock 拒绝写 /etc
```

关键设计：**fail-closed**。如果 landlock 创建失败（内核太老、规则冲突），进程 exit 1 而不是降级执行。这意味着一个脆弱的沙箱配置不会静默退化成无保护——要么 jail 成功，要么命令不跑。

Landlock ABI 版本处理是另一个注意点：

```go
abi := landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)
switch {
case abi >= 5: mask |= IOCTL_DEV
case abi >= 3: mask |= TRUNCATE
case abi >= 2: mask |= REFER
}
// ABI 1: 基础 FS 写权限
```

不同内核版本支持不同的权限集，硬编码所有最新权限会在旧内核上导致 `EINVAL`。ABI 版本掩码是必须的。

### 24.3.4 与 autopilot 的集成

柱 O 与柱 N 的集成是自动的——autopilot 模式下，`buildSubagentRunner` 检测 `sandbox.Available()` 并为每个子代理的 bash 工具注入 sandbox：

```go
if opts.bashSandbox {
    if wt := job.Policy.CWD(); wt != "" && sandbox.Available() {
        bt = bt.WithSandbox(sandbox.Options{WritableDirs: []string{wt}})
    }
}
```

子代理的 bash 被限制在 worktree 目录内读写，网络也默认禁止（仅 macOS）。

### 24.3.5 验收

commit `4d44490`（初始实现）+ `c21167d`（landlock + 集成修复）。测试覆盖了 macOS seatbelt profile 生成、Linux landlock ABI 探测 + 降级 + trampoline，以及 CI 上的 landlock 运行时验证（内核不支持时自动 skip）。

---

## 24.4 柱 P — ACP 编辑器集成

### 24.4.1 为什么是 ACP

列到 v6 结束时，seek 已经有了 `seek -rpc` JSON-RPC 2.0 server 模式——可以接受 IDE 的请求。但那是**自定协议**，没有工具链支持。

ACP（Agent Client Protocol, agentclientprotocol.com）是**标准协议**——Zed、VS Code 等编辑器用它驱动 AI agent。Reasonix 已经支持 ACP。柱 P 就是"seek 也说 ACP"。

> 柱 P 是**追平项**——它不创造新差异（ACP 不是技术壁垒），但消除一个"别人有、你没有"的缺口。定位是"补触达"。

### 24.4.2 实现：适配层，不是新 agent

柱 P 的关键设计约束是**不碰 agent 内核**。ACP 是一组 JSON-RPC 2.0 方法（`initialize`、`session/new`、`session/prompt`、`session/cancel`、`session/update`），seek 已经有了完整的 agent 循环——ACP 只是**把 ACP 协议调用翻译成 agent.Prompt 调用**。

架构：

```
Zed（ACP client）
    │ JSON-RPC 2.0 over stdio
    ▼
internal/acp/server.go
    │ dispatch: initialize → session/new → session/prompt
    ▼
cmd/seek/main.go — acpBackend
    │ acpBackend.Prompt → ag.Prompt(ctx, text)
    ▼
pkg/agent — 已有的 agent 循环
```

`acpBackend` 只有大约 30 行有效代码：

```go
func (b *acpBackend) Prompt(ctx context.Context, p acp.PromptParams, sendU func(acp.SessionUpdate)) (acp.PromptResult, error) {
    text := p.PromptText()
    if text == "" {
        return acp.PromptResult{StopReason: "end_turn"}, nil
    }
    for ev := range b.ag.Prompt(ctx, text) {
        if e, ok := ev.(agent.ErrorEvent); ok {
            return acp.PromptResult{}, e.Err
        }
        if u, ok := acpUpdate(p.SessionID, ev); ok {
            sendU(u)
        }
    }
    return acp.PromptResult{StopReason: "end_turn"}, nil
}
```

`acpUpdate` 把 agent 的事件流（`AgentStart`、`TurnStart`、`TextDelta`、`ToolCall`、`ToolResult`）映射为 ACP 的 `agent_message_chunk` 和 `tool` 事件。未映射的事件（reasoning chunks、turn 书签）静默跳过。

### 24.4.3 通信协议

ACP 的传输是 **newline-delimited JSON-RPC 2.0**——和 seek 已有的 MCP client（`pkg/mcp`）完全相同。不是 HTTP、不是 WebSocket，就是 `json.Encoder`/`Decoder` 加上 `\n` 分隔。这决定了 `seek acp` 就是一个直接读写 stdin/stdout 的进程，用户（或编辑器）把它作为一个子进程启动。

### 24.4.4 限制（MVP）

柱 P 作为 MVP 交付，有几项已知限制：

- 无审批门——ACP 的 `request_permission` 方法还没有路由到 `ask_user`
- 无 session resume——每次 `seek acp` 启动一个干净 session，不能继续之前的
- 无 slash 命令——`/plan`、`/review` 等 TUI 命令在 ACP 模式下不可用（见第 23 章 §23.7，这些命令是 TUI 层的）
- 短暂存：`seek acp` 进程存活期间 session 就在，退出后就没了

这些限制都是可解的（每一件都是一个中等 PR），但 MVP 的核心交付——握手、流式回复、工具调用渲染——已经跑通并在真 Zed 上验证过。

### 24.4.5 验收

commit `4d44490`。验收的关键不是模拟测试（虽然测试覆盖很全），而是一个**脚本化的真 server e2e**：一个 shell 脚本以 newline-JSON-RPC 客户端身份，驱动真 `seek acp` + 真 agent，完整走了一遍 initialize → session/new → session/prompt（流式收到 `agent_message_chunk`）→ `end_turn` → session/cancel（不崩）→ EOF 干净退出。

---

## 24.5 柱 Q — 离线图片 OCR

### 24.5.1 问题

seek 是纯文本模型。但用户在日常工作中截图的场景太多了：报错截图、设计稿标注、白板草图、PDF 中的代码片段。每次遇到这些场景，用户都得手动把图中的文字敲出来。

柱 Q 让 seek 能在**离线、本地、无网络**的情况下读取图片中的文字。不是 VLM、不是云 OCR，就是本地引擎。

### 24.5.2 "引用图片"的交互模式

用户不需要任何特殊命令——只要在 prompt 中放入图片路径：

```
你：修复 @error.png 中的报错

    ↓ seek 检测到图片引用 → 自动 OCR

模型看到的：修复 @error.png 中的报错

[image: error.png — OCR]
TypeError: Cannot read properties of undefined (reading 'map')
    at renderList (components/List.tsx:42)
[/image: error.png]
```

OCR 是**静默附加**的——原始 prompt 不变，识别结果作为文本块追加到末尾。模型看到"图片路径 + OCR 结果"后同时理解了上下文和图片内容。

### 24.5.3 三种 OCR 引擎

```
macOS:   Apple Vision API（内嵌 Swift 源，go:embed + 首次自动编译）
           ↓ 零配置开箱即用，识别完成后缓存到 ~/.seek/cache/

Linux:   ocr.command（如 tesseract）
           ↓ 需要用户安装 + 配置

Windows: ocr.command
           ↓ 需要用户安装 + 配置
```

macOS 实现上最不寻常：`internal/ocr/vision_ocr.swift` 是一个完整的 Swift 程序（Apple Vision API 调用），通过 `go:embed` 编译进 seek 二进制。用户在 macOS 上首次遇到图片引用时，seek 会在后台调用 `swiftc` 编译这个 Swift 源文件，缓存结果到 `~/.seek/cache/vision_ocr`。以后不再编译。

这意味着 seek 的安装路径仍然是 `tar -xz seek`——不需要 bundle 额外文件，不需要 macOS release runner，goreleaser 打包方案被 go:embed 完全取代。

### 24.5.4 防假阳性

图片引用的检测不是简单的正则查找 `.png` 子串——那样会在代码中的字符串或注释里误触发：

```go
// DetectImageRefs 检查每个 token 是否同时满足：
// 1. 有图片扩展名（.png / .jpg / .webp / .heic / …）
// 2. os.Stat 确认是真实文件
// 两者缺一不可
```

如果用户在 prompt 中提到了 `icon.png` 但这个文件在当前目录下不存在，OCR 不会触发。"@符号"前缀是为了方便——`@error.png` 比 `error.png` 更显眼，但两种形式都支持。

### 24.5.5 架构注入点

OCR 的注入点选在 `Agent.Prompt` 的**统一预处理器**——`internal/ocr` 是一个独立的纯函数包，不依赖 agent、不依赖 TUI、不依赖 provider：

```
用户输入文本
     │
     ▼
  PrePromptHook chain
     ├─ memory injection
     ├─ context blocks
     └─ OCR Expand ← 在这里发生
     │
     ▼
  Agent.appendMessage → API call
```

这意味着 OCR **在所有模式下都工作**：TUI、`-p` inline、`seek acp`、cron 子进程。且完全离线，不增加 API 调用次数。

### 24.5.6 验收

commit `4d44490` + `c21167d`。真二进制 e2e 验证了中英混排的 OCR 识别。测试覆盖了图片检测（含假阳性过滤）、输出格式、以及 Apple Vision API 调用路径。

---

## 24.6 一个观察：如何在一个"已追上"的市场保持领先

v7 的标题"打擂台 Reasonix"揭示了一个残酷的事实：编程 agent 这个领域已经不是一个"追赶 Claude Code"的市场了——它是**同质化竞争**的市场。

Reasonix 证明了：基于 DeepSeek + Go 的编程 agent 是可以被复制的。它也有工具调用、有子代理、有 checkpoint、有 ACP、有 macOS 沙箱。在超过 10 项能力的逐项对比中，大部分是"对等"。

在两三个领域里，seek 还没有"赢"——Reasonix 有生产级 web 前端、有语义索引（`codegraph`）、有更大的测试规模。

但 seek 在两个Reasonix根本做不了的事情上保持了领先：**并行 worktree 子代理**和**时序自治**。这不是"做得更好"的领先——这是 Reasonix 架构选择决定了它在这两轴上无法追赶的领先。

v7 把这两轴做成了产品：
- Autopilot 不是"子代理"的另一个名字——它是"无人值守时也能自主工作"的产品承诺
- Sandbox 不是"隔离"的另一个名字——它是"睡觉时也能信任 seek 改代码"的产品承诺

这句话可能概括了 v7 的心态：**当对手能在功能表上复制你时，唯一的护城河是架构选择——而那些选择必须在产品层面被显式表达出来**。Autopilot 和 Sandbox 不是功能，它们是产品陈述。

---

## 24.7 增补（v0.11）：远程控制平面——Frontend 端口与执行器

24.6 承认过 seek 还没赢的领域里有"生产级 web 前端"。v0.11 平台化线交出的答案是**远程控制平面**：一个常驻的 `seek serve` 进程，把"远程触达 + 常驻在场 + 跨项目调度"做成 frontend-无关的核心——任何聊天渠道或浏览器，只要实现一个三方法的 `Frontend` 端口，就能驱动 seek。

先诚实地划边界（这是设计的第一课）：平面**不扩展 seek 能干什么，它扩展 seek 从哪被驱动**。中枢感来自触达，不来自新权能——`pkg/agent`、agent loop、cron 一字不动，平面驱动 seek 的方式只有一种：exec 它。

### Frontend 端口：收/发分离，不假设双工

所有渠道的收发机制各不相同，而且**天然半双工**：飞书长连接只承载下行事件推送、回消息走独立 HTTPS REST；Telegram long-poll 收、bot API 发；网页 SSE 收、HTTP POST 发。"核心逻辑一份、渠道接入多份"的形态天生是一个端口 + 多个适配器——但抽象的度是刻意收紧的：

```go
type Frontend interface {
    Name() string
    Run(ctx context.Context, in chan<- Inbound) error    // 阻塞收，自管传输+重连
    Send(ctx context.Context, conv ConvID, r Reply) error // 发一条回复回该会话
}
```

三个方法 + 两个结构体。v1 只实现一个 frontend，第二个只做**纸面验证**——把"Telegram frontend 怎么落"写成一段设计文字，写得顺 = 接缝成立，写不顺 = 端口抽错了。这是抽象的验收测试，比先写第二个适配器便宜一个量级。

这个接缝后来被兑现了两次。第一次：用户拍板撤销飞书排期——不绑定具体渠道，Frontend 端口已是资产，渠道选型开放。第二次：**首个真实渠道换成了 web**——纯 stdlib `net/http` + `go:embed` 单页，连 build tag 都不需要（依赖隔离门 D-F1 存在的理由是"frontend SDK 别进默认构建"，而 `net/http` 就是 stdlib）。`ConvID` 带 `<frontend>:` 前缀（如 `web:default`），绑定表与串行队列天然跨渠道不撞键。

### 执行器：子进程 + 稳定 JSONL 契约，自愈是平面的责任

执行器是这个设计里最"不发明"的部分——它不调任何内部 API，就是：

```
首条:  seek -p '<msg>' --json                 cmd.Dir=<绑定项目目录>
后续:  seek -p '<msg>' --json -resume <sid>
```

第 15 章 §15.1 的 `--json` JSONL 输出在这里从"让脚本能用"升格为**平面与 seek 之间的稳定 wire**：末行 `agent_end` 事件的顶层 `session_id` 字段（schema 注释声明为稳定契约）就是多轮续接的全部所需——平面抓 id、回写 binding、下条带 `-resume`；实测 resume 续跑前缀缓存命中，远程多轮的经济性与本地一致。

坑都在边界上，且全部是实测回填的：

- **坏 id**：`-resume` 失败时 seek 直接 exit 1、stdout 全空、不自愈——**自愈必须是平面自己的责任**（检测非零退出，降级为首条重跑、回写新 id）；一次坏 id 不能卡死会话。
- **mid-run error**：stdout 有事件行但没有 `agent_end`——id 不上 stdout，平面拿不到，只能弃上下文重开（记为已接受边界）。
- **权限**：print 模式的初始 pref 就是 deny（无人可交互审批）——危险动作返回 `ErrDenied` **工具结果**（第 5 章 §5.4 的老朋友），agent 收到后自行调整、run 正常结束。于是远程默认体验是个有趣的倒置：**比本地 TUI 更保守**——能读写项目目录、不能跑任何 shell；per-binding 显式 yolo 是唯一放宽口。
- **并发**：同会话串行（防 transcript 写冲突 + resume 竞态）、跨会话并行、全局并发上限；`/cancel` 故意放在执行器**外面**——取消指令绝不能排在它要杀的那条 run 后面。

bindings（ConvID ↔ 项目路径 ↔ session id）持久化在 `~/.seek/serve/bindings.json`。这**不违反**"agent 运行时状态从誊本重建、不养平行状态文件"的纪律：那条约束管的是 agent/plan 状态——平面的路由表在 agent transcript 之外，本来就没有第二个事实来源，持久化它是诚实而不是漂移。

### 与零-daemon 铁律的正面冲突

第 22 章把"零常驻进程"立为 v5 的铁律（cron 靠 OS scheduler 拉起、跑完即退），而平面**必须有进程一直在**（保活连接 / 监听端口）。解法不是重释铁律，而是**隔离偏离**：常驻只活在显式 `seek serve` 里，核心 seek 零改动。铁律管的是"用户没主动要常驻时不得有常驻"——serve 是用户亲手拉起、亲手退出的进程，显式性与 cron 的 OS 拉起同量级。这个措辞上的让步值得较真：**架构铁律的生命力在于偏离必须被显式声明、被隔离在一个入口里**，而不是永不开口子。

> 平面的决策表（D-C1–D-C5）、消息生命周期与验收口径以 [`docs/prd/feature-remote-control-plane.md`](../prd/feature-remote-control-plane.md) 为权威；web 渠道设计见 [`feature-web-frontend.md`](../prd/feature-web-frontend.md)；运维契约（drain 预算、退出码、launchd/systemd）见 [`docs/serve/README.md`](../serve/README.md)。

---

### 相关踩坑

v7 Autopilot / Sandbox / ACP 实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. Landlock ABI 版本不匹配导致 create_ruleset 返回 EINVAL**

- **Saw**：在旧内核上启动 sandbox 失败。
- **Why**：Landlock LSM 的 ABI 版本不同支持不同的 access right。固定 mask 包含了当前内核不支持的 right。
- **Fix**：运行时查询内核支持的 Landlock ABI 版本，按版本降级 access right mask。

**2. 子进程夺取 /dev/tty——Esc 无法中断**

- **Saw**：sandbox 内的子进程夺取终端控制权，主进程按 Esc 无反应。
- **Fix**：子进程 I/O 通过 pipe 而非共享终端。

**3. Setsid 后孙进程孤儿导致 Wait() 死锁**

- **Saw**：创建新 session 后孙进程成为孤儿，管道写入端不关闭，Wait() 永远阻塞。
- **Fix**：取消时强制 kill 整个进程组。

**4. /proc/PID/children 在 CI 上为空**

- **Saw**：CI 环境无法通过 `/proc/PID/children` 枚举子进程。
- **Fix**：回退到 PPID 扫描。

**5. WSL sudo 逃逸进程组 kill**

- **Saw**：WSL 中 `sudo` 启动的进程不在同一进程组，kill 失败，Wait() 超时。
- **Fix**：使用 `sudo` 配合 `--kill-child` 或直接以非 root 用户运行 sandbox。

**6. 全部文件被 build tag 排除的包，`./...` 静默跳过**（§24.7）

- **Saw**：计划用 `//go:build feishu` 隔离 frontend SDK；实测 `go build/vet/test ./...` 对这种包**静默跳过**，只有直接点名才报错。
- **Fix**：留一个未打 tag 的 `doc.go` 作包锚点 + 用 `go/build.Default.ImportDir` 在普通 CI 测试里断言默认构建的 `GoFiles == [doc.go]`——排除证明本身跑在默认 tags 下。
- **Lesson**：`./...` 绿不等于 tag 门接对了；门要双向证明——默认 tags 排除文件 + `-tags` 下编译运行。

**7. 挂着 SSE handler 时 `http.Server.Shutdown` 永不返回**（§24.7）

- **Saw**：web frontend 关停时 `srv.Shutdown(ctx)` 挂死到 deadline，进程带着活连接僵着。
- **Why**：Shutdown 等"活跃连接转空闲"，而 SSE 连接按设计永远活跃；请求 ctx 由客户端断开触发，不由 Shutdown 触发。
- **Fix**：平面 ctx 结束后直接 `srv.Close()` 强断；流式端点的"优雅退出"要么逐连接记 cancel，要么承认 force-close 才是正确姿势。

**8. ProjectID 哈希的是路径字符串，不是目录**（§24.7）

- **Saw**：同一物理目录经 `/tmp/...` 与 `/private/tmp/...` 两种拼写产生两个 ProjectID——两套分叉的项目状态空间。
- **Fix**：保持纯哈希契约（内部 EvalSymlinks 被否决——那是伪装成修复的身份迁移）；真实车道传规范路径即可。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

> **下一章**：暂无。v7 四柱交付后的平台化线（远程控制平面 / web 前端 / GLM 车道 / skill 蒸馏）已以 v0.11 增补节进入第 14、17 章与本章 §24.7；seek 的下一阶段走向见 [`docs/prd/vision.md`](../prd/vision.md)。
