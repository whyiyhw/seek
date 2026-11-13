# Feature: Skill 自动蒸馏（auto skill-distillation）——S→K 蒸馏、草稿区、人审闸

**所属版本**：独立 feature PRD · 📐 设计稿 · 未排期（自进化内环第二块；第一块 pitfall→eval 见 butler 卡 `c_w7kob6`）
**前置阅读**：[`v1.md`](v1.md) §6（`/distill`、`memory_observe`、auto-dream，及 M5.7「SessionEnd 调 reasoner」的弃用史）、[`v2.md`](v2.md)（skill 生命周期 + §6.1 AI 侧两段式安装）、[`docs/narrative/seek.md`](../narrative/seek.md)（自进化闭环定位）、本仓 `AGENTS.md`（「`~/.seek/skills/` 写盘只许 skillmgr/skillstats」约束）、[`internal/skill/loader.go`](../../internal/skill/loader.go)（dotfile 跳过是本设计的承重墙）、[`internal/memory/hook.go`](../../internal/memory/hook.go)（`OnSessionEnd` no-op 注释 = v1 弃用实录）
**状态**：📐 设计稿；M-SD1（/distill 扩展手动链路）已实施（butler 卡 `c_j5qzb7`，2026-11-13）。M-SD2/SD3 未动。
**预估工作量**：~6 天（M-SD1 ~3d / M-SD2 ~2d / M-SD3 ~1d）。

> **一句话**：从会话历史里自动蒸馏「值得复用的程序性知识」，以 Anthropic skill 包**草稿**形态落进隔离草稿区；**自动路径永远不可能**把任何东西写进 loader 可见的位置——安装是且仅是人的动作。这给自进化内环补上「会话 → 能力」的出口（另一出口是 pitfall→eval 的「失败 → 回归用例」）。

---

## 0. 卡片四问 → 章节映射

| 卡片问题 | 章节 | 结论速览 |
|---|---|---|
| ① 触发点（SessionEnd 钩子 vs `/distill` 扩展 vs cron 批量） | §3 | `/distill` 扩展先行（M-SD1）；自动路径 = **下次 SessionStart 后台批量补蒸馏**（auto-dream 同构，M-SD2）；SessionEnd 同步调 reasoner 与 cron 均否决 |
| ② 新颖性判定（与 skill 库 / L 记忆查重，避免每会话产垃圾） | §4 | 四层漏斗：机械门槛 → 提示词内判重 → 机械查重 → 语义过滤；外加**跨会话复发累积**（N≥2 同构）与草稿区 GC |
| ③ 产出形态（Anthropic skill 布局，对齐 `internal/skill` loader） | §5 | `~/.seek/skills/.drafts/<name>/SKILL.md` + `.distill.json` sidecar；草稿必须能被 `skill.Parse` 直接解析（dot 目录天然不被 loader 扫描） |
| ④ 人审闸（草稿区隔离，install 前必须过目——与 RSI 同构） | §6 | 草稿区对 loader 不可见；install 是唯一的激活动作，走 `skillmgr` 既有安装链 + scope 选择；自动路径无 install 权限 |

## 1. 动机与定位

### 1.1 自进化内环的第二块

叙事稿（`docs/narrative/seek.md`）给 seek 的目标是「能自我进化的本地 agent 平台」，内环 = 失败 → eval → 修复 → 发布。两块拼图：

- **pitfall→eval**（`c_w7kob6`）：把踩坑记录变成回归用例——**失败**沉淀为防回归资产。
- **skill 自动蒸馏**（本文）：把会话里摸索出的**成功做法**沉淀为可复用能力。

v2 已经把 skill 的「装—用—看—卸」闭环做完（install/stats/status/uninstall），缺的正是「**产**」：今天一个用户在 seek 里摸索出一套多步骤操作流程（比如「修 TUI 状态机 bug 的定位顺序」），下次会话一切从零——它既不是 M 层该记的决策，也没有任何机制把它升格成 skill。

