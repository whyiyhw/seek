# Feature: 复合 code-review skill（v6 柱 J）

**所属版本**：seek v0.7.0 · v6 柱 J 第二项（柱 I AskUserQuestion v2 已 ship）
**前置阅读**：[`v6.md`](v6.md) §3.2 草稿、[`internal/tui/commands.go`](../../internal/tui/commands.go) `cmdReview` + 三个 `*ReviewPrompt` builder（L929–L1080）、[`internal/skill/builtin/plan-mode.md`](../../internal/skill/builtin/plan-mode.md) 内置 skill 范式、[`internal/tools/skilltool/skilltool.go`](../../internal/tools/skilltool/skilltool.go) Skill 工具 schema、[`docs/comparison.md`](../comparison.md) §"开发工作流" 第 250 行 P1 项
**状态**：🚀 已交付（v0.7.0 · v6 柱 J）。内置 `code-review` skill + `/code-review` slash 命令（effort 解析 + `--fix`/`--comment`）+ `/review` = `/code-review quick` 别名。走 D3 推荐路（别名）。新增/迁移测试全过，`internal/{tui,skill,tools/skilltool}` 全 `-race` 绿，全 repo `go test ./...` 绿。
**effort 档位（实测后定稿）**：原设计 4 档（low/medium/high/max），但 D4 的 eval（`eval/cases/code-review-effort-*`）实测 DeepSeek **分不开 4 档**（组内方差吞没组间信号）。**已收敛为 2 档**：`quick`（精确优先，默认）/ `thorough`（穷尽召回）；旧 4 名作软别名映射（low/medium→quick，high/max→thorough），不报错。详见 D4。
**预估工作量**：~2 天（与 v6 §3.2 估时一致）
**实现期修正**：(1) §5.3 — skill-arm 指令烧进注入 prompt，不用 `m.pendingSkill`（程序化提交绕过 `consumeArm`）；(2) §2.1 目标 #6 — 三个 `*ReviewPrompt` builder 被统一为单个 `codeReviewPrompt`，其单测**迁移**为 `TestCodeReviewPrompt_*`（断言原样保留：changes/diff included、no-fix 时含 read-only 指令）；`TestReview_UnknownArgsError` / `TestReviewChoices_*` 原样继续工作。

---

## 1. 真实差距（已校 v6 §3.2 的事实假设）

v6 §3.2 草稿把柱 J 描述成"写一个 `code-review` 内置 skill，由 `/code-review [low|medium|high|max] [--fix] [--comment]` 调用"。读代码后确认，这里有**两条隐含假设与 ground truth 冲突**，必须先纠正再设计：

| v6 草稿假设 | ground truth | 影响 |
|---|---|---|
| skill 由 `/code-review` slash 命令带参数调用 | `Skill` 工具 schema 只有 `{name}`（`skilltool.go:31` `schemaBytes`，`args struct{ Name string }`）；skill 是**模型触发**的——用户敲 `/skill use <name>` 只是把 `m.pendingSkill` 装填，模型在下一轮自行调用 `Skill` 工具读 Body | effort 等级 + 两个 flag **没法走 Skill 工具传进去**。参数必须落在一个 slash 命令上（见决策 D1） |
| seek 现有 `/review` ≈ Claude Code `/code-review` 入门款，新 skill 与它"共存" | `/review`（`cmdReview`，`commands.go:929`）是**直接注入 prompt** 的 slash 命令：3 个 builder（working-tree / branch-diff / fallback），带 branch picker，**完全不走 skill 子系统**，且 prompt 明确 "Do NOT write or edit files"，**不进 plan 模式** | "共存"会变成两个语义重叠的入口。需要决定 `/review` 与 `/code-review` 的关系（D3） |
| `--fix` "走 plan-mode v2 propose + per-step approve" | plan-mode v2（propose / plan 工具 / per-step 预批准）**已 ship**且可复用，但现有 `/review` 是**只读**姿态 | `--fix` 是一次**显式的只读→可写姿态切换**，不是给现有 review prompt 加一行；要设计 review→propose→execute 的衔接（D5） |

