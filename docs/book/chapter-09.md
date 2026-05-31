# 第 9 章：M5.1 — 会话持久化

> 对应代码：`internal/session/`
> 起点：M4.5 的 TUI 可用，但关掉终端，整个对话就消失了。
> 终点：每次 stream 结束都落盘；下次启动可以用 `--resume <id>`、`--continue`、`--list` 拿回历史。坏掉的旧会话在加载时自动修复。

---

会话持久化听起来是简单工作——把消息列表序列化到文件，下次读回来。但只要你认真做，就会遇到一连串细节：文件格式的选择、`--list` 子命令的延迟、schema 演进、上一章那条孤儿 `tool_calls` 留下的脏数据……每一项单看都不大，加起来决定了"用起来顺不顺手"。

这一章按"从数据到 CLI"的顺序，把 M5.1 的所有决策走一遍。

---

## 9.1 我们到底要持久化什么

回到最朴素的问题：要让用户能"接着上次的对话继续"，需要保存哪些东西？

至少这些：
- 完整的消息历史（system / user / assistant / tool）
- 当前用的 model
- CWD（重要：`/yolo off` 在不同目录有不同含义）
- 用量统计（token、缓存命中），用来在新 session 启动时显示"上次累计花了 X"
- 元数据：创建时间、最后更新时间、轮数、工具调用数

不需要保存的：
- 当前 TUI 状态（视口位置、命令菜单是否打开）——这是 UI 状态，不是会话状态
- Permission policy 的当前 mode——每次启动重新决定（默认是 Ask）
- 流式渲染缓存——重启后没意义

这条边界很重要。`Session` 不应该变成"程序状态的快照"，否则迁移、修复、跨版本兼容都会变成噩梦。它只是"对话本身"。

```go
// internal/session/session.go
type Session struct {
    SchemaVersion int                `json:"schema_version"`
    ID            string             `json:"id"`
    CreatedAt     time.Time          `json:"created_at"`
    UpdatedAt     time.Time          `json:"updated_at"`
    Model         string             `json:"model"`
    Yolo          bool               `json:"yolo"`
    Plan          bool               `json:"plan,omitempty"`
    Effort        string             `json:"effort,omitempty"`
    CWD           string             `json:"cwd"`
    SystemPrompt  string             `json:"system_prompt,omitempty"`
    Messages      []deepseek.Message `json:"messages,omitempty"`
    Turns         int                `json:"turns"`
    ToolCalls     int                `json:"tool_calls"`
    Usage         deepseek.Usage     `json:"usage"`
    ParentID      string             `json:"parent_id,omitempty"`
}
```

`ParentID` 这个字段下一章 `/branch` / `/compact` 会用到，本章先存着不展开。

`Effort` 是 `/effort` 命令的持久化结果——空字符串表示"不覆盖，由模型和 Agent 自行决定"；`"high"` 或 `"max"` 表示强制开启 Thinking 并指定推理深度。每次会话的开始、恢复、切换都能读到正确的 Effort，使得上一轮手调的推理深度不会意外泄漏到新会话。默认运行时的 Effort 是 `"max"`（`cmd/seek/main.go`），但磁盘上 `omitempty` 保证空值时 JSONL header 不出现 `effort` 键。

### ID 设计：可排序就是免费索引

```go
// "20260121-103045-a1b2c3" = 时间戳 + 6 字符随机后缀
func generateID(t time.Time) string {
    var rnd [3]byte
    if _, err := rand.Read(rnd[:]); err != nil {
        // Fallback: nanosecond-precision hex suffix from the timestamp.
        // The fractional seconds make IDs unique even at high concurrency.
        return fmt.Sprintf("%s-%s",
            t.Format("20060102-150405"),
            fmt.Sprintf("%06x", t.Nanosecond()/1000))
    }
    return fmt.Sprintf("%s-%s",
        t.Format("20060102-150405"),
        hex.EncodeToString(rnd[:]))
}
```

ID 用"年月日-时分秒-随机后缀"格式有两个好处：
- 字典序 == 创建时间序，`os.ReadDir` 的输出本身就有意义的顺序
- 同一秒创建两个会话也不会撞 ID（随机后缀负责）

不用 UUID 是因为 UUID 字典序无意义，看不到"哪个旧哪个新"。

---

## 9.2 为什么是 JSONL 而不是单文件 JSON

