# Feature: memory↔skill 打通——蒸馏落点（review 第四动作）+ skill 生命周期对齐

> **性质**：独立 feature 设计稿（自进化内环）。
> **状态**：📐 设计稿（butler 卡 `c_5fdsqb`，2026-11-16，分支 `drive/c_5fdsqb`）。本卡**只出设计，零实现**；里程碑见 §9。
> **依据**：[`notes-memory-skill-gap.md`](archive/notes-memory-skill-gap.md) §5 P0 两问（备料笔记，2026-11-12）；[`feature-skill-distill.md`](feature-skill-distill.md)（M-SD1/2/3 + 草稿收件箱已交付——本文的现状基线）；`internal/memory`、`internal/skilldistill`、`internal/skillmgr`、`internal/skillstats`、`internal/skillcli`、`internal/tui/distill*.go` 源码。
> **范围边界**：不动 M-SD2 自动路径（永不 install 的铁律不变）、不动 `.drafts/` 收件箱语义、不动 skill 加载器、不给 skill 加衰减机制（理由见 §5）。

---

## 0. 卡片两问 → 章节映射

| notes §5 P0 问题 | 本文章节 | 结论一句话 |
|---|---|---|
| ① 蒸馏产物落点：`/distill` review 加第四动作，经 skillmgr 新入口写 project 级 | §2 §3 §4 | review 击键即闸：`s` = 本条改存为 **project 级** `<project>/.seek/skills/` 的 skill，M 候选走一次 reasoner 转换（tagline→description 重写），skill 候选直写；不发明新 gate |
| ② 生命周期对齐：decay 移植 vs skill 永生+判重兜底，二选一 | §5 §6 | 选 **skill 永生 + 蒸馏判重兜底**；decay 语义对 skill 是错的工具（§5.2 四条论证）；`.stats.jsonl` 消费端补 `--idle` 观察视图；v1.md↔score.go 的 GC 偏差按「实现是对的」勘误（§7） |
| P1 约束（tagline→description 必须重写、判重复用现成 Jaccard/CJK） | §4.3 §8 | 写进转换 prompt 契约与判重选型；对 notes 原文「复用 merge.go」的一处偏离及理由见 §8.2 |

## 1. 动机与定位

现状：S→M→L 漏斗完整（含 decay GC/升格/归档），S→K 蒸馏已交付（`/distill` 并行跑 M 段 ⚡ 与 skill 段 🛠，M-SD2 自动路径落 `.drafts/`），但 **M↔K 零互通**（代码级零 import，notes §3 已坐实）。三个具体断层：

1. **M 候选没有 skill 出口**。用户在 ⚡ review 里看到一条候选，意识到「这不是一次性决策，是可复用程序」——今天只能 y 落 M（错层）或 n 丢弃再手写 SKILL.md（断链）。
2. **手动链路产物绕道无人值守的闸**。🛠 review 按 `y` 落 user 级草稿，之后还要 `seek skill drafts approve` 才生效——可人在模态里已经逐行看过 description、evidence、body 了，草稿收件箱这道闸是为 M-SD2（无人看过 session）设计的，对手动路径是冗余仪式。
3. **生命周期断层**：M 有 decay，skill 永生。「蒸馏把还热的知识搬进 skill = 移出衰减体系」（notes §4.3）——这个担忧需要正面回答而不是回避（§5）。

本设计 = notes §5 P0 两问的答案。一句话定位：**给已验证的 y/n/e review 加一个方向，让「改存为 skill」和「存进 M」一样，是一个击键的事。**

## 2. 决策一（P0①）：蒸馏产物落点 = project 级 `<project>/.seek/skills/`，review 击键即闸

### 2.1 落点候选对照

| 落点 | 进闸方式 | 生效时机 | 共享性 | 负担 | 判定 |
|---|---|---|---|---|---|
| user 级 live（`~/.seek/skills/`） | 谁来闸？ | 下次启动 | 不共享 | 全局命名空间污染，影响**所有**项目 | ✗ 爆炸半径不可接受 |
| user 级草稿（`.drafts/`，现状 M-SD1 的 `y`） | review + 收件箱 approve 双闸 | approve 后下次启动 | 不共享 | 人在场时双闸冗余 | 保留为 `y` 不动（§2.4） |
| **project 级 live（`<project>/.seek/skills/`）** | **review 击键即闸** | 下次启动 | **git 可共享** | **无 `.install.json`**（v2 §4.2 既有约定：project 安装刻意不写 sidecar）；只影响本项目 | ✓ 本文选择 |

