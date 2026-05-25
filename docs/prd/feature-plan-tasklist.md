# Plan Task List —— TUI 上的结构化任务追踪

> ⚠️ **已废弃 / SUPERSEDED**：scope 误判（只覆盖了 plan 模式工作流的"执行追踪可视化"这一个窄面，忽略了 ANALYZE → PROPOSE → CONFIRM 的核心门）。继任者：[`feature-plan-mode.md`](feature-plan-mode.md)，把 plan 模式作为完整闭环重新设计，task list 可视化推到 v2。
>
> 本文保留作设计推演审计——为什么 .md checkpoint 文件方案被否决（§1.2）、为什么"派生自 transcript"的事件驱动 cache 是正确架构（§2.3）—— 这些推演 v2 加面板时仍然适用。

**目标**：让 plan-exe-reflect 工作流的"我执行到哪一步了"在 TUI 上**可见且权威**。把当前依赖模型口头叙述（"step 1 done, moving to step 2"）的隐式状态，升级为由专门工具维护、TUI 专区渲染、随 session JSONL 天然持久化的一等结构。

**状态**：❌ 已废弃，scope 不对。见上方 banner。

## 一、问题陈述

### 1.1 触发场景

`internal/skill/builtin/dual-model.md` 的 plan→exe→reflect 三步流里，Step 2（执行）目前**没有任何机制**让"执行到第几步"成为可观察状态：

- think 返回的 plan 是 assistant message 里的一段纯文本
- 模型自己口头报"step N done"，混在常规对话里
- 用户在 TUI 上看到的是滚动的 markdown，没有"当前任务"的固定视图
- 跨会话恢复（Ctrl-C → `seek resume`）依赖模型重读 transcript 去推断"上次到哪了"

### 1.2 一个被否决的备选：写 `.md` checkpoint 文件

2026-05-25 曾在 dual-model.md 加过 Step 1.5："写 plan 到 `<project>/.seek/plans/YYYY-MM-DD-<topic>-plan.md`，markdown checkbox 格式，执行中翻 `- [ ]` → `- [x]`"。审查后 revert，理由：

1. **双事实源**：transcript（`~/.seek/sessions/`）已有完整执行记录，再写一份 markdown 等于把同一份信息抄两遍，且容易不同步（skill 自己第 64 行就承认要 think reconcile）。
2. **Git 污染**：`.seek/skills/` 是 git 跟踪的，`.seek/plans/` 默认会进 `git status`。改成 `~/.seek/plans/` 可以绕过，但解决错了层——
3. **解决错了层**：跨会话恢复本质是 session UX 问题（`seek resume <id>`），不应该塞在 skill 里靠模型记得读 `.seek/plans/`。
4. **写权限摩擦**：每个 multi-session 任务开头多一次 `write` 权限确认，执行中多一次 `edit`。
5. **Stale 累积**：没有清理机制，`.md` 坟场无人收拾。

记录在用户 memory（`feedback_plan_mode_tui_tasklist`，仓库外不可见）里：**禁止以后再提议用 markdown 文件持久化 plan**。

### 1.3 为什么 TUI task list 是正解

| 维度 | `.md` 文件方案 | TUI task list 方案 |
|------|---------------|--------------------|
| 事实源 | 文件 + transcript 双份 | 工具调用本身就在 transcript，唯一 |
| 持久化 | 单写 + git 污染 | 随 session JSONL 自动 |
| 权限摩擦 | 每次 `write` 弹 y/N | 工具调用无 fs 写入，免确认 |
| 清理 | stale `.md` 文件 | 随 session 老化，零人工 |
| 跨会话恢复 | 模型要记得翻 `.seek/plans/` | `seek resume <id>` 天然带 |
| 可视化 | 混在 chat 流的 markdown 里 | TUI 固定区，已完成 strike-through / 当前加亮 |

## 二、设计

### 2.1 新工具 `internal/tools/plan/`

沿用现有"单工具 + 字符串字段分派"模式（参见 `internal/tools/git/git.go` 的 `subcommand` 字段）。

