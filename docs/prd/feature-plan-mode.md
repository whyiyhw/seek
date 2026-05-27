# Plan Mode v2 —— 显式确认的 plan→exe 闭环

**目标**：把 seek 的 `/plan` 从"单向只读 toggle"演化为一个**带显式用户确认门**的工作流：模型分析上下文 → 反思充分性 → 提案问题与解决方案 → 用户审批 → 解锁执行 → 完成报告。异议触发从中断点 re-plan，已完成工作不丢。

**状态**：🚀 v2 P1-P6 全部实施 + v2.x 扩展持续中。本 PRD 取代旧的 `feature-plan-tasklist.md`（仅追踪 TUI task list 可视化，scope 过窄、误判了 plan 模式的核心价值在确认门而非可视化）。

### 实施状态速览（2026-05）

| 子系统 | 状态 | 落地处 |
|--------|------|--------|
| propose 工具 + 三个 agent.Event 类型 | ✅ 已上线 | `internal/tools/propose/`、`pkg/agent/events.go` |
| mode reminder 多态（plan-analyze / plan-execute） | ✅ | `pkg/agent/agent.go:modeReminder` |
| permission policy 子态联动 | ✅ | `internal/permission/permission.go:202-235` |
| status bar `PlanSubstate` + `N/M` 计数 | ✅ | `internal/tui/statusbar.go` |
| TUI task list 面板（原 §1.3 推到 v2 的 item） | ✅ v2.x | `internal/tui/view.go:renderPlanTaskList` |
| per-step 进度追踪工具 `plan(start\|complete\|skip)` | ✅ v2.x | `internal/tools/plan/` |
| `seek -resume` 重建 plan state（event-sourcing） | ✅ v2.x | `internal/tools/plan/reconstruct.go` |
| Re-plan 时已完成步骤的结构化注入 | ✅ v2.x | `propose.ProgressReporter` 可选接口 + `planBridge.ProgressSummary` |
| propose 第 4 选项 `approve_batch`（step 级预批准） | ✅ v2.x | `permission.Policy.preApproved` + `planBridge` |
| plan-analyze 内 bash 只读白名单 | ✅ v2.x | `internal/tools/bash/readonly.go` + `permission.Action.ReadOnly` |
| propose maxSteps 12 → 20 | ✅ v2.x | `internal/tools/propose/propose.go:34` |
| 重复 plan 短路 | ✅ v2.x | `propose.DuplicateChecker` 可选接口 + `planBridge.IsDuplicateOfLastApproved` |
| **plan artifact 文件**（write-once markdown 快照） | ✅ v2.x | `internal/tools/plan/artifact.go` + `paths.Project{ID,Dir,Plans}` + `propose.ContextReceiver` / `ArtifactReporter` |

仍推迟到未来：多 plan 并行 / 嵌套、artifact CLI（`seek plan list/delete`）、团队共享 / 跨设备同步。

## 一、目标与范围

### 1.1 plan 模式的正式定义

```
/plan on
   │
   ▼
┌─────────────────────────────────────────────────────────────────────┐
│  ANALYZE 子态  (permission = ModePlan, 只读)                         │
│  • 模型读上下文：read / grep / list_dir / git / think                │
│  • 遇到歧义 → ask_user 澄清                                          │
│  • 自评：上下文是否足够定义问题 + 拿出可执行方案                     │
│  • 充足 → 调 propose(problem, steps[]) ────────────┐                 │
└─────────────────────────────────────────────────────────────────────┘
                                                      │
                            propose 工具内部弹 picker │
                            options = approve / adjust / cancel
                                                      │
        ┌─────────────────────────────────────────────┴──────────────┐
        │ approve                  adjust                   cancel   │
        ▼                          ▼                        ▼        │
┌──────────────────────┐   ┌────────────────────┐   ┌────────────────┐│
│ EXECUTE 子态          │   │ 回 ANALYZE         │   │ /plan off      ││
│ permission = ModeAsk  │   │ permission 不变    │   │ 流程终止       ││
│ mode reminder 切换    │   │ free-text 反馈进   │   └────────────────┘│
│ 模型按 steps 执行     │   │ transcript，模型   │                    │
│ 完成 → 报告           │   │ 重新 think         │                    │
│                       │   │ ⚠️ 已做工作以      │                    │
│ 中途模型察觉用户异议  │   │   chat narration   │                    │
│ → 调 propose 重新提案 │   │   形式保留         │                    │
│ → 等同 adjust 分支    │   └────────────────────┘                    │
└──────────────────────┘                                              │
        │ all done                                                    │
        ▼                                                             │
┌──────────────────────┐                                              │
│ 模型 report 完成     │ ──────────────────────────────────────────────┘
│ 用户继续提问 = 新轮  │   （新一轮回到 ANALYZE，permission 重锁）
└──────────────────────┘
```