### 2.2 为什么 review 击键可以当闸（不发明新 gate）

- **M 层先例**：⚡ review 按 `y` 直接落 `memory.jsonl`，没有草稿阶段。`s` 与 `y` 同一信任级别、同一模态、同一逐条人工审阅——只是出口方向不同。这正是 notes §5 P0①「不发明新 gate——y/n/e review 是已验证的用户安全网」的落实。
- **爆炸半径有界**：project 级 skill 只影响本项目的 session；文件是 git 工作树里的普通文件——diff 可见、可 review、可 revert、可 rm。装错了的代价 = 一个 commit 的撤销，不涉及全局状态。
- **命名空间不外溢**：不动 `~/.seek/skills/` 全局层，跨项目零影响；M name 本就 per-project，project skill name 同 scope，命名空间天然对齐（notes §4.7 的错位问题在这一路径上不存在）。
- **对照：自动路径不能这么快**。M-SD2 无人看过 session，草稿收件箱闸是 load-bearing，保持不变。闸的松紧跟着「有没有人看过内容」走，而不是一刀切。

### 2.3 skillmgr 新入口：`WriteProjectSkill`

唯一新增的写入口（AGENTS.md 红线不变：skills 树仍然只有 skillmgr / skillstats 能写）：

```go
// internal/skillmgr/writeproject.go（实现期落位）
type WriteProjectOptions struct {
    ProjectDir string   // 默认 cwd（与 skill.Load / resolveTargetParent 同口径）
    Name       string   // kebab-case，须与 md frontmatter name 一致
    MD         []byte   // 完整 SKILL.md 字节（调用方已渲染好）
    Now        func() time.Time // 测试注入
}
func WriteProjectSkill(opts WriteProjectOptions) (dir string, err error)
```

行为链：`skill.Parse(md)` 校验（不合法即拒，draft 同款契约）→ frontmatter name 与 `opts.Name` 一致性校验 → **目标目录已存在同名即拒**（自防御，不覆盖）→ `atomicWriteFile` 落 `<ProjectDir>/.seek/skills/<name>/SKILL.md`（复用 `draft.go` 的原子写）→ **不写 `.install.json`**（project 级先例）。内部与 `Install` 的 commit 段共享校验核心，避免第二套「验证 + 落盘」逻辑漂移。

调用侧（TUI）负责的**跨层同名检查**：`s` 前查 `skills.Get(name)`（三层级联 Set，project>user>builtin first-writer-wins）——任一层已有同名 skill 即拒绝，提示「改既有 skill（`seek skill update` / 直接编辑文件），不要新写一份」。宁可保守拒绝（loader 的 shadowed 是加载期兜底，不该被蒸馏路径当正常通道用），与 L2 `CheckNovelty` 的同名拒绝语义一致。

### 2.4 与 M-SD1 草稿流的关系（两闸并存，语义各自成立）

| 动作 | ⚡ M review | 🛠 skill review |
|---|---|---|
| `y`（不变） | 落 M（`Project.Add`） | 落 **user 级草稿**（`DraftStore.WriteDraft`），之后 `seek skill drafts approve [--project/--user]` |
| `s`（新增） | 转换后落 **project 级 skill**（§3） | **直写** project 级 skill（§4.6——description 已是 reasoner 产物，无需转换） |

`y` 的语义完全不改动（收件箱流照旧服务「想要 user 级 / 想慢一点」的场景）；`s` 是快通道，且**只在人在场的 review 里存在**——自动路径（M-SD2）没有任何直写能力，铁律不破。

## 3. 决策一实现面：⚡ M review 第四动作 `s`

### 3.1 交互流与状态机

`internal/tui/distill.go` 的 `handleDistillKey` 增加 `s` 分支；模态新增三个字段：`distillConverting bool`、`distillConverted *skilldistill.SkillCandidate`、`distillConvertNote string`（承载 GREY 判定文案）。

