# Feature: AskUserQuestion v2（v6 柱 I）

**所属版本**：seek v0.7.0 · v6 柱 I 第一项
**前置阅读**：[`v6.md`](v6.md) §3.1 草稿、[`internal/askuser/askuser.go`](../../internal/askuser/askuser.go) v1 实现、[`internal/tools/askuser/askuser.go`](../../internal/tools/askuser/askuser.go) v1 schema
**状态**：🚀 已交付（v0.7.0 · phase 1+2a+2b）。Schema + Batch/AskBatch（cb8ed16）→ TUI 多题 stack 状态机（5b775fe）→ Preview 双栏 + 截断（本提交）。27 个新测试全过，全 repo `-race` 绿。
**预估工作量**：~2 天

---

## 1. 真实差距（已校 v6 §3.1 的事实错误）

v6 草稿误把 `multiSelect` + "Other 自由文本" 列为 v2 新增；ground-truth 检查代码后确认：

| 能力 | v1 状态 | v2 是否新增 |
|---|---|---|
| 单题 picker | ✅ shipped | — |
| `multi_select: true` 多选模式 | ✅ shipped (`askuser.go:96`) | — |
| Option `label + description` | ✅ shipped | — |
| TUI 自动追加 "Other / type your own answer" 行 | ✅ shipped (`askuser.go:60`，测试 `TestValidate_RejectsReservedOtherID` 钉死) | — |
| **一次多题（按顺序 stack 渲染）** | ❌ | ✅ **v2 新增** |
| **Option 的 `preview` 字段（侧栏渲染 mockup / 代码片段）** | ❌ | ✅ **v2 新增** |
| **`header` 字段（短 chip-style label，显示在题旁）** | ❌ | ✅ **v2 新增**（可选） |

实际新增 = 多题 + preview + header。其他都是借用 v1 的现成能力。

## 2. 目标与非目标

### 2.1 目标

1. 模型一次 `ask_user` 调用可问 1–4 题（按 Claude Code `AskUserQuestion` 上限）
2. 每题选项可附 `preview` 字符串（多行 mockup / 代码片段 / ASCII diagram），TUI 切换左右双栏，hover 选项时右栏显示其 preview
3. v1 单题 schema **零破坏**——已有的 skill / prompt / model-trained-shape 全部继续工作

### 2.2 非目标

- **不做** GUI 控件（slider / color picker / file picker）—— preview 是只读文本展示，v6 §3.1 anti-goal 明确
- **不做** 多题之间的条件依赖（Q2 选项依赖 Q1 答案）—— Claude Code 也不做，超出范围
- **不做** preview 的 markdown 渲染——纯 monospace 文本，与 seek 现有 ASCII mockup 一致
- **不引入新工具**——polymorphic schema 走单一 `ask_user`，不新增 `ask_user_v2`

## 3. Schema 设计（关键决策）

### 3.1 polymorphic schema 而非新工具

```jsonc
{
  "type": "object",
  "oneOf": [
    {
      // v1 form: single question
      "required": ["question", "options"],
      "properties": {
        "question": { "type": "string" },
        "header": { "type": "string" },          // v2 NEW (optional)
        "options": { "type": "array", "minItems": 2, "maxItems": 4 },
        "multi_select": { "type": "boolean" }
      }
    },
    {
      // v2 form: multi-question batch
      "required": ["questions"],
      "properties": {
        "questions": {
          "type": "array",
          "minItems": 1,
          "maxItems": 4,
          "items": {
            "type": "object",
            "required": ["question", "options"],
            "properties": {
              "question": { "type": "string" },
              "header": { "type": "string" },
              "options": { "type": "array", "minItems": 2, "maxItems": 4 },
              "multi_select": { "type": "boolean" }
            }
          }
        }
      }
    }
  ]
}
```

Option struct gets `preview` field (optional):

```jsonc
{
  "id": "glassmorphism",
  "label": "Glassmorphism",
  "description": "Frosted-glass look with backdrop blur",
  "preview": "┌────────────────────┐\n│  ░░░░ blur 12px   │\n│  rgba(255,.1)     │\n└────────────────────┘"
}
```