**已经就绪、可直接复用的件**（不重造）：
- 内置 skill 框架：`//go:embed builtin/*.md`（`loader.go:17`），新增一个 `.md` 即生效；`Parse(data, "builtin:"+name)`。
- 三个 review prompt builder + `gatherChangedFiles` / `gatherBranchDiff` / `reviewChoices` / branch picker（`commands.go`）——`/code-review` 直接复用这套 diff 采集 + picker UX。
- plan-mode v2 全链路：`propose` 工具、`plan(start|complete|skip)`、per-step 预批准、Esc 撤销、`-resume` 重建。`--fix` **不写新代码**，只是让 review 收尾时走这条已存在的路。
- `bash` 工具（execute 姿态下）跑 `gh pr review`——`--comment` 路径已可用。

## 2. 目标与非目标

### 2.1 目标

1. 一个内置 skill `code-review` 承载**复审方法论**：严重度分类、4 档 effort framing 的语义定义、`--fix` 工作流、`--comment` 工作流、anti-goals。模型 armed 或自主匹配时读它。
2. 一个 `/code-review [low|medium|high|max] [--fix] [--comment] [branch]` slash 命令承载**参数**：解析 effort + flag，复用现有 diff 采集 + picker，注入一段把 effort/flag 固化进去的 prompt，并 arm `code-review` skill。
3. effort 等级语义可感知：`low`/`medium` precision-first（少而高置信）；`high`/`max` recall-first（广覆盖、可含不确定项并标注置信度）。
4. `--fix` 通过**已 ship 的 plan-mode v2**（propose + per-step approve）落地修复，绝不绕过权限门直接写。
5. `--comment` 通过 `gh pr review` 发 inline comment，带 `gh` 可用性 pre-check 与优雅降级。
6. **零破坏**：现有 `/review` 命令 + 它的全部测试（`TestReview_UnknownArgsError` / `TestReviewChoices_*` / `Test*ReviewPrompt*`）继续工作。

### 2.2 非目标

- **不做云端 ultra review**（v6 §3.2 anti-goal；架构选择：seek 是本地工具，对比文档 §127 已记 `/code-review ultra` 为"seek 不计划做"）。
- **不做自定义 lint 规则集**——skill 描述里点明用户应配 `pre-commit` / IDE lint；本 skill 关注 LLM 能发现的语义问题（bug / 安全 / 设计 / 简化）。
- **不扩 `Skill` 工具 schema**（D1）——effort/flag 不进 Skill 工具，避免改动被 prefix-cache 钉死的 `schemaBytes`。
- **不触 `pkg/agent` / `pkg/deepseek` / `internal/permission` 接口**（v6 §2 跨柱约束）——`--fix` 复用现有 plan-mode FSM，不新增 Kind / Workflow。
- **不让 `--fix` 自动写**——必须经 propose 批准 + per-call/per-step y/N，与 plan-execute 现状一致。

## 3. 关键设计决策

### D1 — 参数落在 slash 命令，不扩 Skill 工具

`Skill` 工具 schema 是 package-level `[]byte` 常量（`schemaBytes`），CLAUDE.md "Tool JSON schemas… identical bytes is what lets DeepSeek's prefix cache hit" 把它钉成不可逐调用变形。给它加 `effort`/`fix`/`comment` 字段会：(a) 改动被缓存的 schema 字节；(b) 把 Skill 工具变成 per-skill 多态（每个 skill 都要声明自己的参数），是一次远超"单点工具"的泛化。

**决策**：effort + 两个 flag 由 `/code-review` slash 命令解析，烧进注入的 prompt 文本；Skill 工具保持 `{name}` 不变。这与现有 `/review`（slash 命令注入参数化 prompt）是同一机制，blast radius = `internal/tui`。

### D2 — skill 承载方法论，命令承载参数（职责切分）

| 件 | 放什么 | 为什么 |
|---|---|---|
| `internal/skill/builtin/code-review.md`（skill Body） | 复审方法论：严重度表、4 档 framing 定义、`--fix` 流程、`--comment` 流程、anti-goals | markdown 易调、模型可自主发现、可被 v5 子代理复用（v6 §5 "explore 子代理可加 skill"）。**不**把方法论写死成 Go 字符串 |
| `/code-review` slash 命令 | 解析 effort+flag、复用 diff 采集 + picker、注入"effort=X, fix=bool, comment=bool；按 code-review skill 执行"短 prompt、`m.pendingSkill="code-review"` | 参数是用户每次都不同的输入，必须在命令层解析；Skill 工具传不了（D1） |

