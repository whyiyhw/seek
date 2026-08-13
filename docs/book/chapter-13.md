# 第 13 章：成本与上下文预算

> 对应代码：`internal/cache/`、`internal/pricing/`、`internal/budget/`，以及 `pkg/agent/agent.go` 里 `finish_reason="length"` 的处理。
> 起点：前面章节里反复埋伏笔——前缀缓存、status bar 上的 ctx%、`/compact` 阈值、cumulative vs last-turn——但都没正面讲。
> 终点：这三件事汇成一个故事：seek 怎么知道现在花了多少钱、上下文还剩多少空间、应该在什么时候提醒用户停一停。

---

这一章在原 TOC 里不存在。它是 M5 / M6 落地之后才浮出来的需求——前面那么多设计决定都把"成本意识"当成是隐含的，但等真用起来，"我现在到底花了多少 / 我还能聊多久"这两个问题用户每天问，每天找不到答案。

三个独立的小模块各管一摊事：

- **`internal/cache`** 跟踪每一轮的 token 用量，特别是缓存命中率
- **`internal/pricing`** 把 token 数翻译成美元
- **`internal/budget`** 把"还剩多少上下文"分级成 OK / Warn / Critical

它们都很短（每个 100 行上下）。短不代表无趣——三个模块各自有一两条反直觉的设计决定。

---

## 13.1 `cache.Tracker`：每轮一条记录, 成本在 Record 时锁定

```go
// 每轮一条 turnRecord——usage 是事实, cost 是当时按 (model, tier) 算好的
type turnRecord struct {
    Usage deepseek.Usage
    Model string
    Tier  pricing.Tier
    Cost  float64  // 在 Record 时定格, 之后不再重算
}

type Tracker struct {
    mu    sync.Mutex
    turns []turnRecord
}

func (t *Tracker) Record(u deepseek.Usage, model string, tier pricing.Tier) {
    cost := pricing.Cost(model, tier, u)
    t.mu.Lock()
    t.turns = append(t.turns, turnRecord{Usage: u, Model: model, Tier: tier, Cost: cost})
    t.mu.Unlock()
}
```

整个 Tracker 干的事情依旧很少:append 一个结构体。没有滑动窗口、没有 ring buffer、没有累计字段。

**保留原始数据 + 按需聚合**:每轮的 `Usage` + 当时计算好的 `Cost` 一并存进切片, 需要什么 token 聚合就遍历现算。聚合代价 O(N), N 是轮数(典型 < 100), 完全可以接受。

```go
func (t *Tracker) Cumulative() deepseek.Usage {
    // 累加 PromptTokens / CompletionTokens / Hit / Miss / Total
    // ...
}

func (t *Tracker) Last() deepseek.Usage {
    // 最近一条 turnRecord 的 Usage
    // ...
}

// 钱不是从 Cumulative 推出来的——必须用 CumulativeCost
func (t *Tracker) CumulativeCost() float64 {
    var sum float64
    for _, r := range t.turns { sum += r.Cost }
    return sum
}

func (t *Tracker) LastCost() float64 {
    if len(t.turns) == 0 { return 0 }
    return t.turns[len(t.turns)-1].Cost
}
```

> **为什么不在 render 时按当前 (model, tier) 重算？**
>
> 早期版本只存 `[]deepseek.Usage`, 状态栏每帧调一次 `pricing.Cost(currentModel, currentTier, cumulative)`。直到 commit `064a37a` 这条路径出问题:
>
> - 用户在 V4-Flash 跑了 30 轮, `/model deepseek-v4-pro` 切到 V4-Pro。下一帧渲染时, 前面 30 轮的 token 全部被按 V4-Pro 价格(~3.1× 贵)重新计价——累计成本数字突然跳一大截, 但 DeepSeek 那边其实已经按 V4-Flash 价格扣过钱了。
> - 同样地, 跨过 00:30 北京时间的离峰窗口边界, 历史成本会被整段从 standard 价拉到 5 折——状态栏的"已花"和真实账单永远对不上。
>
> 修法:Record 时就锁定 cost。`Usage` 还是事实, 但 `Cost` 是"那一刻按 (model, tier) 算出来"的不可变快照。Cumulative 还是 token 聚合视图; **成本不再是 token 的派生量, 而是一个独立的, 按事件流追加的数。**