```
⚡ candidate i/j ── s ──▶ converting（spinner 行 "· converting to skill…"，
   y/n/e/q/s              异步一次 reasoner 调用，90s 上限，ctx 挂 m.opts.Ctx）
                              │ err ──▶ 错误行，回到 candidate 提示态（y/n/e/s/q 仍可用）
                              │ L2 REJECT ──▶ 拒绝行（同名/重叠 ≥0.60），回到 candidate 提示态
                              ▼
                          converted 预览态：description（重写后的 when-to-use）置顶
                          + GREY ⚠（若有）+ body（=content 直译）
                          [y] install to project   [n] back   [q] quit review
                              │ y ──▶ WriteProjectSkill ──▶ 成功行
                              │        "· skill installed: <dir> (takes effect next session)"
                              │        计数 distillSkilled++，advance 到下一条
                              │ n ──▶ 回到 candidate 提示态（y/n/e/s 仍可用——改主意还可以落 M）
                              ▼
                          review 结束行扩展："…, N saved, M dropped, K → skill, …"
```

失败路径全部「降级不终止」：转换失败 / 落盘失败只出错误行，review 继续到下一条（与 `distillAcceptCurrent` 对 `Project.Add` 失败的处理同构）。编辑态（`e`）不感知 `s`——编辑的提交语义是落 M，保持不变。

### 3.2 转换 = 按需一次 reasoner 调用（不是提取时双投影）

两个候选：

- **A. 提取时双投影**（M 蒸馏 prompt 顺手让每个候选多产一个 `skill_description` 字段）：省一次往返，但 M prompt 背上 skill 职责、绝大多数候选该字段永远用不上、且两个 reasoner 的质量标准会互相拉低。✗
- **B. 按需转换**（按下 `s` 才发一次 V4-Flash thinking 调用）：成本只在真用 `s` 时付；转换器可以拿到 skill manifest 与 M 索引做上下文；模态状态机代价可控（§3.1）。✓

选 B。模型与 `Distiller` 一致（V4-Flash + thinking，90s bound，同 M-SD1 的 `startSkillDistillCmd` 口径）。

### 3.3 转换器契约（落位 `internal/skilldistill/convert.go`）

```go
// MCandidate 镜像 memory.Candidate（MEntry 先例：skilldistill 不 import internal/memory，
// 依赖箭头指向小类型而非整个 memory 子系统）
type MCandidate struct { Name, Tagline, Content string; Tags []string }

type Distiller struct { … }
func (d *Distiller) ConvertToSkill(ctx context.Context, cand MCandidate,
    skills *skill.Set, mIndex []MEntry) (SkillCandidate, L2Result, error)
```

转换 prompt 的**硬性要求**（P1 约束落点，验收 §10 #5）：

1. `description`：**必须从 `tagline` 语义重写**——tagline 答「决定了什么」（what was decided），description 答「何时该用」（when to use）。prompt 里给正反例；响应里 `description` 与 `tagline` 逐字相同视为违约（机械校验拒回）。
2. `body`：`content` **直译**（verbatim）——P1 已裁定可直译，不要求重组为 When to use/Steps/Verification 结构（那是 skill 提取器的职责；这里是「决策以 skill 形态落档」，诚实胜过化妆，用户可在 git 里继续编辑）。
3. `name`：复用候选 name（kebab 校验沿用 `validName`）。
4. `keywords`：`tags` 直通（P1 #3：keywords 本就 reserved 未用，此处接通第一个生产写入方）。
5. 拒绝方向：若转换器判断该候选**没有**可表述的 when-to-use 触发（纯一次性事实），返回空 description——调用方按失败处理并提示「这条更适合 M（按 y）」。

返回前对转换产物跑 `CheckNovelty`（见 §8），REJECT 拒回、GREY 随候选上浮到预览态。

### 3.4 落盘形态

frontmatter：`name` / `description`（重写后）/ `keywords`（=tags）/ `version: 0.1.0`（**非** `0.1.0-draft`——这是装好的 skill）/ `author: distilled`（沿 RenderSKILLMD 先例，作为「机器蒸馏产物」的永久标记）。渲染复用 `RenderSKILLMD` 的变体（参数化 version + keywords，保持「输出必须过 `skill.Parse`」的自检契约）。写入后 loader 下次启动按 project>user>builtin 优先级拾取，进 system prompt manifest——**生效时机 = 下次会话**（manifest 启动时固定，v2 既有契约），成功行必须带这句提示。