最早的版本（schema v1）用单文件 JSON：整个 `Session` 结构 `json.MarshalIndent` 落盘。能用，跑了一段时间。然后写到第 12 章的 MCP 那会儿，会话长到 200 多轮，缓存命中 + 思考 + 工具调用混在一起，单文件 JSON 开始显得不对劲：

- **每次 Save 都要重写整个文件**——历史有多大，I/O 就有多大
- **加载延迟无法避免**——`json.Unmarshal` 必须把整个数组吃完才能解析任何字段
- **`--list` 把所有文件都完整读一遍**才能拿到 model / turns / updated_at，10 个会话就要几百毫秒
- **崩溃半截写入 = 整个文件作废**——JSON 必须括号配对才能解析

M5.1 在快做完的时候把存储格式改成 JSONL（commit `c4dd8f4`，schema 升到 v2）：

```
line 1:    session 头部（所有元数据，messages 字段省略）
line 2..N: 一行一个 deepseek.Message
```

切换到 JSONL 后：
- **append 是天然的**——未来如果做"流式落盘"，新消息追加在文件尾即可，header 不动
- **崩溃只丢最后一条**——前面行始终是完整的合法 JSON
- **`loadMeta` 只读 line 1**——`--list` 从感知得到变成感知不到（9.5 节细说）
- **`grep` / `jq` / `tail` 友好**——你想看某个会话的最后一条消息？`tail -n 1 xxx.jsonl | jq` 一行搞定

代价是 **Save 现在仍然原子重写整个文件**——理论上 append 优化是可能的，但 M5.1 优先正确性，先把 JSONL 落定，append 留作后续。这是一个有意识的"先对再快"取舍。

### 一个微妙的 omitempty 细节

JSONL 的头部行是这样写出来的：

```go
header := *sess         // 复制一份
header.Messages = nil   // 明确清空切片
enc.Encode(&header)     // 行 1：header
for i := range sess.Messages {
    enc.Encode(&sess.Messages[i])  // 行 2..N：每条消息单独一行
}
```

为什么写 `header.Messages = nil`？因为我们希望头部行**完全不出现 `messages` 字段**——它们是接下来的 N 行，不应该重复出现在第 1 行的 JSON 对象里。这依赖于 `json` 包对 `omitempty` 的一个微妙规定：

> **坑 #12：`omitempty` 在 slice 字段上"既 omit nil 也 omit 空 slice"**
>
> 这两种状态都会被省略：`s.Messages = nil` 和 `s.Messages = []Message{}`。
>
> 不直观的地方：如果你某天想区分"这个 session 真没有 message"和"这个字段还没填"，`omitempty` + slice 会让你无法在 wire 格式上区分。需要区分时改用 `*[]T`：nil 指针是"没填"，指向空切片的指针是"明确空"。
>
> 在我们这里这正是想要的——nil 和空切片都不该出现在 header 行——所以 `omitempty` + 显式置 nil 就够了。但写代码时心里要记得这条 ambiguity，下次需要"在 wire 上区分 nil 和空"的场景到的时候不至于踩坑。

### `json.Encoder.Encode()` = JSONL 原语

```go
enc := json.NewEncoder(f)
enc.Encode(&header)
for i := range sess.Messages {
    enc.Encode(&sess.Messages[i])
}
```

注意我们没有手写任何 `\n`。`json.Encoder.Encode` 的文档说得很明确："writes the JSON encoding of v followed by a **newline character**"——它本身就是 JSONL 的原语。手动 `json.Marshal(v) + "\n"` 是常见但不必要的写法，用 `Encoder` 一次到位。

读端对称：

```go
dec := json.NewDecoder(r)
var sess Session
dec.Decode(&sess)            // 读 line 1（header）
for dec.More() {
    var msg deepseek.Message
    dec.Decode(&msg)         // 读后续每一行
    sess.Messages = append(sess.Messages, msg)
}
```

`dec.More()` 检查是不是还有下一个 JSON 值要读——它对"换行后跟着另一个对象"这种情况天然支持，不需要手工 `bufio.Scanner` 按行切。

---

## 9.3 Save：原子写 + 一次性重写

```go
func (s *Store) Save(sess *Session) error {
    sess.Touch()

    final := filepath.Join(s.dir, sess.ID+".jsonl")
    tmp := final + ".tmp"

    f, _ := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
    enc := json.NewEncoder(f)

    // 行 1：header（Messages 置 nil → omitempty 省略）
    header := *sess
    header.Messages = nil
    enc.Encode(&header)

    // 行 2..N
    for i := range sess.Messages {
        enc.Encode(&sess.Messages[i])
    }

    f.Close()
    os.Rename(tmp, final)  // 原子替换
    return nil
}
```

