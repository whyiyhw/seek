# 第 18 章：Plan Mode 与交互工具

> 对应代码：`internal/tools/propose/`、`internal/tools/git/`、`internal/skill/builtin/plan-mode.md`、`pkg/agent/`(plan events)
> 起点：第 15 章。模型有一整套工具（read / write / edit / bash / grep），但所有操作都在"收到用户消息 → 模型回复 → 执行工具 → 继续"的单一循环里。第 17 章给了 CLI 级别的 skill 安装，但 agent 的交互模式没变。
> 终点：模型可以主动提议方案、用户审批后再执行、中途用选择器消歧义、用只读 git 查询历史而不触发权限提示。

---

前 17 章我们关注的是"agent 能做什么"——读文件、写代码、跑命令、查记忆、装 skill。到了 Plan Mode，问题变成：**agent 的工作方式是什么**。

在 M8 之前，seek 只有一种工作模式：

> 用户输入 → 模型回复(可能调工具) → 继续

这叫"自由对话模式"(ask mode)。它灵活、低摩擦、适合探索和快速修复。但当你让 agent 做一个变更涉及多个文件、可能影响生产环境、或者需要先理解历史再做决定时，自由对话模式的三个问题就浮现了：

1. **没有"事前确认"**——模型想好就开始写，写完你才看到结果。如果方向错了，浪费一次推理
2. **没有"只读区间"**——你想让模型"先看看代码再决定改不改"，但它看的过程中随时可以调用 write/edit/bash（只要它认为有必要），而用户只能在事后叫停
3. **没有"消歧义工具"**——模型不确定"你说的 auth 模块是指 middleware 还是 user service"时，只能猜。猜错了，继续写，错得更远

Plan Mode 不是加一个新工具——它是一整套交互模式的升级。

---

## 18.1 从"单向 toggle"到"分析→提案→执行→报告"闭环

Plan mode 最初只是 `/plan` 的一个 toggle：打开后让模型"更谨慎、更多推理"。它本质上是自由对话模式的变体——没有结构性改变。

Plan Mode 把它重构成一个**带显式确认门的工作流**：

```
/plan on
   │
   ▼
┌─────────────────────────────────────────────────────┐
│ ANALYZE 子态 (只读)                                    │
│ • read / grep / list_dir / git / think / ask_user     │
│ • 怀疑上下文不够 → ask_user 澄清                       │
│ • 够了 → propose(problem, steps, why_now?) ────────┐  │
└─────────────────────────────────────────────────────┘  │
                                                         │
                    propose 工具内部弹 picker             │
                    options = approve / adjust / cancel   │
                                                         │
  ┌─────────────────────────────┬──────────────────┐      │
  │ approve                     │ adjust           │cancel│
  ▼                             ▼                  ▼      │
┌──────────────────┐   ┌────────────────┐   ┌────────────┐│
│ EXECUTE 子态      │   │ 回 ANALYZE     │   │ /plan off  ││
│ 写入解锁          │   │ 用户补充了意见  │   │            ││
│ • 执行 plan 步骤  │   │ 重新分析       │   │            ││
│ • 边执行边报告    │   │ 再 propose     │   │            ││
└──────────────────┘   └────────────────┘   └────────────┘
```

三个关键设计决策：

**1. ANALYZE 是只写不读的禁区。** 在分析子态下，`write`、`edit`、`bash` 全部被 permission policy 拒绝。模型只能在 plan-analyze 里读。这保证"先理解再行动"不是靠模型自觉，而是靠系统强制。

**2. adjust 不丢已完成的工作。** 如果用户的 feedback 是"改一下第三步，其他不变"，模型不需要重新 propose 全部步骤。它只需要在已有的方案上调整、解释 diff，然后继续审批流程。这个设计在 PRD 里叫"从中断点重构，不从头重来"。

**3. 提交流程由 propose 工具驱动，不是 /plan。** `/plan on` 只进入分析态，不会自动 propose。模型需要自己判断什么时候上下文足够了。`/plan off` 在任何子态都可用——取消后回到自由对话模式，已经执行的部分不受影响。

### 字节稳定性原则的延伸

第 6 章（M3 — think 工具）讲过"完全隔离历史"的设计。Plan mode 继承同一原则：

