# dsh 是怎么被造出来的：从 git 历史与仓内文档反推

> **姊妹文档**：[`dsh-analysis.md`](dsh-analysis.md) 分析 dsh **是什么**（架构、机制、可借鉴项）。本文分析它**怎么被建成的**——节奏、分工、制度。
> **分析日期**：2026-08-13 · **仓库**：`deepseek-ai/deepseek-harness` @ `47f9438`（`master`）
> **数据源**：完整 git 历史（12,293 commits）+ `.agents/notes/` 1,386 篇设计笔记 + `docs/` + `scripts/`
> **可复现**：文末[附录](#附复算方法)给出每个数字的命令。

---

## 0. 一句话结论

dsh 是**九周内、由 25+ 人和大量编码 agent 共同写出的 12,293 个提交**——但让这个速度不塌掉的不是人力，是**第一天就立好、此后再没松过的制度**：仓库的第一个提交是 `AGENTS.md`，功能代码之前先落地了全套质量门，每个非平凡改动强制附一篇结构化设计笔记，最终形成 1,386 篇的决策语料。

**速度是果，制度是因。** 抄它的架构不难；难的是抄这套让 agent 能高速产出而不腐化的约束。

这套约束可以压缩成三个提问——见 [§7 ② 判据：三个提问](#-判据三个提问)。它们不需要 12,293 个提交才能抄。

---

## 1. 数字概览

| 维度 | 数值 |
|---|---|
| 时间跨度 | 2026-06-10 → 2026-08-13，**约 9 周** |
| 提交总数 | **12,293**（非 merge 6,683 / merge 5,610） |
| PR 编号 | 最后一个是 **#2519** |
| 贡献者 | **25+**（作者字段；真实同时在岗 10-15 人，见 [§3](#3-谁在写58-的-pr-来自-agent--worktree-分支)） ；前三名占 63%（5,235 / 1,360 / 1,151） |
| 单周峰值 | **3,542 commits**（W31，≈506/天） |
| 每 PR 提交数 | ≈ 6.8 |
| 单提交改动文件 | 中位数 **5**，p90 **29** |
| 设计笔记 | **1,386** 篇（implemented 507 / archived 143 / proposed 25 / rejected 11） |
| `scripts/` 下的脚本 | **138** 个，其中 `verify-*` / `check-*` 门控 **39** 个 |

### 周节奏

```
W24   67  ▏
W25  355  ███
W26  108  ▏
W27  440  ████
W28  684  ██████
W29 1749  ████████████████
W30 2169  ████████████████████
W31 3542  █████████████████████████████████
W32 1966  ██████████████████
W33 1213  ███████████
```

W26（06-22~06-28）的回落到 108 值得单独看一眼。那一周的提交前缀是 `fix:` 19 / `docs:` 13 / `refactor:` 7 / `feat:` **4**——几乎不产新功能。产出的笔记是 `web-capability-seam`、`file-context-as-event-gate`、`fsspec-style-fs-seam`，提交里反复出现 `fix: address codex review round 1/2/3`。

**这是一周的返工与收口**：把 `tool-fs`/`tool-web` 的 per-tool subpath 插件砍掉、把 file-context 从方法服务改成事件门、对齐 web 包的构建布局。紧接着 W27 就跳到 440、W29 到 1749。**先把缝改对，再加速。**

---

## 2. 第一天：先立门，后写码

最有信息量的是**最初 25 个提交**（06-10 至 06-11，两天内）。按实际顺序：

```
06-10  Initialize repo with README, AGENTS.md, and CLAUDE.md symlink
06-10  Link MVP requirement analysis and microkernel architecture docs in AGENTS.md
06-11  Set up monorepo infra: Yarn 4 workspaces, tsc -b + dumble build, vitest
06-11  Vendor Cordis framework packages as source
06-11  Add abstract service interface packages
06-11  Implement the agent loop plugin
06-11  Add runnable echo-agent example
06-11  Document the architecture and rewrite AGENTS.md
06-11  Fix architecture-review findings in the loop and service packages
06-11  Document the codebase thoroughly and tighten type safety
06-11  Fix schema-DSL findings from the second Codex review        ← ①
06-11  Enable maximum-strict TypeScript across our packages
06-11  Add ESLint: typescript-eslint strict-type-checked + stylistic
06-11  Enforce 100% per-file test coverage on packages/*/src        ← ②
06-11  Add repo hygiene gates: knip, publint, yarn constraints
06-11  Add lefthook git hooks with a vendor-manifest guard
06-11  Add CI workflow: full gate matrix on node 24 and 26
06-11  Add branded ID types: CallId, SessionId, AgentId
06-11  Add assertNever with closed-vs-extensible exhaustiveness guidance
06-11  Backfill architecture decision records                       ← ③
06-11  Add RFCs for the remaining quality-proposal ideas
```

三个值得单独标注的点：

**① `the second Codex review`** —— 项目**第二天**就已经在用 AI 做代码审查，而且是"第二轮"。审查不是后来加的流程，是从一开始就在环里。

**② `Enforce 100% per-file test coverage`** —— 在只有"agent loop + echo 示例"的时候就上了**每文件 100% 覆盖率**门控。这个顺序不可逆：先有门再有码，代码只能长成能过门的样子；反过来永远补不上。

**③ `Backfill architecture decision records`** —— 第二天就回填决策记录。这是 1,386 篇笔记语料的起点。

**第一个提交就是 `AGENTS.md`，而 `CLAUDE.md` 是指向它的符号链接。** 这个仓库从存在的第一秒起，就是为"被 agent 读、被 agent 改"设计的。

```
lrwxr-xr-x  CLAUDE.md -> AGENTS.md
```

---

## 3. 谁在写：58% 的 PR 来自 agent / worktree 分支

git 里没有 AI 署名 trailer（全仓仅 1 条 `Co-authored-by`，且是人类）。**他们不标注 AI 作者。** 但分支命名泄露了构成：

| 分支前缀 | PR 数 | 性质 |
|---|---|---|
| `worktree/*` | **210** | 独立 git worktree，斜杠命名 |
| `codex/*` | **209** | OpenAI Codex 的分支命名约定 |
| `worktree-*` | **130** | 同一工作流的连字符命名变体 |
| `feat/*` | 104 | 常规 |
| `fix/*` | 92 | 常规 |
| `xtr/*` | 22 | — |
| `docs/*` | 22 | 常规 |
| `feature/*` | 16 | 常规 |
| `agent/*` | 15 | agent |
| `claude/*` | 3 | Claude Code |

两个层次要分开看，因为证据强度不同：

- **工具署名分支**（`codex/` 209 + `agent/` 15 + `claude/` 3）= **227，占 23%**。分支名直接是 agent 工具名，这是硬证据。
- **worktree 家族**（`worktree/` 210 + `worktree-` 130）= **340，占 35%**。证明的是"并行 worktree 工作流"，worktree 里坐的是人还是 agent，分支名不说。

两者合计 **567 / 984 = 58%**。

另一个佐证更具体：**3,839 次"把主干合进分支"的同步提交，散布在 779 个分支上**（均值 4.9 次/分支）。但分布是重尾的：

```
87  codex/simp-hide-concrete-agent-loop
86  codex/simp-hide-subagent-internals
67  codex/simp-ui-identity-residue
62  worktree/web-multimodal-image-input
58  feat/todo-multi-in-progress
```

**单个分支被同步了 87 次**——在一个总共只有九周的项目里，这条分支活了相当长一段时间，期间主干动了 87 次而它一直在跟。这是"agent 在自己的分支上持续工作、主干不断前进、反复 rebase/merge"的典型形态，和人类那种"开分支、几天内合掉"的节奏完全不同。

顺带暴露了一个家族：**26 个 `codex/simp-*` 分支**——专门做简化的 Codex 分支。这与 §5.1 里"简化提案要过辩护"和 §4 里"`simplification` 笔记稳定在每月 21 篇"是同一件事的三个侧面：**删代码在这里是一条常设流水线，不是偶尔的清理。**

> ⚠️ **推断边界**：58% 是从**分支命名**推断的，不是声明的作者身份。`feat/*` 里也可能有 agent 写的，`codex/*` 里也可能有人类接手改的。23% 那一档是下限，58% 那一档是上限。

### worktree 家族最大，这件事本身有信息量

两种命名加起来 340，超过 `codex/*` 的 209——说明他们的主力不是"调用云端 agent 服务"，而是**在本地起多个 git worktree 并行干活**。分支名里还混着 `worktree/charming-swartz-83bf33` 这种机器生成的随机代号，说明至少一部分 worktree 是被工具批量开出来的，不是人手起的。

这正是 seek 的 `enter_worktree` / `exit_worktree` 工具在做的事，只是他们把它变成了日常工作流的默认形态。

### 3.1 Codex 和 Claude Code 分别在做什么

同样是被写进仓库配置的两个 agent，实际角色差得很远：

| | Codex | Claude Code |
|---|---|---|
| 写代码 | `codex/*` **209 PR / 186 个唯一分支** | `claude/*` **3 PR / 5 个唯一分支** |
| 评审 | 提交信息含 "codex review" **98 条**、"codex round" 31 条 | "claude review" **1 条** |
| 专项流水线 | `codex/simp-*` **26 个分支**，专做简化 | 无 |
| 仓库入口 | 原生读 `AGENTS.md`（无 `.codex/`） | `CLAUDE.md → AGENTS.md`，`.claude/skills → ../.agents/skills` |
| 在产品里 | `packages/subagent/subagent-codex` + hooks 桥 | `packages/subagent/subagent-claude-code` + hooks 桥 |

**Codex 是主力写手，也是唯一的常设评审员。** `fix: address codex review round N` 这一个句式在非 merge 提交里出现 **20 次**，加上 `fix(subagent)` / `fix(lsp)` 等带 scope 的变体共 26 次，还出现过 `round 3`。评审是**阻塞式、多轮**的，不是一次性建议。

**这轮评审不在 CI 里。** `.github/workflows/` 15 个 workflow 中没有任何 AI review action。评审是人手动发起的——大概率就是让 Codex 加载 `.agents/skills/dsh-code-review/SKILL.md`（49 行，6 条 blocking requirements + 14 条 manual checks）。

**Claude Code 的 3 个 PR 全在同一簇**：`web-pi-ai-provider-form` / `web-llm-pi-ai-config` / `pi-ai-model-discovery` / `docs-model-providers` / `unified-environment-credentials`——都是 pi-ai provider 接入与 Web 配置表单。这不像"主力开发"，更像某个人的某一段工作恰好用了 Claude Code。

对 seek 的意义：**"支持哪个 agent" 和 "用哪个 agent 干活" 是两回事。** dsh 对两者一视同仁地做了 subagent provider 和 hooks 桥（产品决策），但日常写码和评审压倒性地押在 Codex 上（工程决策）。

### 真实人力：作者字段 ≠ 同时在岗

"25+ 贡献者"是 git **作者字段**的数字，不等于任何一周同时在岗的人力。三个证据把真实人力压到 **10-15 人（核心 3-5 人）**：

**① 扩编曲线**——每周去重作者数：

```
W24   2   W25   6   W26   8   W27   9   W28  13
W29  15   W30  20   W31  27   W32  24   W33  29
```

两周内从 2 人起步，之后每周 +5 人，稳定在 25-29。这是"核心 2 人 + 滚动扩编"的形态，不是 29 人从头干到尾：核心成员全程在岗（Tianyi Cui 64 天、Hypatia May 63 天、Yichen Jiang 56 天、imccyu 57 天），相当一部分作者只在岗 2-4 周。

**② 单时区作息 + 深夜不熄**——提交按小时分布（全天 12,293）：

```
00:00 626   06:00  97   12:00 562   18:00 659
01:00 482   07:00  82   13:00 678   19:00 557
02:00 395   08:00  78   14:00 747   20:00 728
03:00 236   09:00 179   15:00 852   21:00 812
04:00 128   10:00 481   16:00 832   22:00 712
05:00 109   11:00 602   17:00 859   23:00 800
```

凌晨 4-8 点是明确低谷（78-128/h），白天 10-23 点高峰（600-860/h）——**单一时区**（全仓作者为中文名 + deepseek.com 邮箱，同一个物理办公室），不是全球分布式轮班。但 00:00-03:00 仍有 1,700+ 提交：真人不会每天稳定干到凌晨 3 点，这是"睡前挂 agent、醒来收"的形态——agent 在人不在场时继续跑。

**③ 头部作者速度远超人肉上限**——Tianyi Cui 5,235 提交 / 64 天 ≈ **83 提交/天**。即便每天 16 小时也是 5 分钟一个提交，还要配 review、测试、设计笔记；结合 58% PR 来自 worktree / agent 命名分支，他名下是**大量并行 agent 会话的聚合**，不是单人手工产出。

> ⚠️ **推断边界**：与上方 58% 同源——基于作者字段与时间分布的反推，不是声明的身份。`git log` 的作者可配置、agent 可挂任何账号；真实人力可能更低（≥3 人），几乎不会更高。

**合起来**：约 **10-15 个真人**（核心 3-5 人全程、其余滚动加入）在 9 周内靠大量本地 worktree 并行 agent 写出 12,293 个提交。**按提交量计，agent 占 70-85%**：58% 的 PR 来自 agent / worktree 分支（工具署名那一档 23% 是硬下限）+ 每 PR 6.8 提交 + 峰值 506 提交/天——即便 15 个真人每人每天手写 5 个提交也只有 75。反过来说，W26 那周（4 个 feat + 反复 `fix: address codex review round 1/2/3`）是人类在场密度最高的一周——**返工和收口只能人来做**。

---

## 4. 构建顺序：先缝后填

按包组首次出现的日期排（六七月的数据可靠；见文末[数据陷阱](#数据陷阱)）：

```
06-11  llm, session            ← 模型适配器与会话日志，最先
06-16  acp                     ← 编辑器协议（第 6 天就接外部集成）
06-18  compaction*             ← *笔记日期
06-20  core, spill*
06-21  subagent, util
06-22  fs                      ← 文件系统缝
06-25  web                     ← Web UI
06-29  todo
07-01  hooks
07-05  workflow, skill
07-06  sandbox
07-07  mcp, plan
07-08  code-runtime, guard, spill
07-10  session-query
07-14  context
07-15  sdk, lsp
07-19  client, goal, host
07-22  plan
07-23  attachment
07-24  storage, workspace
07-26  subprocess              ← 把子进程抽成缝（在 bash 之后很久）
07-28  e2b, settings, typert   ← 云沙箱
07-29  credentials, feedback
07-30  boot, interaction
08-03  preset
08-05  schedule
08-06  bundle
08-07  api
```

三个规律：

1. **`session` 和 `llm` 在第一天**。会话日志不是后加的持久化层，它和模型适配器同期落地——印证了 [`dsh-analysis.md`](dsh-analysis.md) §2 的判断：事件溯源是地基，不是特性。
2. **缝（seam）晚于实现**。`bash` 在 06-12 就有了，`subprocess` 缝到 07-26 才抽出来；`sandbox` 07-06，`e2b` 云沙箱 07-28。**先写具体实现，跑通了再抽象成可替换的缝**——不是先设计接口。
3. **外部集成很早**。ACP（编辑器协议）在第 6 天，Web 在第 15 天。§8.0 说的"它想成为你已有界面底下的那一层"，在时间线上是从一开始就在做的事，不是后期转向。

### 三个阶段（按笔记分类的月度分布）

| | 6 月 | 7 月 | 8 月 |
|---|---|---|---|
| architecture | **21** | 69 | 39 |
| feature | 12 | **97** | 61 |
| bug-fix | 0 | 34 | **43** |
| process | 7 | 38 | 24 |
| simplification | 6 | 21 | 21 |

- **6 月 = 打地基**：architecture 是 feature 的近 2 倍，bug-fix 为零。
- **7 月 = 铺开**：feature 冲到 97，architecture 仍有 69——边加功能边改地基。
- **8 月 = 收口**：bug-fix 从 34 涨到 43 并逼近 feature，architecture 回落。

`simplification` 从 6 月的 6 篇升到 7、8 月各 21 篇并**稳住**——铺开期开始，删代码就和加代码同频进行，不是某个阶段的清理活动。累计 `implemented/simplification` 48 篇，另有 5 篇简化提案被**驳回**（见 §5.1）。

---

## 5. 支撑这个速度的三项制度

高速产出会腐化，除非有东西持续对抗熵。dsh 用了三样，全部在第一周落地。

### 5.1 强制设计笔记

`.agents/notes/README.md` 的原文规定：

> **Every non-trivial change MUST add or update at least one Agent Note in the same PR.**

- 路径即元数据：`{lifecycle}/{class}/yyyy-mm-dd-topic.md`
- 生命周期：`proposed/` → `implemented/` → `archived/`，或 `rejected/`
- 六个类别：`feature` / `bug-fix` / `simplification` / `architecture` / `process` / `testing`
  - **`refactor` 被刻意排除**——它和 `simplification` 重叠，而后者的判别标准（"可观察行为变了吗"）已经覆盖
- 固定格式：**Problem → Decision → Alternatives considered → Consequences**
- 中英双语 + `.i18n.yaml` 一致性记录（三件套必须齐全）
- **不许建 INDEX.md**（有专门一篇笔记论证为什么）
- `archived/` 一旦封存**永久冻结**，由 `verify-archived-agent-notes.ts` 用 append-only manifest + sidecar 哈希强制

最能说明问题的是**否决记录**。`rejected/` 只有 11 篇，绝大多数是**被驳回的简化提案**：

```
drop-bash-output-spill-files           collapse-workflow-to-foreground-core
drop-durable-step-boundaries           fold-compaction-package-split
truncate-interrupted-turns             fold-session-persistence-interface
prune-unused-skill-registry-api        dependency-swaps-rejected-by-nih-audit
```

配合 `implemented/simplification`（每月 ~20 篇）和 `.agents/skills/dsh-find-simplifications`（专门找简化机会的 skill）——**这个项目对"能不能删掉"是持续提问的，而复杂度是经过辩护后保留的，不是没人管长出来的。**

### 5.2 门控先于代码

`scripts/` 下 138 个脚本，39 个是 `verify-*` / `check-*`。CI 分成十余个门组：

```
ci-primary  ci-static  ci-coverage  ci-snapshot  ci-artifacts  ci-consumers
ci-windows-blocking / -complete / -observational   node-compat   doc-sync
```

测试分七条泳道：`test`（单元）、`test:e2e`（真 API）、`test:snapshot`、`test:web`、`test:web:perf`、`test:web:stress`、`test:gui`。

值得注意的是门控的**对象**不止代码：

- `doc-sync` —— 文档与代码同步
- `verify-doc-budgets` —— 文档**字数上限**（根 `AGENTS.md` ≤ 1600 词）
- `check-vendor-manifest` —— vendored 依赖清单
- `verify-archived-agent-notes` —— 笔记归档的冻结契约
- `markdown-cross-link-lint` —— 跨文档链接
- 双语配对门 —— 英文改了中文没改就红

**他们把"文档腐化"当成和"代码腐化"同级的工程问题，用同一种手段治理。**

### 5.3 事故报告

`docs/postmortem/` 有 4 篇编号事故，双语，带 `Status:`。它们不是摆设——`docs/testing.md` 的测试政策直接引用 postmortem 0001 当论据：

> 178 个无密钥测试全绿，而真实 ACP 客户端会话瞬间崩溃。

于是政策写成：

> **We are DeepSeek — do not ration real-API tests.** A no-key test proves plumbing; only a with-key run proves the agent works against a real model.

**事故 → 政策 → 门控**是闭环的：0001 催生了真 API e2e 政策，0004（landlock 误分类）催生了"status-gated fatal evidence after exact informational exclusions"的分类规则。

### 5.4 一本规则，N 个 agent

整个仓库只有**一份** agent 规则，其余全是符号链接：

```
AGENTS.md                        ← 真身，15,737 字节
CLAUDE.md → AGENTS.md            （根 / packages/ / examples/ 三处）
.agents/skills/                  ← 真身，11 个 SKILL.md 共 957 行
.claude/skills → ../.agents/skills
```

AGENTS.md 自己写死了这条规矩：

> `CLAUDE.md` symlinks `AGENTS.md` at root, `packages/`, and `examples/`; **edit the real file**.

代价是规则必须写成**工具无关**的——不能出现"用 Read 工具"这种某个 agent 专有的词汇。收益是换 agent 零成本，以及**不会出现两份规则悄悄分叉**。（seek 的 `AGENTS.md` / `CLAUDE.md` 是两份可控分叉的文件，见 CLAUDE.md 的 "AGENTS.md vs CLAUDE.md" 一节——那是有意的选择，因为两个宿主的工具词汇不同；但结构性内容必须同改。）

`.agents/skills/` 这层更值得注意：它把**流程**也做成了 agent 可加载的可执行文本，而不是留在人的脑子里。

| skill | 行数 | 管什么 |
|---|---|---|
| `dsh-find-simplifications` | 146 | 怎么找到值得删的东西，什么算"强候选" |
| `dsh-merging-stacked-prs` | 127 | 堆叠 PR 的合并顺序 |
| `dsh-pre-push-checks` | 115 | push 前跑哪些检查（明确要求**跑最小集**，不要反射性跑全量） |
| `dsh-code-review` | 49 | 6 条 blocking requirements + 14 条 manual checks |
| `dsh-prose-standard` | 81 | 注释/文档/prompt/可见字符串的写法 |
| 其余 6 个 | 439 | 归档笔记、文档站同步、翻译、CoT 泄漏清理… |

`dsh-code-review` 开头第一句就是 **"This skill is guidance, not a complete checklist."** ——它不假装能替代语义评审，它做的是**把评审者的注意力预先分配好**：先跑 `change-scope` 定位脏层，再读 diff，"a short review with one substantiated blocker is better than a list of nits"。

这解释了 §3.1 的现象：Codex 之所以能当常设评审员，不是因为它比别的模型强，而是因为**评审标准被写成了 49 行可加载文本**，谁加载谁就按同一把尺子量。

---

## 6. `fix:` 是 `feat:` 的 3.3 倍

提交前缀分布：

| 前缀 | 数量 |
|---|---|
| `fix:` | **2,248** |
| `docs:` | **1,355** |
| `test:` | 947 |
| `feat:` | 687 |
| `refactor:` | 447 |
| `ci:` | 184 |
| `chore:` | 162 |

`fix : feat = 3.3 : 1`，`docs` 接近 `feat` 的两倍，`test` 也超过 `feat`。

这个比例可以有两种读法，我认为**第二种更贴合证据**：

- ❌ "质量差，全在修 bug" —— 但 `bug-fix` 类笔记只有 77 篇（对比 feature 170 篇），说明大部分 `fix:` 不是缺陷修复。
- ✅ **"写出来只是开始"** —— 结合每 PR 6.8 个提交、3,839 次分支同步（头部分支各自 60–87 次）、以及大量 agent 分支：一个功能的典型生命周期是"agent 出一版 → 审查 → 反复修 → 文档 → 测试 → 合并"。`fix:` 大量对应的是**同一个 PR 内的迭代**，而不是上线后的返工。

无论哪种读法，结论一致：**在这个流程里，产出代码是最便宜的一步。**

---

## 7. 对 seek 的启示

seek 是单人 + agent 的项目，dsh 是 25 人 + agent 的项目，规模差两个数量级。但下面这些与规模无关：

### 可以直接学的

1. **门控先于代码，且门控的对象包括文档。** seek 已经在这个方向上——本次会话刚加了 `verify-doc-budgets.sh`。dsh 的清单还有：跨文档链接检查、文档与代码同步、双语配对。seek 至少可以加**链接有效性检查**（`docs/` 里已有大量交叉引用，断链无人发现）。

2. **`rejected/` 语料。** seek 的 `docs/pitfalls.md` 记的是**出过什么错**；dsh 的 `notes/` 记的是**做过什么决定、否决过什么、为什么**。后者 seek 目前散在 PRD 和 `dsh-analysis.md` §9.2 里，没有独立位置。**被否决的方案连同理由，是防止同一个提案每半年重来一次的唯一办法**——本次会话里"approval 事件溯源化"就是一个被否决且理由值得留存的例子。

3. **事故 → 政策 → 门控的闭环。** seek 有 `pitfalls.md`（≈ 事故），但从 pitfall 到**可执行门控**的转化是零散的。dsh 的每一篇 postmortem 都能指出它催生了哪条政策和哪个门。

### 不该照搬的

- **1,386 篇笔记的规模**对单人项目是负担而非资产。可迁移的是**格式**（Problem / Decision / **Alternatives** / Consequences）和**强制性**，不是体量。
- **每文件 100% 覆盖率**在 dsh 是第一天立的规矩，代码从一开始就长成那个形状。seek 已有 74 个包，事后强加只会催生为覆盖率而写的空测试。
- **双语全覆盖**只在有中英双语受众时才划算。

### 最该记住的两件事

#### ① 时序：制度必须在代码之前

dsh 在只有 "agent loop + echo 示例" 的时候就上了 100% 覆盖率、strict TS、CI 门矩阵和决策记录。这个顺序不可逆——先有门，代码只能长成能过门的样子；先有码，门永远上不去。

seek 的对应时刻已经过去了，但每个**新子系统**都是一次小型的"第一天"。

#### ② 判据：三个提问

把 dsh 的架构、测试、文档三条线抽干净，剩下的是三个**换了主语的提问**。它们不需要 12,293 个提交才能抄——每一个都是评审时可以立刻开始问的。

**共同的主语替换：从"我这么设计对不对"，换成"犯错的那条路还在不在"。** 这是把 "agent 是主要作者" 当成一等设计约束的直接后果：人类作者会读注释、会记得纪律；agent 不会，它只会走还开着的那条路。

---

**提问一：违反这个设计的东西，还能不能被构造出来？**

> 传统：这个设计对不对？
> dsh：**不可表达 > 可检测。**

`reconstructable-requests` 笔记明确否决了 detect-and-report 方案（比对相邻请求、偏离即告警），理由是**违规请求仍然构造得出来，并且会发出去**。落地形态：`deriveMessages()` 返回 deep-frozen 的共享消息，改它直接抛异常——"通过投影修改历史"这件事不可表达。

于是前缀缓存稳定性被降级成**推论 #1**，笔记原话是 *"stability is emergent, not managed"*。

> **seek 的对照**：同一个目标，我们用的是纪律——CLAUDE.md 的"永不修改旧消息"、token 优化只在 write-time、`TestCompose_IsDeterministic` 守卫 `Compose` 是纯函数。**纪律会被下一个不读 CLAUDE.md 的贡献者破坏，结构不会。** 重写地基对一个单二进制本地 CLI 不划算（详见 [`dsh-analysis.md`](dsh-analysis.md) §9.2），但**每加一个新特性时问一遍这个问题**，成本是零。

---

**提问二：谁来证明检查器自己没坏？**

> 传统：功能对不对？
> dsh：**活着的缓存不能给自己背书。**

`dsh-agent-loop/invariant` 伴生插件跑在生产路径上，用**一个全新 `Session`** 把每个 loop 请求独立重建一遍，再跟实际发出去的逐字段比对——原话 *"so the live cache cannot vouch for itself"*。

这套东西推广成了每包一个 `./invariant`，判据定得很硬：

> 有用的运行时不变量**关联的是跨时间的观察或一个可变数据结构**。仅仅确认某个方法存在、某个插件叫某个名字、某个常量示例仍返回已知值，**不算**这种关联。

103 个包里 **21 个有可执行检查，82 个是经过论证的空实现**——每个空的都要写一行 `No runtime invariant: <本包为什么没有可观察关系>`。**空实现是架构结论，不是占位符。**

> **seek 的对照**：本次会话的 `pkg/agent/cache_e2e_test.go` 正是这条的一次实践——在此之前，缓存纪律被破坏时**没有任何测试会红**，因为 fake-backend 测试只证明了解析器能跑。凡是"用被测对象自己的状态来断言被测对象"的测试，都值得问一遍这句话。

---

**提问三：下次有人再提这个被否的方案，他能不能查到理由？**

> 传统：文档写清楚了没有？
> dsh：**否决判例要留档。**

`.agents/notes/rejected/` 11 篇里 **10 篇是简化提案**，其中 5 篇是同一天（06-20）批量扫描的产出，**当天全被驳回**。留下来的不是"我们没做这个"，是**为什么不做**——见 §5.1 和 [§3.1](#31-codex-和-claude-code-分别在做什么)。

配套的 `dsh-find-simplifications` skill 要求每份提案自带一节 `## Why not keep it?` / `## What we give up`：**提案人必须先把反对自己的最强理由写出来。**

> **seek 的对照**：`docs/pitfalls.md` 记的是**出过什么错**，没有地方记**提过什么、为什么被否**。而这类判例已经攒了一批——`approval` 事件溯源化（`Yolo` 已持久化，没有 per-action 状态可丢）、canonical output contract（Go 类型系统在编译期已提供）、spill 全文检索（head+tail 已解决真问题）、容器化（2026-05-30，现有安全原语够用）。**这四条目前只活在对话记录里，仓库中没有位置**——半年后会原样重来一次。

---

## 附：复算方法

```bash
cd ~/code/github/deepseek-harness
git fetch --unshallow          # clone 默认是浅的，只有 1 个提交

# 规模与节奏
git log --oneline | wc -l
git log --format='%ad' --date=format:'%Y-W%V' | sort | uniq -c
git shortlog -sne --all | head -25

# 引导顺序
git log --reverse --no-merges --format='%ad %s' --date=format:'%m-%d' | head -30

# PR 来源构成（注意：只数 'Merge pull request #N from org/BRANCH' 这一种
# merge，984 个；'Merge branch X into Y' 是分支同步，不是 PR）
git log --all --merges --format='%s' \
  | grep -oE 'Merge pull request #[0-9]+ from [^/]+/[A-Za-z0-9._/-]+' \
  | sed -E 's|.*from [^/]+/||' | cut -d/ -f1 | sort | uniq -c | sort -rn

# worktree- 连字符家族（别漏掉：cut -d/ 数不到它）
git log --all --merges --format='%s' \
  | grep -oE 'Merge pull request #[0-9]+ from [^/]+/[A-Za-z0-9._/-]+' \
  | sed -E 's|.*from [^/]+/||' | grep -c '^worktree-'

# Codex vs Claude：写码 / 评审 / 唯一分支数
for p in codex claude agent; do
  echo "$p PR: $(git log --all --merges --format='%s' \
    | grep -cE "Merge pull request #[0-9]+ from [^/]+/$p/")"
  echo "$p 唯一分支: $(git log --all --merges --format='%s' \
    | grep -oE "$p/[A-Za-z0-9._-]+" | sort -u | wc -l)"
done
git log --all --format='%B' | grep -ci 'codex review'   # 98
git log --all --format='%B' | grep -ci 'claude review'  # 1
git log --all --no-merges --format='%s' | grep -c 'address codex review round'  # 20

# 长分支同步：总次数、分支数、重尾分布
git log --merges --format='%s' | grep -cE "^Merge (branch|remote-tracking branch) '"
git log --merges --format='%s' | grep -E "^Merge (branch|remote-tracking branch) '" \
  | sed -E 's|.* into ||' | sort | uniq -c | sort -rn | head

# 提交前缀分布
git log --no-merges --format='%s' | grep -oE '^[a-z]+(\([^)]+\))?:' \
  | sed -E 's|\(.*\)||' | sort | uniq -c | sort -rn

# 单提交改动文件数
git log --no-merges --numstat --format=%H | awk '
  /^[0-9a-f]{40}$/ {if (h) print n; h=1; n=0; next} NF==3 {n++} END {if (h) print n}' \
  | sort -n | awk '{a[NR]=$1} END {print "median:", a[int(NR/2)], "p90:", a[int(NR*0.9)]}'

# 笔记语料
find .agents/notes -name '2026-*.md' -not -name '*.zh.md' | wc -l
find .agents/notes -name '2026-*.md' -not -name '*.zh.md' | sed 's|.*/||' \
  | cut -c1-7 | sort | uniq -c

# 每周活跃作者（去重，判断真实同时在岗人力）
git log --all --format='%ad %an' --date=format:'%Y-W%V' | awk \
  '{if (!seen[$1" "$2]++) w[$1]++} END {for (k in w) printf "%s %d\n", k, w[k]}' | sort

# 提交按小时分布（判断单时区 vs 轮班、人肉 vs 挂机）
git log --all --format='%ad' --date=format:'%H' | sort | uniq -c

# 作者在岗天数（BSD awk 无 strftime，用 python3）
git log --all --format='%an|%at' | python3 -c "
import sys, datetime
first, last, cnt = {}, {}, {}
for line in sys.stdin:
    a, t = line.strip().rsplit('|', 1); t = int(t)
    first.setdefault(a, t); last[a] = t; cnt[a] = cnt.get(a, 0) + 1
def f(ts): return datetime.datetime.fromtimestamp(ts).strftime('%m-%d')
for a in sorted(cnt, key=cnt.get, reverse=True)[:15]:
    print(f\"{cnt[a]:4d} {a[:18]:18s} {f(last[a])} ~ {f(first[a])} ({(first[a]-last[a])//86400}天)\")
"
```

### 数据陷阱

1. **默认 clone 是浅的**（`depth=1`），`git log` 只有一个提交。不 `--unshallow` 的话本文所有数字都得不到。
2. **08-13 那批包的"首次出现"是假的**。最后一天有一次全仓 `refactor: apply repository naming contract`，`git log -- <dir>` 甚至 `--follow` 都会把重命名当成新增。受影响的至少有 `compaction`、`shell`、`jobs`、`terminal`、`test-support`——本文用**笔记文件名的日期**做了校正（例：compaction 真实日期 06-18，不是 08-13）。
3. **PR 编号 #2519 与 984 个 merge-commit 对不上**。差额是 squash/rebase 合并与关闭未合的 PR，git 历史里没有 merge commit。所以"984"是 merge-commit 口径，不是 PR 总数。
4. **没有 AI 署名 trailer**。§3 的 23%/58% 两档均由分支命名推断，不是声明的作者身份。
5. **同一个工作流有两种分支命名**：`worktree/*`（210）和 `worktree-*`（130）。用 `cut -d/ -f1` 归类只能数到前者，会把 worktree 家族少算 38%。本文初稿即栽在这里（当时得出 44%），修正后为 58%。凡是按分隔符归类分支名的统计，都要先看一眼分隔符是不是唯一的。

### 本文看不到的

只有 git 与仓内文档：**看不到** PR 评审意见、issue 讨论、内部沟通、CI 运行记录、以及任何未进入仓库的决策过程。所有关于"为什么"的判断都是从留痕反推的，不是他们的陈述。