### 1.2 什么该是 skill，什么不该（K 与 S/M/L 的分界）

记忆三层回答的是「**知道什么**」；skill 蒸馏回答「**会做什么**」。判据：

| 维度 | M 条目（决策/事实） | L 条目（用户特质） | **K = skill（程序性知识）** |
|---|---|---|---|
| 内容 | 本项目做过什么决定、为什么 | 用户是什么样的人 | **一套可重复执行的步骤**：触发条件 → 工具序列 → 验证方式 |
| 形态 | name + tagline + ≤500 词 | 特征句 + 来源 | `<dir>/SKILL.md`（frontmatter + 正文） |
| 注入成本 | 索引常驻（~3k tokens 上限） | ≤500 tokens 硬上限 | **name+description 进每个未来会话的 manifest，永久** |
| 激活 | `memory_recall(name)` | 常驻 | 模型看 manifest 自主调 `Skill(name)` |
| 写入路径 | `/distill` / `memory_observe` | 仅 dream | **本文：自动产草稿 + 人审安装** |

**skill-worthy 判据**（提取提示词的硬要求）：(a) **程序性**——至少两步以上的工具操作序列或明确的操作流程，不是单条事实；(b) **有触发条件**——未来什么情况下该照着做，能写成具体的 when-to-use（"修 X 类 bug 时"是好例子，"写代码时"是坏例子）；(c) **跨会话可复用**——同样的流程在别的会话/别的项目还成立。

**反例**（必须拒绝）：一次性项目决策（→ M 路径）、用户偏好（→ L 路径，dream 管）、通用编程常识（哪儿都不去）、单会话特有的一次性操作（哪儿都不去）。

### 1.3 为什么 description 是全系统杠杆最高的一个字段

`Set.Manifest()` 把每个 skill 的 name+description 注进**每个未来会话**的 system prompt（[v2 §6](v2.md) prefix-cache 约束：manifest 启动时固定）。这意味着：

- 一条好 description = 模型在未来无数会话里**自主想起**这套流程的触发器；
- 一条烂 description = 永久的 prefix token 死重 + 模型永不用它。

所以蒸馏的质量要求**重心压在 description 上**（§5.3），且安装闸（§6）要求人过目时首先看它。这也解释了为什么新颖性判定（§4）的门槛要远高于 `/distill`——M 条目写歪了污染的是一个项目的索引，skill 写歪了污染的是所有未来会话的 prompt。

## 2. 总览

```
                ┌──────────────────────────────────────────────────┐
                │  S  会话历史（~/.seek/projects/<pid>/sessions/…） │
                └──────────────────────┬───────────────────────────┘
                                       │  触发（§3）：
                                       │  M-SD1  /distill 扩展（人在场）
                                       │  M-SD2  下次 SessionStart 后台批量
                                       ▼
                ┌──────────────────────────────────────────────────┐
                │  提取（reasoner，Flash/Pro + thinking）            │
                │  输入 = 会话誊本 + 既有 skill manifest + M 索引     │
                │  输出 = ≤N 条候选；空数组是合法且常见的结局         │
                └──────────────────────┬───────────────────────────┘
                                       ▼
                ┌──────────────────────────────────────────────────┐
                │  新颖性漏斗（§4）：L0 机械门槛 → L1 提示词内判重    │
                │  → L2 机械查重 → L3 语义过滤 → L4 复发累积         │
                └──────────────────────┬───────────────────────────┘
                          通过（或人工确认）│ 被拒（静默或给理由）
                                       ▼                     ▼
        ┌─────────────────────────────────────────┐      丢弃
        │  草稿区 ~/.seek/skills/.drafts/<name>/   │
        │  SKILL.md（loader 不可见：dot 目录跳过）  │
        │  .distill.json（证据/来源/复发数/scope）  │
        └──────────────────────┬──────────────────┘
                               │  人审闸（§6）：
                               │  /distill 模态 y → scope 选择 → install
                               │  /skill drafts 收件箱随时补审
                               │  自动路径：只能写草稿，到此为止
                               ▼
        ┌─────────────────────────────────────────┐
        │  skillmgr.Install（既有安装链）           │
        │  user:  ~/.seek/skills/<name>/           │
        │  project: <cwd>/.seek/skills/<name>/     │
        │  → 重启会话后 manifest 生效               │
        └─────────────────────────────────────────┘
```

