# seek Context Budget — 上下文窗口预算 / Context window budget

seek 内置了**上下文窗口预算系统**，在你接近模型限制时提前给出警告——避免"写到一半突然报 token 超限"的中断。

> seek has a built-in context budget system that warns you before hitting model context limits — preventing mid-turn token overflow.

---

## 1. 模型上下文限制 / Context limits

seek 维护了一份各模型的上下文窗口大小硬编码表（来源：各厂商文档）。未知模型使用 128K 的安全默认值。

| 模型 | 上下文窗口 |
|------|-----------|
| `deepseek-v4-flash` | 1,000,000 |
| `deepseek-v4-pro` | 1,000,000 |
| `deepseek-chat`（V4 别名） | 1,000,000 |
| `deepseek-reasoner`（V4 别名） | 1,000,000 |
| `claude-3-5-sonnet-20241022` | 200,000 |
| `claude-sonnet-4-20250514` | 200,000 |
| `gpt-4o` | 128,000 |
| `gpt-4o-mini` | 128,000 |
| `gemini-2.0-flash` | 1,000,000 |
| `gemini-1.5-pro` | 2,000,000 |
| 未知模型 | 128,000（保守默认值） |

> 表格是硬编码的，**不自动探测**。vendor 发布新限制时手动更新——过时的表安全地少报容量（fallback 到保守值），比运行时探测引入的 OSC-11 / TTY 问题要好。

---

## 2. 预算警告 / Budget warnings

当上下文使用量接近模型限制时，TUI 状态栏会显示警告：

- **琥珀色（amber）**——使用量超过 60%（`WarnFraction = 0.60`）
- **红色 + `/compact soon` 提示**——使用量超过 75%（`CriticalFraction = 0.75`）

> Warning thresholds are hard-coded in `internal/budget/budget.go` (`WarnFraction = 0.60`, `CriticalFraction = 0.75`). They were lowered from the former 0.80/0.95 so the `/compact` nudge fires early enough to be actionable — with a 1 M-token model, 95% would be 950 K tokens, by which point the summary call itself is expensive. No picker is shown; no conversation is interrupted. Budget is recalculated at the start of each new turn.

警告阈值是固定的（硬编码），不会弹出 picker 或打断对话。预算仅在**每个新回合开始时**重新计算。

---

## 3. 如何查看当前使用量

```bash
# TUI 状态栏（右上角）
# 显示当前上下文占比百分比，如：
#   ctx 42%          ← 安全区，显示为低调灰色
#   ⚠ ctx 63%        ← 超过 60%，琥珀色警告
#   ⚠ ctx 77% — /compact soon  ← 超过 75%，红色加粗
#
# The label format is "ctx N%" (integer percent), driven by
# internal/budget.Fraction → internal/tui/statusbar.go:formatBudget.
```

---

## 4. 预算耗尽后的行为

当达到模型上下文限制时：

1. **seek 不会静默截断历史**——截断会破坏前缀缓存
2. 建议使用**会话管理**功能（`/branch` 分叉 / `/compact` 压缩）来缩小上下文
3. 也可以使用 `--resume` 在新会话中继续，旧会话作为参考

参见 [guide-sessions.md](guide-sessions.md) 了解如何管理会话上下文。

---

## 5. 设计要点 / Design notes

- **硬编码优于自动探测**——vendor 文档可能变化，但过时的表安全地少报容量，远好于运行时探测引入的 TTY 类问题
- **没有动态预算分配**——预算系统是**被动的**（仅警告），不由 agent 主动管理
- **预算与 token 计数**——预算基于 LLM API 返回的 token 使用量（`usage` 字段），不是本地估算