三件值得展开的事情：

**`0o600` 权限**：会话文件存的是系统提示词、CWD、对话内容——对其他用户不可读。`umask` 在多用户机器上不一定够，显式设权限更稳。

**`tmp + rename` 原子化**：直接写目标文件，如果进程在中途崩溃，文件就处于半截写入状态，下次读会失败。先写临时文件，写完再 `os.Rename` 替换——`rename` 在同一文件系统上是原子的，要么完整看到新内容，要么完整看到旧内容，没有中间态。

**没有 fsync**：理论上 `f.Close()` 不保证数据已落到磁盘，断电可能丢。seek 是单用户本地工具，断电恢复不是设计目标——`fsync` 的代价（每次 100ms 量级）换不回相应价值。如果将来要做 CI 自动化、共享会话之类的高可靠场景，这是要重新考虑的点。

### 触发时机：每次 stream 结束都落盘

```go
// internal/tui/update.go — streamEnd 路径
case streamEndMsg:
    m.persistSession()  // ← 这里
    // ...

func (m *Model) persistSession() {
    if m.opts.Session == nil || m.opts.Store == nil || m.opts.Agent == nil {
        return  // 临时 / --no-save 模式：无 Store 时直接跳过
    }
    m.opts.Session.Messages = m.opts.Agent.Messages()
    m.opts.Session.Turns = m.turns
    m.opts.Session.ToolCalls = m.toolCalls
    m.opts.Session.Usage = m.opts.Tracker.Cumulative()
    m.opts.Store.Save(m.opts.Session)
}
```

设计选择是"每次 stream 结束都 Save"，不是"每 N 轮"或"退出时 Save"。理由：

- 崩溃损失永远 ≤ **当前正在进行的那次 stream**。已经完成的对话不会丢
- 用户体验上"我刚问完一个问题，关掉 terminal" = 已经持久化
- 一次 Save 的代价：现在大约 10-50 KB JSONL（典型会话），原子写一遍 < 5 ms。在 stream 结束（用户开始读结果）这个时机做，对感知零影响

这里的隐含条件是 stream 结束**之间**用户不太可能做什么"我要这次结果但不要保存"的操作。如果有需求，那是 `--no-save` 的职责。

---

## 9.4 Load 与透明的 schema 迁移

```go
func (s *Store) Load(id string) (*Session, error) {
    // 先试新格式 .jsonl
    path := filepath.Join(s.dir, id+".jsonl")
    f, err := os.Open(path)
    if err == nil {
        defer f.Close()
        return decodeJSONL(f, id)
    }
    if !os.IsNotExist(err) {
        return nil, fmt.Errorf("session: open %s.jsonl: %w", id, err)
    }

    // .jsonl 不存在 → 回落到老格式 .json
    legacyPath := filepath.Join(s.dir, id+".json")
    data, _ := os.ReadFile(legacyPath)
    var out Session
    json.Unmarshal(data, &out)
    return &out, nil
}
```

逻辑很直白：先尝试新格式，找不到再回落到老格式。重要的是 **fallback 不显式**——用户不需要知道自己有"老格式文件"和"新格式文件"两种东西。

老格式被读回内存后，下一次 `Save` 自动用新格式写出来（因为 `Store.Save` 只生成 `.jsonl`）。**老的 `.json` 文件不会被自动删除**——这是有意的：万一新格式有 bug，老文件还在，可以人工恢复。等用户运行一段时间确认没问题，再手动清理就行。

这种"读时兼容，写时升级，旧文件保留"的模式适用于任何低风险的格式演进。代价只是磁盘上短期内有两份文件，换来的是迁移过程完全无感。

### `SchemaVersion` 这个字段当前怎么用

老实说：**当前还没真正用**。`CurrentSchemaVersion = 2` 写在每个新建的 `Session` 上，但加载时我们没有读它来做版本判断——靠的是文件扩展名（`.jsonl` 还是 `.json`）来区分新旧。

那为什么还要有这个字段？因为下一次格式演进时——比如要把 message 的 `tool_calls` 内部结构换一下，或者引入新字段需要默认值——文件扩展名同样是 `.jsonl`，没法靠扩展名区分。那时候 `SchemaVersion` 就上场，读到旧版本号就走旧字段映射，写出来时升级到当前版本。

