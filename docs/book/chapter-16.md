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
}
```

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

## 16.3 注入机制: PrePromptHook 与 prefix-cache 的死结

L 和 M-index 怎么进入每次请求? 最早的方案是改 `pkg/agent` 在 prompt 拼装时直接插入。两周后回来看——这条路完全错。

**问题**: `pkg/agent` 现在要懂 memory 的存在;memory 改一行格式, 要碰 agent;memory 写测试要 mock 整个 agent 的请求拼装路径。两个子系统耦合死了。

修法:**hook 子系统** (`internal/hooks`)。agent 不知道 memory, 只知道"在 prompt 拼出来之前, 给注册的 hook 一次修改机会":

```go
// internal/hooks/hooks.go
type PrePromptHook interface {
    OnPrePrompt(ctx context.Context, in PrePromptIn) (PrePromptOut, error)
}

type PrePromptIn struct {
    UserText string
    History  []deepseek.Message  // 只读快照
}

type PrePromptOut struct {
    UserText      string     // 修改后的用户消息文本
    PrependBlocks []string   // 在用户消息前注入的 <context> 段
}
```

`internal/memory/hook.go` 实现 `PrePromptHook`:每次 `Prompt()` 触发前, 读 `~/.seek/soul.md`(L-stable) 和当前 project 的 M-index, 渲染成两段 `<context>...</context>` 块, 前缀到用户消息。

这条路径有一个**前缀缓存毒杀风险**——也是 v1 写完后撞到的最贵的一次坑:

> **坑(v1 #1):PrePromptHook 输出必须字节稳定, 否则前缀缓存全失效**
>
> DeepSeek 的前缀缓存以**完整 prompt 历史的字节序列**为 key。PrePromptHook 的输出处在缓存查找之前——它产生的 bytes 进入 prefix 的一部分。如果同一份磁盘状态下 hook 输出的 bytes 在两次 Prompt 之间不同, **所有旧消息都变成 cache miss**。
>
> 触发源:
> - Go map iteration 顺序随机
> - `time.Now()` 戳进注入文本
> - "让 LLM 现场格式化一遍"
>
> 这三种都是**静默的前缀缓存杀手**。修法:
> - `Project.Index()` 按 Name 字典序排序后输出
> - `FormatLCandidatesMarkdown` 对来源排序 + 去重
> - 集成测试 `TestHook_OnPrePrompt_ByteStable` 对同一份磁盘状态调两次 `Hook.OnPrePrompt`, **SHA-256 校验输出字节一致**, 不一致就 fail build
>
> 教训:**每一个被 PrePromptHook 产出的 byte, 必须是从磁盘内容确定性推出来的**。content-addressed render + hash 回环测试是这条纪律的"建筑约束"。

整套 PRD v1 §8 的 12 条验收里, 这条字节稳定性测试是排名第一的——比起"功能能跑", "缓存不毁"是 seek 整个项目持续命中 70%+ 缓存的命脉。

---

## 16.4 调用面:两个工具 + 两个命令 + 一组自动化

M 层的写入和召回, 暴露成模型可调用的工具:

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

容忍 reasoner JSON 输出不严格是这一层最值钱的事——这是 v1 的另一条坑:

> **坑(v1 #2):reasoner 的 "respond with JSON only" 指令不可靠**
>
> chain-of-thought 训练把 reasoner 模型往"先思考再回答"的方向推。哪怕 system prompt 写"Respond with JSON only", 它经常还是:
>
> - 先来一段 prose 序言("Here's what I extracted...")
> - 套一层 ```` ```json ```` 围栏
> - 单条目时返回 object 而不是 1-element array
>
> 修法:`internal/memory/distill.go:ParseCandidates` 和 `dream.go:ParseLCandidates` 都写了**容忍解析器**——剥围栏、跳 prose 直到第一个 `[` 或 `{`、把单 object 升成 `[obj]`。教训:**对 reasoner 的输出, 解析器要乐观, 不是严格**——严格解析换来的不是"作者认真填 prompt", 而是"经常解析失败用户骂街"。

### `seek -dream` — M → L 做梦

CLI flag, 离线跑:

```bash
$ seek -dream
  → 扫描 ~/.seek/projects/*/memory.jsonl
  → 收集最近 30 个 session 的关键决策
  → reasoner 提取 "用户跨项目的本源倾向"
  → 候选要求 N ≥ 2 个项目源, 否则丢弃
  → 输出 L-pending 到 stdout (-dream-write 直接 append 到 soul.md ## Pending)
```

`-dream` 是 *只读 + 输出*, `-dream-write` 是 *写文件*。两者分开让作者可以先看一眼候选, 再决定是否真写。 v1 没有任何路径让模型直接写 `~/.seek/soul.md`——**L 永远不被模型直写**。这条不变量值得显式记录, 因为破坏它就意味着模型可以静默改变用户的画像, 没有任何 review。

### M5.7 / M5.8 — 自动化层

手动 `/distill` 和 `seek -dream` 用户经常会忘了跑——记忆就退化成"有就有, 没有就算"。自动化层把蒸馏 / 做梦的 *时机* 也变成 seek 自己的事:

