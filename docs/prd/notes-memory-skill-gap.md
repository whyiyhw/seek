# Notes：memory 与 skill 联动现状盘点——蒸馏漏斗里 L/M/S 与 SKILL.md 的断层

> 性质：备料笔记（`notes-` 前缀），为「skill 自动蒸馏」设计卡提供事实底座。只盘点，不改代码，不做设计承诺。
> 日期：2026-11-12 · 分支 `drive/c_evvow4` · 依据：`docs/prd/v1.md`、`docs/prd/v2.md` §4、`internal/memory/`、`internal/skill*/`、`cmd/seek/main.go`、`docs/narrative/seek.md`
> 背景：`docs/narrative/seek.md:25,51` 已把「skill 不会自动蒸馏」列为自进化内环的缺口，设计卡待开。

---

## 1. 蒸馏漏斗现状（memory 侧产出形态）

### 1.1 漏斗结构与三个出口

```
S  session JSONL（~/.seek/sessions/<id>.jsonl，schema_version=2）
   │  ① /distill（手动，用户触发，reasoner 提取 ≤3 条，y/n/e review）
   │  ② memory_observe（v2，模型对话中调，异步 V4-Flash 判重，auto_sourced=true）
   ▼
M  ~/.seek/projects/<hash>/memory.jsonl（JSONL，项目内）
   │  ③ seek -dream / 自动做梦（V4-Pro thinking，跨项目 ≥2 源归纳用户特质）
   ▼
L  ~/.seek/soul.md（markdown，## Stable / ## Pending 两段）
```

- **没有第三跳「M → skill」**。漏斗到 L 为止；skill 不在蒸馏路径上。
- 三个出口的产出物形态：

| 出口 | 触发 | reasoner | 产出结构 | 落盘形态 | 用户安全网 |
|---|---|---|---|---|---|
| ① `/distill` | 手动 TUI 命令 | V4-Flash thinking | `Candidate{name, tagline, content, tags}`（`internal/memory/distill.go:19`） | review 通过后写 M JSONL | y/n/e 逐条 review |
| ② `memory_observe` | 模型对话中调用 | V4-Flash 异步过滤（ACCEPT/REJECT/TIMEOUT） | 同 Entry schema，`auto_sourced=true` | 直接落 M，`[auto]` 前缀进索引 | scrollback 通知 + `/distill` 补审 + observe_count/recall_count ≥3 自动升格 |
| ③ `-dream` / 自动做梦 | 手动 CLI / cadence（N=20 sessions 或 K=14 天，SessionStart 后台跑） | V4-Pro thinking | `LCandidate{trait, why, sources[]}`（`internal/memory/dream.go:30`） | 追加 soul.md `## Pending` | N≥2 项目源过滤；Pending 需 ≥3 源 + ≥14 天才升 Stable（M5.10 evaluatePending）；>30 天无新证据删除 |

### 1.2 M 条目 schema（`internal/memory/memory.go:35`）

```json
{
  "schema_version": 1, "name": "kebab-case", "tagline": "一句话决策",
  "content": "完整理由 ≤500 字", "tags": [...],
  "created_at": ..., "updated_at": ..., "last_recalled_at": ..., "recall_count": N,
  "pinned": false, "stale": false, "stale_since": ...,
  "source_session_id": "...", "auto_sourced": false, "observe_count": 0
}
```

注入：`name + tagline` 索引常驻（Turn-1 快照，user-role `<context type="memory.index">`，按 tag 分组，auto_sourced 条目带 `[auto]` 前缀；pinned 只影响排序置顶，无显式标记）；详情靠 `memory_recall(name)` 按需取。L Stable 整段注入 `<context type="memory.soul">`（≤500 tokens）。

### 1.3 M 层 decay GC（`internal/memory/score.go`）