这种"数据是事实, 聚合是视图"的分离让模块非常稳定。Tracker 在重写为 `turnRecord` 后, 外部调用方只需要在原来 `Cost(...,Cumulative())` 的地方换成 `CumulativeCost()`——所有视图都迁移完。

---

## 13.2 cumulative vs last-turn：埋了三章的反直觉

第 8 章和第 9 章都埋过一句"ctx% 必须用 `Last()` 而不是 `Cumulative()`"。现在来正面讲。

直觉是这样的：状态栏的 ctx% 表示"当前上下文占用百分比"。最朴素的算法：

```go
ctxPct := 100 * tracker.Cumulative().PromptTokens / contextLimit
```

跑十几轮没问题。跑五十轮——超过 100%。模型还在愉快地工作着（实际 context 只占 30%），状态栏却显示 173%。

发生了什么？

**每一轮的 prompt 都重发完整历史**。第 4 章讲过这是 chat completions 协议的本质——历史无状态、整段发出去。所以：

- 第 1 轮：prompt = 10k token（system + user）
- 第 2 轮：prompt = 12k（含第 1 轮的 assistant）
- 第 3 轮：prompt = 18k（再加 tool 结果）
- ...
- 第 N 轮：prompt ≈ N × 平均轮长

`PromptTokens` 在每轮 Usage 里**已经是这一轮发出去的完整 prompt 大小**，不是"这一轮新增的"。`Cumulative.PromptTokens` 把每轮的完整 prompt 全加起来——是个 **O(turns²)** 的量。50 轮的 cumulative ≈ 50 × 平均一次性 prompt size，但**实际上下文窗口的占用**只是最后一次 prompt 的大小。

> **坑 #15：multi-turn 客户端的"累计 prompt tokens"不是上下文预算信号**
>
> 每轮重发历史 = `Cumulative.PromptTokens` 在 O(N²) 增长，但实际 context 利用率只取决于"最近一次 prompt 有多大"。
>
> 把 cumulative 当作"还剩多少空间"的指标，会在 50 轮左右开始撒谎，在 100 轮时让用户看到 "ctx 287%"。后果不只是显示难看——基于这个数字触发的"该 compact 了"提示会**永远早 / 永远迟**地触发（取决于平均轮长），用户因此对它失去信任。
>
> 规则：**`Cumulative` 用来算钱**（已花的 token × 单价 = 真实成本），**`Last` 用来算空间**（最近一次 prompt 的大小 = 当前 context 利用率）。两者是不同维度的事实。
>
> commit `e240ea5` 修复，正确的字段：`tracker.Last().PromptTokens / budget.Limit(model)`。

这条坑值得展开是因为它说明了一个更通用的现象：**当系统的某个状态变量呈非线性增长，把它当线性指标用一定会在某个 N 之后撒谎**。常见的还有：磁盘 IO 总字节当带宽用、累计请求数当 QPS 用、累计错误数当错误率用。原始数据没错，错的是把它当成另一类指标。

### `Last` 的边界情况

```go
func (t *Tracker) Last() deepseek.Usage {
    t.mu.Lock(); defer t.mu.Unlock()
    if len(t.turns) == 0 {
        return deepseek.Usage{}    // ← 零值，不是 nil
    }
    return t.turns[len(t.turns)-1]
}
```

会话刚开始没有任何轮时，`Last()` 返回 `Usage{}`（零值）。状态栏看到 `PromptTokens == 0` 时显示 `ctx 0%`——这是正确的，也是想要的。如果返回 `nil` 或 sentinel，调用方就得加一层判空逻辑。

零值是 Go 的 affordance，**任何你能用零值替代 nil 的地方都该用零值**——少一处 nil check，少一类 panic 路径。

---

## 13.3 `pricing`：定价表 + 离峰窗口

```go
type ModelPricing struct {
    InputMissPerMTok float64 // cache miss
    InputHitPerMTok  float64 // cache hit
    OutputPerMTok    float64
}

var standardRates = map[string]ModelPricing{
    deepseek.ModelV4Flash: {
        InputMissPerMTok: 0.44,
        InputHitPerMTok:  0.014,
        OutputPerMTok:    1.32,
    },
    deepseek.ModelV4Pro: {
        InputMissPerMTok: 1.32,
        InputHitPerMTok:  0.044,
        OutputPerMTok:    3.96,
    },
    // ...
}
```

