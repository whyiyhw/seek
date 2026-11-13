# Feature: 减法机制 slim-loop —— 删代码与缩文档的流程层与自噬层（L2/L3）

**所属版本**：独立 feature PRD · 📐 设计稿 · 未排期（自进化内环第三块：pitfall→eval 是「失败 → 防回归」，skill-distill 是「会话 → 能力」，本文是「长出来的肉 → 代谢掉」）
**前置阅读**：立项决策（butler 2026-11-13「减法机制立项」：三层 L1 秤与网 / L2 账与刀 / L3 自噬循环）；[`feature-skill-distill.md`](feature-skill-distill.md)（人审闸同构——自动路径永不激活）；[`feature-goal.md`](feature-goal.md)（goal run 循环 + no-remote 守卫 + yolo-local）；[`feature-autopilot.md`](feature-autopilot.md)（worktree 原语 + 本地 commit 不 push 的默认）；[`feature-routines.md`](feature-routines.md)（cron 触发）；[`docs/narrative/seek.md`](../narrative/seek.md)（自我进化平台定位）；[`scripts/doc-budgets.tsv`](../../scripts/doc-budgets.tsv) + [`scripts/verify-doc-budgets.sh`](../../scripts/verify-doc-budgets.sh)（已落地的预算先例）
**状态**：📐 设计稿。本文只定义流程与口径，无代码。**依赖两张在飞 L1 卡**：`c_vt7fiw`（weight.sh 体重秤 + budget.json 预算清单，只读只报）、`c_rtjjsl`（首轮 deadcode/unused 扫描候选清单，只报不删）——L1 产物未合并前，巡检（M-SL1）与 L3 首跑（M-SL3）都不能启动。

> **一句话**：给 seek 立一条与「怼功能」对偶的「删冗」流程——**秤先上（L1，在飞）、账与刀进流程（L2，本文）、最后让 seek 自己夜里挥刀、人早上验收（L3，本文）**。安全网只有两张：代码减法行为不变，文档减法信息不丢。

---

## 0. 卡片五问 → 章节映射

| 卡片问题 | 章节 | 结论速览 |
|---|---|---|
| ① 触发（每版一刀 / 双周巡检 / 预算超限） | §2 | 三触发器并存：版本线强制 ≥1 刀（`Slim:` trailer 记账，git 侧机械可查）；双周巡检读四路证据开 ≤3 张带证据卡；预算 gate 变红即开卡，不等巡检 |
| ② 验收（代码=行为不变 / 文档=信息不丢） | §3 | 代码：`go vet` + `go test -race` 全绿 + `eval/run.sh` 全例不回归（单次红是 signal 不是 proof，判回归须双轮）；文档：被删信息三去向必有其一——指针、归档、本就该代码承载 |
| ③ 产品级自噬 | §4 | cron 夜间在 worktree 里跑 `seek goal run`，本地 commit 绝不 push，晨间人审 diff 后才可能进 main——与 skill-distill 人审闸同构，作为平台「代谢」示范 |
| ④ 防自噬 | §5 | 机制自身代码 ≤500 行（budget.json 自己携带该条目，吃自己的药）；eval 用例仅 flaky/过时两口径可删且必须归档；修剪用例的卡与使用该用例的减法卡强制分离（裁判不裁自己的案子） |
| ⑤ 与 doc-budget 纪律的关系 | §6 | doc-budgets.tsv 是已验证的局部先例；budget.json 是泛化载体，**同船吸收** doc-budgets.tsv，单一载体单一 gate，任何时刻不允许两 manifest 并存 |

## 1. 动机与定位

### 1.1 为什么需要减法

用户原话（立项记录）：无脑怼代码，可维护性差。seek 的自进化内环目前只有合成方向——pitfall→eval 把失败变成资产、skill-distill 把成功变成能力；没有任何机制负责**分解代谢**：死代码、复述型文档、没人用的 skill / 工具分支只会累积。功能卡天然有动力（有人要），减法卡天然没动力（没人催）——所以它必须是**流程强制的**，不能靠自觉。

### 1.2 三层切分（立项已拍板）