- **分数**：`score = (recall_count + 1) × exp(-age / half_life)`，`last_active_at = max(created, updated, last_recalled)`，半衰期默认 30 天（`SEEK_MEMORY_HALFLIFE_DAYS` 可调）。
- **规则链**（RunGC，SessionStart 跑）：pinned 永不评估 → auto_sourced 宽限期 30 天 → 新条目宽限 7 天 → score ≥0.5 保留 → <0.5 标 stale（剔出索引、仍可 recall）→ stale 且 <0.1 持续 60 天归档 `archived.jsonl`。
- **升格通道**（与 GC 对称的「生命周期向上」）：`observe_count ≥ 3`（跨 session 重复观察）或 `recall_count ≥ 3`（模型实际在用）→ `auto_sourced` 自动置 false。
- **⚠ PRD 与实现偏差**：`docs/prd/v1.md:438` 与验收 #14 写的是 auto_sourced **硬豁免**（「无论 score 多低、创建多久，都不被标记 stale 或归档」），但 `score.go:40-46` 实现的是 **30 天宽限后参与正常 GC**（注释明确说硬豁免会导致无界增长）。设计卡引用 GC 语义前需先把这处偏差坐实到 PRD 或代码任一侧。

### 1.4 L 层维护（M5.10/M5.12，已交付）

dream 每次跑三步：产出新候选 → `evaluatePending()`（机械规则：≥3 源 + ≥14 天升 Stable；>30 天无新证据删）→ `evaluateStable()`（reasoner 扫全项目 M 找 ≥2 条反例则降级回 Pending）。Stable 注入每个 session；dream 相似候选合并用 Jaccard ≥0.55 + CJK Levenshtein（`internal/memory/merge.go`）——**这套判重是现成资产，M↔skill 判重可直接复用**。

## 2. SKILL.md 布局现状（skill 侧）

### 2.1 文件与字段（`internal/skill/skill.go`）

```
<dir>/<name>/SKILL.md      # v2 目录包（canonical，Anthropic Agent Skills 布局）
<dir>/<name>.md            # v0 单文件（仍支持）
<dir>/<name>/.install.json # 安装源 sidecar（仅 skillmgr 写）
<dir>/<name>/references/   # `seek skill create` 脚手架附带的资料目录
```

frontmatter 识别字段：`name`（kebab-case 强校验）、`description`（when to use，进 manifest）、`version`/`license`/`author`/`keywords`/`allowed-tools`，未识别键进 `Extra` 透传。**Body 永不进 manifest**，模型调 `Skill` 工具时按需取。

加载优先级：`<project>/.seek/skills/` > `~/.seek/skills/` > builtin（go:embed，现有 4 个：code-review / dual-model / go-test-runner / plan-mode）。同名 first-writer-wins，落败者记入 `shadowed`。

### 2.2 生命周期与统计

- **写入权收敛**：`internal/skillmgr` 是唯一允许写 `~/.seek/skills/` 的包（AGENTS.md 架构约束写死；`internal/skillstats` 只写 `.stats.jsonl`）。`seek skill create` 是**面向人手写的脚手架**（TODO body anchors，非交互必须给 `--description`），没有任何「从已有知识生成 skill」的入口。
- **无衰减**：skill 只有 install/uninstall/update，没有 stale/归档/pinned 概念，装上就永生。
- **update 语义依赖安装源**：`.install.json`（git/https）才可 update，且 update 会按安装源覆盖本地改动；project 级安装刻意不写 sidecar（git 共享、无 update）。手工 `cp` 的 skill = 不可更新。
- **调用统计**：`internal/skillstats` 追加 `~/.seek/skills/.stats.jsonl`（全局单文件，6 字段：ts/name/session_id/project_id/model/provider，`paths.UserSkillStats()`）。**append-only，从不读回**——没有任何 GC/推荐消费它，查询侧在 `internal/skillcli/query.go`（M8.4 CLI）。

### 2.3 注入通道（与 memory 的关键差异）

skill manifest 经 `sysprompt.Compose(SkillManifest)` 进 **system prompt**（`cmd/seek/main.go:1280`），session 内字节恒定、子 agent 继承；M/L 索引进 **user-role context**（Turn-1 快照 + 尾部 delta）。冲突顺序（v1.md §6）：用户当前指令 > AGENTS.md > M > L-stable > L-pending > 系统默认（auto_sourced M 再压一档）。

## 3. 重复点——同一条知识两边各存一份

### 3.1 结构级重复（两套平行的「索引 + 详情 + 计数」机制）