## 3. 触发点（①）

### 3.1 四个候选的评估

| 候选 | 结论 | 理由 |
|---|---|---|
| **A. `/distill` 扩展**（用户在会话内主动触发） | ✅ M-SD1 先行 | 复用既有心智（用户已经学会"收尾时跑一下 /distill"）；review 模态（y/n/e）现成；人在场 = 新颖性门槛可以放宽到单会话（人就是闸）。零自动化风险，先把「产草稿 → 审 → 装」链路跑通 |
| **B. SessionEnd 钩子调 reasoner** | ❌ 否决 | **本仓已有失败史**：v1 M5.7 在 `OnSessionEnd` 里调 reasoner，v2 整体移除（`internal/memory/hook.go` 注释实录：启发式满意度检测 + 90s 超时卡进程退出）。会话结束路径必须保持零 API 调用 |
| **C. cron / launchd 批量** | ❌ 否决 | 违反 v5 零-daemon 铁律（常驻只许活在 `seek serve` sidecar）；调度配置在二进制之外，违背单二进制交付；且批跑完的 review 没有自然入口 |
| **D. 下次 SessionStart 后台批量补蒸馏**（吸收 B/C 的自动化价值） | ✅ M-SD2 | **auto-dream 同构**（v1 §6 M5.8）：SessionStart 检查 cadence → 到期则在后台 goroutine 跑（不阻塞首轮 Prompt，没有 90s 问题——进程还活着，goroutine 慢慢跑）；批量读多个会话文件让「复发判定」成为可能；scrollback 通知用户"有新草稿" |

D 的具体形状：状态文件 `~/.seek/projects/<pid>/skill-distill-state.json`（镜像 `dream-state.json`：`last_distill_at` + `sessions_since_distill`）。SessionStart 时 `sessions_since_distill ≥ 8` 或 `距上次 ≥ 7 天` 才触发；批处理对象是**当前项目**自上次以来的未处理会话（会话按项目归档在 `~/.seek/projects/<pid>/sessions/<sid>/`，[paths.go](../../internal/paths/paths.go) L179，天然按项目切好）。env `$SEEK_AUTO_SKILL_DISTILL` 总开关，**首个交付版本默认关**——auto-dream 有实时通知做安全网，这里的安全网只是"草稿不会自己装"（更弱），先 dogfood 再决定默认值。

### 3.2 为什么不是 memory_observe 式的实时工具（skill_observe）

memory 的演进序是 `/distill`（手动）→ `memory_observe`（实时模型触发）。skill 暂不跟进而去补 D，因为：**(a)** 决策信号在对话中段就能确定（"我们刚拍了板"），而**程序性知识的边界要到会话末尾才清楚**——流程是否可复用、验证是否通过，都取决于后续轮次；**(b)** `skill_observe` 每次调用都要带全量 manifest 做判重上下文，工具面膨胀；**(c)** 草稿区与人审闸（§5/§6）必须先存在，实时工具才有地方落。列为后续版本（§11），演化顺序与 memory 一致。

## 4. 新颖性判定（②）——四层漏斗 + 复发累积

目标：**空会话不产草稿、重复会话不产重复草稿、已覆盖的知识不产第 N+1 份**。漏斗自上而下成本递增，越便宜的越早挡。

### L0 · 机械门槛（确定性，零 API）

- 会话誊本 ≥ 20 轮消息且含 ≥ 3 次工具调用（程序性知识的必要非充分条件——纯问答会话在此挡掉）；
- 会话所属项目可解析（有 `~/.seek/projects/<pid>/`；无项目的临时会话不参与自动路径）；
- **加分信号**（提优先级，非必要条件）：誊本里出现 `Pitfall:` commit trailer——修过坑的会话正是「踩坑→绕过」流程最密集的地方，与 pitfall→eval 卡（`c_w7kob6`）形成同一信号的两条消费路径。