| 层 | 名字 | 内容 | 状态 |
|---|---|---|---|
| L1 | 秤与网 | weight.sh 体重秤 + budget.json 预算清单 + deadcode 扫描（**只报不删**）；安全网 = go test + eval 全例「行为不变」口径 | 在飞：`c_vt7fiw`（秤 + 预算）、`c_rtjjsl`（首扫清单） |
| L2 | 账与刀 | 每版一刀规矩 + 双周减法巡检 + 文档「信息不丢」口径 | **本文 §2–§3** |
| L3 | 自噬循环 | seek goal run 夜间自瘦身 + worktree 隔离 + 人审 diff | **本文 §4** |

L2 是人的流程（卡片驱动），L3 是把同一流程交给产品自己跑（goal 驱动）。**L3 不引入新验收口径——它跑的就是 §3 的验收命令**；L3 新增的只是执行者（产品自己）与时间（夜里）。

### 1.3 定位：自我进化平台的「代谢」示范

叙事稿给 seek 的目标是「能自我进化的本地 agent 平台」。只会长肉的平台不算自我进化。L3 的意义超出省下的行数：**seek 用自己的 goal / cron / worktree / webhook 瘦自己**，是平台能力闭环的可演示 dogfood——合成代谢（skill-distill）与分解代谢（本文）合起来才是完整的「代谢」。

---

## 2. 触发（①）

三个触发器并存，信号源、节奏、产出各不相同：

| 触发器 | 信号源 | 节奏 | 产出 | 谁执行 |
|---|---|---|---|---|
| 每版一刀 | 版本线发布（v0.N.0 cut） | 每个 minor 版本线 ≥1 张 | ≥1 张合并的减法卡（`Slim:` trailer 记账；零刀须记零刀说明） | 功能开发顺带 / 巡检储备 |
| 双周巡检 | weight 趋势 + deadcode 清单 + skillstats + 工具使用率 | 双周一次 | ≤3 张带证据卡 | butler 主会话 |
| 预算超限 | generalized budget gate（CI，§6）变红 | 即时 | 1 张减法卡（或 PR 内显式 raise + 理由） | 谁看见谁开 |

### 2.1 每版一刀

**规矩**：每个 minor 版本线（v0.N——对应一条 `docs/prd/vN.md` 的交付线）合并 **≥1 张行为不变的减法卡**。

**记账（合并账可统计）——双账本，互为冗余：**

- **git 侧（机械账，权威）**：减法 commit 独立成 commit（不与功能改动混船），message 带 `Slim: <对象与实测减重>` trailer——完全仿 `Pitfall:` trailer 先例。核对命令：`git log v0.9.0..v0.10.0 --grep='Slim:'`，≥1 条即达标。不为此新增脚本；若日后需要固定化，属 L1 范围（秤与仪表）。
- **butler 侧（叙事账，可溯）**：卡片标题带「减法」二字（建卡即带），事件记实测减重。`memory_search 减法` 可回溯每刀的来龙去脉。巡检顺带对账：机械账查得到而叙事账缺失 → 补事件；反之 → 查 commit 是否漏带 trailer。

**零刀说明**：版本线内确无可删（候选清单空、预算全在 target 内）时**不硬凑**——主会话在 butler 记 `kind=note` 事件「v0.N 零刀：<理由>」。为凑数硬删比不删更伤。零刀说明本身进合并账：`git log` 查不到 `Slim:` + butler 查得到零刀说明 = 该版本已履约。

**口径**：功能下线（如柱 Q OCR 随 feature-vision 同船移除）本质是产品决策，走功能卡；但其 commit 若主要 diff 是删除，可自愿带 `Slim:` trailer 计入一刀。宽松口径，防规则律师。

### 2.2 双周减法巡检

**节奏**：双周一次，挂在 butler 主会话既有晨间刷新上（逢单周周一）；首版不另设提醒机制（cron 化属 M-SL3 之后的可选项）。

**输入（四路证据，全部只读）：**

