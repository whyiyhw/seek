# Feature: 建议回复（Suggested Reply / 模型猜下一步）

**所属版本**：v4 · 柱 D（闭环工效 / 模型自校准）
**前置阅读**：[PRD v3 umbrella](v3.md)、[`feature-tui-ergonomics.md`](feature-tui-ergonomics.md)（M9.5 Tab 补全语义在 input 框的现有约定）、[`internal/tui/update_key.go`](../../internal/tui/update_key.go)（M9.4 后的 keymap 分发）
**状态**：🚀 已交付（M10.0 / v0.5.0）。`--no-suggest` CLI flag（`cmd/seek/main.go:667`）+ `suggest_reply` config + 预测尾调用 + Tab 接受 + `renderSuggestedReplyHint`（`internal/tui/view.go:256`）+ reverse-injection 闭环全部 ship。
**预估工作量**：~3 天（预测尾调用 + Tab 接受 + UI + 反向注入闭环 + 测试）

---

## 1. 动机

LLM 回完话后，多数情况下用户的下一句是高度可预测的：

- 模型刚给了 3 个选项 A/B/C 并推荐 A，用户大概率回 "用 A" 或 "A 方案"
- 模型刚说"我已经改完了，建议跑一下测试"，用户大概率回 "跑测试" / "yes"
- 模型刚问"要继续 yolo 模式还是开 plan？"，用户大概率回二选一

现状是用户每次都得**完整敲一遍**，即便意图毫无歧义。这是工效面板里的一个未利用空档。

竞品对比：

- Claude Code：无
- Cursor (chat panel)：无
- Aider / Cline：无

这是个真正空白的差异化点——前提是设计成"低风险、零打扰、可关闭"的形态。

## 2. 设计目标与不做什么

### 目标

1. **预测产生于 LLM 回合末尾**，与用户输入解耦——用户感知零延迟，不为 placeholder 卡顿
2. **Tab 单一语义 = 接受建议**——零歧义，没有"还是 Enter？"的纠结；接受后**仅填入输入框**，用户可继续编辑或按 Enter 发送
3. **不破坏 prefix cache**——主流 transcript 字节序列完全不变；预测走单独 API 调用
4. **失败安静退化**——预测调用超时 / 错误 / 用户已经开始打字，安静丢弃，绝不阻塞主流
5. **Mispredict 闭环**：模型猜错 → 下一轮把 mismatch 信号注入下游 system note，让模型重新校准对用户的理解。**这是柱 D 的核心闭环**，不是可选项
6. **单一开关可关停**：`--no-suggest` / `suggest_reply: false` 一刀切——禁用预测、UI、注入全部子系统；用户层面零认知负担

### 不做什么（v4 明确延后）

- ❌ **预测过程在主 transcript 内联**（如让模型在响应末尾输出 `<next-action>` tag）——会污染 transcript 字节序列、破缓存命中率，且依赖模型遵守约定；走单独尾调用更干净
- ❌ **Inline ghost-text 在用户输入中段实时续写**（Copilot 模式）——latency / 成本 / 干扰式 UX 都不划算（见 [feature-tui-ergonomics.md §2 "不做什么"](feature-tui-ergonomics.md)）
- ❌ **Status bar 长期 accuracy 指标显示**（"我猜对了 7/10"）——模型对自己打分的怪味；先不暴露给用户。后续若用户反馈想看可加
- ❌ **LLM-as-judge 做 mismatch 判定**——MVP 用 normalized 字符串 contains，便宜可靠；语义判官留给 v5 if needed
- ❌ **预测结果发到 agent 主 transcript**——预测**仅**作为 TUI placeholder 存在，不写入会话历史；只在用户**采纳**后（按 Tab）才进入主流
- ❌ **Sliding-window 噪声过滤**（"最近 5 次错 3 次才触发校准"）——第一版每次 mismatch 都注入；如果实际数据显示模型频繁误报需要降噪再加
- ❌ **多候选预测**（top-N 让用户用数字键选）——第一版只 top-1；先证 top-1 不够用

## 3. 触发条件

预测**仅在 TUI 模式**触发。print 模式（`-p`）、`--rpc` 模式、`--no-save` 模式都不生成预测。

具体触发点：

- agent 主流 stream 自然结束（不是用户 Esc 取消的）
- 模型最后一轮以**纯文本输出结尾**（不是工具调用结尾——工具调用结尾意味着任务还没完，预测用户下一步没意义）
- 距离上一次预测调用 ≥ 配置阈值（默认 0s，可设为 5s 避免连续短回合时的浪费）