反例（要避免的）：把方法论塞进命令的 Go 字符串 builder——那正是现有 `/review` 三个近乎重复的 builder 的味道。方法论进 skill Body，参数进命令，是对齐 seek 架构的诚实切分。

### D3 — `/review` 收敛为 `/code-review medium` 的别名（推荐，可调）

v6 字面说"与 `/review` 共存"，但两个语义重叠的命令会让用户困惑。

**推荐**：保留 `/code-review` 为全功能入口；把 `/review` 实现为 `/code-review medium`（无 flag）的薄别名——**复用同一套 diff 采集 + picker + prompt 注入**。这样：
- 现有 `/review` 的肌肉记忆 + branch picker UX 全保住；
- `TestReview_UnknownArgsError` / `TestReviewChoices_*` / 三个 `*ReviewPrompt` 测试全部继续过（`medium` 路径就是今天的行为）；
- 只多一个命令名 `/code-review`，不是两套并行实现。

**备选**（若 reviewer 否决别名）：`/review` 与 `/code-review` 完全独立并存（v6 字面）。代价：两份 picker/采集逻辑或一层共享 helper + 文档要解释二者差异。**本 PRD 默认走推荐路；这是唯一需要 reviewer 拍板的产品决策，实现时若改主意只动 `cmdReview` 的分发，不影响 skill Body。**

### D4 — effort 等级 = prompt framing，需 dogfood 校准

4 档不是硬门，是 framing：

| 档 | 取向 | framing 关键词（写进 skill Body） |
|---|---|---|
| `low` | precision-first | 只报你高度确信的 bug；压制 speculative；≤ 最关键的几条 |
| `medium`（默认） | balanced | 确信的 bug + 明显的简化/复用机会；标严重度 |
| `high` | recall-first | 广覆盖正确性/安全/性能/设计；可含不确定项，**逐条标 confidence** |
| `max` | exhaustive recall | high + 边界情况 / 并发 / 错误路径 / 测试缺口；宁可多报也别漏，未确认项明确标注 |

**风险**：等级区分度取决于模型，需在 DeepSeek V4（v0.5+）上 dogfood 校准。**遵循 CLAUDE.md "tool description 是最高杠杆 + 先建 eval case"**——本柱进实现前先在 `eval/cases/` 加一个 code-review-effort 案例（同一 diff 跑 low vs max，断言 finding 数量带 + 是否带 confidence 标注的差异），baseline → 调 framing → 对比。

**⚠️ eval 实测结论（2026-05-29，已 supersede 上表）**：建了 `eval/cases/code-review-effort-{low,max}`（字节一致 fixture，仅 effort 句不同），实跑真实 DeepSeek（n=3 各）：

| metric | low | max | per-run 干净分离？ |
|---|---|---|---|
| `completion_tokens` | 1386 / 2065 / **3514** | 2389 / 3174 / 2705 | **否**——区间重叠，low 上界(3514) > max 上界(3174) |
| `review_line_refs` | 0 / 5 / 0 | 4 / 13 / 6 | 弱——max 趋高但仍重叠 |

首对比较（1386 vs 2389，看似干净 1.7×）是**小样本陷阱**：组内方差 swamp 了组间信号。定性读一对：low ~5 条阻断 bug，max ~12 条（含未植入的无界增长 / TOCTOU / 文件类型 / 缺 Len）。

**决策（D4 定稿）**：4 档纯 framing **撑不起清晰梯度**，**收敛为 2 档** `quick`（precision-first，默认）/ `thorough`（exhaustive recall）。旧 4 名软别名映射。若未来要恢复细分，须给 high/max **机械牙齿**（强制 reasoner / 子代理 fan-out）而非靠措辞，再重跑此 eval 期待真分离。eval 案例降级为 **record-only**（无 per-run 分离 bound，否则 ~⅓ flaky），趋势进 `eval/results/`。

### D5 — `--fix` = 只读 review → plan-mode execute 的显式切换

`--fix` **不**改 review 本身的只读姿态。流程：

1. review 照常只读跑（采集 diff、报 findings）。
2. skill Body 指示：若本轮带 `--fix`，把可机械修复的 findings 整理成 `propose(problem, steps)`——steps = 具体修复动作（"Fix nil-deref in handler.go:42" 这类可验证项）。
3. 用户在 propose picker 批准 → 进 `plan-execute` → 每个 `edit`/`write` 仍走 per-call y/N（或用户选了 auto-approve-per-step 则 per-step）。
4. Esc 撤销 → 继承 plan-mode v2 现有处理（撤 pre-approval、回 analyze）。