### L1 · 提示词内判重（提取调用自带）

提取 reasoner 的输入**必须**包含：三源（project/user/builtin）全部已装 skill 的 name+description（即 `Set.Manifest()` 的原文）+ 当前项目 M 索引（name+tagline）。系统提示词硬性要求（沿用 `DistillSystemPrompt` 的空数组纪律——"DO NOT invent weak candidates to fill the quota" 是 v1 蒸馏不产垃圾的关键句）：

- 与既有 skill 的 description 语义重叠的候选**不输出**；
- 该内容更像项目决策（M 已覆盖或该进 M）的**不输出**；
- 无合格候选时返回 `[]`。

### L2 · 机械查重（解析后、落草稿前，确定性）

- **name 碰撞**：候选 name 命中已加载 `skill.Set` → 直接拒绝，理由回显「与已装 skill `<name>` 冲突；改进它请走 `/skill update` 流程」（首个交付范围不做 supersedes/diff 更新流，见 §11）；
- **文本重叠**：候选 description+body 与既有 skill description 做 trigram Jaccard：≥ 0.6 拒（判为同物）；0.35–0.60 灰区 → 送 L3 并把那条 skill 的 body 一并塞进过滤上下文；< 0.35 过。同法对 M 条目的 name+tagline 做一遍（「这条其实是个决策」的机械信号）。

### L3 · 语义过滤（一次 Flash thinking 调用）

复用 `Distiller.Filter` 的 ACCEPT/REJECT 模式（[internal/memory/distill.go](../../internal/memory/distill.go)），上下文 = 全部既有 skill manifest + 该项目 M 条目 + 灰区命中的 skill body。拒绝理由落进草稿 sidecar（人审时可见为什么差点被毙，也方便调阈值）。**手动路径**（`/distill`，人在场）可跳过 L3——人是更强的过滤器；**自动路径**必过。

### L4 · 复发累积（dream 的 N≥2 同构，只在自动路径）

自动路径的候选落草稿时先与草稿区既有草稿按 name + description 相似度**合并**——命中则该草稿 `recurrence++`、`sources` 追加本次会话；未命中则新建草稿，`recurrence=1`、`status: "weak"`（**不进** `/skill drafts` 收件箱，只占 GC 配额）。`recurrence ≥ 2`（两个不同会话各自独立提取到同一流程）才升 `status: "reviewable"`，浮出收件箱。**FilterByEvidence 的同构**：一个模式只在单会话出现过，更可能是该会话的特例而非可复用能力。手动路径（`/distill`）不受此限——用户亲自触发，草稿直接以 `status: "reviewable"` 落盘，当场审。

跨项目复发自然升级 scope 建议：sources 里出现 ≥2 个不同 project_id → sidecar 的 `suggested_scope` 从 `project` 升 `user`（草稿区是全局的，本身就是跨项目累积器——这是把草稿区放全局而非每项目一份的核心理由，见 §5.1）。

### 草稿区 GC（防累积垃圾）

- 未审草稿存活 **14 天**，下次 SessionStart 批处理时自动清除（通知一行）；草稿区上限 **10 份**，超限最旧先清。宁可让 reasoner 下次重蒸，也不让收件箱变垃圾场。
- `skillstats` 反哺（后续版本，§11）：候选与一个**从未被调用过**的已装 skill 高度重叠时，提示「合并/替换」而非新增。

## 5. 产出形态（③）

### 5.1 草稿区：`~/.seek/skills/.drafts/`

```
~/.seek/skills/
├── .stats.jsonl            # 既有（v2 调用统计）
├── .drafts/                # ★ 新增：草稿区（loader 不可见）
│   └── fix-tui-state-bug/
│       ├── SKILL.md        # 草稿本体（Anthropic 布局）
│       └── .distill.json   # 蒸馏 provenance sidecar
├── code-review/            # 正式区（loader 扫描）
└── …
```