1. **weight 趋势**——L1 体重秤的读数序列。单次读数无意义，趋势才有意义（哪棵子树在持续变胖）。读数台账属秤的一部分（L1 评审决定落盘形态）；巡检只消费，不新建状态文件。
2. **deadcode/unused 候选清单**——重跑 L1 扫描（只报不删），与上次清单 diff：新增候选 = 新卡素材。
3. **skillstats**——`paths.UserSkillStats()` 的 skill 调用统计 JSONL。安装后长期零调用的 skill 是「能力肥胖」候选（卸载候选，人拍板）。
4. **工具使用率**——session 转录（`~/.seek/projects/*/*.jsonl`）里的 `tool_start` 计数，或 inspect-rpc stats 口径。长期零调用的工具 / 工具分支是候选。

**产出**：≤3 张带证据卡。每张卡必须带：候选对象（文件:行 / 文档段落）、证据数字（引用数 0 / 调用数 0 / 字数趋势）、预期减重（行数或字数）、验收命令（§3 对应口径）。「带证据」是硬要求——「感觉这文档太长了」不构成开卡理由。

**为什么 ≤3**：减法与功能开发抢同一个派发池。上限是流程级防自噬（§5.3）——巡检的作用是持续供刀，不是淹没队列。三张都开不完说明候选枯竭，是健康信号。

### 2.3 预算超限即开卡

generalized budget gate（§6：doc-budgets 的泛化形态）变红 = 与任何红 CI 同级阻断。修复动作二选一：

- **开减法卡**：把超限对象缩回 ceiling 内（默认路径）；
- **显式 raise**：在 PR 里改 budget manifest 并写明理由（沿用 doc-budgets.tsv 既有规矩「Raising a ceiling requires explicit justification in the PR」）。

不等双周巡检——红 gate 本身就是带证据的开卡理由。

---

## 3. 验收（②）

减法只有两种对象，两种口径。共同点：**减法卡的 diff 必须独立成 commit**，回滚 = 单 commit revert。

### 3.1 代码减法 = 行为不变

| 闸 | 命令 | 口径 |
|---|---|---|
| 静态 | `go vet ./...` | 零告警（CI 同款） |
| 单测 | `go test -race ./...` | 全绿（CI 三 OS 同款） |
| 行为 | `eval/run.sh`（全例） | **不回归**：无「持续绿 → 红」用例 |

eval 的随机性口径（`eval/README.md` 已立）：单次红是 signal 不是 proof。减法卡的验收跑**两轮**（或查 `eval/results/` 趋势确认该用例在基线二进制上稳定绿）后才判「回归成立」。当前 20 例 × $0.02–0.05 ≈ 每轮 $0.4–1.0，两轮 $1–2/卡，可接受。

**diff 形态约束**：纯删除，或行为等价简化（删间接层、并重复逻辑）。任何行为变化——哪怕更好——都不是减法卡，拆出去走功能卡。这是「行为不变」能当安全网的前提。

**已知盲区**（写进每张代码减法卡的执行提醒）：deadcode 类工具看不见反射 / JSON 序列化 / 跨进程契约引用。零引用清单只报不删（L1 原则）；减法卡执行时必须全仓 grep + 查 `docs/test-plan-*.md` 契约用例；涉及会话格式的符号另要考虑老 session 文件的加载兼容（schema_version ≤ 1 迁移路径是先例）。

### 3.2 文档减法 = 信息不丢

被删的每一段信息，三去向必有其一：

| 去向 | 判定 | 先例 |
|---|---|---|
| **指针** | 信息本该住在别处（pitfall / PRD / reference doc），原地留一行指针 | verify-doc-budgets.sh 的原话「leave a pointer」；AGENTS.md 指向 docs/prd/ 的既有写法 |
| **归档** | 被取代的设计/记录整体 `git mv` 进归档区，取代处留链接 | `docs/prd/archive/`：feature-image-ocr.md 被 feature-vision.md 取代即走此路径，README 表内留链 |
| **本就该代码承载** | 文档在复述代码的行为/字段——复述即债，删掉不算丢信息 | 「wire-format strings are contracts」的口径：代码是唯一事实源 |

**验收动作**（卡片 checker 人工抽查，不做机械 gate）：取被删段的 2–3 个关键事实（数字/命令/文件名），grep 全仓 + docs/ 仍可解析到出处；归档件在 archive/ 可寻回。