整个定价表是一个 `map`——**写死在代码里**，每次 DeepSeek 涨价/降价时改一次。上面是 2026-08-16 起生效的**高峰价**；错峰时段按 `offPeakDiscount` 打 5 折（DeepSeek 官方定义为"错峰 = 高峰半价"）。

为什么不在启动时从 DeepSeek 拿一份"current pricing"？两个理由：

- **DeepSeek 不提供这个 API**——价目表只在网页上，没有官方接口
- **就算有，也不该用**：成本显示是 seek 的**纯防御性功能**——它不是核心路径，但反过来如果它依赖网络抓取，就给启动路径加了一个失败模式。"网络断了 → 启动失败 → 用户看不见错误就一直在打字" 是真实可发生的坏体验

PRD §4.8.4 把这件事写明白了：**embedded rates, bump them with each release**。每次发版核对一次价目表，刷新这个 map 的值，commit。代价是低频维护，换来"没网络也能启动"的可靠性。

> **V4 别名 sunset 与一次错误定价的教训**(commit `94d8cbc`)
>
> DeepSeek 在 2026-01 发 V4 时, 同时保留了两个 legacy 别名:`deepseek-chat`(对应 V4-Flash, thinking 关)和 `deepseek-reasoner`(对应 V4-Flash, thinking 开)。官方公告:**2026-07-24 这两个别名下线**, 之后必须直接写 `deepseek-v4-flash` / `deepseek-v4-pro`。
>
> seek 的 pricing 表对这两个别名做了显式映射, 共享 V4-Flash 的费率(当时的费率 `$0.14` miss / `$0.0028` hit / `$0.28` output, 全部按每百万 token; 2026-08-16 起已换成峰谷价):
>
> ```go
> deepseek.ModelChat:     { 0.14, 0.0028, 0.28 },  // → V4-Flash, sunset 2026-07-24
> deepseek.ModelReasoner: { 0.14, 0.0028, 0.28 },  // → V4-Flash + thinking, sunset 同上
> ```
>
> 早期版本曾经把 `ModelReasoner` 错配成 V4-Pro 的费率(`$0.435` miss / `$0.87` output)——理由是"reasoner 听起来比 chat 贵"。结果一个用 `deepseek-reasoner` 跑了几小时的会话, 状态栏显示 $1.30+, 实际 DeepSeek 那边只扣 $0.42, **大约 3.1× 的虚高**。修法不只是改两个数字, 而是把"别名 → 真实后端"的映射在 pricing.go 注释里写明白:**reasoner 本质是 thinking-on 的 V4-Flash**, 不是另一档定价。
>
> 教训:**当两个 SKU 名字暗示价格差异但底层是同一引擎时, 你的定价表必须显式记录这个事实, 否则未来某个人会按命名直觉再踩一次**。

### 缓存命中 / 未命中分别计价

```go
func Cost(model string, tier Tier, u deepseek.Usage) float64 {
    p := PricingFor(model, tier)
    const million = 1_000_000.0
    return float64(u.PromptCacheMissTokens)*p.InputMissPerMTok/million +
        float64(u.PromptCacheHitTokens)*p.InputHitPerMTok/million +
        float64(u.CompletionTokens)*p.OutputPerMTok/million
}
```

这是 seek 整个成本故事里**最有意义的一行代码**。

DeepSeek V4-Flash 的 input cache miss 是 $0.44/MTok，cache hit 是 $0.014/MTok——**约 31 倍**差距（2026-08-16 起的峰谷价表，此前促销价是 $0.14 / $0.0028，差距 50 倍）。一份典型 seek 会话里，system prompt + 工具 schema 加起来 ~2 KB，每一轮都重发；如果第二轮命中了前缀缓存，这 ~500 个 token 的成本从 $0.00022 降到 $0.000007——单次几乎免费。

第 2、5 章反复强调的"Schema 必须是 package-level `[]byte` 常量"、"system prompt 字节稳定"——这条 `Cost` 函数是这些约束的最终兑现点。两个会话跑同样的逻辑，命中率 70% 的那个比 0% 的便宜 5-7 倍。

