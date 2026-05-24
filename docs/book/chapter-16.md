# 第 16 章：M5 — 三层认知记忆 (L/M/S)

> 对应代码：`internal/memory/`、`internal/hooks/`、`internal/tools/memorytool/`、`cmd/seek/dream.go`
> 起点：v1.0。会话级 JSONL 历史已经稳定, 但每个新会话从零开始——用户得反复告知偏好、项目上下文、修过的坑。
> 终点：seek 拥有跨会话的"长期 / 中期 / 短期"三层记忆。`memory_recall` / `memory_remember` 工具进入工具表；`/distill` 把当前会话提炼成可复用决策, `seek -dream` 把多项目的中期记忆蒸成用户本源倾向；自动化层让蒸馏 / 做梦在合适时机自己跑。

---

## 16.1 为什么需要"记忆", 不只是"会话历史"

seek 在 v1.0 已经能保存、`--list`、`--resume`、`/branch`、`/compact` 会话——这是非常完整的"session 级持久化"。但持久化不等于记忆。两者的差别在用户的真实使用里很尖锐:

- **用户偏好被反复告知**:"先写失败的测试, 再写修复"——同一个用户在不同项目里要说三遍。
- **跨项目模式无法被识别**:用户在 repo A 喜欢显式错误处理, 在 repo B 也喜欢, 在 repo C 还喜欢——这是"用户的特性", 不是"任意一个项目的特性"。session 历史无法表达这层抽象。
- **项目级决策易被遗忘**:"上次我们决定不用 ORM, 是因为 X"——半年后再来这个项目, 这个决策的 *理由* 已经不在任何 session 历史里。

三个需求, 三个抽象层级:**长期(用户本源)、中期(项目决策)、短期(当前会话)**。把它们扁平存成一份 KV 不行——不同层级的写入频率、生命周期、可信度都不一样。强行平铺会产生"项目偏好污染其他项目"和"跨项目模式无法识别"两种典型故障。

PRD v1 把这件事定型为 **L / M / S 三层架构**:

```
┌────────────────────────────────────────────────────────────────────┐
│  L   长期记忆 / Soul       ~/.seek/soul.md                          │
│      跨项目, 用户本源倾向、思维习惯。整段常驻 system prompt 之前。  │
│      ~500 token 硬上限。仅由 `seek -dream` 写入候选, 用户审定升级。  │
└────────────────────────────────────────────────────────────────────┘
                              ↑ 做梦 (M → L 蒸馏)
┌────────────────────────────────────────────────────────────────────┐
│  M   中期记忆 / Project    ~/.seek/projects/<hash>/memory.jsonl     │
│      项目内做过的关键决策 + 为什么。每项目独立。                     │
│      索引常驻 prompt, 详情按需 memory_recall(name) 拉。              │
└────────────────────────────────────────────────────────────────────┘
                              ↑ 蒸馏 (S → M)
┌────────────────────────────────────────────────────────────────────┐
│  S   短期记忆 / Session    ~/.seek/sessions/<id>.jsonl              │
│      当前 session 的完整消息历史 (第 9 章已实现)                     │
│      session 结束触发 /distill 提炼可复用决策入 M                    │
└────────────────────────────────────────────────────────────────────┘
```

两条"单向蒸馏"是核心:

1. **S → M (蒸馏)**:session 结束时由用户触发 `/distill` (或满足条件时自动跑)。LLM 扫描这次会话, 提取 ≤3 条"在这个项目里值得记住的决策", 用户 review → 落 M。
2. **M → L (做梦)**:用户运行 `seek -dream`(或满足 cadence 时自动跑)。LLM 扫描所有项目的 M + 最近 N 个 session, 归纳出 "用户倾向 X 胜过 Y", 落 L-pending。后续 K 次会话里没出现反例, 才升正式 L。

> **为什么不在 M 上自动派生 L**?一开始 PRD 草稿里写的是 "M 累积到 N 条就跨项目聚类"。但很快发现:**蒸馏需要外部样本压力——只有用户在多项目里反复表达同一种偏好, 才算"本源倾向"**, 单项目内一次决策反复出现可能只是项目特性。把 M→L 限制成"必须有 ≥N 个项目里有同源证据"是这一层抽象成立的前提。