### 3.5 同名/重叠/更新语义（notes §4.5 §4.7 收口）

- **同名**：任一层已有 → 拒绝（§2.3）。绝不覆盖已装 skill（与 L2 同名拒绝、`drafts approve` 的 collision 处理同调）。
- **重叠**：转换产物与既有 skill 或 M 条目 trigram-Jaccard ≥0.60 → 拒回；0.35–0.60 → GREY 警告上浮预览，人裁决（与 L2 完全同阈值同语义）。
- **更新**：`s` 落的 skill 无 `.install.json` → 不可 `seek skill update`（正确：它的「上游」是 git，不是安装源）。**重新蒸馏同名 → 拒绝并指向「编辑文件」**。用户手改永远不会被下一次蒸馏覆盖——notes §4.5 担心的「重蒸馏覆盖手改」在这条规则下结构性不存在。
- **双存保证**：`s` 的语义是**改存**（不是「M 也存一份」）——按 `s` 并 confirm 的候选不写 M。M↔skill 双存的入口在这里被显式关闭；存量双存的检测见 §8.4（展示级告警）。

### 3.6 注入通道代价的显式回答（notes §4.6）

把知识从 M（user-role context）搬进 skill manifest（system prompt）确实改变了缓存策略区与冲突优先级（skill 与 AGENTS.md 同层、高于 M）。**值得付**，理由：

- 该知识只付**一行** manifest 成本（name + description）；body 按需取，与 M 的「索引常驻 + recall 详情」同构。
- 搬进 manifest 的收益正是「何时用」触发器的**主动性**——procedural 知识需要被前置激活，留在 M 索引里只能被动等人想起 recall。
- 积累速度有界：入口有 review 闸 + 判重拒绝，出口有 uninstall / `--idle` 观察（§6），且 project 级清单在 `seek skill list` 与 git diff 里全程可见。

## 4. 决策一实现面：🛠 skill review 同步获得 `s`

M-SD1 的 🛠 候选的 `description` 本来就是提取 reasoner 写的 when-to-use（`SkillDistillSystemPrompt` 强约束），**无需任何转换**：`s` = 渲染（`RenderSKILLMD` 变体，version `0.1.0`）+ `WriteProjectSkill` 直写。同名/重叠检查沿用候选过 L2 时的判定（同一 Set、同一次 review 会话内有效）。

按键提示行更新为：`[y] save draft  [s] install to project  [n] drop  [q] quit`——`y` 保留草稿慢通道，`s` 是「我看完了，现在就要它在下个项目会话里生效」。这也补上了 feature-skill-distill §6.1 当初规划过、M-SD1 交付时被简化掉的「模态内直装」缺口——但用第四动作实现而非 scope picker 模态（不发明新交互件，且 scope 二选一被键位直接表达）。

## 5. 决策二（P0②）：skill 永生 + 蒸馏判重兜底（**不做** decay 移植）

### 5.1 两个候选方案

| | (a) decay 语义移植 | (b) skill 永生 + 判重兜底（**选定**） |
|---|---|---|
| 机制 | skill 加 stale 标记 + `.stats.jsonl` 读回算 recall_count 等价分，低分剔出 manifest | skill 无衰减；入口判重（L2 已在生产）防积累，出口人审（uninstall / git rm），观察面 `--idle` |
| 需要新建 | stale 状态 + 写者 + loader 读 stats 算分 + GC pass + 全套失败路径测试 | 仅 §6 的 `--idle` 视图（一个 flag） |
| 淘汰依据 | **使用度**（没人用 → 过期） | **人的判断**（错了/过时了 → 删） |

### 5.2 为什么 decay 对 skill 是错的工具（四条，独立成立）