把命中/未命中**分开计价**而不是"算一个平均输入价"——是为了让用户看到"我命中了多少 / 我省了多少"。`Tracker.SavedTokens()` 就是这个数据：

```go
func (t *Tracker) SavedTokens() int { return t.Cumulative().PromptCacheHitTokens }
```

返回命中的 token 数。这些是"假如没缓存就要按 miss 价付的 token"——状态栏可以显示 "saved 280k tokens"，让用户对自己的工作流有个直观印象。

### 离峰折扣

```go
// 高峰窗口（北京时间）：09:00–12:00 和 14:00–18:00，
// 对应 DeepSeek 官方高峰 01:00–04:00 / 06:00–10:00 UTC。
// 其余时间全部错峰（半价）。
const (
    peak1StartMins = 9 * 60
    peak1EndMins   = 12 * 60
    peak2StartMins = 14 * 60
    peak2EndMins   = 18 * 60
)

const offPeakDiscount = 0.5

func CurrentTier(now time.Time) Tier {
    b := now.In(Shanghai)
    mins := b.Hour()*60 + b.Minute()
    if (mins >= peak1StartMins && mins < peak1EndMins) ||
        (mins >= peak2StartMins && mins < peak2EndMins) {
        return TierStandard
    }
    return TierOffPeak
}
```

DeepSeek 从 2026-08-16 起实行正式的**峰谷计价**：每天两个高峰时段（北京 09:00-12:00、14:00-18:00，即 UTC 01:00-04:00、06:00-10:00），其余时间全部错峰半价。注意语义反转：**错峰是默认，高峰是例外**——所以 `CurrentTier` 的默认返回值是 `TierOffPeak`。对常加班的程序员来说，晚上和中午跑大任务都能吃半价——状态栏显示当前是 off-peak / peak，让用户知道"现在跑大任务划算还是不划算"。

时区写死成 `Shanghai = time.FixedZone("CST", 8*60*60)`，**不调用 `time.LoadLocation("Asia/Shanghai")`**。理由：`LoadLocation` 依赖系统 tzdata，最小 Linux 容器（Alpine、distroless）经常不带——`time.LoadLocation` 在那些环境上会返回错误。`FixedZone` 是纯计算，零文件系统依赖，永远工作。

代价：不处理 DST（夏令时）。但中国从 1992 年就不用 DST 了，所以这个简化对 DeepSeek 离峰窗口完全没影响。

### `NextTransition`：让用户知道还剩多久

```go
// 现在是 peak，下次切换到 off-peak 是什么时候？
// 现在是 off-peak，下次切换到 peak 是什么时候？
func NextTransition(now time.Time) (Tier, time.Time) {
    // ...
}
```

这个函数驱动状态栏的"next 🌙 in 2h14m"倒计时——四个边界（两个高峰的起止）里，`NextTransition` 返回下一个。

预设 hook 但不接入的代价基本为零（几十行代码 + 一组测试），收益是"未来想加 UI 时不需要回头补底层逻辑"。这是值得做的小事。

---

## 13.4 `budget`：把 token 数翻译成"几分严重"

```go
const (
    WarnFraction     = 0.60  // 状态栏开始变色
    CriticalFraction = 0.75  // 明确提示 /compact
)

type Severity int

const (
    SeverityOK Severity = iota
    SeverityWarn
    SeverityCritical
)

func Classify(model string, usedTokens int) Severity {
    limit := Limit(model)
    if limit <= 0 { return SeverityOK }
    frac := float64(usedTokens) / float64(limit)
    switch {
    case frac >= CriticalFraction: return SeverityCritical
    case frac >= WarnFraction:     return SeverityWarn
    default:                       return SeverityOK
    }
}
```

`Classify` 是状态栏和 `/compact` 提示的唯一信号——所有 UI 颜色变化都查这个函数。`usedTokens` 接的是 `tracker.Last().PromptTokens`（上一节讲的）。

### 60/75 而不是 80/95

最早的阈值是 80/95——commit `b01bc17` 降到了 60/75。

为什么降？DeepSeek V4 是 1M context。95% × 1M = 950k token。"95% 才提示 compact" 意味着用户在 950k token 之后才看到提示——这时候：

- **compact 调用本身代价不小**：summary 请求要处理 ~900k token 的 prompt，按当前定价就是真金白银（$0.44 × 900k / 1M = $0.396 美元单次，高峰价）
- **模型质量在那个规模已经在下滑**：长 context 上模型注意力衰减是公认的，95% 不是"还能用"的点，是"快用不动了"的点