---

## 16.2 数据模型: 三层用三种不同的形态

三层的写入频率和读取方式完全不同, 所以存储也分别选了最合适的形态:

### S — 复用现有 session JSONL

第 9 章讲过的 session JSONL 不动。memory 子系统**只读**这层——`/distill` 把 session 历史读出来给 reasoner, 不修改。

### M — 每项目一目录, 内容是 JSONL + manifest

```
~/.seek/projects/<sha256(abs-path)[:16]>/
├── manifest.json     # 项目身份 (ProjectID, AbsPath, FirstSeen, LastSeen)
├── memory.jsonl      # 活跃 Entry, 一行一条
└── archived.jsonl    # 长期 stale 后归档的 Entry (同 schema, 后面会讲)
```

`Entry` 结构是这套子系统里最稳定的一块:

```go
// internal/memory/memory.go
type Entry struct {
    SchemaVersion   int       `json:"schema_version"`
    Name            string    `json:"name"`
    Tagline         string    `json:"tagline"`        // 一行摘要, 进 M-index
    Content         string    `json:"content"`        // 详情, 仅在 memory_recall(name) 时进 prompt
    Tags            []string  `json:"tags,omitempty"`
    CreatedAt       time.Time `json:"created_at"`
    UpdatedAt       time.Time `json:"updated_at"`
    LastRecalledAt  time.Time `json:"last_recalled_at"`
    RecallCount     int       `json:"recall_count"`
    Pinned          bool      `json:"pinned,omitempty"`
    Stale           bool      `json:"stale,omitempty"`
    StaleSince      time.Time `json:"stale_since,omitzero"`
    SourceSessionID string    `json:"source_session_id,omitempty"`
    AutoSourced     bool      `json:"auto_sourced,omitempty"`
    ObserveCount    int       `json:"observe_count,omitempty"` // M5.11: 跨 session 重复观察计数
}
```

`ObserveCount` 是 M5.11 加的——后面 §16.4.5 详述。

几个非显然的设计:

- **`Tagline` 和 `Content` 分开存**。Tagline 进每次 prompt 的 M-index, 必须短;Content 详情, 只在模型显式 recall 时才进。**索引常驻、详情按需**——和第 11 章 Skill 的 "manifest vs body" 完全同源。
- **`LastRecalledAt` 永远非零**。第一次写时 = `CreatedAt`。这样 PRD §6 的衰减打分有一个 stable 的 anchor, 永远不要判 null。
- **`StaleSince` 用 `omitzero`**(Go 1.24+)。`omitempty` 对 `time.Time` 不生效——会把零值 `0001-01-01T00:00:00Z` 写进每一行。`omitzero` 在序列化时识别 `IsZero()` 跳过。这是 v1 写完后撞过的具体坑, 修法就是把 tag 改对(commit `6c8f475`)。
- **`AutoSourced` 是 M5.7 加的**:自动蒸馏写进来的 Entry 标记 `auto_sourced=true`, 下一次手动 `/distill` 把这些条目浮出来给用户复核——避免"模型对自己的感知静默漂移成事实"。

### L — 一份用户可手编的 Markdown

```markdown
<!-- ~/.seek/soul.md -->
---
schema_version: 1
updated_at: 2026-04-30T08:22:00Z
---

# Stable

- 倾向显式错误处理胜过 panic
- 注释只在 *why* 非显然时写, 不解释 *what*

# Pending

- (dreamed 2026-04-29) 在 Go 项目里, 对 stdlib 的偏好高于第三方 (3 项目 5 source)
```

为什么 L 用 markdown 而不是 JSON?

- **用户可以手编**。soul.md 是用户的画像, 不是 seek 私有的二进制状态。`vim ~/.seek/soul.md` 删掉一条不准的, 整个系统兼容。
- **整段进 system prompt**。markdown 直接是 LLM 友好的注入格式, 不需要再转。
- **Pending / Stable 两节明显隔离**。`Stable` 是确认了的, 整段注入;`Pending` 是 dream 候选, 仅在 `seek -dream-write` 期间展示, **不进 prompt**——确保模型不被未确认的"标签"提前影响。