### 1.2 与现有 `/plan` 的关系：扩展，不替换

现有 `/plan`（`internal/tui/commands.go:722`）是单向 toggle，进入后 `permission.ModePlan` 锁住 write/edit/bash，模型的"plan"就是文字输出。v2 不改这个入口：

- **入口语义不变**：用户依然手敲 `/plan` 进入
- **进入后默认是 ANALYZE 子态**——和现状完全等价
- **新增能力**：模型可以调 `propose` 把"我要做什么"提请用户审批；audit 后进入 EXECUTE 子态，permission 解锁
- **退出语义不变**：再敲 `/plan` 或 `Shift+Tab` 切回 ask 模式终止流程

不影响现有 `/plan` 用户的肌肉记忆：只调用过 read 工具的纯审查场景，行为零变化；想"读完真去动手"的用户，多了一条提案→审批→执行的合法路径。

### 1.3 v1 明确不做的事

为避免 scope 漂移，以下统一推到 v2：

- ❌ **TUI task list 面板**（步骤列表可视化、`[x]/[>]/[ ]` 状态机、strike-through）—— 旧 PRD `feature-plan-tasklist.md` 的核心，已论证 v1 不必要
- ❌ **per-step 进度追踪工具**（start / complete / skip 系列）—— EXECUTE 子态中模型靠 chat narration 报告进度
- ❌ **Re-plan 时已完成步骤的结构化注入**—— v1 靠 mode reminder 提醒模型"在 re-propose 前先 summarize 已做工作"，模型自己在 chat 里写
- ❌ **plan 历史 / plan 版本对比**—— transcript 本身已记录每次 propose 的 schema 参数
- ❌ **多 plan 并行 / plan 嵌套**

## 二、设计

### 2.1 状态机概念模型

Plan 模式作为一个**子态机器**叠在现有 permission mode 之上：

| 顶层 mode | plan substate | permission | mode reminder |
|----------|---------------|-----------|---------------|
| `/plan` 关 | — | ModeAsk / ModeYolo | 现有 yolo/空 |
| `/plan` 开 | `plan-analyze`（默认） | ModePlan | "Read context. Call propose() when ready. Use ask_user if unclear." |
| `/plan` 开 | `plan-execute` | ModeAsk | "Execute the approved plan: <steps>. If user disagrees, summarize what's done in chat, then call propose() to re-plan." |

子态切换由 `propose` 工具的返回事件驱动（§2.3），不需要新斜杠命令。

### 2.2 新工具 `internal/tools/propose/`

**Schema：**

```json
{
  "type": "object",
  "properties": {
    "problem": {
      "type": "string",
      "description": "One-paragraph problem statement: what the user wants and what makes it non-trivial. Self-contained — assume the reader hasn't read prior turns."
    },
    "steps": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "maxItems": 12,
      "description": "Ordered concrete actions you will take. 3–8 items typical. Each step must be verifiable by the user (e.g. \"Add X handler in handlers.go\"), not internal phases (\"think about Y\"). Don't include sub-bullets — if a step is too big, split before proposing."
    },
    "why_now": {
      "type": "string",
      "description": "Optional. Briefly: why is now the right time to commit to this plan? Surfaces hidden assumptions to the user (e.g. \"this assumes the auth refactor in #234 is merged\")."
    }
  },
  "required": ["problem", "steps"],
  "additionalProperties": false
}
```

**Execute 内部逻辑：**

1. 校验 schema（steps 非空、≤ 12 项、每项 ≤ 200 字符）
2. 调用注入的 `askuser.Policy.Ask(...)`，传入：
   - title = problem
   - body = numbered steps + optional why_now
   - options = `[{id: "approve", label: "Go ahead"}, {id: "adjust", label: "Adjust"}, {id: "cancel", label: "Cancel /plan"}]`
   - 允许 free-text 附注（用户选 adjust 时可写"step 3 太激进"）
3. 根据用户选择 emit agent.Event（§2.3）并返回结构化结果给模型：

```
proposal: approved (3 steps, ready to execute)
  1. Inventory current middleware call sites
  2. Define TokenStore interface
  3. Migrate session-backed store
```

或：

```
proposal: rejected — user wants adjustments
user feedback: "step 3 should be split — interface first, store change separate"
```

或：

```
proposal: cancelled by user — exit plan mode
```

**权限**：propose 不写 fs、不跑 shell，**无需 `permission.Policy` 注入**——和 `think`、`ask_user` 同级。但它**有副作用**（emit 改变 mode 的事件），所以工具构造时需要持有 `agent.EventSink` 或等价回调（实现细节，待 P1 落地时定）。