降到 60/75 后：

- 60%（600k token）变色提醒——用户开始意识到 "我对话已经很长了，下个大任务前考虑 compact 一下"
- 75%（750k token）明确建议 compact——这个点 compact 后能省下 65% 的 context，下次跑还能跑很久

阈值是**根据"早多远还能行动"反推**，不是按"还剩多少容量"凭感觉。

### `Default = 128_000`：未知模型的保守回退

```go
const Default = 128_000

func Limit(model string) int {
    if v, ok := contextLimits[model]; ok { return v }
    return Default
}
```

当用户用了一个 seek 没认识的 model 名（比如刚发布的、表里没写的），不能假装它是 1M context——这会让 budget 永远不报警，用户实际溢出时才发现。Default = 128k 是"足够保守又不至于早报警到不能用"的折中。

写死的模型限制：

```go
var contextLimits = map[string]int{
    "deepseek-v4-flash":          1_000_000,
    "deepseek-v4-pro":            1_000_000,
    "deepseek-chat":              1_000_000,  // legacy 别名
    "deepseek-reasoner":          1_000_000,  // legacy 别名
    "claude-3-5-sonnet-20241022": 200_000,
    "claude-sonnet-4-20250514":   200_000,
    "gpt-4o":                     128_000,
    "gpt-4o-mini":                128_000,
    "gemini-2.0-flash":           1_000_000,
    "gemini-1.5-pro":             2_000_000,
}
```

M6 之后的多 Provider 名字一并写进来了——第 14 章会讲。即便那时还没接 OpenAI，把 limit 表预先填上，将来切 provider 不会出现"忘了改 budget 表"的 bug。

---

## 13.5 `finish_reason="length"`：一条没人提就静默的错误

写到这一段的时候有个相关 bug 值得记。

OpenAI / DeepSeek 的 chat completions 协议里，`finish_reason` 可以是 `"stop"`（正常完成）、`"tool_calls"`（要调工具）、`"length"`（**达到 `max_tokens` 上限被截断**）、`"content_filter"`（内容审核拦截）等。

第 4 章的 Agent 循环最早的代码只判断了前两种——`stop` 退出循环、`tool_calls` 执行工具。剩下的全都走"退出循环"路径，**没有任何提示**。

用户实际遇到的现象：问一个需要长回答的问题，看到模型答了一段、戛然而止、TUI 显示完成。中间没有任何错误，用户也不知道答案被截断了——他可能会以为模型就是答到这。

修复（commit `e240ea5`）：

```go
// pkg/agent/agent.go
if finish == "length" {
    out <- ErrorEvent{Err: fmt.Errorf(
        "agent: response truncated (finish_reason=length, max_tokens=%d) — use /compact to free context or ask me to continue",
        a.cfg.MaxTokens)}
}
```

把 `length` 当作 `ErrorEvent` 发到 TUI，让用户看到"截断了"的明确信息，附带建议（继续 / compact）。

> **坑 #16：API 协议里的非 `stop` 完成原因都要显式处理，否则会变成静默截断**
>
> 协议设计上 `finish_reason` 是个枚举，每个值有不同含义。代码里写 `if finish == "stop"` 之后默默 fallthrough 的路径会把所有非 stop 情况吞掉。
>
> 包括但不限于：`length`（被 max_tokens 切了）、`content_filter`（被审核拦截）、`function_call` / `tool_calls`（要调工具）、`null` / 空（流被切断或服务端 bug）。每一种都要 case，且每种都要可视化给用户。
>
> 反过来，写 `switch` 而非 `if` 是这条纪律的"建筑约束"——`default` 分支强迫你想"还有没有其他值"。

这件事直接和这一章相关：`length` 截断意味着"模型答到一半就停了"——如果用户不知道，他会**重新问一次同样的问题**（再花一次 token），或者基于半截答案做错决定。protocol-level 错误如果在 UI 上不可见，token 成本就翻倍。

`MaxTokens` 同时也从服务器默认 4096 提到了 8192——简单的、立即可见的缓解（commit `e240ea5` 里的另一半）。但治本的还是上面那段 ErrorEvent。

---