**为什么放 skills 树内的 dot 目录**：(a) `loadFromDir` 对 `.` 开头条目直接跳过（[loader.go](../../internal/skill/loader.go) L114–120，注释明言 dot 目录跳过是刻意的）——不可见性由既有承重墙保证，测试固化即可；(b) `.stats.jsonl` 已经开了「seek 私有状态以 dotfile 形态住在 skills 树里」的先例；(c) `cp -r ~/.seek/` 备份/迁移时草稿随树走。**草稿区是全局唯一**（不分项目）：收件箱只有一个地方可看，且跨项目复发累积（§4 L4）才有落点；项目归属记在 sidecar 的 `sources` 里，不靠目录结构表达。

**写盘纪律**（对齐 AGENTS.md「`~/.seek/skills/` 下写盘只许 `internal/skillmgr` 与 `internal/skillstats`」）：`.drafts/` 的一切写删由 **`internal/skillmgr` 新增的 draft API** 独占（§7）；蒸馏认知逻辑所在的 `internal/skilldistill` 零文件系统副作用。

### 5.2 `.distill.json` sidecar（seek 私有，作者可无视）

与 `.install.json` 同一设计哲学（作者编辑的文件与 seek 写的本地状态分离，[v2 §4.2](v2.md)）：

```json
{
  "schema_version": 1,
  "created_at": "2026-11-12T09:00:00Z",
  "updated_at": "2026-11-12T09:00:00Z",
  "status": "reviewable",
  "recurrence": 2,
  "suggested_scope": "project",
  "sources": [
    {"session_id": "20261112-090001-a3b9", "project_id": "011c0e58aa79deb2", "at": "2026-11-12T09:00:00Z"}
  ],
  "evidence": ["…誊本中的逐字摘录（≤3 段，人审时的对照材料）…"],
  "filter_trace": [{"stage": "L3", "verdict": "ACCEPT", "reason": "…"}],
  "generator": {"model": "deepseek-v4-flash", "prompt_rev": "skill-distill-v1"}
}
```

`evidence` 必须是**誊本逐字摘录**（reasoner 提示词硬约束）：人审时能看到「草稿声称的流程在原会话里真的发生过」，这是对幻觉程序的核心防线，也是与 pitfall→eval 共享的证据观（可回溯到源）。

### 5.3 SKILL.md 契约：草稿即合法包

草稿必须能被 `skill.Parse` 原样解析通过（name 过 kebab 正则、description 非空）——「草稿」与「正式包」的差别只在位置（`.drafts/` vs 扫描路径），不在格式。落草稿前跑一遍 `Parse` 校验，不合格直接拒（reasoner 产出的 name 不合规是可预期的常见失败）。

frontmatter 与正文模板（body 结构对齐 `seek skill create` 脚手架，作者路径与蒸馏路径产出同构）：

```markdown
---
name: fix-tui-state-bug          # 必需。kebab-case（loader 正则即校验器）
description: |                   # 必需。写「什么时候用」，不写「是什么」——
  When fixing TUI state-machine bugs where a modal's key handler     # 这是进 manifest 的触发器，
  swallows keys after exit, follow this reproduction-first sequence. # 是全系统杠杆最高的字段（§1.3）
version: 0.1.0-draft             # 语义：semver 预发布段；install 后由人决定去留
license: MIT
author: distilled
---

## When to use
（触发条件，从 description 展开成具体判据 + 反例）

## Steps
（编号步骤；每步写「调什么工具、看什么信号、失败怎么办」——
  蒸馏的原料就是会话里的真实工具序列，不是想象的最佳实践）

## Verification
（怎么确认做对了——会话里出现过的验证命令/断言）

## References
（可选。若会话里引用了值得保留的文档/命令输出，摘录于此）
```

**description 质量条款**（提取提示词的验收标准之一）：必须包含具体触发条件（"when X happens"式），禁止 "improves productivity" 类空话——与 v1 蒸馏对 tagline 的要求同款措辞。审稿闸（§6）首屏展示 description。