1. **decay 淘汰的是无害集合**。decay 由使用度驱动：没人触发的 skill 才会过期——而没人触发的 skill 的全部危害是一行 manifest token。真正危险的「**错且常用**」skill（description 频繁命中、内容已过时）在 decay 下 recall_count 高 → score 高 → **永不过期**。语义错误需要的治理手段是矛盾检测（notes P2 的 skill 审计 pass / dream 对称 evaluateStable），方案 (a)(b) 下都得另做——decay 没有覆盖真正的风险面。
2. **stale 需要一个写者，而写哪都不对**。M 的 stale 写在本地 `memory.jsonl`，天然私有。project skill 在 **git 共享**目录：把 stale 标记写进 skill 目录 = 把「本机使用频度」泄漏进共享仓库（A 机器常用、B 机器闲置，谁的 stale 算数？），还制造 git churn。不落盘、只在加载时算？→ loader 每次启动读 `.stats.jsonl` 算分排序，违背 loader 零成本启动原则（skillstats 包注释明示的定位），且「stale 但不落盘」无法表达归档。
3. **记账域错位**。`.stats.jsonl` 是**全局单文件**（`paths.UserSkillStats()`），行带 `project_id`；skill 是 project>user>builtin 三层级联命名空间。同一 skill 在三个层、N 个项目下的「使用分」如何聚合没有不别扭的答案——为decay 硬造一个聚合规则，是给未来埋坑。
4. **先例校验**：builtin skill 不衰减、Anthropic Agent Skills 模型无衰减概念、本仓库「实现是对的」的 score.go 语义（有界增长优先于永久豁免）同样反对给 skill 开一个无消费端的复杂机制。技能是「持有的工具」，不是「会凉的记忆」——M 层的 decay 存在是因为 M 是**可能过时的决策流水**；skill 是**经人审的制品**，制品的生命周期由制品的宿主（git / uninstall）管理。

**显式声明（规范级）**：skill（三层全部）不引入 stale / 归档 / pinned / 衰减分。skill 的生命周期 = 安装（人审）→ 使用（`.stats.jsonl` 记账）→ 观察（`--idle`）→ 移除（uninstall / git rm）。错误知识的治理走 P2 审计 pass，不走 decay。

### 5.3 永生的护栏（四道，全部已有或本设计补齐）

1. **入口判重**：L2 `CheckNovelty`（同名拒绝 + trigram-Jaccard 0.60/0.35，对 installed skills **和** M 索引）已在手动/自动两条路径生产运行；`s` 路径复用同一判定（§3.3）。
2. **人审闸**：手动 = review 击键；自动 = 草稿收件箱 approve。零无人直写。
3. **git**：project 级 skill 是工作树普通文件，diff / review / revert 全程可用。
4. **观察出口**：§6 `--idle` 让「还在用吗」有答案，喂给人的 uninstall 决策——**永生 ≠ 不可见**。

## 6. `.stats.jsonl` 消费端（P0② 的伴随项）

**现状先坐实**（notes §2.2 的「从不读回」已过时）：`skillstats.Read` 存在；消费端已有两个——`seek skill stats`（M8.4：`--top/--since/--json`，按 name 聚合调用数/最近使用/top model）与 `seek skill stats --distill`（三期漏斗报表，stage×verdict 计数）。**「哪怕只是 CLI 查询」这个最低要求今天已满足。**

本设计补的是生命周期视角的缺口——「**哪些 skill 还在被用**」目前只能从正向排行反推。新增：

```
seek skill stats --idle 720h [--json]
```

行为：加载当前 skill Set（`list` 同款），与 stats 聚合做**外连接**——窗口内 0 调用（含**从未被调用**、stats 里根本没有行的 skill）的已装 skill 全部列出，按 last_used 升序（从未用过排最前），输出 `NAME / LAST_USED(或 never) / SOURCE(project|user|builtin)`。只做观察，不做任何自动动作（§5.2 已论证使用度不构成删除依据；它只是人审 uninstall 的输入）。实现是对 `cmdSkillStats` 加一个 flag + 一个 join，~半天。

## 7. v1.md ↔ score.go GC 语义偏差清理（勘误，随本卡落盘）

**偏差**（notes §1.3 坐实）：v1.md 三处写 auto_sourced **硬豁免**（「无论 score 多低、创建多久，都不被标记 stale 或归档」——L438 段落、§6 剔除规则表行、验收 #14）；`score.go:40-46` 实现的是 **30 天宽限（自 CreatedAt）后参与正常 score-based GC**，注释明确说明硬豁免会导致无界增长。`TestRunGC_AutoSourcedGracePeriod` 钉死实现语义（宽限内跳过、过宽限无 recall → stale）。