要点：`--fix` 是把"复审"和"已 ship 的 plan-mode 修复"串起来，**不新增权限机制**。skill Body 明确：不带 `--fix` 时绝不写文件（与今天 `/review` 一致）。

### D6 — `--comment` 带 `gh` pre-check + 优雅降级

skill Body 指示：发 inline comment 前先 `bash command -v gh`（或 `gh auth status`）自检。
- `gh` 不存在 / 未 auth → **不报错中断**，改为：在聊天里输出完整 findings + 一行 "install & auth gh to post inline comments (`gh auth login`)"。
- `gh` 可用 → 走 `gh pr review <PR> --comment -b ...`（execute 姿态 bash，per-call y/N）。

理由：`--comment` 依赖外部 CLI 是软依赖，不该让缺 `gh` 把整个复审废掉——对齐 CLAUDE.md "tool 失败先自恢复，优雅降级"。

## 4. Skill Body 草稿（`internal/skill/builtin/code-review.md`）

> 下面是 skill 的完整初稿；进实现时按 D4 的 dogfood 结果微调 framing 措辞。Body 用英文（与 `plan-mode.md` / `dual-model.md` / `go-test-runner.md` 一致——skill 是模型面文本）。

```markdown
---
name: code-review
description: Review the current diff for correctness bugs and reuse/simplification/efficiency cleanups at the given effort level (low/medium: fewer, high-confidence findings; high/max: broader coverage, may include uncertain findings). Pass --comment to post findings as inline PR comments, or --fix to apply the findings to the working tree after the review. Triggered by /code-review (and its alias /review). Skip for general "explain this code" questions — this is a structured review pass, not a walkthrough.
---

# code-review

Triggered by `/code-review [low|medium|high|max] [--fix] [--comment] [branch]`.
Defaults: `medium`, no `--fix`, no `--comment`, working-tree diff. The slash command
injects the chosen effort + flags into your prompt; read them there, then follow this body.

## What you review

The diff in scope (working-tree changes, or a branch diff if a branch was passed). Look for:
- **Correctness**: bugs, nil/None derefs, off-by-one, error-path mistakes, race conditions.
- **Security**: injection, unvalidated input, secret leakage, SSRF, auth gaps.
- **Design**: wrong abstraction, leaky boundaries, missing tests for failure modes.
- **Cleanups**: reuse over reinvention, simplification, dead code, efficiency.

NOT your job: lint/format nits a linter catches (tell the user to run `pre-commit` / their
IDE linter), or rewriting working code to taste. Focus on what an LLM finds that tools don't.

## Effort levels (precision ↔ recall)

- **low** — precision-first. Report ONLY findings you're highly confident are real. Suppress
  speculation. A handful of the most important issues. Empty result is a valid answer.
- **medium** (default) — balanced. Confident bugs + obvious simplification/reuse wins.
  Categorise each by severity.
- **high** — recall-first. Broad sweep across correctness/security/perf/design. You MAY
  include uncertain findings — but label each with a confidence (high/medium/low) and say
  what would confirm it.
- **max** — exhaustive recall. Everything in `high` plus edge cases, concurrency, error
  paths, and test gaps. Prefer over-reporting to missing something; mark unconfirmed items
  explicitly. Use only when the user asked for `max` — it is noisy by design.

## Output shape

Group findings by severity (Critical / High / Medium / Low / Nit). For each: `file:line`,
one-line description, why it matters, and (high/max only) a confidence tag. End with a
one-line verdict. If the diff is clean at this effort level, say so plainly — don't invent
findings to fill space.

## --fix (apply findings to the working tree)

Only when the prompt says `--fix` is set. Do NOT write files otherwise — review is read-only
by default.

1. Finish the read-only review first; present findings.
2. Turn the mechanically-fixable findings into a `propose(problem, steps)` call — each step a
   concrete, verifiable fix ("Fix nil-deref at handler.go:42", "Extract dup'd parse into
   helper"). Skip findings that need a human judgement call (design rewrites, behaviour
   changes) — list those for the user instead of proposing them.
3. On approval you enter plan-execute; apply fixes step by step. Each edit still prompts y/N
   (or per-step if the user chose auto-approve-per-step). Stay in approved scope — re-propose
   if a fix turns out larger than stated. This is the standard plan-mode loop; see the
   plan-mode skill if you need the full FSM.

## --comment (post inline PR comments)

Only when the prompt says `--comment` is set. Requires the `gh` CLI, authenticated.

1. Pre-check: `bash command -v gh` (and `gh auth status`). If gh is missing or unauthenticated,
   do NOT fail — print the full findings in chat and tell the user: install & run `gh auth login`
   to post inline comments. Then stop.
2. If gh is ready: post findings via `gh pr review <PR> --comment` (one review, body summarises;
   for line-level, use the review API). Confirm what you posted in chat.

## Anti-goals

- No cloud "ultra" multi-agent review — seek is a local tool; that mode is out of scope.
- No custom lint rulesets — that's `pre-commit`/IDE-linter territory.
- Don't auto-write without `--fix`; don't bypass the propose/per-call gate even with `--fix`.
```