### 5.4 scope 建议（草稿不决定，只建议）

| 证据 | `suggested_scope` | install 默认项 |
|---|---|---|
| sources 全部来自 1 个项目 | `project` | `<cwd>/.seek/skills/`（进 git，团队共享） |
| sources 跨 ≥2 个项目 | `user` | `~/.seek/skills/`（本机全 session） |

与 dream 的 N≥2 同构：跨项目复现的模式才是「用户级工作习惯」，单项目的是「项目工序」。最终 scope 由人在安装时选（§6），建议只决定 picker 的默认项。

## 6. 人审闸（④）——与 RSI 同构

### 6.1 三态流转与不可逆边界

```
   （自动路径止步于此）
        │
        ▼
   ┌─────────┐   y + scope 选择    ┌──────────┐  重启会话
   │  草稿    │ ──────────────────→ │ 已安装     │ ─────────→ manifest 生效
   │ (draft) │   （skillmgr.Install)│ (installed)│
   └─────────┘                     └──────────┘
     │    ▲ n = 删除草稿                 ▲
     │    └── /skill drafts 随时补审 ──────┘  /skill uninstall 可回滚
     └── 14 天未审自动 GC
```

**不变量：从「会话誊本」到「loader 可见」的通路上，唯一的门是人的显式动作。** 自动路径的代码里不存在 install 调用（验收 #10 用测试钉死）；这是 RSI 安全形态的最小同构——**提议**（草稿 artifact，带证据）、**审议**（人，过目 description 与 evidence）、**激活**（install = 目录移动）、**可回滚**（uninstall；project scope 还有 git）。seek 自己就是被这么治理的（butler 卡片：worktree 写作 → review → merge），本设计把同一治理形态内化进产品。

### 6.2 两个审阅入口

1. **`/distill` 模态扩展**（M-SD1）：既有 review 模态（`internal/tui/distill.go` 的 y/n/e/q）在 M 候选审完后续审 skill 候选，头部换 `🛠 skill candidate i/j`（M 候选是 `⚡`）。`e` 编辑的是 SKILL.md body；`y` 后弹 **scope picker**（askuser 二选一，默认项来自 `suggested_scope`，Esc = 不装、留草稿）→ `skillmgr.InstallDraft` → 成功行提示「重启会话后生效」（manifest 启动时固定，v2 既有契约）→ 删除草稿（防重复审阅）。
2. **`/skill drafts` 收件箱**（CLI `seek skill drafts` 同构镜像，`--json` 给脚本）：列 name/recurrence/suggested_scope/age，逐份进入同一模态补审。自动路径产出的草稿只从这里进（scrollback 通知里直接提示该命令）。

### 6.3 权限模型（零新增机制）

- 草稿写入：发生在用户触发的 TUI 流程或进程内 hook，**不注册为 agent 工具**——与 `/distill` 写 M、auto-dream 写 soul.md Pending 同一待遇（用户发起/hook 内部动作，不走 permission policy）；
- 安装：`/distill` 与 `/skill drafts` 均为 TUI 用户流程，scope picker + 模态本身就是闸；**未来**若给 agent 侧开口（§11 的 skill_observe 或蒸馏对话工具），必须走 `skill_fetch`/`skill_commit` 先例——commit 带 permission gate（ModeAsk y/N），本文不为 agent 侧开任何新口子；
- prefix-cache：安装后 manifest 变化 → 下次会话 miss 一次（v2 install 同款预期成本，既有文档承诺，本文不新增负担）。