**为什么 polymorphic 而非新工具**：
- 模型在过去训练上下文里见过 `ask_user({question, options})` 形态；split 成两个工具会让模型在简单场景下犹豫"选哪个"
- prefix cache：schema bytes 一次变更（v2 ship 即 baseline），之后稳定
- 工具数量越少，schema 注入文本越短，每轮节省 ~200 tokens

**Validation**：tool 的 `Execute` 先尝试解 `questions` 数组——非空走 v2 路径；空再 fallback 解 v1 字段。错误信息明确指向 "用其中之一"，不要同时提供。

### 3.2 Answer 形状

v1 单题返回 `{chosen_ids, free_text, cancelled}`。v2 多题返回**数组**，按 question 顺序对齐：

```json
{
  "answers": [
    { "chosen_ids": ["react"], "free_text": "", "cancelled": false },
    { "chosen_ids": [], "free_text": "Tailwind + DaisyUI", "cancelled": false }
  ]
}
```

v1 路径返回保持原 shape（不加 `answers` 包装）—— 兼容。

### 3.3 一题中途取消的语义

多题 stack 中用户在第 2 题按 Esc：
- 已答的 Q1 答案**保留**
- Q2 标 `cancelled: true`
- Q3..N **不再渲染**，全部标 `cancelled: true`
- 整体 result 仍返回 `{answers: [...]}`，模型自己判断哪些 cancelled 决定下一步

理由：用户 Esc 通常意味着"我后悔开始这轮问答"——但已确认的答案不该丢，模型可基于部分答案继续。这跟 v1 单题 Esc 行为一致（Cancelled 是状态而非错误）。

## 4. 内部类型扩展

`internal/askuser/askuser.go`：

```go
// Option gets preview (optional).
type Option struct {
    ID          string `json:"id"`
    Label       string `json:"label"`
    Description string `json:"description,omitempty"`
    Preview     string `json:"preview,omitempty"` // v2 NEW
}

// Question gets header (optional). MultiSelect + Options unchanged.
type Question struct {
    Question    string
    Header      string  // v2 NEW
    Options     []Option
    MultiSelect bool
}

// Batch is the v2 entry point — holds 1..4 questions.
type Batch struct {
    Questions []Question
}

// Validate now takes Batch. v1 single question is wrapped to
// Batch{[]Question{q}} before validation.
func ValidateBatch(b Batch) error {
    if len(b.Questions) < 1 || len(b.Questions) > 4 {
        return fmt.Errorf("batch must have 1-4 questions, got %d", len(b.Questions))
    }
    for i, q := range b.Questions {
        if err := Validate(q); err != nil {
            return fmt.Errorf("question %d: %w", i, err)
        }
    }
    return nil
}

// Policy.AskBatch is the v2 entry point. v1 Policy.Ask remains for
// backward callers (and internal use); the tool layer always calls
// AskBatch now, wrapping single questions to a 1-element batch.
func (p *Policy) AskBatch(b Batch) ([]Answer, error) { ... }
```

Validate (v1) 保留原签名 — 内部只检查单题；ValidateBatch wrap 之。

## 5. TUI 实现

### 5.1 状态扩展（`internal/tui/model.go`）

```go
// New field replaces pendingQuestion for v2.
pendingBatch *askuser.Request
// Index of currently-active question in batch.
pendingBatchIdx int
// Accumulated answers for already-completed questions.
pendingBatchAnswers []askuser.Answer
```

v1 `pendingQuestion` 保留作为兼容字段——后端 wrapper 实际把 v1 Request 也升级成 1-题 batch，但旧字段名 + 类型 alias 让旧调用站点继续 compile。

### 5.2 多题 stack 渲染

题目按顺序竖向 stack：
- 已答（idx < pendingBatchIdx）：显示题目 + 选中行的 label（灰色），不再可交互
- 当前题（idx == pendingBatchIdx）：完整 picker 渲染（与 v1 同形）
- 待答（idx > pendingBatchIdx）：仅显示题目 + "..."（占位，dim 色）