## 5. Slash 命令设计（`internal/tui/commands.go`）

### 5.1 注册与解析

```go
// 在 commands 表里新增（与 /review 并列）：
{names: []string{"/code-review"},
 usage: "/code-review [low|medium|high|max] [--fix] [--comment] [branch]",
 description: "Code-review the diff at a chosen effort level. --fix applies findings via plan-mode; --comment posts inline PR comments (needs gh).",
 handler: cmdCodeReview},
```

解析规则（table-test 钉死，见 §6）：
- 第一个非 flag token 若是 `low|medium|high|max` → effort，否则当 branch 名；缺省 effort = `medium`。
- `--fix` / `--comment` 任意顺序、任意位置；未知 flag（`--frob`）→ 返回明确错误，不静默吞掉。
- 未知 effort 拼写（`/code-review hihg`）→ 明确错误 + usage 提示，不静默降级 medium。
- 余下非 flag token 当 branch（复用 `/review` 的 branch-diff 路径）。

### 5.2 复用 diff 采集 + picker（不重造）

`cmdCodeReview` 复用 `reviewChoices` / `gatherChangedFiles` / `gatherBranchDiff` / `currentGitBranch`。无 branch 参数且 effort 给定时直接采集 working-tree diff；无任何参数时与 `/review` 一样弹 picker。

### 5.3 注入 prompt + arm skill（⚠️ 实现期修正）

命令不再手写完整方法论（那是 skill Body 的活），只注入一段**参数化引子** + diff，由 `codeReviewPrompt(effort, fix, comment, scope, context)` 构造：

```
Please use the "code-review" skill for this task.

Review the current git working-tree changes. Examine the changes for bugs,
security vulnerabilities, style violations, and design problems. Categorise
findings by severity. Effort level: high — recall-first: broad coverage; you
may include uncertain findings but label each with a confidence. --fix is set:
after the read-only review, propose the mechanically-fixable findings via the
propose tool and apply them on approval (per-call y/N still applies).

<diff or changed-file summary>
```

**⚠️ 实现期 ground-truth 修正**：草稿原写"同时 `m.pendingSkill = "code-review"`"。读代码（`model.go:consumeArm`）后确认：`m.pendingSkill` 的消费（加 "Please use the X skill" 前缀）**只发生在用户手输提交**；`/review` 这类**程序化提交直接走 `submit()`，绕过 `consumeArm`**。若在此设 `pendingSkill`，arm 不会被本次提交消费，反而错误地挂到用户**下一条手输消息**上。

**正确做法**：把 `Please use the "code-review" skill` 指令**直接烧进注入的 prompt 文本**（`codeReviewPrompt` 第一行），不碰 `m.pendingSkill`。effort/flag 也在引子里固化，skill Body 提供其语义（D2）。这条已记入 `docs/pitfalls.md`。

### 5.4 `/review` 别名（D3 推荐路）

`cmdReview` 改为 `return cmdCodeReview(m, "medium "+args)`（或内部共享 helper），保持现有三 builder 为 `medium` 路径。`pickerPurpose` 仍复用 `"review"`，picker 回调里按当时记录的 effort 注入。

## 6. 实施拆解与估时

| 任务 | 估时 |
|---|---|
| 写 `internal/skill/builtin/code-review.md`（§4 草稿 + 按 D4 校准 framing） | ~1d |
| `cmdCodeReview`：解析（effort/flag/branch）+ 复用 diff 采集/picker + 注入引子 + arm skill；`/review` 收敛为别名 | ~0.5d |
| 测试 + 文档（`guide-skills.md` 提一句、`comparison.md` §250 P1 项标 ✅、`eval/cases/` effort 案例、AGENTS/CLAUDE 若需提及） | ~0.5d |
| **合计** | **~2d**（对齐 v6 §3.2） |