| 角色 | memory 侧 | skill 侧 | 备注 |
|---|---|---|---|
| 唯一 id | `Entry.name`（kebab，项目内命名空间） | `Skill.name`（kebab，三层级联命名空间） | 同一正则 `[a-z][a-z0-9-]*`，但碰撞语义不同：M 按 auto_sourced 决定可否覆盖；skill first-writer-wins + shadowed |
| 索引行 | `tagline`（"what was decided"） | `description`（"when to use this"） | 都注入为「name + 一句话」清单；**语义不同：一个答"是什么"，一个答"何时用"** |
| 详情正文 | `content`（≤500 字，JSONL 字段） | `Body`（markdown，任意长，按需取） | 都是"模型先看清单、决定要才拉全文" |
| 使用计数 | `recall_count`（写回 Entry，喂 decay GC） | `.stats.jsonl` 行数（从不读回） | 计数语义对齐、消费端缺失 |
| 聚合反馈 | `observe-stats.json`（M5.13，每项目，observe 通过率注入索引底部） | `.stats.jsonl`（全局，无注入） | 两套「让模型自校准」的统计，格式/位置/消费全不通用 |
| 标签 | `tags[]` | `keywords[]`（"reserved for v3 search"，未消费） | 近义字段两边各一份 |

### 3.2 内容级重复（现实中会双存的场景）

1. **M entry ↔ 手写 SKILL.md**：一条项目决策（如「session 用 JSONL 因 prefix-cache 要 byte-identical」）被 `/distill` 落进 M，之后用户把同一知识写进项目 skill——没有任何机制发现两份是同一条。skill 无从引用 M（除 prose 提示模型去调 `memory_recall`）。
2. **M ↔ AGENTS.md**：冲突顺序里 AGENTS.md > M，但项目约定到底放 AGENTS.md 还是 M 全凭手感，两边都会长「本项目用 X」类条目。skill 蒸馏落地后这个问题变三份。
3. **M ↔ pitfalls 台账**：`docs/pitfalls.md`（commit trailer 双信号）已经是「非显而易见知识」的持久层，与 M entry 高度同题材；narrative 内环规划的「pitfall→eval→skill」管道意味着同一 pitfall 未来在三处有副本。

### 3.3 今天唯一的桥（prose 级，无代码）

`docs/prd/v1.md:675`：「Skill 的 `.md` 可引用 `memory_recall("name")` 拉项目决策详情」——改动量标 0。即：skill body 里**写一句话让模型自己去调 memory 工具**。没有 schema、没有校验、没有反向（M 条目不能指向 skill）。代码级两侧零 import（`internal/memory` 不识 skill；`internal/skill*` 不识 memory，grep 到的两处 "memory" 是无关用法）。

## 4. 失联点——漏斗断在哪

1. **漏斗终点缺失**：S→M→L 有完整漏斗 + 生命周期（decay/升格/归档），M→skill 这一跳不存在。narrative 已承认（「skill 不会自动蒸馏」，`docs/narrative/seek.md:25`）。
2. **写入权与蒸馏路径互斥**：蒸馏产物要落 `~/.seek/skills/` 或 `<project>/.seek/skills/`，架构上必须经 skillmgr；但 skillmgr 只有 install（外部源）/create（人写脚手架）两个入口，没有「从 M entry / session 生成」的入口。设计卡要么扩 skillmgr，要么破例开新写入口（不建议，AGENTS.md 约束是 load-bearing）。
3. **生命周期断层**：M 有 decay GC + pinned + auto_sourced 升格阶梯；skill 永生。若蒸馏把「还热的知识」搬进 skill，等于把它移出衰减体系——知识越用越固化，错误也固化（skill 无 stale，错了只能人肉 uninstall）。
4. **统计单向失联**：`.stats.jsonl` 记了 skill 被用，但（a）不喂任何 GC，（b）不作为「这条 M 知识值得升格为 skill」的信号；反向 `recall_count ≥3` 升格只把 auto→confirmed，不跨层。两个计数器老死不相往来。
5. **更新语义冲突**：蒸馏生成的 skill 没有 `.install.json`（不可 update，符合预期）；但「重新蒸馏覆盖旧版」与「用户手改 skill 后被下次蒸馏覆盖」的冲突规则未定义——这是 M 层 `auto_sourced` 覆盖规则（未确认可覆盖/已确认拒绝）需要在 skill 侧的等价物，目前完全没有。
6. **注入通道迁移的缓存/优先级代价**：知识从 M（user-role context）搬进 skill（system prompt manifest）会（a）改变 prefix-cache 策略区（system prompt 是最敏感字节区，v1.md §5 特意把 memory 放 user-role 就是为这个），（b）改变冲突优先级（skill/manifest 与 AGENTS.md 同层，高于 M）。设计卡必须显式回答「蒸馏产物进 manifest 是否值得付这个代价」。
7. **命名空间无映射**：M name 是 per-project，skill name 是全局级联；蒸馏时同名跨项目冲突（proj-A 和 proj-B 各有一条 `use-jsonl`）没有既定解法（现状 skill 靠 shadowed 记录，但那是加载期兜底不是蒸馏期策略）。