- **M5.7 — 自动 S → M 蒸馏**(commit `a9f89d5`)。SessionEnd hook 检查 satisfaction signal:user_turns ≥4、最近 5 条消息无 reject 关键词、tool 错误率 < 5% 等。**所有条件都通过**才触发自动 distill, 写进 `auto_sourced=true` 的 Entry。下次用户手动 `/distill` 时这些 entries 会浮出来供 review。 阈值故意偏保守——**false positive 污染 M, false negative 只是跳过一次 session**。
- **M5.8 — 自动周期 dream**(commit `374cfad`)。SessionStart hook 读 `~/.seek/dream-state.json`(`{last_dream_at, sessions_since_dream}`), 满足 "≥N 次 session 或 ≥K 天"任一条件时, 启动后台 goroutine 跑 dream, 把候选 append 到 `## Pending`。同样, **永不直接升 Stable**——pending 仍然要用户审定。

两条自动化都被环境变量 gated(`$SEEK_AUTO_DISTILL`、`$SEEK_AUTO_DREAM`), 用户可以关。开关在 ENV 而不是 config 文件, 因为这两个特性的"我想试试 / 不想要"切换得快, 改 ENV 比 edit config 直接。

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

## 16.8 一个观察: 记忆是 seek 从"工具"到"伙伴"的换挡

回到 §16.1 的动机:用户偏好被反复告知、跨项目模式无法识别、项目级决策易被遗忘——v1 验收清单全绿之后这些问题依然在, 因为 v0 只解决了"一次会话内的能力"。

加入三层记忆之后, seek 的形态发生了一个不那么显眼但很本质的变化:**它对"用户是谁"和"这个项目走到哪里"有了状态**。一个新会话不再从零开始, 它开始时已经知道:

- 你在这个项目里偏好不用 ORM, 因为 X (M-index 注入 + memory_recall 拉详情)
- 你跨项目总倾向显式错误处理 (L-stable 整段注入)
- 你上次会话决定了 Y, 还没改完 (S 历史 + `/distill` 把上次的决策 promoted 到 M)

这种连续性不是更多 tokens, 是**有结构的少量 tokens 在正确的层级被注入**。用户的体感是"这工具记得我", 但底下是 hook 协议、衰减打分、字节稳定 SHA-256 测试、双悬崖归档——一套不那么浪漫的工程纪律。

这跟第 13 章那条"诚实大于神秘"是同样的事:**所谓"工具懂你", 都是一系列小决定的累积:存什么、什么时候写、什么时候注、怎么避免缓存失效、用户怎么否决**。每一处都做对, 加起来是"它记得"。每一处偷懒, 加起来是"它在假装聪明"。

---

## 本章小结

- 三层架构 L/M/S 不是"为了对称", 而是**用户偏好 / 项目决策 / 当前会话**这三件事在写入频率、生命周期、可信度上根本不同, 强行扁平存会导致污染和漂移
- 两次单向蒸馏(S→M, M→L)各有自己的触发器、容忍 reasoner JSON、用户审定门——**模型从不直写 L**, 永远是"提议 → 用户升级"
- PrePromptHook 输出必须**字节稳定**, 否则前缀缓存被打成 0%; 用 SHA-256 round-trip 测试作为建筑约束(**v1 坑 #1**)
- `memory_recall` 无害, `memory_remember` 走 permission y/N——继承第 5 章"拒绝是工具结果, 不是崩溃"的原则
- M5.7 自动 distill 用 satisfaction signal 保守门槛(**false positive 污染 M, false negative 仅跳过一次**), 写入 `auto_sourced=true` 让下次手动 review 浮出
- M5.8 自动 dream 用 cadence 状态文件 + ENV 开关; pending 永不直升 stable
- GC 用衰减打分 + 双悬崖归档——避免边缘 entry 在 active 和 archived 之间通勤(hysteresis 模式)
- 项目身份用 `sha256(absPath)[:16]`, mv 兼容用 `<project>/.seek/project-id` 指针;后者在 .gitignore(commit `7d019f5`)
- 整个子系统通过 `internal/hooks` 协议与 `pkg/agent` 解耦——agent 不知道 memory 存在, memory 不依赖 agent

下一章进入 M8 — Skill 生命周期管理。我们会看到目录包 vs 单文件 .md 怎么共存、`seek skill install` 三种来源(local / git / https tarball)的取舍、`.install.json` sidecar 为什么不和 SKILL.md frontmatter 合并、调用统计 `.stats.jsonl` 怎么用 O_APPEND 做 race-free 写入。

---

*对应 commit:`08660a1`(hooks substrate)、`8b14061`(M5.a wire hooks)、`6c8f475`(M5.0 storage)、`c748d2b`(M5.1 tools)、`46a3e7e`(M5.2 PrePrompt 注入 + GC)、`6817a0b`(M5.3 distill logic) + `a377a2a`(M5.3 TUI modal)、`64f0250`(M5.4 dream)、`21f5158`(M5.5 archive)、`86dd270`(M5.6 集成测试)、`a9f89d5`(M5.7 auto distill)、`374cfad`(M5.8 auto dream)、`7d019f5`(project-id gitignore)。运行 `go test -race ./internal/memory/... ./internal/hooks/...` 验证。详 PRD 见 `docs/prd/v1.md`。*