## 13.6 三个模块怎么 wire 到一起

```go
// internal/tui/view.go 里的状态栏渲染
tier := pricing.CurrentTier(now)
ctx := budgetCtx{
    Model:            m.opts.Model,
    Tier:             tier,
    Usage:            m.opts.Tracker.Cumulative(),  // 算钱用
    LastUsage:        m.opts.Tracker.Last(),        // 算 ctx% 用
    StreamElapsed:    streamElapsed,
    StreamDeltaBytes: m.streamDeltaBytes,
}
```

三个模块在 view 层汇合，画成一行：

```
  ● 4.2s · ↓~312tok    ctx 28%    model: deepseek-chat    standard    $0.0247 saved 280k
                              ↑                              ↑              ↑
                       budget.Classify              pricing.TierLabel    pricing.Cost
                       (LastUsage)                                       (Cumulative)
                                                                        cache.SavedTokens
```

每个字段背后都是上面三个模块里的一个函数。

事件流向：

- Agent 每轮结束发 `TurnEnd{Usage: ...}` 事件
- TUI 收到，调 `tracker.Record(e.Usage, currentModel, pricing.CurrentTier(time.Now()))`
- 下一次 statusTick / 下一帧 View 时，从 tracker 取 `Last` / `Cumulative` 看 token, 取 `LastCost` / `CumulativeCost` 看钱

整个数据流是单向的——Agent 只写 Tracker，TUI 只读 Tracker。中间没有共享状态、没有 race。

`tracker.Record` 内部加了锁，因为 TUI 读 Cumulative/Last 和 Agent 写 Record 可能并发（Agent 在 goroutine，TUI 在 main loop）。锁是必须的，不是装饰。

### 在 `/compact` 路径上也要 Record

第 10 章讲过：`Summarise` 自己也是真实 LLM 调用，它的 token 用量必须录进 tracker，否则状态栏的成本数字会偷偷少算。

```go
// internal/tui/update.go: handleCompactDone
if m.opts.Tracker != nil {
    // 注意:这里要传当时调用 Summarise 用的 model + tier, 不是"现在的"——
    // /compact 是同步调用, 通常就一瞬间, 但纪律要保持一致
    m.opts.Tracker.Record(msg.usage, msg.model, msg.tier)
}
```

这一行不在 Agent 的正常事件流里——`Summarise` 是个一次性同步调用，不发 TurnEnd。需要手工 Record。如果忘了：每次 compact 之后状态栏的成本数字都会少几分钱，越长的会话偏差越大。

这种"在主路径之外的额外用量"是状态栏数据准确性的常见坑。`/branch` 没有额外 LLM 调用，所以不用记；`/compact` 有，必须记。**任何花 token 的动作都要走过 tracker**——这是一条简单的纪律，但漏一处状态栏就开始撒谎。

> 同样的纪律也覆盖了 v0.2.x 加进来的 memory 子系统:`/distill`(S→M 蒸馏)和 `seek -dream`(M→L 做梦)都跑独立 LLM 调用, 它们的 usage 也走 `tracker.Record`——第 16 章会专门讲。

---

## 13.7 一个观察：成本意识是项目里的"诚实度指标"

回看这一章：cache.Tracker 只存原始数据、pricing 写死表不抓远程、budget 用 last-turn 不用 cumulative、compact 自身的 usage 也要 record……每一条都在做同一件事——**让显示给用户的数字是真的**。

如果状态栏少算了 compact 的钱（少几分钱），用户不会立刻发现，但他会慢慢对状态栏失去信任："这个数到底准不准？我也不知道。"——一旦失信任，状态栏就变成装饰，没人看，再加新功能也没用。

反过来，如果数字准——用户知道"我今天写代码花了 $0.34，省了 1.2M token"——他会主动去优化自己的工作流（多用 /compact、避免破坏缓存的操作）。状态栏从"装饰"变成"行为反馈回路"。

这个"诚实度"很难写进 PRD（"状态栏数字必须准确"听起来像废话），但它是一系列具体决定的累积——**每一个数字都要追到一个明确的数据源 + 一个明确的算法 + 一个明确的更新时机**。任何一处偷懒（"差不多"、"估算"、"先 TODO 后面补"），用户都会感觉到。

---

### 相关踩坑