- **analyze 期间的所有工具结果**不会被写入持久化的 session 文件——它们是"思考的脚手架"，不是"和用户的对话"
- 只有当用户 approve 后进入 execute 子态，工具调用才会正常记录
- 这让 resume 行为可预测：resume 一个 plan-mode 会话时，不会重放之前分析阶段的所有 read/grep 结果

这跟第 16 章（M5 — 记忆）里的 snapshot+delta 策略是同一个姿势：**区分"给模型看的 context"和"需要持久化的对话历史"**。

---

## 18.2 分岔点：为什么 plan-mode 需要自解析，而不是靠工具返回值

最开始的实现尝试过另一种路径：让 propose 工具的返回值包含"用户是否 approve"的信息，agent 循环根据返回值决定下一步。

问题在于：**agent 循环是同步的**。propose 工具返回结果后，模型必须立即决定下一步。但用户可能需要补上下文、改步骤、甚至暂时离开——模型无法在同一个"turn"里等用户。

最终方案：**propose 工具内部弹一个 TUI 选择器，返回值异步到达**。propose 返回时只有"用户选择了什么"的信息，不携带"下一步是什么"的指令。模型根据返回值自己决定：

```
返回值 { chosen: "approve" }  → 模型: "用户批准了，开始执行"
返回值 { chosen: "adjust" }   → 模型: "用户要调整，重读 feedback"
返回值 { chosen: "cancel" }   → 模型: "用户取消了，退回自由对话"
```

这个"工具返回数据，模型做决策"的模式跟 ask_user 工具一模一样——工具只负责收集用户意图的机械部分，解释交由模型完成。这保持了 agent 循环的单纯性：它不需要理解"approve"和"cancel"的区别，只需要把选择结果投递给模型。

---

## 18.3 `propose` 工具：problem + steps + TUI 选择器

实现上，propose 跟 ask_user 共享同一个底层选择器（`internal/askuser.Question`）。它的 schema 有三个必要字段：

```json
{
  "properties": {
    "problem": { "type": "string" },
    "steps": {
      "type": "array",
      "minItems": 1,
      "maxItems": 12,
      "items": { "type": "string", "maxLength": 200 }
    },
    "why_now": { "type": "string" }
  }
}
```

`problem` 必须是一段自包含的问题描述——用户在选择器里看到它时，可能已经忘了前一轮的对话。steps 有 200 字符硬上限，防止模型把整篇设计文档塞进一个步骤。

### 18.3.1 在 plan-execute 子态下禁止 filesystem 和 shell 写入

执行态不是"想做什么就做什么"——它只是在**已批准的步骤范围内**解锁写入。超出范围的操作会被拒绝：

```go
// internal/permission/permission.go
case ModePlan:
    if substate != "execute" {
        return Deny("plan mode: writes are locked until the user approves a plan")
    }
    // 执行态：检查工具是否在已批准的步骤列表里
```

但检查"工具调用是否对应已批准的步骤"在工程上几乎不可能——步骤是自然语言描述，工具调用是具体操作，模糊匹配要么太松（漏放危险操作），要么太紧（频繁误杀）。最终实现选择了**更简单但不完美**的方案：执行态解锁全部写入，但每步调用仍然有独立的 y/N 审批。用户的眼睛是最后的过滤器。

### 18.3.2 adjust：用户说"改"时不丢掉已完成的工作

当用户在 propose 选择器里选 "adjust"，可以选择输入 feedback。模型收到 feedback 后回到 ANALYZE 子态，**但之前已经获得的所有 context（read / grep / git 结果）仍然在 history 里**。它不需要重新 grep 一遍文件，只需要根据 feedback 修改方案，然后再次调用 propose。

这个"保留脚手架，重提方案"的模式跟用户在自由对话中说"不，换个方向"本质上一样——只是形式上更结构化了。

---

## 18.4 `git` 工具：只读 git wrapper + plan-mode 豁免

在引入 propose 工具的同时，一个更底层的问题暴露出来：**plan-analyze 需要查 git 历史，但 bash 工具在 plan mode 下被拒绝**（而且即使在 ask mode，每次 bash 调用都会弹出 y/N 审批，影响效率）。

解决方案是一个专门的 git 工具，只做一件事：**安全地执行只读 git 命令**。

```go
// Subcommand allowlist（18 个）
log, diff, show, status, blame, branch, tag, rev-parse,
ls-files, ls-tree, cat-file, shortlog, describe, reflog
// 例外：ls-remote（只读网络）
```