**裁定**：实现是对的——硬豁免 + 「用户 review 前不 GC」组合下，永远没人 review 的 auto 条目无限积累（auto-dream / M-SD2 时代 auto 产出只会更多）；30 天宽限已给足 review 窗口（对照：正式条目新条目宽限才 7 天）。**改 PRD 三处勘误**（本卡直接落盘，见 v1.md 内 `勘误 2026-11-16` 标记），此后全仓库只有一套 GC 语义，本设计与后续卡引用 `score.go` 语义即安全。

## 8. P1 约束钉死（设计级，实现不得偏离）

### 8.1 tagline→description 语义翻转必须 reasoner 重写

见 §3.3 第 1 条。机械护栏：`ConvertToSkill` 校验响应 `description ≠ tagline`（逐字相同拒回）；prompt 契约进 `ConvertPromptRev = "m-to-skill-convert-v1"` 版本化。**禁止**任何「拷贝 tagline 兜底」的降级路径——宁可失败回到 y/n/e。

### 8.2 判重不另起炉灶——但对 notes 原文有一处修正

notes §5 P1 #4 写「复用 `merge.go` 的 Jaccard/CJK」。该笔记写于 2026-11-12，**早于** M-SD1 落地（2026-11-13）；今天仓库在 M↔skill 边界上已有**生产中的**判重实现：`skilldistill.CheckNovelty` 的 `trigramJaccard`（rune 三元组 Jaccard，注释明确为中文描述设计——CJK 正确；阈值 0.60 拒绝 / 0.35 灰区，成本不对称已校准）。裁定：

- **M↔skill 边界判重 = `skilldistill.trigramJaccard`**（`s` 转换、双存告警、以及未来一切跨层判重）。理由：(i) 同一边界已在生产用它，两套度量并存才是真风险；(ii) 对中文文本，`merge.go` 的 word-Jaccard 退化成单「词」走 Levenshtein 兜底，而 trigram 均匀好 behaved；(iii) `skilldistill` 刻意不 import `internal/memory`（`MEntry` 镜像先例），引 `merge.go` 会反转这个依赖箭头。
- `merge.go` 的 `traitSimilarity`（word-Jaccard + CJK Levenshtein）**留在 L 特质合并**（dream 的 `MergeIntoL`）——它服务的文本形态（短句 trait）与 skill 描述不同，继续各守各的边界。
- P1 约束的**实质**（不新造相似度算法、复用仓库现成资产）完整保留；变更的只是资产点名，且本节即为变更记录。

### 8.3 字段映射其余项

`name→name`（kebab 校验沿用）、`content→Body` 直译、`tags→keywords` 接通（首个生产写入方；loader 的 keywords 字段本就透传进 `Extra`，manifest 不变，零缓存代价）。

### 8.4 存量双存的展示级告警（§3.5 的补充面）

⚡ review 渲染每条候选时，对其 `name+tagline+content` 与已装 skills 的 `description` 跑同一 `trigramJaccard`：≥0.60 出 `⚠ overlaps installed skill "X" — pressing y will double-store; consider s / editing the skill`。**纯展示，不设闸**——裁决权在已验证的 review 安全网，不在新规则里。

## 9. 实现里程碑（本卡不实现，排期供接卡参考）

| ms | 内容 | 预估 |
|---|---|---|
| **M-B1** | `skillmgr.WriteProjectSkill` + 测试（happy / Parse 拒 / 同名拒 / 原子写 / name 不一致拒） | ~0.5 天 |
| **M-B2** | `skilldistill.ConvertToSkill`（prompt + 解析 + 机械护栏 + CheckNovelty）+ ⚡ review `s` 全状态机 + 测试（含 ctx 取消、模态中途 q、reasoner 烂 JSON、description==tagline 拒回、落盘失败不终止） | ~2 天 |
| **M-B3** | 🛠 review `s` 直写 + 提示行更新 + 测试 | ~0.5 天 |
| **M-B4** | `seek skill stats --idle`（Set 外连接）+ ⚡ 模态双存告警 + 测试 | ~1 天 |
| **M-B5** | AGENTS.md skill 子系统段一行注记（`s` 落点 + WriteProjectSkill）+ pitfalls 记录 + v1.md 勘误收尾核对 | ~0.5 天 |

