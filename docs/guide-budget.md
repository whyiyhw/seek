# seek Context Budget — 上下文窗口预算 / Context window budget

seek 内置了**上下文窗口预算系统**，在你接近模型限制时提前给出警告——避免"写到一半突然报 token 超限"的中断。

> seek has a built-in context budget system that warns you before hitting model context limits — preventing mid-turn token overflow.

---

## 1. 模型上下文限制 / Context limits

seek 维护了一份各模型的上下文窗口大小硬编码表（来源：各厂商文档）。未知模型使用 128K 的安全默认值。

| 模型 | 上下文窗口 |
|------|-----------|
| `deepseek-flash` | 1,000,000 |
| 旧名（`deepseek-v4-*`，已退役） | 128,000（按未知模型的保守默认值） |
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

## 2.5 自动压缩 / Auto-compact

到点自动执行 `/compact`（同一套 fork-snapshot 语义：压缩前的完整历史留在磁盘上，新会话带着摘要继续）。**默认关**——自动改写对话历史属于行为变更，所以必须显式开启。**仅交互式 TUI 车道**生效（`-p` / `-json` / ACP 车道没有回合末的空闲判定点，仍靠手动）。

```json
{
  "auto_compact": { "at_tokens": 350000 }
}
```

两种写法，回答的是两个不同的问题：

| 字段 | 含义 |
|------|------|
| `at_tokens` | **与模型无关**的绝对阈值。`350000` 就是 35 万 token，换车道不跟着变；与 `at_fraction` 同时设置时**它优先** |
| `at_fraction` | 按**当前模型窗口**的比例。`0.35` 在 1M 窗口上就是 35 万；`/model` 换到小窗口模型时触发点跟着下移 |

- 两者都缺省 = 关。低于 1000 token 的值一律按关处理（摘要调用本身比重放那点历史还贵）。
- `at_fraction` **不做上限钳制**：填 `1.5` 只是永远不会触发，不会造成压缩风暴。
- 临时覆盖（一次启动）：`SEEK_AUTO_COMPACT=0.35` / `=350000` / `=off`。带小数点的按比例，纯整数按 token 数，空值或无法解析时沿用配置文件的值。
- 触发时机：**回合自然结束、且真的空闲**时检查。排队消息、待发送的 steer、`/goal` 循环进行中、以及用户 Esc 取消的回合都不触发——那些状态下已经有另一个回合在路上，它自己的回合结束会再判一次。
- 压缩中状态栏的 ctx% 段变成 `! ctx N% — auto-compact`（红），warn 段落在触发点的 80% 处；配置了自动压缩时百分比仍按**模型窗口**算，触发点只决定颜色。
- 一次会话里压缩失败不会关闭该功能：失败会打印一行错误，下一个回合结束继续按阈值判。

---

## 3. 如何查看当前使用量

```bash
# TUI 状态栏（右上角）
# 显示当前上下文占比百分比，如：
#   ctx 42%          ← 安全区，显示为低调灰色
#   ⚠ ctx 63%        ← 超过 60%，琥珀色警告
#   ⚠ ctx 77% — /compact soon  ← 超过 75%，红色加粗
#   ⚠ ctx 35% — auto-compact   ← 配了 auto_compact 时，到点变红
#
# The label format is "ctx N%" (integer percent), driven by
# internal/budget.Fraction → internal/tui/statusbar.go:formatBudget.
```

---

## 4. 预算耗尽后的行为

当达到模型上下文限制时：

1. **seek 不会静默截断历史**——截断会破坏前缀缓存
2. 建议使用**会话管理**功能（`/branch` 分叉 / `/compact` 压缩）来缩小上下文；配置了 [`auto_compact`](#25-自动压缩--auto-compact) 则到点自动压
3. 也可以使用 `--resume` 在新会话中继续，旧会话作为参考

参见 [guide-sessions.md](guide-sessions.md) 了解如何管理会话上下文。

---

## 5. 设计要点 / Design notes

- **硬编码优于自动探测**——vendor 文档可能变化，但过时的表安全地少报容量，远好于运行时探测引入的 TTY 类问题
- **没有动态预算分配**——预算系统是**被动的**（仅警告），不由 agent 主动管理
- **预算与 token 计数**——预算基于 LLM API 返回的 token 使用量（`usage` 字段），不是本地估算