## 5. 打通优先级建议（供设计卡取用，非决定）

**P0 —— 设计卡必须先钉死的两件事**

1. **蒸馏产物落点 + 复用 review 安全网**。最小路径：`/distill` review 增加第四个动作（如 `s`：本条改存为 skill 草稿），经 skillmgr 新入口写 **project 级** `<project>/.seek/skills/`（git 可共享、无 .install.json 负担、不动全局命名空间）。理由：不发明新 gate——y/n/e review 已经是全漏斗验证过的用户安全网，蒸馏到 skill 只是给既有出口加一个方向。
2. **生命周期对齐**：回答「蒸馏 skill 衰减吗」。两个可选先例：（a）M 的 decay 语义移植（需要给 skill 加 stale + `.stats.jsonl` 读回作为 recall_count 等价物）；（b）明确声明 skill 永生、靠 uninstall + 蒸馏时判重兜底。无论选哪个，都应把 `.stats.jsonl` 的消费端补上（哪怕只是 CLI 查询），否则「skill 是否还被用」永远无信号。**同时清理 §1.3 的 PRD↔代码 GC 偏差**，设计卡不能引用两套 GC 语义。

**P1 —— 结构打通**

3. **字段映射与 prompt 改造**：`Candidate{name→name, content→Body}` 可直译，但 `tagline→description` 语义翻转（"是什么"→"何时用"）必须在蒸馏 prompt 里让 reasoner 重写而非拷贝；`tags→keywords` 顺手接通（keywords 本就 reserved 未用）。
4. **判重复用**：M entry 与既有 SKILL.md 的去重直接复用 `merge.go` 的 Jaccard/CJK 相似度（dream 已在生产用它合并 L 候选），避免 §3.2 的双存。

**P2 —— 可以晚做**

5. 跨层升格信号（recall_count 高的 M → 提示蒸馏为 skill；`.stats.jsonl` 低频 skill → 提示归档），与 AGENTS.md/pitfalls 的三重存储治理。
6. dream 扩展为「skill 审计 pass」（对称 evaluateStable：扫 skill body 与现行 M/AGENTS.md 的矛盾，产出 demotion 建议）。

---

## 附：盘点时坐实的事实清单（防设计卡重考古）

- `internal/memory` ↔ `internal/skill*` 代码级零相互引用；唯一联动是 PRD 一句 prose（§7）。
- skill manifest 在 system prompt（`sysprompt.Compose`），M/L 在 user-role context（Turn-1 快照 + delta）——两个注入通道、两套缓存策略、两套冲突优先级。
- skillmgr 是 skill 目录唯一合法写入者（AGENTS.md 架构约束）；skillstats 只写不读；skill 无任何衰减机制。
- M 的 GC：auto_sourced 实现是 30 天宽限后正常参与（`score.go:46`），PRD 写的是硬豁免（`v1.md:438` / 验收 #14）——文档与代码二选一需要对齐。
- dream 的相似度合并（Jaccard ≥0.55 + CJK Levenshtein fallback）是仓库内唯一现成的跨文本判重实现。
- `seek skill create` 是人写脚手架（TODO anchors、非交互需 `--description`），不是生成入口。