## 7. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| **`internal/skilldistill`（新包）** | 认知层：提取/判重/过滤的提示词、誊本渲染、候选解析、L2 机械查重、L4 合并逻辑。**零文件系统写**（可测性 = memory 包 Distiller 同款：chatClient 接口注入假客户端） | 新 ~400 行 |
| **`internal/skillmgr`（扩展）** | 落盘层：`DraftStore`——`WriteDraft` / `StrengthenDraft`（recurrence++ 合并）/ `ListDrafts` / `InstallDraft`（调既有 `Install`，source=草稿目录，type "local"）/ `GCDrafts`。`.drafts/` 写删独占（AGENTS.md 约束）；`Parse` 校验前置 | 新 ~250 行 |
| `internal/skill` | **零改动**。dot 目录跳过已承重；`Parse`/`Manifest` 原样复用（草稿校验器 + 判重输入） | 0 |
| `internal/hooks` | M-SD2：memory.Hook 旁新增（或同体实现）SessionStart 的 cadence 检查 + 后台 goroutine——结构逐行对照 `maybeAutoDream`/`runAutoDream` | 小 |
| `internal/session` | 只读：按项目列会话、读誊本（`loadMeta` 只读 header 的既有纪律） | 极小 |
| `internal/skillstats` | 零改动（反哺留 §11） | 0 |
| TUI | `/distill` 模态扩展（skill 候选段 + scope picker）；`/skill drafts` 命令 | 中 |
| `cmd/seek` | `seek skill drafts [--json]` 子命令 | 小 |
| permission | 零新增机制（§6.3） | 0 |
| eval | 蒸馏质量用例（见验收 #1/#5 的黑盒化）后续接入 `eval/cases/` | 后续 |

**包边界一句话**：`skilldistill` 负责「想」（LLM I/O，无副作用），`skillmgr` 负责「落」（文件系统，独占写权）——与 v2 已有的 `skill`（读）/`skillmgr`（管）切分同构，不出现第三个能写 `~/.seek/skills/` 的包。

**模型选型**：手动路径 Flash + thinking（交互延迟优先，与 `/distill`/observe 过滤一致）；自动路径默认 Flash + thinking、`$SEEK_SKILL_DISTILL_MODEL` 可切 Pro——自动批跑无人值守、产物吃人审注意力、频率低（cadence 8 会话/7 天），质量优先的参数空间与 dream 选型同款（[v1 §6 模型选型](v1.md)），但有 L3+人审双闸兜着，Pro 非必需。

## 8. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | 无程序性知识的会话（纯问答）跑 `/distill`：skill 候选为空，无草稿落盘（空数组纪律） | 单元测试（假 reasoner） |
| 2 | 合格会话产出的草稿 `SKILL.md` 能被 `skill.Parse` 原样解析（name 正则、description 非空） | 单元测试 |
| 3 | `.drafts/` 有草稿时 `skill.Load()` 结果与无草稿时完全一致（dot 跳过回归钉死） | 单元测试 |
| 4 | 候选 name 与已装 skill 碰撞 → 拒绝并回显理由，不写草稿、不覆盖 | 单元测试 |
| 5 | 候选与既有 skill description trigram ≥0.6 → 拒；0.35–0.60 → L3 过滤上下文包含该 skill body | 单元测试（阈值表驱动） |
| 6 | 候选实为 M 已录决策（name+tagline 重叠命中）→ 拒，理由指向 M 条目 | 单元测试 |
| 7 | 模态 `y` + scope=user → `~/.seek/skills/<name>/` 落盘（含 `.install.json` type "local"），草稿目录被删；scope=project → `<cwd>/.seek/skills/` 且**无** install sidecar（v2 project 语义） | 集成测试 |
| 8 | 安装后新会话 manifest 含该 skill；**当前**会话 manifest 字节不变（prefix-cache 契约） | 集成测试 |
| 9 | 自动路径：`$SEEK_AUTO_SKILL_DISTILL` 默认关；开启后 SessionStart 不被阻塞（首 Prompt 先于批处理完成——用 done channel 测时序，对照 `autoDreamDone` 手法）；recurrence=1 草稿为 `weak` 且**不出现**在 `/skill drafts` 列表，第二次独立会话命中后升 `reviewable` 且 `sources` 含两会话 | 单元测试 + `-race` |
| 10 | 自动路径全程无 install：跑完后 skills 树除 `.drafts/` 与 `.stats.jsonl` 外零新增 | 集成测试 |
| 11 | 草稿 GC：>14 天未审草稿在下一次批处理时清除；超 10 份最旧先清 | 单元测试（注入 Now） |
| 12 | reasoner 输出畸形 JSON / 缺 name → 草稿不落盘；手动路径报错给用户，自动路径静默记日志不崩 | 单元测试（失败路径） |
| 13 | `.distill.json` 的 `evidence` 每段都能在源会话誊本中逐字找到（反幻觉防线，机械可验） | 单元测试 |