**Schema 草案：**

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["set", "start", "complete", "skip", "show", "clear"],
      "description": "set: 用 steps 数组初始化或重置任务列表；start: 将指定 index 标记为 in_progress；complete: 将指定 index 标记为 done；skip: 标记为 skipped 并附 note；show: 只读返回当前 plan 状态（不改变任何字段，用于模型已忘了 plan 长什么样时查询）；clear: 清空当前任务列表（仅在整个 plan 完成或被推翻时使用）。"
    },
    "steps": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Only valid with action=set. One concrete step per element. Don't include sub-bullets; if a step fans out, call set again with the refined list."
    },
    "index": {
      "type": "integer",
      "description": "0-indexed step to operate on. Required for start/complete/skip."
    },
    "note": {
      "type": "string",
      "description": "Optional short annotation. For skip: why it was skipped. For complete: a one-line outcome (rarely needed; transcript already has the diff)."
    }
  },
  "required": ["action"],
  "additionalProperties": false
}
```

**返回值**：永远返回当前完整任务列表的紧凑表示——给模型作为"权威 ground truth"，避免它在后续轮次里基于自己上一条叙述误判。例：

```
plan (3 of 6):
  [x] 0  Inventory current middleware call sites
  [x] 1  Define TokenStore interface
  [>] 2  Migrate session-backed store        ← in_progress
  [ ] 3  Wire new middleware in cmd/server/main.go
  [ ] 4  Update integration tests
  [ ] 5  Self-review with think(reflect=true)
```

**权限**：`plan` 不操作 fs、不跑 shell，与 `think` 一样**无需注入 `permission.Policy`**。CLAUDE.md 里"如果工具能 mutate filesystem or shell，inject `*permission.Policy`"的反向也成立。

### 2.2 状态数据模型

```go
// internal/tools/plan/plan.go (草案)

type Status string
const (
    StatusPending    Status = "pending"
    StatusInProgress Status = "in_progress"
    StatusDone       Status = "done"
    StatusSkipped    Status = "skipped"
)

type Step struct {
    Text   string `json:"text"`
    Status Status `json:"status"`
    Note   string `json:"note,omitempty"`
}