设计上做了四层防护：

1. **无 shell**：`exec.Command("git", subcommand, args...)`，参数是 slice 不是字符串，无法注入
2. **子命令白名单**：push、commit、reset、checkout、rebase、merge、clean、fetch、pull、clone — 全部拒绝
3. **参数黑名单**：即使子命令在白名单里，某些参数也能造成破坏——`git -c core.sshCommand='rm -rf' log` 用 `-c` 覆盖了 git 配置。`-c`、`--exec`、`--upload-pack`、`--git-dir`、`--work-tree`、`--output`、`-C` 等全部在黑名单里
4. **输出上限**：500 行硬封顶。默认 100 行，模型可以降低但不能提高

### 18.4.1 为什么不用 bash git：安全 + plan-mode 可执行

最直接的问题是：**bash 拒绝 / 审批窗口无法区分只读和写入操作**。`bash("git log")` 和 `bash("git push")` 在 bash 工具看来都是"执行一个字符串"。要让 bash 安全地支持只读 git，需要解析命令字符串去判断「是不是 git + 是不是只读」——这比在 git 工具里做同样的事复杂、脆弱得多。

第二个问题是用户体验：即使在 ask mode，`bash "git log -n 5"` 也会弹一个 y/N 审批窗口。git 工具注册的 `KindGit` 权限在 ask mode 下自动通过，不弹审批——因为安全已经在工具实现层保证了。

### 18.4.2 覆盖本地子命令与网络只读 (`ls-remote`)

`ls-remote` 是唯一被允许的网络操作。它枚举远程仓库的 refs（分支、tag）而不 fetch 任何数据——纯读操作。

这看起来是个小决定，但实际上覆盖了一个真实的用户场景：**"看看上游有没有新 tag"**。在没有 git 工具时，用户要么手动浏览器打开 GitHub，要么冒着撞审批墙的风险用 bash。现在模型可以直接：

```
git("ls-remote", ["--tags", "origin"]) → 返回 tag 列表
```

跟使用本地子命令完全一样的接口。`fetch` / `pull` 仍然被拒绝——它们修改本地 refs 或工作目录，需要显式用户确认走 bash 路径。

---

## 18.5 `ask_user` 工具：内联 TUI 选择器

`propose` 工具解决的是"结构性确认"——用户审批一整个方案。但 agent 的工作流里还有很多**小范围的歧义**：2 到 4 个选项，选错了方向就要重做，但还没大到需要一个完整的 propose 流程。

`ask_user` 就是为这个间隙设计的。

```json
{
  "question": "分支合并策略？",
  "options": [
    { "id": "rebase",  "label": "Rebase",      "description": "线性历史" },
    { "id": "merge",   "label": "Merge commit", "description": "保留分支拓扑" }
  ]
}
```

它跟 propose 共享同一个底层 TUI 选择器，但语义不同：
- **propose**：提交一个计划，等待结构性审批（approve / adjust / cancel）
- **ask_user**：提交 N 个选项，让用户指一条路（单/多选 + free-text 兜底）

### 18.5.1 三个适用条件 vs 与 permission 提示的职责边界

这是整个 Plan Mode 里最容易被误用的边界，值得用表格说明：

| 场景 | 应该用 |
|---|---|
| "装到 user 还是 project 级？" | **ask_user** — 2 选 1，方向影响了后续行为 |
| "我可以跑这个命令吗？" | **permission 审批** — 这是工具调用权限，不是方向选择 |
| "用 sync 还是 async 实现？" | **ask_user** — 两种可行方案，选错了要重做 |
| "能修改 production 数据库吗？" | **permission 审批** — 这是危险操作确认 |
| "先修 bug 还是先加 feature？" | **ask_user** — 优先级决策 |
| "这个方法应该叫什么名字？" | **都不是** — 直接问用户即可，不需要选择器 |

三条硬规则：

1. **选择成本高**：方向选错了会产生实质性回滚成本（改代码、跑命令、写文件）
2. **选项可列举**：2-4 个互斥选项，不是开放性问题
3. **模型确实不知道**：从现有 context（代码、git log、prior conversation）无法推断

如果以上三条任意一条不满足，用自由文本询问或直接猜测要比弹选择器好。

