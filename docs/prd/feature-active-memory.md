# 主动记忆策展（Active Memory Curation）

**主题**：把 M 层从"被动衰减 + 等模型调用 `memory_recall`"演化为"主动评估 + 持续策展"。是 seek 后续"主动智能"（proactive intelligence）能力的核心机制。

**状态**：📐 设计稿，未实施。本 PRD 记录设计推演、踩坑路径、阶段决策。

## 一、问题陈述

### 1.1 决定性的洞察

> **信息被使用的路径 ≠ 信息被 recall 的路径**
>
> - 使用路径：M-index tagline → 注入到 prompt → 影响模型行为 → 用户受益
> - recall 路径：模型主动调用 `memory_recall(name)` 获取全文 → 仅当 tagline 不够、需要细节时才触发
>
> 当 tagline 本身就足够（"用户偏好 Go 而非 Python" 这种事实型条目尤其如此），模型**永远不会**调用 `memory_recall` → `RecallCount` 永远不增、`LastRecalledAt` 永远不刷新 → 在当前评分公式下被判 stale → 21 天后从 index 消失。**即使它每天都在影响用户体验。**

### 1.2 一条从未被 recall 的条目的生命周期（当前行为）

```
Day 0   创建                → RecallCount=0, score≈1.0      ✅ 正常
Day 7   度过 gracePeriod    → 开始参与 RunGC 评分
Day 21  score 降到 0.50     → 触及 stalenessThreshold       → Stale=true
Day 21+ 从 M-index 消失      → 不再注入到 prompt（Index() 跳过 stale）
Day 81  持续 stale ≥60 天    → 归档到 archived.jsonl，活动集删除
```

21 天后用户的偏好就从模型眼前消失了——即便它每天都在生效。

### 1.3 为什么这是设计假设错误（不是 bug）

PRD `v1.md` §6 的 decay 模型借鉴自"用户阅读/回顾笔记"的人类记忆模型：重要的东西会被反复访问。但 agent 场景中——

- **用户不需要主动查记忆**——agent 已经帮用户用了
- **agent 才是信息的消费者**，而消费的方式（看 tagline 影响行为）默认不留痕迹
- **写入路径**（`memory_observe`）和**消费路径**（M-index 注入）是两条不通的轨道；GC 评分却只看消费路径里很窄的一根（显式 recall）

也就是说：v1 设计假设"重要的东西被反复 recall"，在 agent 场景中**这条前提不成立**。

## 二、现状审计

### 2.1 已有的"另一线程"路径

seek 在异步评估方向并非空白：

| 机制 | 触发 | 作用 | 是否实时观察对话 |
|------|------|------|----------------|
| `memory_observe` 工具 | 模型显式调用 | 后台 goroutine 跑 distill 过滤再写盘 | ❌（模型主动喂） |
| `dream` 后台 pass | SessionStart cadence | 扫历史 sessions 综合 L 层 | ❌（看完成的 sessions） |
| `OnSessionStart` GC | 每次启动 | 按评分判 stale/archive | ❌（不看对话） |
| `OnSessionEnd` | session 结束 | v2 已退化为 no-op | ❌ |

**共同盲区**：没有任何一条路径**在对话进行中**持续观察"用户/模型行为"并反向更新记忆。

### 2.2 评分公式现状

`internal/memory/score.go:75`：

```go
score = (RecallCount + 1) * exp(-(now - lastActive) / halfLife)
lastActive = max(CreatedAt, UpdatedAt, LastRecalledAt)
```

`stalenessThreshold=0.5`、`archiveThreshold=0.1`、`halfLife=30d`（可由 `SEEK_MEMORY_HALFLIFE_DAYS` 覆盖）。

## 三、踩坑路径（不要重蹈）

### 3.1 ❌ 方案 A：SessionEnd 注入 bump

> 给 `Hook` 加 session 级 `injectedSet`，`OnPrePrompt` 时记录，`OnSessionEnd` 时统一把这批条目的 `LastRecalledAt = now`、`RecallCount` 不动。

**为什么放弃**：把"注入到 prompt"等同于"被使用"——是间接证据。被 inject 但实际过期的条目会被人为续命。需要额外的状态机和并发保护。

### 3.2 ❌ 方案 B：把 decay 锚到 `manifest.LastSeen`

> 改一行 `Score()`：`lastActive` 多 max 一个 `p.Manifest.LastSeen`。"只要项目还在用，里面所有条目都不衰减"。

**为什么放弃**（用户当场指出）：**`LastSeen` 是单个时间戳，无法区分"天天用"和"歇了 30 天后偶尔打开一次"**。用户停用 30 天后开一次 session，LastSeen 一刷新，所有条目衰减时钟归零——比现状更糟（现状至少诚实地让它们衰减掉）。

**这条教训值得记**：用一个时间戳代表"活跃度"是错的；活跃度本质是**频率**，至少需要两个观测点或一个有界数组。