### 2.3 新 agent.Event 类型

`pkg/agent/events.go` 加三个事件：

```go
// PlanProposalApproved fires when propose() returns and the user picked "approve".
// TUI consumers MUST switch permission policy to ModeAsk and update mode label
// to "plan-execute". Steps is the proposal verbatim for v2 panel rendering.
type PlanProposalApproved struct {
    Steps []string
}

// PlanProposalAdjustRequested fires when the user picked "adjust" with optional
// free-text feedback. Permission policy and mode label stay in plan-analyze.
type PlanProposalAdjustRequested struct {
    Feedback string
}

// PlanProposalCancelled fires when the user picked "cancel".
// TUI consumers MUST toggle /plan off (permission → ModeAsk, mode label → "").
type PlanProposalCancelled struct{}
```

三者都实现 `isEvent()`。propose 工具的 Execute 方法在收到 ask_user 回值后通过注入的 event sink 推出对应事件。

**为什么必须新增 agent.Event 而不是复用 `ToolExecEnd`：** propose 的"批准"结果有**带外副作用**（permission 切换、mode reminder 切换），单看 ToolExecEnd 的字符串 result 不足以让 TUI 知道该 react —— 它需要从工具结果里反向解析 "approve"/"adjust"/"cancel"，脆且耦合。明确的事件类型让 TUI 的 type switch 一目了然，也便于 v2 加新消费方（比如 status bar 之外的面板）订阅同一份信号。

### 2.4 mode reminder 多态化

修改 `pkg/agent/agent.go:135-144` 的 `modeReminder` 函数。`ModeLabel` 字符串扩展为可识别子态：

| Label | Reminder 文本 |
|-------|--------------|
| `"yolo"` | （现有）`[Mode: yolo — write, edit, and bash are unrestricted.]` |
| `"plan"` | （兼容现有，等价于 plan-analyze）`[Mode: plan — read-only. Do not call write, edit, or bash; produce a plan instead.]` |
| `"plan-analyze"` | `[Mode: plan-analyze — read context to define the problem; call propose(problem, steps) when ready, or ask_user if unclear. No writes.]` |
| `"plan-execute"` | `[Mode: plan-execute — the user approved your plan; execute it step by step. Narrate progress in chat. If the user disagrees, summarize what's done and call propose() again to re-plan.]` |
| `""` | （现有）空字符串，无 reminder |

`SetModeLabel` 调用方：TUI 在收到 `PlanProposalApproved` 时调 `agent.SetModeLabel("plan-execute")`；在 `PlanProposalAdjustRequested` 时调 `SetModeLabel("plan-analyze")`；在 `PlanProposalCancelled` 或用户手动关 `/plan` 时调 `SetModeLabel("")`。

### 2.5 permission policy 联动

现有 `cmdPlan`（`internal/tui/commands.go:722`）目前只切 `opts.Plan` 布尔值并通过 `opts.SetPlan` 回调让上层调整 permission。v1 需要让 **TUI 在收到 `PlanProposalApproved` 事件时**主动把 permission policy 从 ModePlan 切到 ModeAsk —— 这是"approve 解锁"的实际机制。

实现路径：

1. `tui.Model.opts` 加一个 `SetPlanSubstate func(substate string)` 回调
2. 上层（`cmd/seek`）在构造时让这个回调切 `permission.Policy.SetMode(...)` —— ModeAsk for "execute"、ModePlan for "analyze"、不动 for ""（已退出 plan）
3. TUI 的 `update.go` 处理 `agentEventMsg` 时识别三种新 event，分别调 `SetPlanSubstate("execute"|"analyze"|"")` + `SetModeLabel(...)`

注意：**EXECUTE 子态下 permission = ModeAsk，意思是写操作仍然逐个弹 y/N 询问用户。** approve plan ≠ approve every individual write. 这是有意的——plan-level 的"approve" 是关于方向，不是免除人工对每次危险操作的把关。如果用户想免确认，可以再 `/yolo`（但与 plan 互斥，会退出 plan 模式）。

### 2.6 status bar 子态标签

`internal/tui/statusbar.go` 的 `StatusSnapshot` 加一个 `PlanSubstate string` 字段（值："analyze"/"execute"/""）。渲染逻辑：

- `Plan && PlanSubstate == "analyze"` → 在 status bar 现有"plan"标识旁加 `: analyze`，灰色
- `Plan && PlanSubstate == "execute"` → 加 `: execute`，主色高亮（用户应该警觉"现在能改文件了"）
- `Plan && PlanSubstate == ""` → 兼容老路径，只显示 "plan"（视为 analyze）
- `!Plan` → 不显示