**禁止**：直接 `rm` 被取代的设计文档（必须归档）；把多处复述「压缩」成一处新复述——两处复述变一处复述还是复述，那不是指针，是复述的反模式。

### 3.3 减法卡模板

```
标题：减法：<对象一句话>（预期 -N 行 / -N 字）
- 候选来源：巡检 2026-11-24 第 2 路 / deadcode 清单第 K 项 / budget gate 红
- 证据：<全仓引用数 0 | 90 天调用数 0 | 词数 3200 超限 300 | …>
- 动作：删 X / 缩 Y（指向 Z）/ 归档 W
- 验收：go test -race 全绿 + eval/run.sh 两轮不回归（代码）｜三去向抽查（文档）
- 记账：实测 -N；commit 独立、带 Slim: trailer
```

---

## 4. 产品级自噬（③）—— L3 自噬循环

### 4.1 形态：goal run × worktree × 人审 diff

夜间一条 cron 拉起，在 **worktree 里**跑 `seek goal run`：

```
cron（夜间，柱 H）
  → 建/进 worktree（柱 G 原语：seek worktree / enter_worktree）
  → seek goal run "<slim goal>"    # yolo-local + no-remote 守卫（M-goal.4 同款）+ 可选柱 O 沙箱 confine 到 worktree
  → goal 循环死磕到 judge 判 met / 触 Caps 上限（max-turns / timeout / token-budget / stall）
  → 产出：worktree 内本地 commit（绝不 push——autopilot 同款默认）
  → webhook 摘要推手机（柱 M：删了什么、减重多少、测试状态）
晨间：人审 git diff main → merge 进 feature 分支，或整个 worktree 丢弃
```

**为什么 goal run 而不是 autopilot run**：减法目标是单任务死磕（一个清单删到绿），autopilot 的 decompose + fan-out 是为多任务团队设计的，对单目标是多余一层编排。复用的是**柱 G 的 worktree 原语与柱 N 的守卫原语**，不复用 decomposer。若日后减法候选成批（多方向并行删），升级为 autopilot fan-out、每任务一 worktree——留作 L3.b，不进首版。

**示例目标（立项给定）**：「删除 deadcode 清单前 N 项零引用导出符号；`go test -race ./...` 全绿；`eval/run.sh` 全例不回归；本地 commit；不 push」。judge（便宜模型）判 met 的依据就是这几条——**goal 的条件文本即 §3 的验收口径，不另造标准**。

**安全边界全继承**：goal 跑主 agent（非子代理），故用 M-goal.4 同款 `bash.WithDeny(IsRemoteMutating)` no-remote 守卫；yolo-local 只放行本地写、远程仍被守卫挡；可选柱 O OS 沙箱 confine 到 worktree 目录；Caps 全部沿用，防夜里空转烧 token。

### 4.2 与 skill-distill 人审闸同构（自动路径永不进 main）

| | skill-distill | slim-loop L3 |
|---|---|---|
| 自动路径能做的 | 产草稿到 `.drafts/`（loader 不可见） | 产本地 commit 到 worktree（main 不可见） |
| 激活动作 | install——人的动作 | merge——人的动作 |
| 自动路径永不 | install | push / merge / 开 PR |

这是本仓自动化的统一形状（RSI 同构）：**机器可以提出，激活永远是人**。L3 的 diff 早上必须有人看过才可能进 main——「代谢」不等于「自切」。

### 4.3 作为平台示范的意义

L3 首跑本身就是平台能力的 e2e 演示：cron 触发（柱 H）→ goal 循环（v0.8.x）→ worktree 隔离（柱 G）→ no-remote 守卫（柱 N）→ webhook 摘要（柱 M）→ 人审闸。每一环都是既有零件，焊接方式与 autopilot 不同但同构——「平台」二字的具体含义。叙事稿的「自我进化闭环（失败 → eval → 修复 → 发布）」由此补上反向的一半：**发布 → 瘦身 → 更易维护 → 失败更少**。

---

## 5. 防自噬（④）