### 18.5.2 当用户取消：模型的最佳猜测 + 明确声明

用户不一定总会选——有时他们直接按 Esc 退出了选择器。这时 `ask_user` 返回 `cancelled=true`。

模型的行为准则（写在 plan-mode 内置 skill 里）：

> 当 `cancelled=true`，用你的最佳判断继续，**但在第一条回复里明确声明你的假设**："I'll use X — say so if you want Y"

不重问，不卡死。用户取消一定有原因——可能是觉得"模型应该自己知道"、可能是点错了、也可能是暂时不想决策。无论哪种，模型都应该有默认行为并告知用户。

---

## 18.6 plan-mode 内置 skill：提示自动注入

plan-mode 不是"代码里的一个开关"，它是一个**注入到模型 system prompt 里的一套行为准则**。这套准则写成了 seek 的一个内置 skill：`internal/skill/builtin/plan-mode.md`。

每当用户打开 `/plan`，这个 skill 的内容就被注入到 system prompt 里。内容包括：

- plan-analyze / plan-execute 子态的说明
- 什么时候用 propose vs ask_user
- 工具调用在各子态下的限制
- 如何处理 user adjust（保留已完成的工作）
- 当 propose / ask_user 被取消时的 fallback 行为

关闭 `/plan` 时，这部分 prompt 被移除——模型回到自由对话模式的行为基线。

**为什么不像普通 skill 一样用 `Skill(name=plan-mode)`？** 因为 plan-mode 需要在**每个 turn** 都生效——它不是"特定任务时才调用"的指令集，而是"只要工作模式切换了就一直生效"的规则。这跟 `AGENTS.md` 的行为更接近：按需注入、全周期有效。

---

## 18.7 `/review` 与 per-message 模式提醒

和 plan mode 配套的是两个辅助功能，目标一致：**让用户对"模型将要做什么"有更多的可视性和控制**。

### `/review` 快捷命令

在自由对话模式下，如果模型刚刚执行了一系列工具调用、修改了文件，但用户还不确定变更的内容，可以用 `/review` 触发一次"总结已做变更"的循环——模型会自动调用 git diff / read 来汇总已修改的文件，并请求用户确认是否继续。

它本质上是一个"轻量级的 propose"：没有 analyze 子态，没有结构性确认门，但给用户一个在"自由对话→继续加大投入"之间插入检查点的机会。

### Per-message 模式提醒

每个用户消息的末尾自动追加一行模式提醒：

- 自由对话（ask mode）：无提醒（基线行为）
- plan-analyze：`[Mode: plan-analyze — read context to define the problem and design a solution. Call propose(problem, steps) when you have enough context; use ask_user to clarify ambiguity. No writes until the user approves a plan.]`
- plan-execute：`[Mode: plan-execute — execute the approved plan. Narrate progress in chat. Writes are unlocked but each tool call prompts for permission.]`

提醒在 TUI 里用灰色显示，不干扰消息阅读。新用户第一次进入 plan mode 时，这个提醒同时充当了教学提示——解释了当前子态的约束和下一步该做什么。

---

## 18.8 一个观察：确认门改变了 agent 和用户之间的权力结构

Plan Mode 之前，seek 的权力结构是：

> 用户发号施令 → 模型执行 → 用户审查结果

这是一个"事后审查"模型。用户只有在模型操作完成后才能看到发生了什么。如果方向错了，已经浪费了一次（甚至多次）推理。

Plan Mode 之后变成：

> 模型提议 → 用户确认 → 模型执行 → 用户审查

这是一个"事前确认"模型。用户不再是被动的审查者——他们在模型行动的**准备阶段**就参与了决策。propose 工具里的 "adjust" 选项更是让用户可以"在方向错误之前纠正"，而不是在方向错误之后抱怨。

**这个变化对 prompt 设计的影响远超预期**：

- 模型的写作风格变了。在 plan mode 下，模型倾向于更结构化、更精确地描述"我打算做什么"。不再是"让我看看……然后……"，而是"问题：XXX。步骤：1. 2. 3. 理由是：XXX。"
- 用户的行为也变了。当他们知道模型会先提方案再执行时，他们会更倾向于给出"方向性"的反馈——"改第三步"、"去掉第二步"——而不是反馈自由对话里常见的"好，继续"。
- 幻觉的影响降低了。plan mode 下模型把"思考"和"执行"分开了。在 analyze 阶段，模型可以自由推理——调用 think、调用 git、读文件——而不需要担心"推理过程中不小心写了一个错误的 edit"。执行阶段的路线图已经过用户审批，幻觉被限制在"路线图上的具体实现"而不是"路线的方向"本身。

