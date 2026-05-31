# seek Hooks — 生命周期拦截 / Lifecycle interception

Hooks 是 seek 的**生命周期拦截点**，让你在 agent 的关键事件（如 Prompt 前、Tool 使用前后、会话开始/结束）插入自定义逻辑。memory 子系统就是通过 hooks 实现的。

> Hooks define lifecycle interception points for the seek agent — inject custom logic before/after prompts, before/after tool use, and at session boundaries.

设计决策见 [`docs/prd/feature-shell-hooks.md`](prd/feature-shell-hooks.md)。

---

## 1. Hook 类型 / Hook kinds

两类 hooks，在类型层面区分，以便前缀缓存的安全性在编译时得到保证：

### 装饰器（Decorators）——可修改请求

| Hook | 触发时机 | 作用 |
|------|---------|------|
| `PrePromptHook` | 每轮 Prompt() 调用前，用户消息追加到历史之前 | 注入记忆、展开斜杠命令、改写用户文本 |
| `PreToolUseHook` | 每轮工具调用执行前 | 修改工具参数、注入上下文 |

> **⚠️ 输出必须确定**——DeepSeek 前缀缓存的 key 是 prompt 历史的精确字节序列，非确定性装饰器会静默破坏缓存命中率。

### 观察器（Observers）——只读

| Hook | 触发时机 | 作用 |
|------|---------|------|
| `PreTurn` | 每轮开始前 | 记录上下文 |
| `PostTurn` | 每轮结束后 | 分析结果、提炼记忆 |
| `PostToolUse` | 每个工具执行后 | 记录工具调用 |
| `SessionStart` | 会话开始时 | 初始化 |
| `SessionEnd` | 会话结束时 | 清理、持久化 |

---

## 2. 内置 Hook / Built-in hooks

seek 自带的 hook 实现：

- **Checkpoint 安全网**（`checkpointHook`）——通过 `OnPreTurn` + `OnSessionEnd` 在每轮开始前检查和清理 checkpoint 状态
- **记忆系统**（`internal/memory`）——通过 `PrePrompt` + `PostToolUse` + `SessionStart` + `SessionEnd` 实现 L/M/S 三层记忆

---

## 3. `seek hooks` CLI

```bash
# 列出当前信任的 hook 脚本
seek hooks list

# 检查哪些 hook 会对指定事件/工具触发（dry-run，不执行）
seek hooks check --event <event> [--tool <tool>]

# 查看或撤销项目级 hook 的信任状态
seek hooks trust [--reset[=<path>]]

# 审计所有 hook 调用记录
seek hooks audit
```

---

## 4. 编写自定义 Hook / Writing custom hooks

Hooks 通过 Go 接口实现。一个结构体可以实现多个接口；`Registry.Register` 自动将其分发到它满足的所有 slot。

```go
// 一个同时实现 PrePrompt + PostTurn 的 hook
type myHook struct{}

func (h *myHook) OnPrePrompt(ctx context.Context, in hooks.PrePromptIn) (hooks.PrePromptOut, error) {
    // in.UserText — 用户原始文本
    // in.History  — 当前历史（只读快照）
    return hooks.PrePromptOut{
        UserText: in.UserText + "\n[注入的上下文]",
    }, nil
}

func (h *myHook) OnPostTurn(ctx context.Context, ev hooks.PostTurnEvent) {
    // ev.TurnResult — 本轮结果
    // 分析、提炼、持久化...
}
```

### 注册

```go
registry := hooks.NewRegistry()
registry.Register(&myHook{})
// Registry.Register 自动检测实现的接口，分发到所有 slot
```

---

## 5. 设计要点 / Design notes

- **确定性优先**——装饰器（PrePrompt / PreToolUse）的输出必须确定，否则破坏前缀缓存
- **零侵入观察器**——Observer 接口的签名本身强制它们不能修改对话、请求或控制流
- **单一结构多接口**——一个结构体可以实现多个 hook 接口，Registry 自动分发
- **hook 脚本**——对于 shell-level 的钩子（如 pre-commit 风格的 checks），见 `seek hooks` CLI