不触发的场景：

- 主流被用户 Esc 取消
- 主流结束时输入框**已经非空**——用户在边等边打，预测白费且会覆盖用户文字
- `~/.seek/config.json` 里 `suggest_reply: false`，或 `--no-suggest` 启动标志

## 4. 尾调用架构

### 4.1 总览

```
agent.Stream  ──end──►  predictor.Suggest(transcript)  ─async─►  tea.Msg(suggestionReady)
                                                                          │
                                                                          ▼
                                                                  Model.suggestedReply
                                                                          │
                                                                          ▼
                                                                  view.go render as placeholder
```

要点：

- **goroutine 启动**：stream-end 时 spawn 一个 goroutine 调 `predictor.Suggest(ctx, transcript)`
- **ctx**：用 `m.opts.Ctx` 派生 child ctx + 5s 超时；用户开始新一轮（按下任意 rune 键）时取消
- **返回值**：单条字符串（可能为空）
- **传回 UI**：via tea.Cmd → tea.Msg → Model.Update

### 4.2 Predictor 实现

新包 `internal/suggester`：

```go
package suggester

type Suggester struct {
    client *deepseek.Client
    model  string  // 默认 "deepseek-v4-flash"（或当前 session 主模型的 mini 变体）
    sysPrompt string  // 见 §4.3
}

// Suggest predicts the user's next message given the conversation
// transcript. Returns "" if the model refuses / errors / ctx canceled.
// Never returns an error — best-effort by design.
func (s *Suggester) Suggest(ctx context.Context, msgs []deepseek.Message) string {
    // Append a synthetic instruction at the END:
    //   role=user, content="<predict-next/>"
    // and call Chat with low max_tokens (80).
    ...
}
```

关键决定：

- **不复用主 model**：默认走 `deepseek-v4-flash`（便宜、快）。即便用户主流用 Pro 也用 Flash 预测
- **prefix cache 命中**：主流的 transcript 直接作为输入，前 N-1 条 message 应该全部 cache hit（system 不同会让 system 那一段错位，但 messages 部分可命中）
- **max_tokens 紧**：80 tokens 够说一句话，多了就是模型话痨
- **timeout 5s**：超过 5s 没回来就放弃，用户已经开始打字了
- **不进 transcript**：预测调用的请求 + 响应**绝不**写入 session JSONL 的 messages 列表

### 4.3 Prompt 设计

System prompt（独立于主 system；走 Flash 模型）：

```
You predict the user's next message in a coding-assistant conversation.

Output ONLY the predicted next message text, no quotes, no explanation,
no "I think the user would say…" preamble. 1 short sentence, ≤ 15 words.

If the prior assistant turn ended in a multiple-choice question
("[A] do X, [B] do Y"), predict the choice the user is most likely
to make + minimal expansion ("A" / "A 方案" / "用 A").

If the prior assistant turn ended ambiguously (no clear next step),
output an empty line.

Never assume facts not in the transcript.
```

User-side hint：在 transcript 末尾追加一条合成 user message: `<predict-next/>`。Flash 模型见到这个 tag 就生成预测。

### 4.4 TUI 集成

**Model 字段：**

```go
type Model struct {
    // ...
    suggestedReply       string  // pre-computed prediction; "" = none active
    suggestedReplyValid  bool    // false once user starts typing → suppresses render
}
```

**Render：** 当 `suggestedReply != ""` 且 `m.input.Value() == ""` 且 `suggestedReplyValid == true`，在 textarea **下方**渲染一行 muted 文案：

```
> _
  ↳ tab: 用 A 方案
```

不放在 textarea 内部的 placeholder 字段（textarea 已经被 "Reply…" 占了，且让 placeholder 和实际输入区分太重要）。

**键位：**

- **Tab**（input 空 + suggestedReply 非空时）：调用 `m.input.SetValue(suggestedReply)`；清空 `suggestedReply` 状态；**不发送**。用户随后可继续编辑或按 Enter
- **任何 rune 输入**：`suggestedReplyValid = false`，render 立即停止显示（不清空字符串本身，留给 stats）
- **Esc**：同 rune 输入——`suggestedReplyValid = false` 立即隐藏，但不清字符串（留给统计）
- **Enter 发送**：清空 `suggestedReply` + `Valid=false`（新一轮开始）

**键位优先级**（在 `update_key.go` 的 commandMenuOpen / modelPickerOpen / 等等之 **后**，主 switch 之前）：