这是一个"现在不用，但写入字段一文不值，未来用起来比改格式快得多"的典型设计。早一点决定字段位置，比迁移到时再补容易。

---

## 9.5 loadMeta：只读 line 1 让 `--list` 不再卡顿

`--list` 子命令要列出所有会话的：`ID / 创建时间 / 更新时间 / 用的模型 / 轮数 / 工具调用数`。

朴素实现：

```go
for _, id := range allIDs {
    sess, _ := store.Load(id)  // ← 这里是问题
    printRow(sess)
}
```

`Load` 把整个文件读进内存——header + N 条消息全部反序列化。一个 500 轮的会话可能有 1 MB 大小，几百个 `deepseek.Message` 对象。一次 `--list`、20 个会话、平均每个 500 KB ≈ 10 MB I/O + 上千个反序列化对象——感知得到的延迟。

`loadMeta` 是这个问题的 O(1) 解：

```go
func (s *Store) loadMeta(id string) (SessionInfo, error) {
    // 新格式：只 Decode 第一个 JSON 对象就停，message 完全不读
    path := filepath.Join(s.dir, id+".jsonl")
    f, err := os.Open(path)
    if err == nil {
        defer f.Close()
        var sess Session
        json.NewDecoder(f).Decode(&sess)  // 读 line 1 → 返回
        return SessionInfo{
            ID: sess.ID, UpdatedAt: sess.UpdatedAt,
            Model: sess.Model, Turns: sess.Turns,
            // ...
        }, nil
    }
    // ...
}
```

`json.NewDecoder(f).Decode(&sess)` 只消费第一个 JSON 对象——header 那一行——遇到行尾就停。后面的几百条消息**完全不读**。一次 `--list` 的代价从 O(总消息数) 降到 O(会话数)。

实测：500 轮会话 × 20 个，`--list` 从约 1.2 秒降到大概 40 毫秒。

### 老 `.json` 文件的 incremental skip

老格式整个就是一个 JSON 对象 `{...messages: [...], ...}`。`messages` 数组里有几千个对象，但元数据字段都在外层。怎么"读外层不读 messages"？

答案是 `json.Decoder` 的 token 级 API：

```go
func (s *Store) loadMetaLegacyJSON(id string) (SessionInfo, error) {
    f, _ := os.Open(filepath.Join(s.dir, id+".json"))
    defer f.Close()
    dec := json.NewDecoder(f)
    dec.Token()  // 读开头的 '{'

    var info SessionInfo
    for dec.More() {
        key, _ := dec.Token()  // 拿键名
        switch key.(string) {
        case "id":         dec.Decode(&info.ID)
        case "updated_at": dec.Decode(&info.UpdatedAt)
        case "model":      dec.Decode(&info.Model)
        case "turns":      dec.Decode(&info.Turns)
        // ...
        default:
            skipJSONValue(dec)  // 包括 messages 数组在内的所有其他字段
        }
    }
    return info, nil
}
```

`skipJSONValue` 是一个递归函数：读一个 token，如果是 `{` 或 `[` 就一路读到对应的 `}` 或 `]`，整个过程**只移动 decoder 的位置，不分配 Go 对象**。messages 数组里的 token 流过 decoder 但没有任何东西被构造。

这个 API 不常见但确实是 Go 标准库的一部分，专门为这种"流式跳过部分结构"的场景准备。

### `Latest()` 配合 loadMeta 的一个排序 bug

`--continue` 找的是"最近更新的会话"：

```go
func (s *Store) Latest() (*Session, error) {
    entries, _ := os.ReadDir(s.dir)
    var bestID string
    var bestAt time.Time
    for _, id := range collectIDs(entries) {
        meta, _ := s.loadMeta(id)
        if meta.UpdatedAt.After(bestAt) {
            bestAt = meta.UpdatedAt
            bestID = id
        }
    }
    if bestID == "" { return nil, nil }
    return s.Load(bestID)
}
```

早期版本（M5.1 落地之前）有个 bug：`Latest()` 直接按 `os.ReadDir` 的返回顺序找最后一个文件，假设字典序 == 创建时间序，所以"最后一个"就是"最新的"。