**不**新增独立 badge 区域；复用现有 plan/yolo 显示位置加后缀，diff 最小。

### 2.7 异议时已完成工作的保留

v1 用 mode reminder 引导模型，不做结构化注入：

- `plan-execute` 子态的 reminder 末尾已经写明 "If the user disagrees, **summarize what's done** in chat, then call propose() again to re-plan."
- 模型按引导先 chat 输出 "已完成：A、B；进行中：C；未开始：D、E"，**这段 chat 自然进 transcript**
- 模型紧接着调 propose 时，transcript 里就有这段事实，下一轮 reasoner（如果用了 dual-model）或者本轮模型自己能读到

**为什么不做结构化注入：** v1 范围外。v2 加 task list 后，propose 工具可以接受可选的 `prior_completed: []string`，TUI 从 task list state 自动填，那时再做。v1 走"模型自觉"路线，工作量近零、行为可观察、不达标再加。

## 三、关键决策

### 3.1 为什么 propose 是新工具，不是 ask_user 调用

之前一度倾向复用 ask_user（"工具数 +0、UI 已有"），后改主意。理由：

- **副作用语义**：ask_user 是纯粹的"问问题、拿回答"，无副作用。propose 必然伴随 permission 切换、mode reminder 切换、status bar 切换——这些 side effect 应该由独立工具承载，让阅读代码的人一眼看到"调这个工具会改世界状态"。
- **结构化数据**：propose 的 schema 把 `problem`、`steps[]`、`why_now` 显式建模。ask_user 只有 `question` 字符串，要塞结构化数据只能编码进 question 字符串，v2 加面板时还得反向解析。
- **TUI 识别**：TUI 收到 ask_user 返回值时，目前没法分辨"这是普通澄清"还是"这是 plan 审批"。靠 options 模式匹配（`["approve","adjust","cancel"]`）是脆契约。工具名 == 身份，清晰。

### 3.2 为什么 v1 不做 TUI task list 面板

旧 PRD `feature-plan-tasklist.md` 设计了完整的面板渲染、状态机、in_progress 中断恢复。审查后发现：

- 它解决的是"用户视觉感知进度"问题——但 chat 本身就在做这件事（每次工具调用有 active tool 行，模型自然 narrate）
- 真正 load-bearing 的是确认门（propose）和异议循环，不是面板
- v1 没有面板的 plan 模式**完整可用**；v1 有面板但没确认门的 plan 模式只是个花哨的进度条

把面板推到 v2，先把闭环走通。

### 3.3 为什么"已完成工作"靠 chat narration 而不是结构化追踪

完整的结构化追踪需要：(a) per-step 状态机、(b) start/complete/skip 工具、(c) re-plan 时机的 prior_completed 注入、(d) propose schema 扩展。这一整套是旧 PRD 的核心，工作量 1+ 天。

v1 用 mode reminder 引导模型在 re-propose 前 summarize：实测如果模型听话，效果**够用**——transcript 里有事实，下一轮 reasoner 能看到。如果不听话再加结构化层；现在加是预防自己想象出来的问题。

### 3.4 EXECUTE 子态下写操作仍需逐个 ask 确认

PROPOSE 阶段用户 approve 的是**整个方向**（"我同意你按这 5 步做"），不是"你想跑任何 bash 命令都行"。所以 EXECUTE 子态把 permission 切到 ModeAsk（每次危险动作弹 y/N），不切 ModeYolo。

后果：用户在 EXECUTE 子态会被弹多次 approval prompt。这是有意的——单步审批让用户能在执行中途叫停。如果完全信任，应该用 `/yolo` 而不是 plan 模式。

## 四、v2 留口（明确锁定的接口）

为保证 v1 → v2 是 additive 而非 breaking，**以下接口的语义在 v1 已经定型，v2 沿用不改**：

1. **`PlanProposalApproved.Steps []string`** —— v2 面板订阅同一份事件读 steps 渲染；v1 不消费但发出。
2. **propose schema 里的 `steps` 字段是 `[]string`** —— v2 加 `prior_completed []string` 等新字段，是 additive。
3. **mode label 字符串值** `"plan-analyze"` / `"plan-execute"` —— v2 不引入新子态字符串；如果有第三子态（e.g. `"plan-review"`）再扩。
4. **status bar `PlanSubstate string`** —— 同上。

**未锁定的**：propose 的 ask_user 集成细节（options 文案、free-text 处理）、TUI render 细节、事件 sink 注入方式——这些在 v2 可以重构。

## 五、非目标