```go
// Suggested-reply Tab accept — only when no menu/picker is consuming Tab.
if msg.Type == tea.KeyTab &&
   m.input.Value() == "" &&
   m.suggestedReply != "" &&
   m.suggestedReplyValid &&
   !m.commandMenuOpen && !m.modelPickerOpen && !m.pathPicker.open {
    m.input.SetValue(m.suggestedReply)
    m.suggestedReply = ""
    m.suggestedReplyValid = false
    return m, nil
}
```

### 4.5 数据持久化

Session JSONL 的 assistant message 新增一个可选字段，记录这一轮回合末尾产生的预测（成功生成时）：

```json
{
  "role": "assistant",
  "content": "...主流回答...",
  "ts": "...",
  "predicted_next": "用 A 方案"
}
```

**约束：** 这个字段**仅记录**，不影响后续 turn 的 `messages[]` 序列（session 解析层负责剥离）。Match 判定**不持久化**——每次构造下一轮 ChatRequest 时用 §4.6 的 `normalizedMatch()` 即时计算，这样信号永远跟最新代码一致，schema 也不需要 `predicted_next_match` 第二个字段。

**Schema 升级：** session JSONL 头部 `schema_version` 从 2 升 3。v2 reader 见到未知字段 `predicted_next` 应忽略不 panic（现有 `session.Load` 已经是这样实现的，确认即可）。

### 4.6 反向注入闭环（柱 D 核心）

构造下一轮 ChatRequest 时，**如果** 上一条 assistant message 有非空 `predicted_next`，**且** 当前 user message 跟它 `normalizedMatch == false`，则在 `messages[]` 里**最后一条 user message 之前**插入一条 synthetic system note：

```
messages = [
  {role: "system",   content: "<main sys prompt>"},
  ...prior turns...,
  {role: "assistant", content: "...上一轮回答..."},
  {role: "system",   content: "<calibration note>"},   ← 注入
  {role: "user",     content: "<当前用户输入>"},
]
```

**Calibration note 文案**（固定模板，便于模型识别）：

```
[calibration] Prior turn predicted the user would say: "<predicted_next>".
They actually said: "<actual_first_60_chars>".
Update your model of the user's intent for this turn.
```

**关键约束：**

- 这条 system note **不持久化** ——它是 ChatRequest 构造时的瞬时产物。session 重放时**重新计算** mismatch 并重新注入。单一真相是 `predicted_next` 字段 + 当前 user message
- 注入位置在 user message **之前**，不在它内部 ——这样模型把它看作上下文 hint 而非用户原话的一部分
- Prefix cache 影响：注入只发生在 mismatch 那一回合，前面所有 token 仍可缓存；只重算 calibration note + user message 两条 ——成本上限可控
- 用户可通过单一开关 `suggest_reply: false` 关停（同时禁用预测 + UI + 注入）

**伪代码：**

```go
// internal/agent (或 deepseek client 包装层)
func injectCalibrationIfNeeded(msgs []Message, userMsg Message) []Message {
    if len(msgs) == 0 {
        return append(msgs, userMsg)
    }
    last := msgs[len(msgs)-1]
    if last.Role != "assistant" || last.PredictedNext == "" {
        return append(msgs, userMsg)
    }
    if normalizedMatch(userMsg.Content, last.PredictedNext) {
        return append(msgs, userMsg)
    }
    note := Message{
        Role:    "system",
        Content: fmt.Sprintf("[calibration] Prior turn predicted the user would say: %q. They actually said: %q. Update your model of the user's intent for this turn.",
            last.PredictedNext, firstN(userMsg.Content, 60)),
    }
    return append(msgs, note, userMsg)
}

// normalizedMatch — small-N substring compare; case-insensitive,
// punctuation-stripped, prefix-first-30-chars.
func normalizedMatch(actual, predicted string) bool {
    a := strings.ToLower(strings.TrimSpace(stripPunct(actual)))
    p := strings.ToLower(strings.TrimSpace(stripPunct(predicted)))
    if len(p) == 0 || len(a) == 0 {
        return false
    }
    k := min(30, len(p))
    return strings.Contains(a, p[:k]) || strings.Contains(p, a[:min(30, len(a))])
}
```

**`normalizedMatch` 取双向 contains** ——既允许"用 A 方案"含"A"（预测短）也允许"A"含"用 A"（用户短）。30 char 截断防长预测尾部噪声参与匹配。

### 4.7 单一开关

```
~/.seek/config.json:
  "suggest_reply": true   ← 默认开

CLI:
  --no-suggest            ← 一刀切关闭（覆盖 config）
```

**关闭时：**