### 3.3 🟡 方案 C：`RecentSessions` 滑动窗口（决策保留作为 fallback）

manifest 增加 `RecentSessions []time.Time`，bounded 30 个，每次 SessionStart prepend `now` 并截断。

```go
func (m Manifest) IsActive(now time.Time, window time.Duration, min int) bool {
    cutoff := now.Add(-window)
    count := 0
    for _, t := range m.RecentSessions {
        if t.After(cutoff) { count++ }
    }
    return count >= min
}
```

阈值建议："过去 14 天内 ≥ 3 次 session"，常量用 env var 暴露（`SEEK_MEMORY_ACTIVE_WINDOW_DAYS` / `SEEK_MEMORY_ACTIVE_MIN_SESSIONS`）。

`Score()` 分支：项目活跃 → `lastActive = now`（事实上关 decay）；不活跃 → 退回每条目公式继续 decay。

**保留为 fallback** 而非首选——因为它仍然是粗粒度的项目级判定，不能区分"哪些条目还相关、哪些已经死掉"。但工程量极小（一字段、一方法、5 行公式分支），如果阶段 1（下文）来不及做，可以先上 C 顶着。

## 四、目标架构：两线程主动智能

### 4.1 架构形态

```
┌──────────────────────────────────────────────────────────────┐
│ 主线程：LLM ↔ User                                            │
│   - 用户输入 → Prompt → tool calls → 答复                     │
│   - 写入 memory_observe（模型显式调用，已有）                 │
│   - 读取 M-index（PrePromptHook 注入，已有）                  │
└──────────────────────────────────────────────────────────────┘
            │
            │ 事件流（每轮 / SessionEnd / 周期）
            ▼
┌──────────────────────────────────────────────────────────────┐
│ 评估线程：Memory Curator                                       │
│   能力（按价值排序）：                                         │
│   ① 被动续命：观察哪些 entry 的行为体现 → bump LastRecalledAt │
│   ② 补漏写入：用户说了但模型没 observe → 补一条               │
│   ③ 矛盾检测：用户行为与 entry 冲突 → 标 archive / 触发更新   │
│   ④ 跨 session 综合：dream 已实现的形态，迁入此线程统一调度    │
└──────────────────────────────────────────────────────────────┘
```

### 4.2 分阶段路径

| 阶段 | 评估器形态 | LLM 调用 | 解决 § 1 问题的程度 | 工程量 |
|------|----------|----------|--------------------|--------|
| **0** | 当前 | 无 | 0% — 即问题所在 | — |
| **1** | **Heuristic 后台 updater** | **0** | ~80%（覆盖能力 ①） | 小 |
| **2** | **周期性 LLM 评估**（每 N 轮 / SessionEnd） | V4-Flash，~1 次/N 轮 | ~95%（① + 部分 ②③） | 中 |
| **3** | **连续 LLM 评估**（实时） | V4-Flash，~1 次/轮 | ~100% | 大 |

#### 阶段 1：Heuristic 后台 updater（推荐先做）

**核心**：不调 LLM，纯字符串/关键词匹配。每轮（或每 K 轮）扫最近 N 轮对话，看是否有 entry 的 tagline 关键词 / `Tags` 出现在用户消息或模型响应里——出现就 bump 该 entry 的 `LastRecalledAt`。

**伪代码**：

```go
// hook.go (or a new curator.go)
func (h *Hook) OnPostTurn(ctx context.Context, ev hooks.PostTurnEvent) {
    if h.Project == nil { return }
    recent := windowOfRecentMessages(ev, kTurns)
    for _, entry := range h.Project.Entries() {
        if matchesHeuristic(recent, entry) {
            h.Project.TouchInjection(entry.Name, time.Now())  // 仅刷 timestamp
        }
    }
}
```

**`matchesHeuristic` 的可能实现**（简单到复杂）：

- v1：tagline 拆词 → 在最近 N 轮文本里子串匹配（任意 token 命中即算）
- v2：加 `Tags` 字段命中
- v3：tool 调用名匹配（"edit" tag 的 entry 在 edit 调用频繁的轮里加权）

**为什么"被动续命"在阶段 1 已经基本够用**：决定 entry 命运的是"是否被使用"，行为里出现 tagline 关键词就是直接证据。能力 ②③ 需要语义理解才能做对，留到阶段 2。

**代价**：
- token：0
- 写盘：每轮可能 1 次（仅当有 entry 被匹配中）。但 `TouchInjection` 只改 timestamp 字段——可以做"内存里累积 + 节流写盘"避免每轮 IO
- 复杂度：~150 行 Go + 测试

#### 阶段 2：周期性 LLM 评估

每 N 轮（建议 5 轮）或 SessionEnd 时，用 V4-Flash 跑一次评估 pass：

- 输入：最近 N 轮对话 + 当前 M-index entries
- 输出：JSON `{ touch: [names], archive: [{name, reason}], create: [Entry], merge: [...] }`
- 主线程接到 result 后异步应用变更