错在哪？`UpdatedAt` 跟 `CreatedAt` 不是一回事。如果你用 `--resume` 接上一个三天前的旧会话继续问问题，它的 `UpdatedAt` 会被更新，但 ID（来自 `CreatedAt`）还是三天前的——字典序还在中间，按字典序最后一个找到的会是另一个会话。

修复（commit `62ce49b`）很简单：显式按 `UpdatedAt` 比，扫一遍所有 meta。代价是 O(N) 而不是 O(1)，但 N 通常 < 100，loadMeta 又只读 header，总开销可忽略。

要点：**ID 排序和"最近活动"是两个维度**。如果你在做类似设计，明确想清楚 "我要列出的最后一个" 和 "我要 resume 的最后一个" 是不是同一个东西——通常不是。

---

## 9.6 Repair：把孤儿 tool_calls 在加载时清掉

第 4 章修过孤儿 `tool_calls` 的 bug。但 fix 只覆盖**修复之后**的代码——M5.1 之前已经被破坏的会话文件还在用户的硬盘上。打开 seek、`--resume xxx`、再发一句话——还是会被 API 拒绝。

`Repair` 是加载时的兜底：

```go
func repairMessages(msgs []deepseek.Message) (_ []deepseek.Message, dropped int) {
    for i := len(msgs) - 1; i >= 0; i-- {
        m := msgs[i]
        if m.Role != deepseek.RoleAssistant || len(m.ToolCalls) == 0 {
            continue
        }
        // 找到 i 之后是否所有 tool_call 都有配套的 tool 结果
        needed := make(map[string]bool, len(m.ToolCalls))
        for _, tc := range m.ToolCalls {
            needed[tc.ID] = true
        }
        for j := i + 1; j < len(msgs); j++ {
            if msgs[j].Role == deepseek.RoleTool {
                delete(needed, msgs[j].ToolCallID)
            }
        }
        if len(needed) == 0 {
            return msgs, 0           // 配对完整，整条历史保留
        }
        return msgs[:i], len(msgs) - i  // 有缺失，把 i 及之后全部砍掉
    }
    return msgs, 0
}
```

算法：**从尾向前找最后一个带 `tool_calls` 的 assistant 消息**，检查 `i+1..end` 之间是否每个 `tool_call_id` 都有对应的 tool 结果消息。如果完整，整段历史是合法的；如果有缺失（哪怕只缺一个），从 `i` 开始（含）整体砍掉。

为什么从尾向前？因为对话历史是"一段一段叠加的"——前面的工具调用如果有 bug，早就在那时候被 API 拒绝了。能稳定保存下来到磁盘的、且现在仍然在抱怨的，**只可能是最尾巴那次**。所以只检查最后一次就够。

为什么"有缺失就砍 `i` 和之后所有"？因为 `[i, end)` 是一个不完整的轮——assistant 说"我要调 A B C"，结果只有 A 的 tool 消息——这一整轮是无效的。砍掉之后用户看到的是"上次最后一句完整的 assistant 消息"，可以从这里接着问。比反复砍单条更可预测。

加载时怎么调？在 `cmd/seek/main.go`：

```go
if loaded != nil {
    dropped := loaded.Repair()
    if dropped > 0 {
        fmt.Fprintf(os.Stderr,
            "session: repaired %d orphan message(s)\n", dropped)
    }
}
```

打印一行给 stderr 让用户知道发生了什么。多数情况下 `dropped == 0`，悄无声息地过去。

这种"加载时修复"的模式适合任何"代码 bug 可能产生过坏数据，bug 已修但坏数据仍在"的场景——比 SQL migration 轻量很多，因为不是结构变化，而是内容清理。

---

## 9.7 `--resume` / `--continue` / `--list` / `--no-save`

四个标志，各自的设计权衡：

```go
resume = flag.String("resume", "", "load a saved session by ID (see seek -list)")
cont   = flag.Bool("continue", false, "load the most-recently-updated session")
list   = flag.Bool("list", false, "list saved sessions and exit")
noSave = flag.Bool("no-save", false, "do not persist this session to disk")
```

**`--resume <id>`**：显式选定。`id` 必须完整匹配（短 ID 前缀匹配会引入歧义，而且 ID 已经包含时间戳 + 随机后缀，错拿到别人的会话不应该是"几乎一定"）。找不到就 error；不试图猜。

**`--continue`**：调 `Store.Latest()`，按 `UpdatedAt` 找最新一个。这是日常最常用的——大多数时候你只想接着上次干。