- §4.1 stream-end 钩子不 fire
- §4.4 Tab accept 分支跳过（输入框无 placeholder，Tab 走 textarea 原语义）
- §4.5 predicted_next 字段不写入 session
- §4.6 calibration 注入跳过

零认知负担：用户不需要分别记"我关了 UI 但还在记录 / 还在注入"。要么全开，要么全关。

## 5. 与现有系统集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/suggester`（新包） | `Suggester` + Suggest API | 小 |
| `pkg/agent` | stream-end 后触发 suggester；非阻塞 goroutine | 小 |
| `internal/tui/model.go` | 新增 `suggestedReply` / `suggestedReplyValid` 字段；新 `tea.Msg` 类型 `suggestionReadyMsg` | 小 |
| `internal/tui/update_key.go` | M9.5 之后的主 switch 之前加 Tab accept 分支 | 极小 |
| `internal/tui/view.go` | textarea 下方多一行 muted "↳ tab: …" | 小 |
| `internal/session` | message envelope 加可选 `PredictedNext` + `PredictedNextMatch` 字段；schema_version=3 | 中（要兼容 v2 读取） |
| `internal/config` | `suggest_reply: bool`（默认 true）+ `--no-suggest` flag | 极小 |
| `cmd/seek` | 启动时构造 Suggester、注入 agent options | 极小 |
| Session 重放 / `--resume` | suggester 不读 session，重放零影响 | 0 |
| Prefix cache | 主流 transcript 字节不变；预测调用走独立 system prompt（自己有自己的 cache 命中曲线） | 0 风险 |

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | LLM 主流自然结束后，TUI input 框下方出现一行 muted "↳ tab: <预测>" | 集成测试（fake DeepSeek backend 返回 fixed 预测） |
| 2 | input 空 + Tab → input 填入预测内容；**不发送**；suggestion 渲染消失 | 单元测试（handleKey + Model state） |
| 3 | 用户开始打字（任何 rune）→ suggestion 立即从渲染消失，input 不被覆盖 | 单元测试 |
| 4 | 用户主流被 Esc 取消 → 不生成预测 | 集成测试 |
| 5 | input 在 stream-end 时已非空 → 不生成预测、不渲染 | 集成测试 |
| 6 | 主 transcript 在 stream-end 后字节序列与 v3 一致——prefix cache 兼容性 | byte-diff 单元测试 |
| 7 | suggester ctx 5s 超时 → 返回 ""，UI 不渲染、不报错 | 单元测试（slow fake backend） |
| 8 | `--no-suggest` 启动 → suggester 不启动；零 API 调用；零持久化字段 | 集成测试 |
| 9 | session JSONL `predicted_next` 字段持久化；v2 reader 能忽略 v3 字段不 panic | session round-trip 测试 |
| 10 | `normalizedMatch` 检测正确：predicted="用 A 方案"，actual="好 A" → match=true；多 case 覆盖 | 单元测试 |
| 11 | 上一轮 `predicted_next="A 方案"` + 用户实际说 "用 B"（mismatch）→ 下一轮 ChatRequest 的 messages[] 在 user message 前**多一条**`[calibration]` system note | 单元测试（拦截 ChatRequest 构造） |
| 12 | 同上但用户说 "好 用 A"（match）→ 下一轮 messages[] **无** calibration note 注入 | 单元测试 |
| 13 | session 重启 `--resume` 后再发消息：calibration 注入根据 `predicted_next` + 当前消息**重新计算**（不读持久化的 match 字段，因为它不存在） | 集成测试 |
| 14 | 现有 TUI / agent 测试套件零回归 | 现有测试 |

## 7. 实现计划

### M10.0 — 柱 D 完整交付（~3 天，单 PR / 单 commit 序列）

| 子任务 | 估时 |
|---|---|
| `internal/suggester` 包 + 单元测试（Predictor + normalizedMatch） | 0.5 天 |
| agent stream-end 钩子 + tea.Msg 路由（async goroutine + 5s ctx 超时） | 0.5 天 |
| TUI Model 字段 + Tab 接受分支 + view 渲染"↳ tab: ..." muted 行 | 0.5 天 |
| Session schema_version=3 + `predicted_next` 可选字段 + v2 reader 兼容 | 0.5 天 |
| **反向注入闭环**：ChatRequest 构造器在 mismatch 时注入 calibration system note | 0.5 天 |
| `--no-suggest` flag + `suggest_reply` config + 单开关贯穿全链路的集成测试 | 0.5 天 |

ship 为 **v0.5.0**（v3 完结，v4 第一个柱）。