**升级触发条件**：阶段 1 跑一段时间后，看哪些 case heuristic 没覆盖到（漏写入、过期未归档）。需要语义判断的占比 ≥ 30% 时上阶段 2。

#### 阶段 3：连续 LLM 评估

主线程每轮都触发评估线程，评估线程异步消费。等同于"双 agent" 架构，主消费、副策展。

**升级触发条件**：仅当真实用例需要"实时"反馈（比如对话中段 archive 一条立即影响后续 turn）时。**很可能用不到。**

### 4.3 与现有异步路径的关系

- **dream**：已经是评估器的雏形（看历史 sessions、综合写 L 层）。阶段 2 可以把 dream 从"cadence 触发"扩成"事件触发"——SessionEnd / 每 N 轮 / 内存压力都能触发，不只是按时间
- **memory_observe**：保留为模型主动写入路径。评估线程是补漏，不替代
- **OnSessionEnd**：v2 退化的 no-op 位是评估线程触发的天然钩子

## 五、共有的代价与风险

无论走阶段几，下列代价是共同的：

1. **最终一致性**：评估线程的对话视图滞后于主线程。主线程的 `memory_observe` 写入和后台判定可能撞车——需要 last-writer-wins 或 per-name 锁。`internal/memory/hook.go:85` 已有 `observeLocks` 模式，复用即可
2. **可测试性**：阶段 1 可以纯单元测试；阶段 2/3 涉及 LLM-in-loop，需要 mock 客户端 + 端到端 fixture
3. **可复现性**：session 重放结果不再字节稳定（后台异步写时序非确定）。要么记录评估器的所有决策（多一份 jsonl），要么接受这个事实
4. **token 成本**：阶段 1 零；阶段 2 ~ +20% 总 token；阶段 3 ~ +80%–100%
5. **prefix cache 友好性**：评估线程的写入会改 memory.jsonl → 主线程下次 PrePromptHook 注入字节变了 → 缓存 miss。**关键约束**：评估器的写入应该在 SessionEnd 触发，或者在 PrePromptHook **之前**完成，避免单 session 内 cache 失效

## 六、当下决策

1. **不做"两线程"完整架构**——阶段 3 那一步暂时不上
2. **先做阶段 1**：heuristic 后台 updater，覆盖 § 1 的 decay 体感问题，零 token 成本
3. **保留方案 C（`RecentSessions`）作为兜底**：如果阶段 1 落地遇阻或效果差，先上方案 C 顶住 21-天-消失的 UX 问题，不影响后续演化
4. **PRD 的 § 4.2 阶段 2/3 是路线图**：不在当前 sprint 实施，但写下来给后续提供路径

## 七、决策与升级触发条件

| 当前在阶段 | 升级到下一阶段的触发条件 |
|----------|--------------------------|
| 0 → 1 | 用户反馈"明明天天用却 21 天后消失"出现过一次 |
| 1 → 2 | 阶段 1 上线后 1 个月内，仍有 ≥30% 的"应当 archive 的过期条目"未被 heuristic 识别 |
| 2 → 3 | 阶段 2 的 N=5 轮延迟造成实际 UX 损失（比如 archive 不及时影响后续 turn） |

## 八、相关文件

- `internal/memory/memory.go` — `Entry`、`Manifest`、`IndexEntry`
- `internal/memory/project.go` — `TouchRecall`（参考实现）、`Index()`、`writeEntries()`
- `internal/memory/score.go` — `Score()`、`RunGC()`（方案 C 在这里改公式）
- `internal/memory/hook.go` — `OnPrePrompt`/`OnSessionStart`/`OnSessionEnd`，阶段 1 的 `OnPostTurn` 接入点
- `internal/memory/dream.go` — 阶段 2 后参考改造为"事件触发"的评估器骨架
- `internal/hooks/hooks.go` — `PostTurnEvent`、`SessionEndEvent`（已有，可直接接）
- `pkg/agent/agent.go` — `NotifyPostTurn`（已有，每轮触发，是阶段 1 的事件源）
- `docs/prd/v1.md` §6 — 原始 decay 模型（设计假设的源头）

## 九、为什么这是"主动智能"的核心

记忆策展是 agent 从"被动响应"走向"主动维护内部状态"的第一个、也是最低风险的能力——

- **范围有界**：只读对话流、只写自己的 jsonl，不外溢
- **失败可恢复**：评估错了最多丢条记忆，不会破坏代码或外部状态
- **能力可分阶段**：heuristic → 浅 LLM → 深 LLM 是平滑曲线，不需要架构跳变
- **可观测**：每次评估的输入输出可以全部落盘，回放/复盘成本低

一旦这条主动策展线建好，后续的"主动智能"能力（自动开 task、根据用户模式调整工作流、未问先答）都可以复用同一套**评估线程基础设施**——所以这条不是单纯修一个 decay bug，而是为后续几代 seek 能力埋的地基。