成本与上下文预算实现中遇到的具体问题，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. 累计 prompt tokens 不是上下文预算信号**

- **Saw**：状态栏用 `Cumulative()` 显示 context 利用率（ctx%），50 轮后显示 300%，用户困惑。
- **Why**：累计 prompt tokens 在多轮对话中持续增长（因为消息历史不断追加），不是"当前上下文窗口还剩多少"的信号。
- **Lesson**：context 利用率必须用 `Last()`（当轮消耗），而非 `Cumulative()`（累计消耗）。

**2. `finish_reason="length"` 看起来像正常结束**

- **Saw**：模型输出在 token 上限被截断时返回 `finish_reason="length"` 而非 `"stop"`。未检查此字段的用户以为回答已完整，重问后浪费一轮成本。
- **Fix**：在 TUI 状态栏和消息渲染中显式标记被截断的输出，提示用户需要 `/compact` 或增加 `MaxTokens`。

**3. 成本在模型或 tier 切换后被回溯重算**

- **Saw**：用户中途 `/model` 后，历史对话成本按照新模型价格重算，状态栏金额跳动。
- **Fix**：成本在 Record 时锁定（快照当前 model/tier/价格），不再 render 时重算。

**4. PrePromptHook 输出必须字节稳定**

- **Saw**：memory snapshot、AGENTS.md 注入等 PrePromptHook 每次输出不同，导致系统提示词前缀每次变化，前缀缓存永不命中。
- **Lesson**：任何注入系统提示词的内容必须字节序列稳定，否则每次缓存失效 → 10 倍成本。

**5. 前缀缓存是尽力而为的优化**

- **Saw**：短 prompt 几乎不触发缓存；首次请求缓存命中率 0%。
- **Lesson**：前缀缓存要求提示词前缀 ≥ ~64 token 且字节完全一致。短 prompt 不值得优化。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。

---

## 本章小结

- `cache.Tracker` 每轮一条 `turnRecord`(Usage + Model + Tier + Cost)——token 是事实, 钱是 Record 时锁定的快照, 切 `/model` 或跨离峰边界不会回溯改写历史(commit `064a37a`)
- **Token 聚合用 `Cumulative` / `Last`, 钱聚合用 `CumulativeCost` / `LastCost`**——成本不再是 token × 当前费率的派生量
- **`Cumulative` 用来算 token 已花总量，`Last` 用来算 context 利用率**——这是 multi-turn 客户端最容易踩的指标错配（**坑 #15**）。把 cumulative 当 context 利用率，50 轮后开始撒谎
- `pricing` 表 hardcoded，每次发版手动核对——纯防御性功能不该给启动路径加网络失败模式
- 缓存命中/未命中分开计价，把 `Schema 必须是 []byte 常量` 这条贯穿全书的约束最终兑现成可见的省钱数字
- 时区用 `FixedZone` 不用 `LoadLocation`——避免最小容器没有 tzdata 的常见故障
- `budget` 阈值从 80/95 降到 60/75——按"早多远还能行动"反推，而不是"还剩多少容量"凭感觉
- `finish_reason="length"` 必须显式可视化，否则成本会因为用户重问翻倍（**坑 #16**）
- 任何花 token 的路径都要走 tracker —— `/compact` 的 Summarise 是个容易漏的例子
- 状态栏的"诚实度"是用户对工具信任的基础，由一系列具体小决定累积而成

下一章进入 M6 — 多 Provider 支持。我们会看到 `pkg/llm` 通用接口是怎么"故意不暴露 DeepSeek 独有字段"的、为什么 `pkg/deepseek` 绝对不能 import `pkg/llm`（CI 有 lint 强制），以及 Anthropic / OpenAI / Gemini 三家各自的协议怪癖（合并 tool_result / 没有 call ID / `stream_options` 必须显式打开 usage）。

---

*对应 commit：`e240ea5`（`Last()` + `finish_reason=length` + `MaxTokens` 默认值修复）、`b01bc17`（compact 阈值 80/95 → 60/75）、`064a37a`（per-turn 成本在 Record 时锁定, 不再 render 时重算）、`94d8cbc`（V4 别名 routing + 2026-07-24 sunset 备忘 + 修正 reasoner 错误定价）。运行 `go test -race ./internal/cache/... ./internal/pricing/... ./internal/budget/...` 验证。*
