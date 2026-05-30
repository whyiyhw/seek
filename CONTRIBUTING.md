# Contributing to seek

> Languages: 中文 · [English](#english)

无论你是人类贡献者还是 AI 助理，欢迎参与 seek 的开发。这份指南会帮你理解项目的运作方式、约定和期望。

---

## 目录

1. [项目气质](#1-项目气质)
2. [快速开始](#2-快速开始)
3. [理解代码库](#3-理解代码库)
4. [贡献类型](#4-贡献类型)
5. [工作流](#5-工作流)
6. [代码约定](#6-代码约定)
7. [AI 协助](#7-ai-协助)
8. [沟通](#8-沟通)

---

## 1. 项目气质

seek 的 README 说它是「Claude Code 的工作流，DeepSeek 的价格」。那是面向用户说的。在代码层面，seek 更准确的定位是：

**一个以 LLM 为核心的操作系统外壳的实验。**

这不是又一个聊天封装。这个项目有：

- **PRD 优先的设计文化**——每个版本先写设计文档，再写代码。`docs/prd/` 下从 v0 到 v6 的文档不仅是记录，更是蓝图。
- **字节确定的 prefix-cache 纪律**——DeepSeek 的 10 倍缓存折扣依赖于历史消息的逐字节稳定，这意味着每行代码都要考虑它对缓存命中率的影响。
- **明确的架构边界**——`pkg/deepseek` 不得导入 `pkg/llm`，CI 会检查。skill 文件系统写操作只允许在 `internal/skillmgr` 和 `internal/skillstats`。这些边界不是可选的。
- **测试失败路径，不只是快乐路径**——happy-path-only 的覆盖度是虚假信心。

如果你提交的 PR 改了一个 `const description` 来改善工具选择行为，却**没有**先跑一次 eval 给出前后对比数据，那是会被打回的。如果你添加了一个新工具却没有测试它的取消路径、malformed 输入、并发安全，那也是会被打回的。

这不是严苛——这是项目存活的方式。一个人维护 66 个 Go 包、~85k 行代码的工程，只能靠纪律撑住。

### 你应该读的文件

在提交 PR 之前，请先读这些文件——它们是项目的共享上下文：

- [`AGENTS.md`](AGENTS.md) —— AI 助理的项目指南（人类也应该读）
- [`docs/pitfalls.md`](docs/pitfalls.md) —— 181KB 的经验教训，**每次修非明显 bug 都要追加**
- [`docs/prd/v0.md`](docs/prd/v0.md) §1–4 —— 核心设计决策
- [`docs/prd/`](docs/prd/) 下你改动的对应版本 PRD
- 相关 feature PRD（如果改的是 permission，读 `feature-permission-refactor.md`；改的是 plan mode，读 `feature-plan-mode.md`）

---

## 2. 快速开始

### 前置依赖

- Go 1.25+（你需要 go.mod 里面那个版本或更新的）
- Make（非必需，但 `go test ./...` 你必须会跑）

### 构建

```bash
# 从项目根目录
go build ./cmd/seek
./seek
```

### 测试

```bash
# 跑所有测试（CI 会跑 -race）
go test ./...

# 带竞态检测
go test -race ./...

# 看覆盖度
go test -cover ./...

# 跑特定包
go test ./pkg/deepseek/...

# 跑 eval（需要 DeepSeek API key）
eval/run.sh
eval/run.sh tool-args-hallucination   # 单个 case
```

测试不需要 API key——它们用 `httptest` 伪造 DeepSeek 后端。只有 eval 框架需要真实的 API key。

### 项目布局

```
cmd/seek/          # main 包，CLI 入口
pkg/
  deepseek/        # DeepSeek API 一等人客户端（零外部依赖）
  llm/             # 通用 LLM 接口（二等人 provider）
  agent/           # agent 循环
  mcp/             # MCP 客户端基础设施
internal/
  tools/           # 每个工具一个目录（read/write/edit/bash/git/…）
  permission/      # 双轴权限模型
  session/         # 会话 JSONL 存储
  skill/           # Skill 加载器（只读）
  skillmgr/        # Skill 安装/卸载/更新（唯一允许写 skill 目录的包）
  skillstats/      # Skill 调用统计
  skillcli/        # Skill CLI/TUI 共享调度
  memory/          # 三层记忆子系统
  tui/             # Bubble Tea TUI
  config/          # 用户配置
  checkpoint/      # 文件级 undo/redo
  subagent/        # 子代理 + worktree 隔离
  routines/        # cron/wakeup/triggers
  hooks/           # shell hooks 框架
  suggest/         # next-turn 预测
  askuser/         # TUI picker
  …                # 66 个包，见 go list ./...
docs/
  prd/             # 每个版本 + 每个 feature 的设计文档
  book/            # 系统设计书
  pitfalls.md      # 非明显 bug 和学习记录
eval/              # 行为比较测试框架
```

---

## 3. 理解代码库

### 核心架构原则

1. **DeepSeek 一等人，其他 provider 二等人**。`pkg/deepseek` 有 DeepSeek 专属字段（缓存元数据、FIM 端点、reasoner content）。`pkg/llm` 泛化接口只暴露公共子集。`pkg/deepseek` **不得**导入 `pkg/llm`。

2. **Prefix-cache 字节确定性**。这是 seek 定价优势的技术基础。每个 `tool_call` 的 schema 是 `[]byte` 常量（不是运行时构建）。工具输出在写入端截断（不在发送端）。历史消息从不压缩或重写。任何破坏这些保证的改动都会直接伤害用户的钱包。

3. **权限两轴模型（Pref × Workflow）**。`PrefDeny/Ask/Yolo`（用户立场）× `WorkflowNone/PlanAnalyze/PlanExecute`（当前仪式）。Workflow **永远** trump Pref——`PrefYolo + WorkflowPlanAnalyze` 仍然是只读的。改权限逻辑前先读 `internal/permission/permission.go`。

4. **状态从 transcript 重建，而非平行状态文件**。Plan 状态机是典范：`propose` 参数 + `plan(start|complete|skip)` 调用在 transcript 里是唯一事实来源。`seek -resume` 通过 `plan/reconstruct.go` 重放它们。不要加平行状态文件——你会被 drift 折磨死。唯一例外是 artifact 文件（`~/.seek/projects/<id>/plans/`），它们是**一次性写入的人可读快照**，不是状态。

5. **Sink 接口：不要改主契约，加可选兄弟接口**。当新功能需要更多上下文进入工具的 Sink 时，不要给现有 Sink 方法加参数（每个 fake/recording sink 都会坏）。定义一个兄弟接口并在调用点向上转型。见 AGENTS.md §Sink interfaces。

### 阅读顺序

如果你是第一次看 seek 的代码：

1. `cmd/seek/main.go` —— 入口点
2. `pkg/deepseek/client.go` —— DeepSeek 客户端
3. `pkg/agent/agent.go` —— agent 循环
4. `internal/permission/permission.go` —— 权限模型
5. `internal/tools/<某个工具>/` —— 比如 `read/` 或 `bash/`
6. `docs/pitfalls.md` —— 按分类浏览，了解曾经出过什么问题

### 关键代码约定

来自 AGENTS.md，这里重复最重要的几条：

- **新工具**放 `internal/tools/<name>/`，`New(...)` 构造函数。可变异文件系统或 shell 的注入 `*permission.Policy`。需要用户选择的注入 `*askuser.Policy`。
- **Tool JSON schema 是包级 `[]byte` 常量**，不是运行时构建。
- **Schema 不得暴露让模型绕过输出大小限制的参数**。`read` 的 `limit` 上限 50，`grep` 的 `max_matches` 默认 20，`propose` 的 `steps` 最多 20。
- **Wire-format 字符串是契约**。`[plan: approved]` 这个格式被 `reconstruct.go` 解析，加内容只能在闭合 token 之后（`[plan: approved] (auto-approve-per-step)`），不能在它里面。

---

## 4. 贡献类型

### 🐛 Bug 修复

**门槛**：你复现了它，你找到了根因，你写了测试来防止回归。

如果修复的是非明显 bug（你花了超过一分钟才搞明白的），**必须**：
1. 在 `docs/pitfalls.md` 追加一条记录（用文件顶部的模板）
2. commit message 加 `Pitfall: <一句话总结>` trailer

`scripts/extract-pitfalls.sh` 会检查 commit 的 Pitfall trailer 和 `docs/pitfalls.md` 是否一致——不一致会被 CI 标记（计划中）。

### ✨ 新功能

**门槛**：先写 PRD，再写代码。

seek 有一个 PRD 驱动的设计文化。在新功能（尤其涉及 3+ 文件、新包、新工具或行为变更的）开始编码之前：

1. 读相关已有的 PRD（如果涉及 plan mode，先读 `feature-plan-mode.md`）
2. 要么在已有的 umbrella PRD（如 `v6.md`）里增补一节，要么起一个新的 feature PRD
3. 在 PRD 里记录设计决策、被排除的替代方案、跨设计约束
4. 获得维护者的点头再开始编码

这不意味着你要写长篇大论——一个 `feature-new-thing.md` 可以只有 2-3 页。重要的是**为什么这么选**的记录。

**为什么这么麻烦**：因为 seek 是一个人在维护的复杂系统。如果 PR 提交时才第一次讨论设计，那已经太晚了。PRD 是让讨论在合适的时间发生的容器——编码之前。

### 🧪 测试

这是目前**贡献价值最高**的区域。

项目有 ~143 个测试文件对 ~6000 个 .go 文件，比例偏低。特别需要：

- **取消路径测试** —— 任何 `ctx` 感知函数都需要一个 `ctx.Done` 测试
- **中断测试** —— 流/事件循环在部分状态下被切断
- **malformed 输入测试** —— LLM 输出的 JSON、部分 SSE、缺失必需字段
- **并发测试** —— 如果函数可以被并发调用，`go test -race` 必须通过
- **持久化往返测试** —— 写入、重载、**并验证损坏状态修复**

一个好的 starter PR：为一个已有 suite 但缺少 failure-path 覆盖的工具加测试。见 `docs/pitfalls.md` 第一条——`commit 986a485` 那个 bug 就是因为没有取消路径测试而在 CI 下绿了几周。

### 📖 文档

- **pitfalls.md** —— 如果你修了一个非明显 bug 却**没有**追加记录，你的 PR 会被打回
- **PRD** —— 纠正事实错误、补充遗漏的设计权衡
- **指南** —— `docs/guide-*.md` 系列，修复流程或跨平台问题

### 📊 Eval case

Eval 框架（`eval/`）是 seek 衡量模型行为随时间变化的方法。贡献 eval case 性价比极高：

1. 在 `eval/cases/<name>/` 下创建 `README.md`（说明在测什么）、`prompt.txt`、`expect.json`
2. 跑 `eval/run.sh <name>` 拿到基线结果
3. PR 包含 `prompt.txt`、`expect.json` 和基线结果文件

好的 eval case 源于真实 bug——`tool-args-hallucination` 这个 case 就来自一个具体的、发生过的错误工具调用。

---

## 5. 工作流

### 分支策略

- `main` 是稳定分支，始终可构建、测试通过
- 功能分支从 `main` 分出，命名风格：`feat/<短描述>` 或 `fix/<短描述>`
- PR 目标为 `main`

### commit 约定

```
<type>(<scope>): <简短描述>

<正文（解释为什么，而不是什么——diff 已经展示了什么）>

<可选 trailer>
```

**type**：`feat` `fix` `refactor` `test` `docs` `chore` `style` `perf`

**scope**：包名或功能区域，如 `tui` `deepseek` `agent` `skill` `session` `bubbletea`

**trailer**：
- `Co-Authored-By: seek (DeepSeek) <service@deepseek.com>` —— AI 写的代码必须加这个
- `Pitfall: <一句话总结>` —— 修复非明显 bug 时加

示例：
```
fix(checkpoint): unique temp file names to fix concurrent blob write race

Two goroutines hashing the same content both wrote to a fixed .tmp
path, then the loser's rename failed because the file was already gone.
Switch to os.CreateTemp so every writer gets a unique scratch file.

Pitfall: content-addressed blob storage needs unique tmp per writer
Co-Authored-By: seek (DeepSeek) <service@deepseek.com>
```

### PR 流程

1. 创建 PR 前，跑 `go test -race ./...` 确认全绿
2. PR 描述说明：**问题 / 方案 / 测试方法**
3. 如果改变涉及行为变更，附上 eval 前后对比数据
4. 维护者会在合理时间内 review
5. 反馈循环：改代码 → rebase → 重新请求 review

### Review 期望

- **变更范围越小越好**。一个 PR 做一件事。重构和新功能不要在同一个 PR。
- **测试先于代码**。如果是修复 bug，先写暴露 bug 的测试，再修复。
- **维护者可以要求你拆分 PR**。如果 PR 做了三件独立的事，会被要求拆成三个。

---

## 6. 代码约定

### Go

- **Stdlib first**。`pkg/deepseek` 零外部依赖。新依赖要放在合理的边界后面。
- **`pkg/deepseek` 不得导入 `pkg/llm`**。CI 检查。
- **错误处理**：不要吞错误。`crypto/rand.Read` 返回的错误不能静默丢弃。文件操作后必须先 `f.Sync()` 再 `f.Close()`。
- **路径安全**：必须用 `EvalSymlinks` 解析符号链接，`filepath.Abs` 不够。
- **文件权限**：覆盖文件前用 `os.Stat` 读取原权限，不要硬编码 `0o644`。
- **测试考虑 Windows**：路径断言不能硬编码 `/`，必须考虑 `\`。

### 工具

- 每个工具一个目录：`internal/tools/<name>/`
- `const description` 是**最高杠杆的行为塑形手段**——它在每个 API 请求里，模型必须读才能构造工具调用。改描述前先起一个 eval case。
- 工具的输出在写入端限幅，不在发送端。
- Schema 必须拒绝运行时构建——用 `[]byte` 常量。

### 文档

- `docs/pitfalls.md` 是**非明显 bug 的唯一真相来源**。修了就要写。
- PRD 是设计容器，不是事后笔记。在新功能编码前创建或更新。
- 所有文档的 line wrap 用 ~80 列（英文）或自然段（中文）。

---

## 7. AI 协助

这个项目**大量使用 AI 协助开发**。大部分 commit 带有 `Co-Authored-By: seek (DeepSeek)` trailer。这不是问题——这是项目工作方式的一部分。

### 如果你是人类贡献者

你可以在开发中自由使用 AI 工具。请注意：

- AI 生成的代码也要遵循项目的测试标准——failure path 覆盖、race detection、cross-platform
- AI 无法理解 prefix-cache 字节确定性——你需要 review 生成的代码，确保没有引入运行时构建的 schema 或输出端后处理
- 如果你用 AI 写 commit message，确保它遵循 conventional-commit 格式和 trailer 约定
- 鼓励 AI 助理在非明显修复后追加 `docs/pitfalls.md`——它们往往比你更记得住要写

### 如果你是 AI 助理

你的系统提示里有完整的 `AGENTS.md`。那里有更详细的工具使用工作流。关键点：

- 编码前先读相关代码——用 `grep` 定位，`read(offset)` 读窗口，不要整文件读
- 改 `edit` 前先 `read` 目标行获取精确空白
- 修非明显 bug 后：追加 `docs/pitfalls.md` + commit 加 `Pitfall:` trailer
- `pkg/deepseek` 不能导入 `pkg/llm`——不要在建议中违反这个约束
- 权限拒绝不是错误——它们是工具结果。不要绕过这个流程。

---

## 8. 沟通

- **Issue / PR** 用中文或英文都可以——维护者双语
- **问题**：先在 `docs/pitfalls.md` 和已有的 issue 里搜一下
- **功能请求**：准备好讨论设计权衡，不只是提需求。建议附带一个简短的 PRD 草稿
- **IRL**：维护者在 UTC+8，回复可能在 24 小时内

---

### 行为准则

[`LICENSE`](LICENSE) 是 MIT，社区规范是：**be excellent to each other**。你不必同意所有的设计决策（意见分歧是非常受欢迎的），但请保持尊重。这个项目是一砖一瓦垒起来的——攻击它就是在攻击一个人长时间的劳动。

---

## English

*For non-Chinese readers: the full contributing guide above is bilingual. This section is a minimal English summary pointing you to the right places.*

### Quick start

```bash
go build ./cmd/seek
go test -race ./...
```

### Key files to read before contributing

- [`AGENTS.md`](AGENTS.md) — project guide for AI assistants (humans should read too)
- [`docs/pitfalls.md`](docs/pitfalls.md) — institutional memory, 181KB of hard-won lessons
- [`docs/prd/`](docs/prd/) — design docs, version by version
- [`docs/prd/feature-permission-refactor.md`](docs/prd/feature-permission-refactor.md) if you touch permission gating
- [`docs/prd/feature-plan-mode.md`](docs/prd/feature-plan-mode.md) if you touch the plan subsystem

### What the project values

1. **PRD-first design** — write the design doc before the code, not after
2. **Prefix-cache byte determinism** — never rewrite history, never build schemas at runtime
3. **Failure-path tests** — cancellation, mid-loop interruption, malformed input, concurrent access, persistence round-trip
4. **Pitfall recording** — non-obvious bug fix = `docs/pitfalls.md` entry + `Pitfall:` commit trailer
5. **Sink interface stability** — sibling interfaces, not signature changes

### How to contribute

| Type | Bar | Process |
|------|-----|---------|
| Bug fix | Replicate → root cause → regression test | Fix + pitfall entry + trailer |
| Feature | PRD first | Design doc → maintainer nod → code |
| Tests | Cover failure paths | Add to existing suite or create new |
| Eval case | Measurable behaviour | `eval/cases/<name>/{README,prompt,expect}` |
| Docs | Accurate, concise | PR with the fix |

### Commit format

```
<type>(<scope>): <subject>

<body — why, not what>

Co-Authored-By: seek (DeepSeek) <service@deepseek.com>  # if AI-written
Pitfall: <summary>  # if fixing a non-obvious bug
```

### Communication

- Issues and PRs welcome in Chinese or English
- Search `docs/pitfalls.md` and existing issues before filing duplicates
- Feature requests should come with design thinking, not just a wishlist
- Maintainer is in UTC+8; responses typically within 24h
- MIT license, **be excellent to each other**
