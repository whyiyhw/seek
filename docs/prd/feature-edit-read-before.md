# Edit 调用前强制 read（软性→结构性）

**目标**：杜绝模型在 `edit` 调用前跳过 `read` 凭记忆猜测 `old_string`，导致匹配失败。

**状态**：✅ 已决策（butler 卡 `c_hkqvwk`，2026-11-16）——**维持软提示，不接结构性方案**；方案 1/2/schema 层强制均否决，方案 3 发现已随 `35b0a94` 实现，无需开卡。理由见 §决策；重开条件见文末。本 PRD 转为决策记录，不再排期。

## 现状

system prompt、AGENTS.md 均有 "read before edit" 规则；`workflowReminder` 常量也追加了提醒。但这些是软性提示——模型在"以为记住内容"时仍可能跳过。

## 当前缓解

`pkg/agent/agent.go` 的 `workflowReminder`（当前值）：

> `[Workflow: grep→read(offset,limit≤50). edit→read first.]`

（早期版本曾含完整的一句 "Before calling edit, read the target lines first to capture exact whitespace — never guess indent from memory."；`3dddae3c` 起压缩为 `edit→read first`。）

每条用户消息末尾追加，recency bias 使生效概率高于 system prompt，但不能杜绝。

## 备选方案（按侵入性递增）

### 方案 1：edit 工具级校验

`internal/tools/edit` 内部维护一个最后 read 时间的记录（通过共享的 `*tools.Registry` 或单独 struct）。`edit` 执行前检查目标文件是否在最近 N 秒内被 `read` 工具读过——没读过则返回明确错误，要求模型先 read。

**优点**：改动最小，只影响一个工具包，不进入 agent 核心循环。

**缺点**：需要跨工具共享状态（`read` 工具和 `edit` 工具需要在同一个 Registry 里共享记录），但 `tools.Registry` 已有此能力。

### 方案 2：agent loop 级校验

在 `pkg/agent` 的工具调度前，检查本轮是否有同文件的 read 记录。每次 `read` 工具返回结果时记录 `(file, timestamp)`，`edit` 调度前查表。

**优点**：集中管理，不依赖工具间通信。

**缺点**：侵入 agent 核心循环，增加 `pkg/agent` 的状态复杂度。

### 方案 3：edit 错误诊断增强

> **已实现**（`35b0a94`，早于本 PRD 的评估）：`noMatchError` + `closestCandidate` 在匹配全 miss 时嵌入最近似行窗口 + needle-vs-file 的 unified diff（raw bytes，不可见差异直接可见），把一次失手钉死在一个来回内自修复。落地形态比本节原设想更强——不需要 read 快照跨工具传递，edit 自己 `os.ReadFile` 后就地取材。

不改调度逻辑，只改进 `edit` 失败时的错误信息。匹配失败时不止返回 "old_string not found"，还：

- 展示最近一次该文件 `read` 返回的内容快照
- 用 diff 对比 `old_string` 和文件实际内容

让模型能更快自修复。

**优点**：零侵入调度，改进错误恢复速度。

**缺点**：不阻止犯错，只降低犯错成本。read 快照需要跨工具传递，和方案 1 一样需要共享状态。

## 决策（2026-11-16，butler 卡 `c_hkqvwk`）

**三选一结论：维持软提示（workflowReminder + 工具 description 首句 + AGENTS.md 工作流），不接任何结构性方案；方案 3 视为已交付，不开实现卡。**

### 证据链

1. **触发条件未出现**。本 PRD 定的闸门是「edit 匹配失败频率明显上升→方案 1」。软提示全量落地（`bf56321` + `3dddae3c` 压缩）至今：eval 用例 `edit-unicode-read-first`（正是量这个行为的，`min_read_calls:1` + `max_edit_calls:3`）2/2 通过无失败；`internal/tools/edit/` 除 modernize 重构外无失败率驱动的修复。
2. **真实失败模式不是「跳过 read」**。pitfalls.md「small models burn turns retrying byte-mismatched old_string」的原始案例（v4-flash 连烧 ~10 次 edit）里模型**已经 read 过**，失手在 NFC/NFD、空白等不可见字节上——已被工具层修复（tier-2/3 fallback + diff hint）根治。跳过 read 凭记忆猜的那部分，old_string 精确匹配本身就是安全失败：match miss = 拒写，零损坏风险，浪费一个来回后模型拿着 diff hint 一步对齐。
3. **方案 1 的核心思想已被考虑并否决过一次**（`27b58c1`，2026-08-13，edit.go `WithObserver` 注释）：「edit is NOT guarded by the observer — its exact old_string match is already a stronger check than any freshness token, and requiring a prior `read` would reject a correct edit for no safety gain.」old_string 匹配成功本身就是「模型见过当前内容」的**内容级证明**，比「读过一次」的时间戳证明强。硬闸对三类正确序列是纯误伤：`write→edit`（模型刚 authored 的文件）、grep 已足够定位的单行改、`-resume`/`/compact` 后（fsobserve 内存态清零，开屏连环误弹——而补 reconstruct 就是 AGENTS.md 反对的平行状态形状）。

### 字节稳定性问题的回答（卡片点名）

schema 纪律（AGENTS.md「Tool JSON schemas are package-level []byte constants」）约束的是**不在调用时构造 schema**——保证跨轮字节不变。改常量形状（比如给 edit 加一个必须回传 read token 的参数）在纪律内合法：只在发布边界一次性 bust 前缀缓存，与任何代码变更同价。**所以字节稳定性不是结构性方案的障碍，负收益才是**：

- token 回传方案把失败模式从「old_string 不匹配（带 diff hint，一步自修复）」换成「token 缺失/不匹配（零恢复信息）」——更难自修复，且查不到记忆新鲜度（token 只证明「读过」，不证明「记得对」——后者只有精确匹配能证明）。
- CC（本 harness 即 read-before-edit 硬闸）的经验恰好反证：CC 的闸在**工具执行层的会话状态**（"You must Read the file in this conversation before editing"），不在 schema 形状——schema 层强制本就不是 CC 取的形态。且 CC 需要闸的两个理由 seek 都不成立：CC 的 Edit 依赖先前 read 拿内容渲染审批 diff（seek 的 edit 在 Execute 里自己 `os.ReadFile` + `diff.Unified`）；CC 用闸保证模型见过文件（seek 的精确匹配 + 三层 fallback 已是更强的内容证明）。

### 重开条件

若 eval `edit-unicode-read-first` / `write-elided-edit-fallback` 出现 `suspect:streak`（连续失败）且复盘根因是「未 read 直接 edit」而非不可见字节，重评 CC 式变体：复用 `fsobserve.Check`（会话内读过即可，不查新鲜度）、闸在 `Execute` 内、不动 schema、不动 agent loop——即方案 1 的降杀伤版，绕开 write→edit 误伤（write 成功时已 `Observe`）。

## 相关文件

- `pkg/agent/agent.go` — `workflowReminder` 常量（软提示，维持现状）
- `internal/tools/edit/` — edit 工具实现（含已实现的方案 3：`noMatchError`/`closestCandidate`）
- `internal/tools/read/`、`internal/tools/write/` — fsobserve 的记录/守卫两端
- `internal/fsobserve/fsobserve.go` — 跨工具共享的文件观察状态（方案 1 若重开时的载体）
- `internal/tools/tool.go` — 工具接口（原文误记为 registry.go，该文件不存在；共享状态实际由 fsobserve 承担）