**`--list`**：打印元数据列表后 `os.Exit(0)`，不进 TUI。用 `loadMeta` 而非 `Load` 是这个标志能用的前提（9.5 节）。

**`--no-save`**：完全不挂 Store——`opts.Session = nil` 透传给 TUI，`persistSession` 看到 nil 立刻返回。文件系统上不留任何痕迹。

设计上这四个不是平权的。`--resume` 和 `--continue` 互斥（同时指定时报错），都不能和 `--no-save` 并用（resume 一个会话却又"不保存"是自相矛盾）。

### `--no-save` 的隐含约束

`--no-save` 不仅仅是"不写文件"——它还隐含一个限制：`/branch` 和 `/compact` 这两个 slash 命令在 `--no-save` 下**不能用**，因为它们要"先把当前会话写到磁盘做 snapshot，再 fork 出子会话"——没有 Store，整个流程崩溃。

这个崩溃在 M5.1 落地后被发现：`/compact` 在 `--no-save` 路径 nil-pointer panic（commit `c3082ec`）。修复是在 `handleCompactDone` 加 guard：

```go
if m.opts.Session != nil && m.opts.Store != nil {
    // ...fork + persist...
}
```

更普适的教训：当一个结构体字段在设计上**可以是 nil**（不是 bug 是 feature），每个方法都需要明确检查。把字段叫 `opts.Session`，命名上已经在暗示"optional"——但代码层面要把这个"optional"对应到每处使用，不能只在初始化路径上检查。

---

## 9.8 一个测试观察：`loadMeta` 不能用 `Load`

写到这里你可能注意到一个反向风险：如果未来有人觉得 `loadMeta` 代码重复，把它"重构"成 `func loadMeta(id) { sess := Load(id); return SessionInfo{...sess.fields} }`——`--list` 立刻退化到 O(总消息数)，所有 latency 优势消失。代码看起来更整洁，性能慢了 30 倍，但回归测试不会捕获，因为功能上**完全正确**。

session 包的测试目前覆盖了 Repair 的所有形状、JSONL 序列化、legacy 兼容、Fork 深拷贝——但**没有 "loadMeta 不能读取消息" 的性能/语义测试**。这是一个未补的功课。理想测试形如：

```go
func TestLoadMeta_DoesNotReadMessages(t *testing.T) {
    // 写一个 session，message 区故意放损坏的 JSON
    // 期望 loadMeta 仍能成功返回 header
    // 期望 Load 失败
}
```

把"性能优化路径"翻译成"语义不变量"是测试 mature 的一个标志——可惜不是免费的，得有意识地补。

---

## 本章小结

- `Session` 保存"对话本身"，不保存 UI 状态/policy mode 这些程序态——这条边界让格式演进可控
- ID 用 "时间戳 + 随机后缀"，字典序 == 创建序，免费索引
- 单文件 JSON → JSONL 的切换换来 append 友好、partial-crash 安全、O(1) 元数据读取，代价是 Save 仍然原子重写（可接受）
- `json.Encoder.Encode` 自带换行 = JSONL 原语，没必要手写 `\n`
- `omitempty` 在 slice 上同时省略 nil 和空切片——本章里这正是我们想要的，未来场景里要心里记一笔
- `loadMeta` 只读 line 1 让 `--list` 从感知得到变成感知不到；老 `.json` 文件用 token-level 跳过 `messages` 数组实现同等优化
- `Latest()` 必须按 `UpdatedAt` 找最近活动，不能按字典序的"最后一个"（ID 是创建序，不是活动序）
- `Repair` 在加载时砍掉孤儿 `tool_calls` 末尾轮，把第 4 章那个 bug 在历史文件上也修干净
- `--no-save` 隐含 `/branch` `/compact` 不可用——nil 可空字段需要每处使用都 guard

下一章进入 `/branch` 和 `/compact`——把单线的对话历史变成一棵会话图。我们会看到 `Fork` 的深拷贝实现、`/compact` 用 fork 而非直接覆盖原会话来保留完整历史的设计，以及为什么 `ParentID` 这一个字段足以承载整棵图。

---

*对应 commit：`f13ec3f`（持久化初版）、`c4dd8f4`（JSONL 重写）、`ba90a48`（schema 版本化 + Fork 深拷贝）、`62ce49b`（Latest 排序修复 + loadMeta for List）、`986a485`（Repair 加载时调用）、`c3082ec`（`--no-save` nil guard）。运行 `go test -race ./internal/session/...` 验证。*