**为什么单 PR 不拆**：L1 数据采集 + L2 反向注入是同一个闭环的两半；拆开会让数据采集功能落地后没有闭环消费方，反向注入功能落地后没有数据源。一起 ship 是最小有用单元。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 预测调用增加 API 成本 | Flash 模型 + max_tokens=80 + cache hit on transcript → 单次 ~$0.0001 量级。`--no-suggest` 可关 |
| 预测频繁错 → 用户觉得 seek "瞎猜"，反而拉低体验 | Tab 接受是显式动作；不接受时建议会被 rune 输入吞掉。**只**在你主动按 Tab 时影响输入框 |
| 模型偶尔生成长篇大论填进输入框 | max_tokens=80 + 后处理只取第一行 + 长度 > 200 字符直接丢弃 |
| 预测调用阻塞 stream-end，下一轮提交延迟 | goroutine 完全异步；ctx 5s 超时；用户开始打字立刻取消。stream-end 本身不等 |
| prefix cache 命中率被预测的独立 system prompt 拉低 | 预测走自己的 cache 命中曲线，跟主流完全分离。最差情况就是 predict 路径自己 cold start，主流不受影响 |
| 用户在预测刚渲染时按 Tab 但实际想输入 tab 字符 | 文档说明 + Tab 接受仅在 input 为空时触发。input 非空时 Tab 走 textarea 原语义 |
| session schema 升级到 v3，旧版本 seek 读不了新 session | `session.Load` 已经有 schema_version gate；v2 reader 忽略 v3 字段不 panic（已有 fallback） |
| 模型遵守"empty line"约定的失败率 | 后处理：把 strip 后为空 / 只剩空白的当作 "" 处理；不渲染 |
| 用户终端 emoji / 中文字符宽度算错导致 muted 行错位 | bubbletea + lipgloss 已经支持 wide rune；用现有 `styleMuted` 不引新代码 |
| **Calibration 注入过度敏感**：mismatch 一发生就注入 system note → 模型可能 over-correct，对用户言听计从 | 第一版接受这个 trade-off；如果 dogfood 期间发现"模型变讨好"的趋势，加 sliding window（最近 5 个 ≥3 mismatch 才注入）；ship 时记 pitfall 跟踪 |
| **Calibration prefix cache 风险**：mismatch 那一回合需要重算 calibration note + user message 两条消息的 token | 估算：calibration note ≤ 120 token，user message 平均 ≤ 200 token → 单次 cache miss ≤ 320 tokens；按 Flash 单价可忽略。**前提是**注入位置严格在 user message 之前（不在 system prompt 头部，那样会全 transcript miss） |
| **重放偏差**：session 重启后老 turn 的 calibration 注入跟原始调用时不一致（normalizedMatch 算法变了 / `predicted_next` 内容微调了） | 接受这个偏差。Calibration 不持久化是设计选择——单一真相是 `predicted_next` + 当前用户消息；算法演进时所有历史都按新算法重算，这是 feature 不是 bug |
| **Calibration 文案被未来翻译团队改后行为变化** | 把模板字符串集中在 `internal/suggester/calibration.go` 顶部常量；改动需测试覆盖 |
| 用户隐私顾虑：自己消息片段进入 `[calibration]` 注入回给 LLM | 这本来就是要发给 LLM 的消息（user message 自己），calibration 只是元描述 "用户实际说了 X"，没新增隐私敏感面。**但**`--no-save` 模式下若 user 不希望任何旁路调用，`--no-suggest` 也应同时启用 |

## 9. 后续版本

- **v4.x（柱 D 内）**：sliding-window 噪声过滤（最近 5 次 ≥3 mismatch 才触发 calibration 注入）—— 仅在 v4.0 dogfood 数据显示"频繁误报 → 模型过度顺从"才上
- **v4.x**：semantic match via embedding（替换 normalizedMatch）—— 如果 contains 漏报率 > 30% 才考虑；要权衡每轮多一次 embedding 调用
- **v5**：status bar 长期 accuracy 指标（"🎯 7/10 命中"）—— v4 dogfood 显示用户想看才暴露
- **v5**：基于 prediction history 的 prompt 模板抽取（用户经常说 X 之后跟 Y → 提供 X+Y 的快捷键）
- **v5**：多候选预测（top-3，数字键 1/2/3 选）—— 先证 top-1 不够用
- **v5**：MCP 客户端集成让 server 端工具影响预测（"刚跑了 git diff" → 预测 "提交吧" 概率提升）
- **v5**：跨 session 的 user-style 摘要持久化到 soul.md，让 cold-start 的预测也带个性化