- 跨 session 自动 resume plan 状态——`seek resume <id>` 已经足够（transcript 含完整 propose 历史）
- Plan 模板 / 复用——skill 系统就是这层抽象
- 团队共享 plan / 导出为 markdown —— 需要再说
- 多 plan 并行 / 嵌套
- Plan 时间追踪 / SLA
- 用户在 TUI 上手动编辑 task list——v1 没 task list；v2 即使加了面板，也走"chat 反馈→re-propose"路线（§3.4 同理）

## 六、阶段交付

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P1 | `internal/tools/propose/` 工具实现 + 完整单测 | ~150 行 + 测 ~150 行 |
| P2 | `pkg/agent/events.go` 新增三个事件类型 + propose 工具的 event sink 注入 | ~50 行 + 测 ~30 行 |
| P3 | `pkg/agent/agent.go` `modeReminder` 扩展支持 `plan-analyze` / `plan-execute` | ~30 行 + 测 ~30 行 |
| P4 | TUI 联动：`tui.Model.opts.SetPlanSubstate` 回调、update.go 识别三个事件、statusbar 显示子态 | ~120 行 + view test ~80 行 |
| P5 | 新增或扩展 skill 描述 plan 模式工作流（dual-model.md 或独立 `/plan-flow.md`） | ~80 行 |
| P6 | 端到端验证：ANALYZE → propose → approve → EXECUTE 全跑、adjust 分支、cancel 分支、Ctrl-C 中断 resume | 半天 |

**总计：~1 天。** 先 P1+P2+P3 合一个 PR（工具 + 事件 + reminder），再 P4 合一个 PR（TUI 联动），最后 P5 切换 skill。三个 PR，每个独立可验证。

**P1 测试覆盖**（按 CLAUDE.md "测试失败路径"）：
- happy path：3 种 user choice（approve / adjust / cancel）各跑通
- schema 校验：steps 空数组、steps 13+ 项、step 长度超 200 字符、problem 空
- ask_user policy 在 Deny 模式 / 取消（Ctrl-C）时的处理
- event sink 注入：mock sink 验证三种事件分别在三种用户选择下 emit
- propose 工具返回的结构化结果字符串（给模型读的）格式稳定

**P4 视图测试**：
- `seek resume` 加载含 propose 事件的 session 后，permission policy 状态、mode label、status bar 子态标签三者一致
- approve 后 status bar 切到主色高亮；adjust 后保持 analyze 灰色；cancel 后回 ask

## 七、风险

- **propose 滥用为 chat memo**：模型把"我接下来要 read 几个文件"当 propose 调，把每次微小决定都走审批门——给用户造成 prompt 疲劳。mitigation：schema description 明确"steps must be verifiable actions, not internal phases"；P5 skill 给反例（"propose 是用来获取改文件的许可，不是用来报告读文件的计划"）。
- **EXECUTE 子态漂移**：approve 后模型偏离原 plan 干别的事。v1 没面板提醒用户"approved scope"。mitigation：mode reminder 里嵌入 approved steps（"Execute the approved plan: <steps>"），用 recency bias 加锁；P5 skill 写明"如果发现要做的事不在 approved steps，先 re-propose 再做"。
- **re-plan 时 chat summary 不可靠**：模型可能跳过 summary 直接调 propose。mitigation：mode reminder 用强语气 ("**summarize what's done in chat, then call propose**")；P6 端到端验证里专门跑 adjust 分支看是否 summarize。如果实测高频跳过，再上结构化注入（v2 范围）。
- **ask_user free-text 在 adjust 分支被忽略**：用户 adjust 时写了反馈，模型在 re-think 时没把它当作首要约束。mitigation：propose 工具返回给模型的结构化结果里把 `user feedback` 字段以醒目格式包出来（见 §2.2 返回示例）。
- **status bar 文字溢出**：子态后缀让 status bar 在窄终端被截断。mitigation：复用现有 statusbar 缩窗逻辑；窄到一定程度时只显示 "plan:E" / "plan:A"。

## 八、Plan artifact 文件（v2.x 扩展，✅ 已上线）

**目标**：在每次 propose approval 时，在 `~/.seek/projects/<id>/plans/` 下写一份 **只读 markdown 快照**，记录"在哪一刻、对什么问题、批准了哪几步"。纯 artifact（合约文档），与 §2.3 的 event-sourcing state 正交，不参与 runtime 状态机。