---

## 16.3 注入机制: 快照 + delta —— 从"每轮重注入"到"前缀永不变"

L 和 M-index 怎么进入每次请求?这事经历了两个版本。

### v1 首版:每轮重注入

最早的方案是 `PrePromptHook` 在每次 `Prompt()` 调用时重新构建完整的 L + M-index, 前缀到用户消息之前。这在功能上正确——模型每轮都能看到最新 memory——但有一个**静默的前缀缓存毒杀风险**:

> DeepSeek 的前缀缓存以整个 prompt 历史的字节序列为 key。如果 hook 产生的字节在两次 Prompt 之间不同, 整个前缀作废, 即使后面几千 token 完全相同。

具体来说:你在第 3 轮 `/distill` 加了一条 M entry → 第 4 轮的 M-index 多了一行 → hook 产出的字节变了 → **前缀缓存全 miss**。你越依赖 memory (频繁记录), 缓存命中率越低——一个反直觉的惩罚。

首版做了三件事来减轻这个影响:
- `Project.Index()` 按 Name 排序 (对抗 map 随机迭代)
- 截断键用 `pinned → recall_count → name` 而非 `time.Now()` (对抗时间漂移)
- 集成测试 SHA-256 校验同磁盘状态产同字节

但这只是"让破坏频率降到最低", 没有消除根本矛盾:**可变数据放在消息前缀里, 迟早会触发 cache miss**。

### M5.9:快照 + delta

真正的解不是"让前缀更稳定", 而是**让前缀永远不变**:

```
Turn 1:
  [sys] [soul snapshot] [M-index snapshot] [user msg 1]
         └─ Turn 1 构建一次, 永不变化 ─┘

Turn 2 (无 M 写入):
  [sys] [soul snapshot] [M-index snapshot] [user 1] [assistant 1] [user 2]
         └─ 零注入 —— 快照已在历史中, 前缀完全不变, 缓存全命中 ─┘

Turn 3 (Turn 2 后 /distill 加了一条 entry):
  [sys] [soul snapshot] [M-index snapshot] [user 1] [assistant 1] [user 2] [assistant 2] 
  [memory.delta] [user 3]
   ↑ 尾部追加, 不影响前缀
```

三个关键设计决策:

1. **Turn 1 注入快照, 记录 entry name 集合**。`injectSnapshot()` 在首次 `OnPrePrompt` 时运行, 把 L-stable + M-index 构建一次, 然后 `snapshotInjected = true`。

2. **Turn 2+ 无变化时零注入**。Hook 检测到 `snapshotInjected` 已置位且 `snapshotEntryNames` 跟当前 entries 一致 → 返回空 Prepend。快照已经在历史里, 模型仍能看到。

3. **有变化时 delta 尾部追加**。`buildDelta()` 找出 snapshot 之后新加的 entry name, 构建一条 `<context source="memory.delta">` 仅列出新增条目。这条 delta 附加在消息队列尾部——它之前的字节完全不变, 前缀缓存不受影响。

`OnSessionStart` 重置 `snapshotInjected` 和 `snapshotEntryNames`, 确保 `--resume` 恢复 session 时重新构建快照。

**效果**:从 Turn 2 到 Turn N, 只要无 M 写入, 每条消息的前缀字节与上一轮完全一致 → 热缓存全命中。有写入也只影响尾部 delta 的字节。soul + index 不再每轮重复注入 (50 轮会话从 ~100k tokens 降到 ~2k)。

---

## 16.4 调用面:五个工具 + 两个命令 + 自动化

M 层的操作暴露为五个工具:

### `memory_recall(name)` 与 `memory_remember(...)`

```go
// internal/tools/memorytool/  (示意签名)
memory_recall(name string)
    → 返回 Entry.Content, 更新 LastRecalledAt + RecallCount

memory_remember(name, tagline, content string, tags []string)
    → 走 permission.Policy 的 y/N inline 审批, 通过后 append 进 memory.jsonl
```