M-B1 单独可发布；M-B2 依赖 M-B1；M-B3/M-B4 相互独立。

## 10. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | ⚡ review 按 `s` → 触发转换调用；转换失败/超时出错误行且 review 存活（候选仍可 y/n/e/s/q） | 集成测试（fake client 注错） |
| 2 | 预览态展示重写后 description + body；`y` 落 `<project>/.seek/skills/<name>/SKILL.md`，文件过 `skill.Parse`，**无 `.install.json`**，**不写 M**（双存关闭） | 集成测试 + 文件断言 |
| 3 | 任一层同名 skill 存在 → `s` 拒绝并提示编辑既有；绝不覆盖 | 单元测试（三层各一例） |
| 4 | 转换产物与既有 skill/M 条目重叠 ≥0.60 拒回；0.35–0.60 GREY 警告上浮预览、可继续 | 单元测试（阈值边界） |
| 5 | `description == tagline`（逐字）→ 拒回（P1 机械护栏）；转换 prompt 含语义翻转正反例 | 单元测试 + prompt 快照 |
| 6 | 🛠 review `s` → 直写 project 级（零转换调用）；`y` 草稿流字节不变 | 集成测试 |
| 7 | 成功行含生效时机提示（takes effect next session） | 输出断言 |
| 8 | `seek skill stats --idle 720h` 列出窗口内零调用 skill（含从未调用），按 last_used 升序，`--json` 可脚本化 | 单元测试（空 stats / 垃圾行容忍 / join 完整性） |
| 9 | ⚡ 模态对与已装 skill 重叠 ≥0.60 的候选出 ⚠ 告警行（纯展示） | 单元测试 |
| 10 | M-SD2 自动路径行为零变化（无直写能力）；`.drafts/` 语义零变化 | 既有测试全绿（回归即红线） |
| 11 | skills 树写入仍仅发生于 `internal/skillmgr`（grep 断言无新写者） | CI/审查 |
| 12 | v1.md 三处 GC 勘误落盘且与 `score.go` 语义一致（宽限 30 天 → 正常参与） | 文档审查（本卡已交付，§7） |

## 11. 风险

| 风险 | 缓解 |
|---|---|
| 转换调用在模态内的延迟（Flash thinking 数秒~数十秒） | spinner 行 + 90s 上限；`n` 随时可退；转换只付一次 |
| 重写后的 description 质量差 → manifest 污染 | 预览态 y/n 人在环；§8.1 机械护栏挡最差情形（拷贝）；错了 git revert |
| manifest 随 `s` 使用逐渐膨胀 | 入口判重 + review 闸限速；`--idle` + `seek skill list` 观察；uninstall 出口 |
| 用户把 `s` 误解为「存草稿」 | 提示行明写 `install to project` + 成功行明写下次会话生效；草稿语义仍由 `y` 承载 |
| notes P1「复用 merge.go」的偏离被误读为违背约束 | §8.2 显式变更记录（资产换名、实质保留） |

## 12. 后续版本（明确不做进本设计）

- **跨层升格信号**（notes P2 #5）：recall_count 高的 M → 提示蒸馏为 skill；`.stats.jsonl` 低频 skill → 提示归档。`--idle` 是它的只读前菜。
- **skill 审计 pass**（notes P2 #6）：dream 对称的 evaluateStable——扫 skill body 与现行 M/AGENTS.md 的矛盾，产出 demotion 建议。这是「错且常用」风险的真正解法（§5.2 论证 1）。
- **AGENTS.md / pitfalls 三重存储治理**（notes §3.2.2/3）：同题材知识在 M / AGENTS.md / pitfalls 台账的分布策略。
- **跨项目升格**（project skill → user skill）：走既有 `drafts approve --user` 或手工迁移，不在 `s` 语义里。
- **M 蒸馏 prompt 携带 skill manifest 做提取期判重**：双存告警（§8.4）已覆盖展示侧；提取期注入 manifest 的 token 成本另评。