## 9. 实现里程碑

| 里程碑 | 内容 | 估时 |
|---|---|---|
| **M-SD1** | `internal/skilldistill`（提取提示词/解析/L2/L3）+ `skillmgr.DraftStore` + `/distill` 模态扩展（skill 段 + scope picker）+ `seek skill drafts`。纯手动链路端到端可用 | ~3 天 |
| **M-SD2** | 自动路径：SessionStart cadence 检查 + 后台批处理 goroutine + L4 复发累积 + per-project state 文件 + `$SEEK_AUTO_SKILL_DISTILL`（默认关） | ~2 天 |
| **M-SD3** | 草稿 GC、失败路径测试补全（§8 #12/#13）、`guide-skills.md` 与 AGENTS.md 一行说明、pitfall 记录 | ~1 天 |

M-SD1 单独可发布（手动链路自成闭环）；M-SD2 依赖 M-SD1 的 DraftStore。全自动 + stats 反哺 + eval 接入见 §11。

## 10. 风险

| 风险 | 缓解 |
|---|---|
| reasoner 幻觉出誊本里不存在的「最佳实践」 | evidence 逐字摘录硬约束 + 验收 #13 机械回查 + 人审首屏对照 |
| description 质量差 → 永久 prefix 死重 | 提示词质量条款（§5.3）+ 人审首屏必看 description + install 后 `skill status` 可见 body/est tokens（v2 既有） |
| 草稿区变垃圾场 | L0–L4 漏斗 + 复发门槛 + 14 天 GC + 10 份上限；宁可漏过（用户手动 `seek skill create`），不可放过 |
| 自动路径打扰感（通知噪音） | 默认关；开启后仅 scrollback 一行、不弹模态；批量 cadence 8 会话/7 天起 |
| name 冲突处理太糙（直接拒） | 有意为之：首个交付先保「不覆盖既有 skill」的保守性；supersedes/更新流等真实需求出现再设计（§11） |
| 与 M 层抢内容（同一件事既像决策又像流程） | L1/L2 双向判重（M 索引进提取上下文 + 机械重叠扫描）；边界争议默认归 M（成本低一个量级） |
| `.drafts/` 依赖 loader dot 跳过——未来 loader 改动破坏不可见性 | 验收 #3 回归测试钉死；`.drafts` 名字进 loader 注释 |
| 草稿随 `cp -r ~/.seek/` 迁移到新机器 | 无害：草稿自包含（sidecar 带 sources），GC 照常；已装 skill 的 `.install.json` type "local" 指向已不存在的草稿路径 → update 降级提示重装（v2 既有降级路径，文档一句话说明） |

## 11. 后续版本（明确不做进首个交付）

- **`skill_observe` 实时工具**：memory_observe 的 skill 对应物——模型在会话中自报「这段流程值得存」。前提：草稿区与人审闸先稳定运行（§3.2 的演化序）。
- **supersedes / 更新流**：候选与已装 skill 是改进关系时的 diff 审阅 + `--force` 更新路径。
- **skillstats 反哺**：从未被调用的 skill 与新候选高度重叠 → 合并/替换建议（对抗 skill 库自身腐化）。
- **蒸馏质量 eval 化**：`eval/cases/` 加蒸馏用例（给定誊本 fixture → 期望候选/空数组），与 pitfall→eval 管道（`c_w7kob6`）合流成自进化回归面。
- **草稿区跨机器同步**：随 `~/.seek/` dotfiles 同步策略一并考虑。
