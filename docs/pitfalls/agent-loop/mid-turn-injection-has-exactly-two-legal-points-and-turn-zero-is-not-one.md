### Mid-turn injection has exactly two legal insertion points, and turn 0 is not one of them
- **Saw**: 想给"流式期间用户插话"找入口，直觉是"任何一次 runTurn 之前都可以塞一条 user 消息"，实际只有两个位置成立，其余全部 400。
- **Why**: 在工具批结果落史**之前**插入会破坏 `assistant(tool_calls)`↔`tool` 配对，之后每轮 DeepSeek 都回 `tool_calls must be followed by tool messages`；turn 0 是用户自己的 prompt，往它前面塞等于替用户改写本轮意图；「模型想收口」（`finish != "tool_calls"` 且无 AutoContinue）的结算点也必须排空一次，否则用户插话被静默丢弃——比排队更糟。
- **Fix**: `pkg/agent/inject.go` 的 `commitInjected` 只在两处被调用：`for turn` 循环头（`turn > 0`）和结算点（`turn < MaxTurns-1`），共同前提是上一批工具结果已在 history 里。
- **Lesson**: turn-based agent 里"往对话里插一条消息"是一组离散合法点，不是连续能力——新增注入机制前先枚举这些点（写进注释），不要给"随时可调"的 API（会有人在缝里调用）。
- **Refs**: `pkg/agent/inject.go`（两个调用点的注释）、`pkg/agent/agent.go:700` / `:997`，`internal/tui/inject_test.go`（fallback 断言）
