# Edit 调用前强制 read（软性→结构性）

**目标**：杜绝模型在 `edit` 调用前跳过 `read` 凭记忆猜测 `old_string`，导致匹配失败。

## 现状

system prompt、AGENTS.md 均有 "read before edit" 规则；`workflowReminder` 常量也追加了提醒。但这些是软性提示——模型在"以为记住内容"时仍可能跳过。

## 当前缓解

`pkg/agent/agent.go` 的 `workflowReminder` 已含：

> Before calling edit, read the target lines first to capture exact whitespace
> — never guess indent from memory.

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

不改调度逻辑，只改进 `edit` 失败时的错误信息。匹配失败时不止返回 "old_string not found"，还：

- 展示最近一次该文件 `read` 返回的内容快照
- 用 diff 对比 `old_string` 和文件实际内容

让模型能更快自修复。

**优点**：零侵入调度，改进错误恢复速度。

**缺点**：不阻止犯错，只降低犯错成本。read 快照需要跨工具传递，和方案 1 一样需要共享状态。

## 触发条件

如果 edit 匹配失败频率明显上升，优先实施方案 1。

## 相关文件

- `pkg/agent/agent.go` — `workflowReminder` 常量
- `internal/tools/edit/` — edit 工具实现
- `internal/tools/read/` — read 工具实现
- `internal/tools/registry.go` — 工具注册与共享状态
- `pkg/agent/agent.go` — agent 主循环（方案 2 涉及）