两个细节:

- **recall 是无害的, remember 必须审批**。`memory_recall` 不修改任何外部状态——只是读取并更新内部统计, 模型可以自由调用。`memory_remember` 改用户私有数据, 沿用 `internal/permission` 的 inline y/N 路径(第 5 章), 让用户能否决一个错误记忆的产生。这就是第 5 章那条"permission 拒绝是工具结果, 不是崩溃"原则的延续应用。
- **召回触发自动 GC**。`recall` 时刷新 `LastRecalledAt`, 重置 `Stale` 标记——这条 Entry "刚被用过", 不该被归档。GC 不是另一个独立任务, 它和正常使用是一条 feedback loop:用得多的留下来, 用不到的慢慢淡出。

### `/distill` — S → M 蒸馏

session 跑完用户没有 `/exit` 之前(或者退出时), 可以触发 `/distill`:

```
用户在 TUI 输入 /distill
  ↓
internal/memory/distill.go:
  - 把当前 session 历史压缩成上下文
  - 调 thinking 模式 reasoner, 要求输出 ≤3 条 "在这个项目里值得记住的决策"
  - 解析候选 (容忍 ```json 围栏、prose 前导、单 obj vs array)
  ↓
TUI modal: 逐条 y / n / e / q
  y = 接受, append 进 memory.jsonl
  n = 拒绝, 丢弃
  e = 编辑后接受 (调起 $EDITOR)
  q = 退出 modal (剩下的视作 n)
```

容忍 reasoner JSON 输出不严格是这一层最值钱的事。

### v2 升级: `memory_observe` — 模型实时观察, 异步过滤写 M

v1 的自动蒸馏在 SessionEnd 调 reasoner, 有 90s 超时卡退出的问题。v2 换了一条更干净的路:**模型在对话中自行判断何时值得记, 调 `memory_observe` 写入候选, 经 V4-Flash 异步过滤后落盘**。

```
模型在对话中检测到强信号 (方案确认/纠正确认/关键约束)
    → 调 memory_observe(name, tagline, content, tags)
    → 工具立即返回 "" (不阻塞对话)
    → 后台 goroutine 调 V4-Flash thinking 判重 + 价值评估
    → ACCEPT → 写入 M (auto_sourced=true), TUI 通知
    → REJECT / 超时 → 静默丢弃