这些影响不是我在实现之前就预见的——它们是发布后从用户的真实使用模式里观察到的。这大概是 Plan Mode 最没有争议的一条观察：**工具不仅改变了用户能做什么，也改变了他们选择做什么**。

---

### 相关踩坑

Plan Mode 实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. `[plan: approved]` 是承重线格式前缀**

- **Why**：`[plan: approved]` 这个字符串看起来像日志文本，但它是 `propose` 工具和 `plan` 重构器（`reconstruct.go`）共享的线格式契约。`seek -resume` 时系统扫描会话历史中的这个前缀来重建 plan 状态。
- **Lesson**：一旦某个结果前缀有了解析器，它就是线格式。修改它（如改为 `[plan: approved batch]`）会无声破坏 `--resume` 的 plan 状态重建。**新变体必须加在关闭标记之后**（`[plan: approved] (auto-approve-per-step)`），永远不能改标记本身。

**2. Plan artifact write 需要 Sink.Approved 前的上下文**

- **Saw**：`propose` Sink 的 `Approved()` 触发时需要当前执行上下文（如 projectID、sessionID）来写 plan artifact，但 Sink 接口没有传递这些信息。
- **Fix**：新增可选的 `ContextReceiver` sibling 接口——`Approved` 之前先调 `SetContext(ctx)` 注入上下文。不破坏现有的 Sink 接口签名（sibling interface 模式）。

**3. 审批 callback 两端都需要 ctx-aware select**

- **Saw**：审批回调在 channel 上阻塞等待用户回复时，用户按 Esc 取消，发送端和接收端都可能卡住。
- **Fix**：发送端 `select { case ch <- v: ... case <-ctx.Done(): ... }`，接收端同理。两端同步处理取消。

**4. `/help` 文档声明的 `?` 热键未实现**

- **Saw**：`/help` overlay 的文档里写着"按 ? 快速打开帮助"，但 `?` 键没有对应的 handler。
- **Fix**：在 key handler 中添加对 `?` 的识别，与 Ctrl+H 行为一致。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- plan mode 从"单向 toggle"重构为 analyze → propose → execute → report 闭环，每个子态有明确的读写限制
- **propose 工具**提交 problem + steps 给用户审批，approve/adjust/cancel 三种结果；adjust 保留已有 context 不需要重来
- **git 工具**是只读 git wrapper，四层防护（无 shell / 子命令白名单 / 参数黑名单 / 输出上限），plan mode 下可用且不弹审批
- **ask_user 工具**解决 2-4 选项的小范围歧义，与 permission 审批有明确的职责边界；取消时模型猜测并声明
- plan-mode 内置 skill 在 `/plan` 打开时自动注入、关闭时移除——跟 AGENTS.md 同样的"按需注入、全周期生效"模式
- `/review` 提供自由对话中的轻量检查点；per-message 模式提醒让用户随时知道当前子态的约束
- **确认门把用户从"事后审查者"变成"事前参与者"**——改变了 prompt 风格、用户行为、以及幻觉的影响半径

---

*对应 commit：`31eb8a8`(propose 工具 + analyze/execute substate)、`9ca7f6c`(plan-mode 内置 skill)、`5ec12ab`(plan-mode v2 PRD)、`f3a0105`(git 工具初始实现)、`2e0b485`(git ls-remote 例外)、`b270f9e`(ask_user 工具)、`a2c14ae`(ask_user 使用指南扩展)、`57f17c7`(/review 快捷命令 + per-message 模式提醒)。运行 `go test -race ./internal/tools/propose/... ./internal/tools/git/... ./internal/askuser/... ./pkg/agent/...` 验证。详 PRD 见 `docs/prd/feature-plan-mode.md`。*

---

**[下一章 → 第 19 章：M9.0–M9.1 — 双层 Checkpoint 安全网](chapter-19.md)** *(v0.4.0)* — 在 Plan Mode 的确认门基础上，给 `--yolo` 用户一个可撤销的安全网。