**实施落地点**：
- 写入：`internal/tools/plan/artifact.go:WriteArtifact`
- Slug + render 逻辑：同文件 `extractSlug` / `humanizeSlug` / `renderArtifact`
- 路径 helper：`internal/paths/paths.go:ProjectID/ProjectDir/ProjectPlans`（提升自原 `internal/memory:projectID`）
- propose 上下文桥：`propose.ContextReceiver`（拿 problem + why_now）+ `propose.ArtifactReporter`（回报 path/err）
- 触发 + 写入站点：`cmd/seek/main.go:planBridge.Approved` —— 是上述两个接口的实现处
- 测试：`internal/tools/plan/artifact_test.go`（slug/conflict/atomic/required-fields 15 个用例）+ `internal/tools/propose/artifact_integration_test.go`（接口集成 8 个用例）

### 8.1 设计动机

当前 plan 的设计文档以 propose tool result 形式嵌在 transcript 里。这让 transcript 成为单一事实源（适合 runtime state），但有三个人类侧的不便：

- 不能用 `vim` / `cat` / 编辑器直接看一份 plan
- 不能跨 session 浏览（"上个月我让 seek 做 auth refactor 的 plan 长什么样？"）
- 不能复用（"把这份 plan 拿出来改改用到另一个项目"）

artifact 文件解决这三件事。明确**不**解决：runtime state 持久化（state 仍走 transcript，artifact 不参与），跨 session 自动 hydrate（artifact 是文档，不是机器读的）。

### 8.2 与 transcript event-sourcing 的关系

artifact 和 transcript 是 **两个独立的写入路径**，都在 approval 时落定：

| | transcript event-sourcing | artifact 文件 |
|---|---|---|
| 内容 | propose tool args + result + 后续 plan tool calls 全时序 | problem + steps + why_now + approval metadata，单点快照 |
| 何时写 | runtime（agent loop 自然写） | 只在 approval |
| 何时变 | 每次 plan tool call 都更新 | 永不变 |
| 用途 | runtime state 重建（`seek -resume`） | 人类浏览 / 跨 session 历史 |
| 写盘失败致命? | 是（session save 失败是事故） | 否（warning 即可，artifact 是观测物） |

artifact **不替代** transcript。adjust / re-propose / cancel / duplicate 都不写 artifact，但 transcript 全记录。这让 artifact 永远是"被批准的合约"，没有"被拒绝的草稿"夹进来。

### 8.3 触发条件

写 artifact 当且仅当 `propose` 返回用户选择是：

- ✅ `approve`（per-call 模式）
- ✅ `approve_batch`（auto-approve-per-step 模式，§Phase C）

**不**写 artifact 的路径：

- ❌ `adjust`（带或不带 free-text feedback）
- ❌ `cancel` / Esc / `/plan` off
- ❌ duplicate 短路（§F1 已经走 `[plan: duplicate]` 短路，不调 Sink.Approved）
- ❌ defensive fallback（picker 返回未识别 ID）

写入点：`planBridge.Approved()` 内部，在 `s.planTool.Seed(steps)` 之后、`EmitEvent(PlanProposalApproved)` 之前。

### 8.4 文件路径

`~/.seek/projects/<project-id>/plans/<YYYYMMDD>-<HHMM>-<slug>.md`

- **project-id** = 复用 §memory 的 `sha256(absCWD)[:16]`。当前实现在 `internal/memory/memory.go:projectID`（私有函数）。建议提升到 `internal/paths/` 作为共享 helper：
  ```go
  func ProjectID(abs string) string           // 16-hex sha256 prefix
  func ProjectDir(abs string) (string, error) // ~/.seek/projects/<id>/
  func ProjectPlans(abs string) (string, error) // ~/.seek/projects/<id>/plans/
  ```
- **YYYYMMDD-HHMM** = approval 本地时刻；分钟精度，跨 session 同分钟极少
- **slug** = 从 problem 提取 3-5 个 keyword：lowercase、去停用词、合 alphanum + `-`；提取失败回退为 `plan`
- **冲突解决**：同分钟同 slug 极少见；遇到则递增 `-2`、`-3` …

例子：`~/.seek/projects/a3b8f9e2c5d1742a/plans/20260526-1430-auth-refactor.md`

### 8.5 内容 schema

```markdown
# <Slug Humanized>

- **Approved**: 2026-05-26 14:30:42 +0800
- **Session**: abc123ef4567
- **Approval mode**: per-call | auto-approve-per-step
- **Project**: /Users/whyiyhw/code/github/seek

## Problem

<problem text from propose args, verbatim>

## Steps

1. <step 1>
2. <step 2>
...

## Why now

<why_now if present in propose args; section omitted otherwise>

---

*Write-once snapshot of the plan as approved. Step progress lives in the session transcript (`~/.seek/sessions/<session>.jsonl`), not here. To browse all plans for this project: `ls ~/.seek/projects/<id>/plans/`.*
```

**字段语义**：