任一题确认后 `pendingBatchIdx++` + cursor 重置。最后一题完成后通过 Reply channel 发回 `[]Answer`。

### 5.3 Preview 双栏

只在**当前活跃题**且其选项里至少一个有 `Preview` 字段时启用。布局：

```
┌─ ? Choose framework ──────────────────────────────────────────┐
│ ▸ react       SPA · 18k stars        │ ┌─────────────────┐   │
│   vue         Progressive · 12k       │ │ <App>           │   │
│   svelte      Compile-time            │ │   <Home/>       │   │
│   solid       Fine-grained            │ │ </App>          │   │
│                                       │ └─────────────────┘   │
│                                       │ preview: react        │
│ ↑/↓ navigate · Enter accept · Esc cancel                       │
└────────────────────────────────────────────────────────────────┘
```

实现细节：
- 终端宽度 < 100 cols 时自动 collapse 为单栏，preview 渲染在选项下方（缩进 + 边框）
- preview 内容 trim 到最多 12 行 × 80 列，溢出加 `... [truncated]` 行
- 切换 cursor 时 preview 重渲染（实时）

### 5.4 keymap

完全复用 v1 picker keymap——↑/↓/Enter/Space/Esc 语义不变。新增：
- `j/k` 跨题间快速跳转（如果用户在 Q2 想回看 Q1 答案）—— 在当前 batch 已答区域**只读** scroll
- Esc 行为：在某题 picker 内按 Esc = 取消该题及后续；在 batch 第 1 题 picker 按 Esc = 取消整批（与 v1 相同语义）

## 6. 工具层（`internal/tools/askuser/askuser.go`）

`Execute` 分发：

```go
func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
    // Try v2 first (questions array)
    var v2 v2Args
    if err := json.Unmarshal(raw, &v2); err == nil && len(v2.Questions) > 0 {
        return t.executeBatch(v2)
    }
    // Fallback to v1 (single question)
    var v1 v1Args
    if err := tools.UnmarshalStrict(toolName, raw, &v1, "question", "options", "multi_select", "header"); err != nil {
        return "", err
    }
    return t.executeSingle(v1)
}
```

`executeSingle` 内部走 wrap-to-batch 路径，所以 TUI 永远收到 Batch（少一条 codepath）。result JSON 按入参 shape 决定包装：v1 入 → 单 result；v2 入 → `{answers: [...]}`。

工具描述（`description`）更新：

```
Ask the user to pick from 2-4 discrete choices via an inline TUI picker.

Two forms:
- single question: {question, options, multi_select?}  (backward compatible)
- batch (1-4 questions): {questions: [{question, options, multi_select?, header?}, ...]}

Each option may include an optional `preview` field — a plain-text mockup,
code snippet, or ASCII diagram (~12 lines × 80 cols) rendered in a side
panel when the user hovers the option. Use preview when comparing
visual / structural alternatives where labels alone are insufficient.

Use batch form when you have 2-4 INDEPENDENT decisions to ask at once
(e.g. "framework + styling + state-management"). Don't batch related
follow-ups — those should be conversational. Don't batch a question
where the model genuinely doesn't know what to ask second until it
sees Q1's answer.
```

## 7. 估时分解（与 v6 §3.1 一致）

| 子项 | 估时 |
|---|---|
| Schema 扩展（`questions` 数组 + `preview` + `header`）+ Execute 分发 | 0.5d |
| 内部 Batch / ValidateBatch / Policy.AskBatch | 0.25d |
| TUI 多题 stack 状态机 + 渲染 | 0.5d |
| Preview 双栏 component + 窄终端 collapse | 0.5d |
| 集成测试（v1 兼容 + v2 多题 + preview + mid-batch Esc）+ 文档 | 0.25d |
| **共** | **2d** |

## 8. 测试矩阵（含 CLAUDE.md "5 标准"）