## 7. 测试要求（继承 CLAUDE.md "Testing (load-bearing)" 5 条 + v6 §6 柱 J）

| 标准 | 本柱适用点 |
|---|---|
| **malformed 输入** | `/code-review hihg`（错 effort）→ 明确错误非静默 medium；`/code-review --frob`（未知 flag）→ 错误 + usage；`/code-review high --fix extra weird args` 解析稳定。table-test 覆盖 effort×flag×branch 组合 |
| **mid-loop / cancellation** | `--fix` 进 propose 后 Esc → 继承 plan-mode v2 Esc 处理（撤 pre-approval、回 analyze）；断言 `RevokePlanPreApproval` 被调 |
| **优雅降级** | `--comment` 且 `gh` 缺失 → skill pre-check 路径输出 findings + 提示，不中断（这条更偏 dogfood/手测，单测覆盖命令层不弹错即可） |
| **持久化 round-trip** | `--fix` 走 propose ⇒ `seek -resume` 已由 plan-mode `reconstruct.go` 覆盖；本柱不引入新可持久状态，断言"无新状态文件" |
| **并发 -race** | 命令仅注入 prompt + 写 `m.pendingSkill`，全在 TUI 单 goroutine，无新锁——与 `/review` 同；`-race` 跑现有 commands_test 即可 |
| **prefix-cache 不回退** | 断言 `skilltool.schemaBytes` **未变**（D1：effort/flag 不进 Skill 工具）；新内置 skill 不改其他 schema |
| **零破坏回归** | `TestReview_UnknownArgsError` / `TestReviewChoices_*` / `TestWorkingTreeReviewPrompt_*` / `TestFallbackReviewPrompt` / `TestBranchDiffReviewPrompt_*` 全绿（D3 别名保证） |
| **skill 加载** | `loader_test` 风格：断言 `builtin:code-review` 被加载（`BySource["builtin"]` 计数 +1） |

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| effort 等级靠 framing 区分，模型分辨度不足 | D4：先建 `eval/cases/` 案例 baseline→调 framing→对比；DeepSeek V4 dogfood 校准；measure = finding 数 + confidence 标注差异，非 PASS/FAIL |
| `--comment` 依赖 `gh` + auth | D6：skill Body pre-check + 优雅降级；不缺 `gh` 就废整轮复审 |
| `/review`→别名改动碰现有 picker 回调 | D3：复用同一 `pickerPurpose="review"` + 同 builder，回归测试钉死；产品决策若否决别名，只动分发 |
| skill Body 与命令引子双处描述 effort，措辞漂移 | 引子只给"effort=X, flags=…，按 skill 执行"，语义**单一来源**在 skill Body；引子不复述档位定义 |
| `--fix` 被误解成自动写 | skill Body + 命令 description 双处明确"经 propose 批准 + per-call y/N"；anti-goal 写死 |

## 9. 与 v5 / 现有的关系

- **叠加层，不重构**：新增 1 个内置 skill + 1 个 slash 命令 + `/review` 别名收敛。`pkg/agent`/`pkg/deepseek`/`permission`/Skill 工具 schema 全不动（v6 §2 约束）。
- **复用 plan-mode v2**（`--fix`）与 **bash execute**（`--comment`），二者均已 ship。
- **可被 v5 子代理复用**（v6 §5）：`code-review` 是普通内置 skill，explore/review 类子代理模板可把它纳入——但这超出本柱范围，不在本 PRD 实现。
- **回滚成本**：删 1 个 `.md` + revert `cmdCodeReview` + 还原 `cmdReview`，单 commit 可回滚（v6 §2.3 独立可回滚约束）。

## 10. 开放决策（reviewer 拍板）

1. **D3**：`/review` 收敛为 `/code-review medium` 别名（推荐）vs 两命令完全独立并存（v6 字面）。本 PRD 默认推荐路。
2. **命名**：保留 Claude Code 的 `/code-review` 命名（推荐，对比文档对齐）vs 直接把 effort/flag 加到现有 `/review` 不引入新命令名。推荐前者——`/code-review` 是 Claude Code 用户的肌肉记忆。