```

三道保护:per-name 并发去重 (同一 name 同时只有一个 goroutine)、每 session 上限 10 次 (`$SEEK_OBSERVE_MAX`)、`auto_sourced=true` 标记留待 `/distill` review。

### AutoSourced 的生命周期 (M5.11)

v1 的 `auto_sourced` 是一个布尔值——条目要么"未确认"要么"已确认"。v1.5 把它从一个静止标记变成了会自进化的活体:

**`[auto]` 索引前缀**。M-index 中 auto_sourced 条目展示为 `- [auto] xxx: yyy`, 已确认条目不带前缀。模型无需调 `memory_recall` 就能在 index 层面区分可信度。

**`observe_count` 累进置信度**。每次 `memory_observe` 覆盖同名 auto_sourced 条目时 `observe_count +1`。当 `observe_count ≥ 3` (模型在 ≥3 次独立会话中观察到同一模式), `auto_sourced` 自动置 `false`——重复独立观察的置信度不低于单次用户确认。

**`recall_count` 驱动自升格**。`TouchRecall` 中增加一条规则:如果模型已经 recall 了一条 auto_sourced 条目 ≥3 次 (说明它在实际依赖这条记忆做决策), 自动升格。模型"用出来"的记忆比"看到"的更有说服力。

这三个机制独立运行, 互不依赖——一条 auto_sourced 条目可以通过观察累进、实际使用、或用户手动 `/distill` 确认三条路中的任何一条升格为正式 M 记录。

### 一个工具描述引发的设计修正

M5.11 开发过程中暴露了一个入口机制问题:模型会不加区分地把"发现的 bug"当作"学到的教训"写进 M——因为 `memory_remember` 的 description 只说 "learned something worth knowing", 没说"不用于 bug 追踪"。

修法:在 `memory_remember` 和 `memory_observe` 的 tool description 里显式加了约束:

- M 是 **durable lessons and design decisions**, 不是 bug tracker
- 发现 bug → 先修, 再记可复用的教训
- tagline 的正确写法: `"write-tmp-rename must Sync before Close"` (lesson), 不是 `"Save is missing Sync"` (bug report)

两行描述改动的成本极低, 但模型行为从"记一切"变成"先判断这是可复用的教训还是该修的 bug"。

### M5.13: observe 反馈闭环

`memory_observe` 返回空字符串导致模型对观察结果完全盲区——不知道 ACCEPT/REJECT 率, 无法调整观察策略。

解法:不返回 per-call 结果 (会污染消息历史), 而是在 M-index 快照中注入上一 session 的聚合统计:

```
<context source="memory.index">
...
最近 memory_observe 统计：5 次调用，3 条保存
</context>
```

Hook 在 session 内累积 `observeAcceptCt` / `observeCount`, `OnSessionEnd` 写入 `observe-stats.json`。下一个 session 的 Turn 1 快照读取并注入一行。16 行代码量, 零新依赖, 不破坏缓存 (stats 在 session 内不变)。

### `seek -dream` — M → L 做梦

CLI flag, 离线跑。**dream 使用 V4-Pro + thinking** (而非 V4-Flash)——跨项目特质归纳是全记忆系统里推理难度最高的操作, 而 dream 是低频操作 (每 14 天一次), Pro 的绝对成本可忽略。通过 `$SEEK_DREAM_MODEL` 可覆盖。

```bash
$ seek -dream
  → 扫描 ~/.seek/projects/*/memory.jsonl
  → 收集最近 30 个 session 的关键决策
  → reasoner 提取 "用户跨项目的本源倾向"
  → 候选要求 N ≥ 2 个项目源, 否则丢弃
  → 输出 L-pending 到 stdout (-dream-write 直接 append 到 soul.md ## Pending)
```

`-dream` 是 *只读 + 输出*, `-dream-write` 是 *写文件*。两者分开让作者可以先看一眼候选, 再决定是否真写。

### M5.10: L 层维护 — Pending 不再只增不减

最初的 dream 只往 Pending 追加, 不清理不升级。M5.10 加了两条机械规则:

- **升格**:来源项目 ≥3 且首次观察 ≥14 天 → Pending 移到 Stable (对称于 auto_sourced 的 `observe_count ≥ 3`)
- **过期删除**:最近确认 >30 天 → 从 Pending 删除
- **其他**:保留在 Pending

`evaluatePending()` 在 dream 流程中评估现有 Pending 条目, `applyMaintenance()` 把变更写回 soul.md。L 层从此不再只增不减。Stable 反例检测 (reasoner 辅助) 留给 M5.12。

### M5.8 — 自动周期 dream (默认开启)

SessionStart hook 按 cadence 触发后台 dream。**默认开启** (与 `$SEEK_AUTO_DISTILL` 对称)——通过 `$SEEK_AUTO_DREAM=0` 关闭。加了一个前置保护:**<2 个项目时跳过 reasoner 调用**, 单项目用户不会产生 API 成本 (N≥2 过滤器会拒绝一切结果)。

- Cadence:每 N 次 session 或 K 天 (默认 N=20, K=14)
- 状态记录:`~/.seek/dream-state.json`
- 手动 `seek -dream` 不重置 cadence

---

## 16.5 GC: 衰减打分 + 双悬崖归档

M 不能无限增长——索引整段进 prompt, 几百条之后 manifest 大小开始影响 cache。GC 不是把"用不到的删了"那么简单, 它要回答两个分开的问题:

1. **哪些不值得继续注入 index?** → score 衰减 → 标 stale
2. **哪些可以彻底移出 active 集?** → stale 持续够久 → 移到 archived.jsonl

```go
// internal/memory/score.go (示意)
score = (RecallCount + 1) * exp(-(now - LastRecalledAt) / 30 days)
```

最近用 = 高 score。从未用过但刚创建 = 低 score (但 +1 让它至少有机会被注入若干次)。`Pinned=true` 永不进入 GC 通道(用户显式锚定的不该被衰减)。

GC 不在每个 Prompt 上跑——只在 SessionStart 一次。原因:GC 改 `Stale` / `StaleSince` 字段意味着 manifest 字节会变, 触发缓存失效。**每个 session 开头一次, session 内字节稳定**。

### 双悬崖归档(dual-cliff)

只有 `Stale=true` **且** 持续 `archiveStalePersistence`(60 天)以上的 entry 才被移到 `archived.jsonl`。

为什么要双悬崖?

最初版本是"score < 阈值就归档"。问题: 边界附近的 entry 在两次 session 之间反复跨过阈值——一次归档进 archived, 下次又被 recall 提回来, 反复抖动。`archived.jsonl` 变成了 entries 的"通勤大道"。

双悬崖加了一层 *滞后*: 标 stale → 时间持续到位 → 再归档。被 recall 一下就 reset stale 时间, 重新计时。这给了"边缘条目"一个长窗口的恢复机会, 真正离开 active 的是**长期没人理的**, 不是**偶尔得分掉一下的**。

> 这是经典的"避免颠簸"的设计——hysteresis。控制系统、CPU thermal throttling、UI 滚动加载, 都是同一个模式:**状态切换需要持续证据, 单次越过阈值不够**。

---

## 16.6 项目身份: 用 `abs path → sha256[:16]` 哈希作目录名

```
~/.seek/projects/c4a1b9f5e7d2a830/
                  ↑
                  sha256(absPath)[:16], lowercase hex
```

为什么不直接用 path?

- **路径含 `/`** → 文件系统层级混乱; 要么 url-encode 要么用某个 separator 字符替换, 都是发明 schema 的开始。
- **路径含 spaces / unicode** → 后续 shell 操作 (`ls ~/.seek/projects/My Project/`) 一不小心就出错。
- **路径会变** (用户 `mv` 项目目录) → 用 path 作 key 意味着搬家后旧记忆全丢。

哈希作目录名解决前两个问题(纯 hex, 短, 文件系统友好)。第三个问题(项目搬家)用一个独立机制:

```
<project>/.seek/project-id
```

项目目录下放一份"指向 ~/.seek/projects/<id>/" 的指针文件。seek 启动时:

1. 先看 `<cwd>/.seek/project-id` 是否存在
2. 存在 → 用它指向的 project-id (即使 cwd 路径变了, 记忆还在)
3. 不存在 → 计算 `sha256(absPath)[:16]` 作 fallback
4. project-id 文件第一次访问时写入

`<project>/.seek/project-id` 在 `.gitignore` 里(用户某些机器上的 absolute path 不应该污染 git history)——这是 commit `7d019f5` 修的一个小坑(团队 PR 里出现这个文件, 在 CI 上路径不一致就乱了)。

---

## 16.7 把所有这些 wire 进 agent: 一行 Config

整个 memory 子系统的入口, 在 `cmd/seek/main.go` 里就一段:

```go
// 注册 hook
reg := hooks.NewRegistry()
reg.Register(memory.NewHook(homeDir, projectID, ...))

// 传给 agent
agentCfg := agent.Config{
    // ... 其他字段
    Hooks: reg,
}
```

agent 不知道 memory 存在;memory 不修改 agent。中间只有 `internal/hooks` 这套接口。可以独立测试任一侧:memory 的测试只 mock `PrePromptIn`, agent 的测试只检查 hooks chain 调用顺序。

这种"通过协议解耦"的姿态贯穿整个子系统——`internal/skill`、MCP client、project.md 加载, 没有一个 import `pkg/agent`。agent 是中间的协调者, 不是知道一切的"上帝对象"。

---
	
## 16.10 已知局限

一版做完之后回头看, 有三个诚实问题:

**单项目用户的 L 层透明**。Dream 的 N≥2 过滤器让单项目用户永远得不到 L 候选——"跨项目模式"在单项目里确实不存在, 但这让 L 层成了 power-user feature (M5.14)。

**M-index 在 1500 token 截断线下的取舍**。一条 entry 从创建到 archive 约 100 天。活跃项目一年积攒 200 条是可能的, 1500 token 预算触发截断时按 `pinned→recall_count→name` 排序, 高价值低频条目 (tagged lesson) 可能被丢弃 (M5.15)。

**精确 name 匹配的局限**。`memory_recall` 只能按精确 name 查——模型想找"跟 symlink 安全相关的所有教训"时只能扫 index。v3 引入 embedding 做语义检索。

---

## 16.11 一个观察: 记忆是 seek 从"工具"到"伙伴"的换挡

回到 §16.1 的动机:用户偏好被反复告知、跨项目模式无法识别、项目级决策易被遗忘——v1 验收清单全绿之后这些问题依然在, 因为 v0 只解决了"一次会话内的能力"。

现在 seek 的记忆子系统不是"存下来等人管", 而是**自维护**的——它自己写 (memory_observe), 自己升级 (observe_count + recall), 自己老化 (衰减分数), 自己清理 (archive), 自己反馈 (stats), 自己做梦 (dream)。用户只需要在 `/distill` 里按 y/n, 或者手动编辑 `soul.md`。其他都是自动的。

这种连续性不是更多 tokens, 是**有结构的少量 tokens 在正确的层级被注入, 且系统自己知道什么该留、什么该忘**。用户的体感是"这工具记得我", 但底下是 snapshoting、衰减打分、双悬崖归档、字节稳定 SHA-256 测试、三条独立自升格路径——一套不那么浪漫的工程纪律。

这跟第 13 章那条"诚实大于神秘"是同样的事:**所谓"工具懂你", 都是一系列小决定的累积:存什么、什么时候写、什么时候注、怎么避免缓存失效、用户怎么否决、条目怎么自进化**。每一处都做对, 加起来是"它记得"。每一处偷懒, 加起来是"它在假装聪明"。

---

## 本章小结

- 三层架构 L/M/S 不是"为了对称", 而是**用户偏好 / 项目决策 / 当前会话**三件事在写入频率、生命周期、可信度上根本不同
- 两次单向蒸馏 (S→M, M→L) 各有自己的触发器和用户审定门——**模型从不直写 L Stable**, 永远是"提议 → 用户升级或机械规则评估"
- M5.9 快照+delta:**让前缀永不变化**——Turn 1 注入快照, Turn 2+ 零注入 (无变化) 或尾部 delta (有新 entry)。缓存命中不受 M 写入影响
- M5.11 auto_sourced 从布尔到连续:**三条独立自升格路径**——observe_count≥3、recall_count≥3、用户手动 y。`[auto]` 索引前缀让模型在 index 层面区分可信/未确认
- M5.10 L 层维护: dream 流程中加入 `evaluatePending()` 机械规则——≥3 来源 + ≥14 天 → 升格 Stable；>30 天无新证据 → 删除
- M5.13 observe 反馈闭环: 聚合 stats 注入 M-index——16 行代码, 零新依赖, 不破坏缓存
- 工具描述修正: M 不是 bug tracker——"发现 bug → 先修, 再记可复用的教训"
- GC 用衰减打分 + 双悬崖归档——避免边缘 entry 在 active 和 archived 之间通勤 (hysteresis 模式)
- dream 使用 V4-Pro + thinking (低频高杠杆); auto-dream 默认开启 + ≥2 项目前置检查
- 整个子系统通过 `internal/hooks` 协议与 `pkg/agent` 解耦——agent 不知道 memory 存在

下一章进入 M8 — Skill 生命周期管理。

---

*对应 commit:`08660a1`(hooks substrate) → `374cfad`(M5.8) → `378404b`(M5.9-M5.11) → `31856c0`(M5.13)。运行 `go test -race ./internal/memory/...` 验证 (~170 tests)。详 PRD 见 `docs/prd/v1.md`。*