| 测试 | 覆盖 |
|---|---|
| `TestExecute_V1SingleQuestion_BackwardCompat` | v1 schema 入参 → v1 result shape（保持兼容） |
| `TestExecute_V2Batch_TwoQuestions` | 2 题 batch → result `{answers: [..]}` |
| `TestExecute_V2BatchMaxFour_Allowed` | 4 题 batch（上限）正常 |
| `TestExecute_V2BatchFive_Rejected` | 5 题 → 明确错误 |
| `TestExecute_OptionPreview_FlowsThrough` | preview 字段从入参到 Policy.AskBatch 不丢 |
| `TestValidateBatch_RejectsZeroQuestions` | 空 batch → 错误 |
| `TestValidateBatch_PropagatesPerQuestionErrors` | 第 3 题选项缺 id → "question 2: option 0: id required" |
| `TestAskBatch_MidBatchCancel_PreservesPriorAnswers` | Q2 cancel → Q1 答案保留，Q3..N 全 cancelled |
| `TestAskBatch_ConcurrentSafe` (race) | 并发 AskBatch 调用 — race 检测 |
| `TestTUI_BatchStackRender_StateMachine` | 多题渲染状态机 — 已答 dim、当前 active、待答占位 |
| `TestTUI_PreviewPanel_NarrowTerminalCollapse` | 宽度 < 100 cols → collapse 为单栏 |
| `TestTUI_PreviewPanel_TruncatesOverflow` | preview 超 12 行 → 末行加 `... [truncated]` |
| `TestPolicy_AskBatch_ContextCancel` | ctx cancel mid-batch → 返回 ctx.Err() + 已答保留 |

13 个测试，跑 `-race`，覆盖兼容性 + 新功能 + 错误路径 + 并发。

## 9. 风险

| 风险 | 缓解 |
|---|---|
| polymorphic oneOf schema 在某些 LLM provider 上 validator 报错 | DeepSeek + OpenAI tested OK；Anthropic 需要先验证 — 进入实施时跑 smoke。万一某 provider 严格拒绝，fallback 是接受 `{question, options}` **或** `{questions}`（不用 oneOf 关键字，自己在 Execute 里判断） |
| 模型滥用 batch 把对话切碎（该 1 题硬塞 4 题） | 工具 description 明确"不要 batch 相关 follow-up"；dogfood 后看是否需要在系统 prompt 加更强约束 |
| preview 在窄终端折叠后 UX 变差 | 100 col 阈值是直觉值；dogfood 后调；测试矩阵已 pin 行为 |
| TUI 多题状态机引入新 race 窗口 | 所有状态写入都在 TUI 单 goroutine（bubbletea 模型）；askuser.Policy 的 RWMutex 复用，无新加锁 |
| v1 旧 result shape 在批量场景下被错误返回（包装漏） | `Execute` 返回 shape 由**入参 shape** 决定，不是处理 shape——单题入 = 单题出，batch 入 = batch 出；强制测试覆盖 |
| 模型对 `header` 字段过度使用（每题都加，TUI 变拥挤） | description 说明 header 仅在多题 stack 用于区分题目；单题场景 header == question 时建议省略 |

## 10. 与其他子系统的关系

- **plan-mode v2**：不变。propose 工具的 picker 走另一套（非 askuser），不复用。
- **subagent (v5 柱 G)**：子代理调用 `ask_user` 走父 askuser.Policy（继承）—— 已有行为，v2 不动
- **skill**：内置 / 用户 skill 描述里若提到 ask_user 用法的需要同步更新；扫一遍 `internal/skill/builtin/*.md`

## 11. 后续候选（v0.8+）

- `preview` 支持 markdown / syntax highlighting（要引入 markdown renderer 依赖，慎重）
- 跨题条件依赖（Q2 选项由 Q1 答案决定）
- 历史 batch 复盘视图（`/asks` TUI 面板，类似 `/agents`）
- AskUserQuestion v3 GUI 控件（如果 v2 dogfood 后纯文本预览不够）

## 12. 集成 checkpoint

PRD 通过 → 起新分支 `feature/askuser-v2` → 5 个子项按 §7 顺序实施 → 每子项独立 commit → 全部完成后 squash 或保留按需 → ship 时一并更新 README/comparison/PRD index。

不写 changelog——seek 没建立 CHANGELOG.md 惯例；commit history + PRD README 就是变更记录。