机制自身必须吃自己的药，否则减法机制就是新的肥胖源。

### 5.1 机制自身代码预算 ≤500 行

**计量口径**：slim 机制的全部机器产物——weight.sh、budget gate 脚本、budget manifest 的解析/校验代码、L3 包装脚本、巡检脚本（若有）。文档（本文、模板）不计行数（docs 面由 word 预算管，若被列入 manifest）。

**载体**：budget manifest 里给上述文件立**合计 ≤500 行** ceiling，由 generalized gate 执行——**机制自己的预算写进自己的清单**，超限即红，修复方式是给机制自身开减法卡。这是最有力的自证：如果减法机制需要 600 行才能描述「怎么删东西」，那是设计的失败，不是预算的失败。

500 是立项拍板的数字。含义：够 cloc/wc/grep + 一个 manifest 校验 + 一层 cron 包装；不够任何「slim 平台」（dashboard、数据库、daemon）。不够就砍需求，不是抬预算——raise 需要显式理由 + 用户过目。

### 5.2 eval 用例修剪口径

eval 用例是代码减法验收的安全网（§3.1），删安全网给减法铺路是终极自噬。口径：

**只有两个理由可以删用例**：

1. **过时**：用例探测的行为已不存在（产品下线，如柱 Q OCR 路径），或被**严格更强**的用例取代（同一行为、更严 bounds）。
2. **flaky**：同一二进制版本下 pass/fail 交替到 bounds 无法学习——证据必须是 `eval/results/` 里同版本的多行交替记录，不是「上次跑挂了」。

**修剪动作 = 归档，不是删除**：`git mv eval/cases/<name> eval/archive/<name>`（run.sh 只扫 `cases/`，移出即失效；README 与 results 历史随 git 保留——镜像 `docs/prd/archive/` 先例）。

**分离原则（裁判不裁自己的案子）**：修剪用例的卡，必须独立于、且晚于使用该用例做验收的减法卡——同一张卡里既删代码又删对应 eval 用例，验证车道直接拒。

### 5.3 流程级防自噬

- 巡检 ≤3 卡/双周（§2.2）——供刀不淹队列。
- 每版一刀只设下限 ≥1，不设 KPI 上限——防「刷刀」激励（账本同时记实测减重，删一行注释刷 trailer 无收益）。
- 零刀说明（§2.1）——防为履约硬凑。
- 减法 commit 独立（§3）——防借减法之名夹带功能。
- 机制自身 ≤500 行（§5.1）——防机制肥胖。

---

## 6. 与既有 doc-budget 纪律的关系（⑤）

### 6.1 先例盘点（已落地的 L1 局部）

`scripts/doc-budgets.tsv` + `scripts/verify-doc-budgets.sh` + CI `doc-budgets` job 已在 main 上跑通先例：standing docs 词数 ceiling/target、超限即冻结（frozen-at-current，只能缩不能涨）、缩回 target 后 ceiling ratchet down、raise 需要 PR 显式理由、文件缺失也 fail。**这是已验证的设计，不是提案**——slim-loop 的预算面全部继承这套语义，一个字不新造。

### 6.2 泛化 = 单一载体，同船迁移，绝不并存

budget manifest（L1 在飞卡的载体）把预算面从「standing docs 词数」泛化到三类对象：**docs（词数）+ 代码面（行数）+ slim 机制自身（合计行数，§5.1）**。

铁律：

- **doc-budgets.tsv 的两条目（AGENTS.md / CLAUDE.md）在同一次改动里迁进新 manifest**，verify 脚本改读新载体（或被 generalized gate 吸收退役），tsv 删除。CI job 语义保留（job 名沿用 doc-budgets 或改 budgets 皆可）。
- **任何时刻不允许两个预算 manifest 并存**——那正是「另起炉灶」的字面义。泛化的含义是老条目搬进新家，不是新家盖在老家旁边。
- ratchet / frozen / raise-needs-justification / missing-fails 四条语义原样搬。语义要变，须在本文档记决策。

### 6.3 命名碰撞警示（交给 L1 评审，本文不拍板）