- `Approval mode` 是 `per-call` 或 `auto-approve-per-step` —— 让用户回看时记得当时批准了哪一档
- `Session` 是写 artifact 的那个 session id（用于追到具体 transcript）
- 不写 `Status: completed/in-progress` —— artifact 是 write-once，state 在 transcript

### 8.6 生命周期

- **写一次** on approval
- **永不更新**（永不覆盖、永不 truncate；后续 step start/complete 不动它）
- **永不自动删除**（artifact，不是 cache；磁盘开销可忽略，见 §9.10）
- 用户手动 `rm` 管理；v2.x 不引入 GC 路径

### 8.7 失败模式

写盘失败是 **NON-FATAL**：

- 实现路径：`os.MkdirAll` + `os.WriteFile` + atomic rename（`*.tmp` → 最终路径）
- 失败时：`fmt.Fprintln(os.Stderr, "plan artifact: ", err)` + 在 `propose` 返回的 tool result 尾部加一行小字 `(note: plan artifact write failed: <err> — workflow continues)`，让 user 和 model 都看得到
- 不阻塞 `Approved` 的其他副作用（plan state seed、event emit、permission flip）

理由：artifact 是观测物，磁盘满 / 权限错 / 网络盘断都不应该让 plan 工作流崩。

### 8.8 隐私 / 安全考量

- artifact 内容含 problem 描述，可能涉敏感字串（"重构含密钥的 auth"）。文件位于 `~/.seek/projects/`，受标准 home 权限保护（0700）
- 不写完整的 transcript / messages —— artifact 是合约级摘要，不是日志
- §P5 skill 文档须新增提示："不要在 plan 描述里写明文密钥 / 完整 PII"

### 8.9 v2.x 明确不做

- ❌ **`seek plan list / delete / show` CLI**：`ls` + `cat` / 编辑器够用；CLI 推到 v3
- ❌ **artifact 内嵌当前 step 进度**：会破坏 write-once 语义；进度去看 transcript / TUI
- ❌ **artifact 在新 session 上自动 hydrate plan state**：artifact 是人类文档，机器走 transcript
- ❌ **artifact 跨设备同步 / 团队共享**：用户用 git / Dropbox / iCloud Drive 同步 `~/.seek/projects/` 即可
- ❌ **artifact 内嵌 batch 模式下每个 step 的执行总结**：write-once 语义同上；如要这种 "completion log"，应该是另一种 artifact（"plan-completed-log"），不混进 plan 文件

### 8.10 风险与 mitigation

| 风险 | 概率 | 影响 | mitigation |
|------|------|------|--------|
| 磁盘占用 | 低 | 单文件 < 4KB，一天 5 个 plan × 一年 ≈ 7MB / project | 不做任何 GC；用户嫌多自己 `rm` |
| Slug 冲突 | 低 | 同分钟同 slug | filename 计数器后缀 `-2`、`-3` |
| 失败被吞 | 中 | 用户不察觉 artifact 没生成 | warning 走 stderr + tool result 尾注（双通道） |
| 隐私泄露 | 低 | problem 含敏感字串 | home 权限 + skill 提示 |
| filename 字符越界 | 低 | slug 含路径分隔符 / shell metachar | slug 提取只保留 `[a-z0-9-]`；其他全删 |

### 8.11 阶段交付（✅ 已完成）

| 阶段 | 内容 | 落地处 / 实际工作量 |
|------|------|-------|
| A1 ✅ | `paths.ProjectID/ProjectDir/ProjectPlans` 共享 helper；`internal/memory` 改用 | `internal/paths/paths.go` +47 行；`internal/paths/paths_test.go` +50 行；`internal/memory/memory.go` 改 14 行 |
| A2 ✅ | `plan/artifact.go`：slug 提取 + 冲突计数 + atomic write + markdown render | `internal/tools/plan/artifact.go` +220 行；`artifact_test.go` +200 行（15 个用例） |
| A3 ✅ | `planBridge` 持有 projectAbs / sessionID 闭包 / artifactEnabled / pending context；`Approved` 内调 WriteArtifact，失败回灌 stash | `cmd/seek/main.go` ~+300 行（含两个新接口实现） |
| A4 ✅ | propose 新增 `ContextReceiver` + `ArtifactReporter` 可选接口；`approveResult` 拼 "Plan artifact: ..." 或 "(note: ... failed)" | `internal/tools/propose/propose.go` ~+100 行 |
| A5 ✅ | 接口集成测试（含 OnProposeStart 时序、approve/batch 成功 / 失败 note / 无 reporter quiet / adjust+cancel 不查 status / duplicate 短路 skip 两个 hook） | `internal/tools/propose/artifact_integration_test.go` +200 行（8 个用例） |