type Plan struct {
    Steps     []Step `json:"steps"`
    UpdatedAt int64  `json:"updated_at"`
}
```

**关键约束**：同时只允许 **一个** step 处于 `in_progress`。`start(index=N)` 会自动把当前的 in_progress（如有）退回 pending——避免"多线程错觉"。模型如果真要并行做事，那它在错误地用这个工具，不是工具该支持的语义。

### 2.3 状态持有：事件驱动的内存缓存

**Plan state 是 TUI 的派生状态，不进 `agent.Agent` 结构体。** 规范来源（canonical source）是 session JSONL 里的 `plan` 工具调用历史；运行时缓存活在 `tui.Model` 里：

- `tui.Model` 加一个 `currentPlan *plan.Plan` 字段
- `update.go` 处理 `agentEventMsg{Event: agent.ToolExecEnd}` 时，若 `Name == "plan"` 则解析 `Result` 字段（已是 JSON），覆盖 `currentPlan`
- `View()` 渲染时读这个字段，纯内存访问，零 IO、零扫描

**"派生" 的意义**——不是"每帧从 transcript 扫一遍"，而是：任何消费方（TUI、未来的 web UI、CLI 导出工具）都可以通过重放 transcript 中的工具调用历史**重建** plan state。这条性质保证：

- 单一规范来源是 transcript，不存在"内存丢了就没了"
- `seek resume <id>` 加载 session 后，TUI 在事件回放过程中自然把 `currentPlan` 重建出来——不需要任何额外的恢复逻辑
- 事件流是单向的（agent → TUI），"内存缓存和 transcript 不一致"在正常路径上不可能发生；崩溃恢复走回放路径，等价于"用 transcript 重建一遍"

不需要担心性能：每次 `ToolExecEnd{Name: "plan"}` 仅触发一次 JSON 解析（<1KB），不涉及文件 IO 或全 transcript 扫描。

### 2.4 TUI 渲染

**位置**：在 `internal/tui/view.go` 的 `View()` 主体里，**所有 live tool 行（streaming label / activeTools / queue hint）之上、scrollback 之下**新增一块"plan panel"。理由：plan 是"整体进度"，比单次工具执行更稳定，应该贴近已 commit 的 scrollback；input 紧上方留给"最 ephemeral"的输入辅助。仅当当前 session 存在非空 plan 时显示。

**样式**（沿用 `styles.go` 中已有的 lipgloss 调色）：

| 状态 | 标记 | 文本样式 |
|------|------|---------|
| done | `[x]` | 灰色 + strike-through |
| in_progress | `[>]` | 加粗 + 主色高亮 |
| pending | `[ ]` | 默认 |
| skipped | `[-]` | 灰色 + italic |

**事件流**：在 `update.go` 处理 `agentEventMsg` 时识别 `ToolExecEnd{Name: "plan"}`，直接更新 `tui.Model.currentPlan`，下一次 `View()` 自动重新渲染。**不**新增 agent.Event 类型；TUI 内部可以选择性地走一个 `planUpdatedMsg` 触发 view diff，但这是 TUI 实现细节，不是 agent → TUI 协议的一部分。

**截断策略**：plan panel 最多占 8 行（含标题）。超出时显示前 N 项 + "... (M more)"。窗口高度极小时（< 20 行）整体折叠为单行摘要（"plan: 3/6"）。

### 2.5 跨会话恢复与中断语义

**正常 resume**（零额外工作）：

- `seek resume <id>` 加载 session JSONL 并回放事件后，`tui.Model.currentPlan` 自然重建，View 第一帧就正确
- 模型在新会话起始看到的 transcript 里就有最近的 `plan` 工具结果，可以照着接着干
- 用户在**新** session 接老工作：当前 seek 已经支持 `seek resume`，不需要为此特性额外加路径

**Ctrl-C / 进程崩溃 / 网络断开导致中断**：

- 若中断时某 step 处于 `in_progress`，resume 后该 step 状态保持 `[>]`
- 约定（写进 `plan` 工具的 description，让模型读到就内化）：**resume 时遇到 `in_progress` step，视为"尚未完成、需要重做"**，模型应重新执行该步骤（必要时先调一次 `plan(action=start, index=N)` 刷新 timestamp）
- 之所以不在 resume 路径里自动把 `in_progress` 回退到 pending：那需要在 TUI 启动路径加专门的恢复逻辑，违背"派生状态、零额外恢复"的设计原则。把语义后挪给模型，省一层 wiring

### 2.6 dual-model skill 改动

Step 1 末尾原本是"reasoner 返回 plan"。这里有个非平凡的契约问题：`think` 返回的是含 reasoning + 最终 "Answer:" 块的**自由文本**（参见 `internal/tools/think/think.go:1-25`），不是 JSON list——从这堆文本抽出 5–8 个 "concrete step" 字符串塞进 `plan(action=set, steps=[...])` 是模型必须完成的翻译任务，质量直接决定 task list 的可用性。

**选定策略**：skill 里给一个明确范例 pattern 锚定粒度。备选与放弃理由：

1. **Trust the model**（最简单，但过去经验显示模型会产出 20+ 子步骤的细碎清单）——放弃。
2. **Skill 内 example block**（中等成本，效果可控）—— ✅ 采用。
3. **给 `think` 加 `structured=true` 模式让它返回 JSON list**——侵入大，且影响所有 think 调用者；非 plan 流程无收益。**放弃**。

新增到 skill Step 1 末尾的段落（示意，落地以实际 PR 为准）：

> Translate the reasoner's "Answer:" block into 5–8 concrete, verifiable steps and call `plan(action=set, steps=[...])` **before any other tool call**. Each step should be one action a human could verify is done. Resist the urge to nest sub-steps — if a step is too big, that's a planning-level problem, re-plan instead of subdividing the list.
>
> Example: if Answer says "Refactor auth middleware to a per-request token store, including interface design, migration, and tests", call:
>
> ```
> plan(action=set, steps=[
>   "Inventory current middleware call sites (grep RequireAuth)",
>   "Define TokenStore interface in internal/auth/store.go",
>   "Migrate session-backed store, keeping API compatible",
>   "Wire new middleware in cmd/server/main.go",
>   "Update integration tests",
>   "Self-review with think(reflect=true)"
> ])
> ```

Step 2 改为："开始一步前 `plan(action=start, index=N)`，完成后 `plan(action=complete, index=N)`。如果忘了 plan 长什么样，调 `plan(action=show)` 而不是滚回去读 transcript。"

Step 3 保留不变（reflect 阶段不动 plan，因为 reflect 本身可能产生新的修复步骤——但那应该作为新一轮 plan 或单独 ad-hoc 步骤）。

预计 skill 文件改动 ≤ 50 行（example block 略占空间）。

## 三、关键决策与权衡

### 3.1 为什么不复用 Claude Code 的 `TaskCreate` 工具名

`TaskCreate` 在 Claude Code 是一个 harness 级机制。seek 内的同类工具叫 `plan` 更贴 dual-model skill 的语义（plan→exe→reflect 里的 plan 名词具象化），也避免和未来可能的"agentic subtask 派发"混淆。

### 3.2 为什么 plan 严格单线程（无并行 + 无嵌套）

两条约束合在一起：

- **单 plan 内**：同时只允许一个 step 处于 `in_progress`，`start(index=N)` 自动把当前 in_progress（如有）退回 pending
- **单 session 内**：只追一条 plan，不允许 plan 栈或并行 plan

理由共通：Plan 在这里是单线程执行模型的可视化，不是 DAG，更不是 mini-Airflow。如果模型在 dual-model 流里临时想 plan 一个子任务，应该用 `think`，不是嵌套 plan；如果用户想跑真正并行的任务，应该开多个 session。把语义留窄，TUI 渲染与 resume 行为都简单，schema 也不需要 `plan_id` / `parent` 这种字段。

### 3.3 为什么 `complete` 不需要附带证据/diff

Transcript 里已经记了完成那一步用了哪些工具调用、改了哪些文件。让 `complete` 强制要求 note 等于让模型重复叙述一遍，纯噪音。

### 3.4 为什么不做"plan 编辑器"（让用户在 TUI 上手动勾选）

YAGNI。Plan 是模型对自己的执行追踪，用户的反馈通道是 chat 本身（"这步跳过吧"→ 模型调 `plan(action=skip)`）。给 TUI 加交互态会让 plan panel 从"显示"变成"输入面板"，引入光标、焦点、键位冲突。等真有人提需求再说。

## 四、非目标

- **跨 session 自动 resume 提示**（"上个 session 还有 plan 没完成，要继续吗？"）——`seek resume` 已经够用，加这层是过度智能。
- **Plan 时间追踪 / SLA / 预估**——不是 seek 想做的产品方向。
- **Plan 导出为 markdown / 分享**——transcript 本身就能导出，需要再说。
- **Plan 模板 / 复用**——skill 系统已经是模板复用的机制。
- **多 plan 并行 / plan 嵌套**——见 §3.2。

## 五、阶段交付

P2 取消（原"agent 端 emit `PlanUpdated` event"）——TUI 直接 type-switch `ToolExecEnd{Name:"plan"}` 即可，新加 agent.Event 类型纯属冗余 abstraction。

| 阶段 | 内容 | 大致工作量 |
|------|------|----------|
| P1 | `internal/tools/plan/` 工具实现 + 完整单测（见下方测试覆盖清单） | ~200 行 + 测试 ~200 行 |
| P2 | TUI: `tui.Model.currentPlan` 字段 + `agentEventMsg` 处理 + plan panel 渲染 + styles + 截断策略 + view test | ~150 行 + view test ~100 行 |
| P3 | `dual-model.md` skill 改写 Step 1/2 接入新工具（含 think→plan 翻译范例） | ~50 行 |
| P4 | 端到端验证：plan→exe→reflect 全跑一遍，Ctrl-C 中断后 resume 校验 plan 完整恢复 | 半天 |

总计约 **1.5–2 天工作量**。先 P1 + 完整测试合并，再 P2 合并，最后 P3 切换 skill——三个 PR，每个独立可验证。

**P1 测试覆盖必须包含**（按 CLAUDE.md "测试失败路径，不只是 happy path"）：

- happy path：set / start / complete / skip / show / clear 各跑通
- 状态机非法转换：start 一个已 done 的步骤、complete 一个 pending 的步骤、clear 中途、skip 不存在 index、start 时已有 in_progress（验证自动退回 pending）
- malformed 输入：action 不在 enum / steps 为 null 或空数组 / index 越界 / index 为负 / set 缺 steps 字段 / start 缺 index
- 序列化往返：tool result 字符串 → 模型在下一轮全量 `set` 覆盖 → 状态正确（重要：这条覆盖了"派生自 transcript"的核心承诺）
- in_progress 中断恢复语义（§2.5）：构造一个含 `[>]` 中间态的 session，验证 `show` 返回的状态匹配持久化时的快照（模型按 description 重做的行为由 skill 层保证，不在工具单测覆盖范围）

**P2 视图测试必须包含**：
- `seek resume` 加载含 plan 工具事件的 session 后，第一次 `View()` 渲染结果等价于"一直在线"的渲染——这是 §2.5 的核心承诺
- 截断策略边界：恰好 8 行、9 行、20+ 行；窗口高度 < 20 时折叠为单行

## 六、风险

- **工具被误用为 chat memo**：模型可能往 `plan(action=set, steps=[...])` 里塞超细粒度（20+ 步）的子步骤当 todo list。mitigation：schema description 明确"concrete step"、"don't include sub-bullets"，并在 dual-model.md 的 example block（§2.6）锚定 5–8 步粒度。
- **think → plan 翻译质量**：模型从 think 的自由文本中抽 steps 时粒度可能过粗（3 步覆盖全部）或过细。mitigation：skill example 锚定粒度；若 P4 验证发现仍偏离，可在 `plan` 工具 description 里加上 "若 step ≥ 10，先 re-think" 的提示。
- **plan 与 think reflect 的语义重叠**：reflect 可能发现"原 plan 漏了一步"。约定：reflect 不直接动 plan；需要补救时模型显式 `plan(action=set, ...)` 重置（或后续若加 `append` 动作再说）。
- **`in_progress` 步骤的中断语义靠模型自觉**（§2.5）：约定靠 tool description 传达给模型，但模型可能跳过、直接当 done 处理。mitigation：description 里用强语气写明；若 P4 验证发现误判率高，再考虑 resume 路径里做硬回退（违背"零额外恢复"原则，所以是后备方案）。

## 七、相关文件

- `internal/tools/plan/` — 新工具（待建）
- `internal/tools/git/git.go` — 现有 subcommand dispatch 模式参考
- `internal/tools/think/think.go` — 现有"无 permission gating"的工具范例（plan 沿用）
- `pkg/agent/events.go` — `ToolExecEnd` 已经够用，**不**新增 `PlanUpdated` event
- `internal/tui/model.go` — 新增 `currentPlan *plan.Plan` 字段
- `internal/tui/view.go` — 主 View，新增 plan panel 渲染区域
- `internal/tui/update.go` — 在 `agentEventMsg` 处理处识别 `ToolExecEnd{Name:"plan"}` 更新 `currentPlan`
- `internal/tui/messages.go` — 视情况新增 `planUpdatedMsg`（TUI 内部用，非 agent 协议）
- `internal/tui/styles.go` — 新增 plan 状态相关样式
- `internal/skill/builtin/dual-model.md` — P3 改写
- 用户 memory `feedback_plan_mode_tui_tasklist.md` — 设计原则与历史决策（仓库外，不入版本控制）