seek 已有 `internal/budget`（context window 预算）和 `docs/guide-budget.md`——**budget 一词在本仓有主**。立项决策写的 `docs/budget.json` 与之撞名。本文只立「单一载体」原则；文件名（budget.json / slim-budget.json / …）与它该住 docs/ 还是 scripts/（它更像数据不像文档），留给 `c_vt7fiw` 评审定夺。无论叫什么：**文件头注释必须自述范围**（slim-loop 预算清单，与 context budget 无关）。

---

## 7. 里程碑

| 里程碑 | 内容 | 前置 |
|---|---|---|
| **M-SL1** | 双周巡检首跑：读 L1 四路证据，开首批 ≤3 张带证据卡 | `c_vt7fiw` + `c_rtjjsl` 合并 |
| **M-SL2** | 每版一刀记账生效：下一个 minor 版本线发布时核对 `Slim:` 账（或零刀说明）；AGENTS.md 提案（附录 A）由主会话呈用户批 | 本文合并 |
| **M-SL3** | L3 夜间自瘦身首跑 e2e：cron → worktree → goal run 删首批零引用导出符号 → 测试全绿 → 晨审 merge | M-SL1 + deadcode 清单稳定 |
| **M-SL4**（可选，不排期） | budget gate 泛化收口：doc-budgets.tsv 同船迁入、单 gate 执行 §5.1 的 500 行预算 | `c_vt7fiw` 落地形态确定 |

## 8. 风险

| 风险 | 缓解 |
|---|---|
| deadcode 盲区（反射 / JSON / 跨进程契约）造成行为回归 | 清单只报不删；执行卡必须全仓 grep + 查 test-plan 契约；§3.1 eval 双轮口径兜底 |
| 减法卡验收成本（eval 全例 ×2 ≈ $1–2/卡） | 巡检只开卡不执行；执行集中在卡内一次做完 |
| judge 判 met 过宽（L3 夜里删过头） | goal 条件文本 = 验收命令本身；Caps 上限；晨审 diff 是最后闸，worktree 丢弃零成本 |
| 双账本漂移（Slim trailer 与 butler 卡对不上） | trailer 是机械账（权威），butler 是叙事账；巡检顺带对账（§2.1） |
| 巡检节奏与版本节奏脱钩 | 刻意脱钩：每版一刀按版本线、巡检按双周，互不依赖、各管各的触发 |

## 9. 不做什么（首版边界）

- ❌ **本卡改 AGENTS.md / CLAUDE.md**——「每版一刀」进 AGENTS.md 由主会话呈用户批（附录 A 是提案文本），本卡只出文档。
- ❌ **自动路径 merge / push**——L3 永远只产 worktree 本地 commit，人审是唯一进 main 的门。
- ❌ **两个预算 manifest 并存**（§6.2 铁律）。
- ❌ **新工具链依赖**——扫描工具选型属 L1（`c_rtjjsl`）范围；原则：只报不删、零 runtime 依赖、单二进制哲学不动摇。
- ❌ **减法机制平台化**（dashboard / 数据库 / daemon）——500 行预算就是它的反义词。
- ❌ **行为改变冒充减法**——diff 里藏行为变化的卡直接打回（§3.1 形态约束）。

---

## 附录 A：「每版一刀」AGENTS.md 提案（呈用户批，本卡不落）

以下为提案文本草案，由主会话呈用户批准后落 AGENTS.md（CLAUDE.md 按同步规则镜像）：

> ```markdown
> ## 减法纪律（slim-loop）
> 每个 minor 版本线（v0.N）合并 ≥1 张行为不变的减法卡：commit 独立、message 带
> `Slim: <对象与实测减重>` trailer；确无可删时在 butler 记零刀说明。代码减法验收
> = `go test -race` 全绿 + `eval/run.sh` 全例不回归；文档减法验收 = 信息不丢（指针 /
> 归档 / 代码承载三去向）。流程全貌见 docs/prd/feature-slim-loop.md。
> ```

注：AGENTS.md 当前处于 doc-budgets.tsv 冻结预算下（超 target、ceiling 冻结在当前值）——落这段需同船缩等量词数，或等文档先回到 target。这正是减法纪律给自己上的第一课。