**实际工作量**：约半天，总改动 +1124 / -83，跨 18 文件。`go vet` clean、`go test -race ./...` 44 个包全绿、0 failure。

**关键设计调整 vs PRD 初稿**：propose 主 Sink 接口没动（避免第三次破坏性签名变更），新能力通过两个**可选 upcast 接口** `ContextReceiver` + `ArtifactReporter` 加入。理由见 [docs/pitfalls.md](../pitfalls.md) 的 "Plan artifact write needs context BEFORE Sink.Approved fires" 条。

### 8.12 v2.x 锁定的接口

为保证未来 v3 是 additive：

1. **文件路径 schema** `~/.seek/projects/<id>/plans/<YYYYMMDD>-<HHMM>-<slug>.md` —— 锁定。用户脚本可能 `ls` 这个路径。
2. **front-matter 字段名**：`Approved` / `Session` / `Approval mode` / `Project` —— 锁定。新字段可加，旧字段不删不改语义。
3. **section 标题**：`## Problem` / `## Steps` / `## Why now` —— 锁定。
4. **footnote 段** —— 文案可调；位置（文末分隔线之后）锁定。

未锁定（v3 可改）：slug 提取算法、冲突计数器格式、failure note 文案。

---

## 九、相关文件

### v2 核心（已落地）

- `internal/tools/propose/` — propose 工具实现
- `internal/tools/askuser/askuser.go` — propose 内部复用 `askuser.Policy`
- `internal/tools/think/think.go` — 同样"无 permission gating"的工具范例
- `pkg/agent/events.go` — `Plan*` 事件类型（含 v2.x 新增的 `PlanStepUpdated`）
- `pkg/agent/agent.go:modeReminder` — 子态 reminder 多态
- `internal/permission/permission.go` — `ModePlan` / `ModeAsk` 切换 + v2.x 的 `preApproved` 字段 + `Action.ReadOnly`
- `internal/tui/commands.go:cmdPlan` — `/plan` 入口（保持语义不变）
- `internal/tui/model.go` — `Options` 上的 `SetPlanSubstate` / `PlanSteps` / `PlanCurrentIdx` / `RevokePlanPreApproval`
- `internal/tui/statusbar.go` — `PlanSubstate` + `PlanStepsTotal/Done`（v2.x 加的 `N/M` 计数）
- `internal/tui/update_agent.go` — 识别 `Plan*` 事件，触发回调

### v2.x 扩展（已落地）

- `internal/tools/plan/plan.go` — plan tool（start/complete/skip + Sink 接口）
- `internal/tools/plan/reconstruct.go` — transcript event-sourcing 重建 plan state
- `internal/tools/bash/readonly.go` — plan-analyze 内 bash 只读白名单 + metachar 拦截
- `internal/tui/view.go:renderPlanTaskList` — TUI 任务列表渲染
- `cmd/seek/main.go:planBridge` — propose + plan tool 双 Sink 桥接 + batch 状态机
- `internal/skill/builtin/plan-mode.md` — plan 模式 skill 文档

### v2.x artifact 扩展（PRD §八，✅ 已上线）

- `internal/paths/paths.go` — 新增 `ProjectID(abs)`、`ProjectDir(abs)`、`ProjectPlans(abs)`（提升 `internal/memory/memory.go:projectID` 为共享 helper）
- `internal/tools/plan/artifact.go` — slug 提取 + filename 生成 + markdown 渲染 + atomic write
- `internal/tools/plan/artifact_test.go` — slug 边界、冲突计数器、内容 schema、required-fields、tmp 清理
- `internal/tools/propose/propose.go` — 新增可选接口 `ContextReceiver` + `ArtifactReporter`；`approveResult` 拼接 artifact 路径行 / 失败 note
- `internal/tools/propose/artifact_integration_test.go` — 接口集成验证（含 duplicate 短路、adjust/cancel 不查 status）
- `cmd/seek/main.go:planBridge` — 实现两个新接口；`Approved` 内调 `WriteArtifact` 并 stash (path, err)
- `docs/pitfalls.md` — 一条新 pitfall："Sink 主接口不再扩，新能力走可选 upcast 接口"

### 相邻 PRD

- [`feature-webfetch.md`](feature-webfetch.md) — 解决 plan-analyze 下"想读外部文档但 bash 被 deny"的缺口。专用 HTTP GET 工具，强约束 + SSRF 防御，跟 plan-mode 的安全姿态完全对齐。✅ v1 已上线。

### 历史

- `docs/prd/feature-plan-tasklist.md` — 旧 PRD，被本文取代，保留作设计推演审计
