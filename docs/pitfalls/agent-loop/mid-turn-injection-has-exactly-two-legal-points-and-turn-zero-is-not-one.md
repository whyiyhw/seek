### Mid-turn injection has exactly two legal insertion points, and turn 0 is not one of them
- **Saw**: 想给"流式期间用户插话"找一个入口，直觉是"任何一次 runTurn 之前都可以塞一条 user 消息"。实际只有两个位置成立，其余全部会 400。
- **Why**: (1) `assistant(tool_calls)` 必须紧跟它的全部 `tool` 结果消息——在工具批结果落史**之前**插入（例如 dispatch 循环里、或 `wg.Wait()` 之后但 append 之前）就破坏配对，之后每一轮 DeepSeek 都回 `tool_calls must be followed by tool messages`。(2) turn 0 是用户自己的 prompt，往它前面塞任何东西都等于替用户改写了本轮意图。(3) 反过来，「模型想收口」（`finish != "tool_calls"` 且无 AutoContinue）的结算点也必须排空一次，否则用户那句"等等，改做 X"会被静默丢弃——而静默丢弃比排队更糟，因为它看起来像已经送达。
- **Fix**: `pkg/agent/inject.go` 的 `commitInjected` 只在两处被调用：`for turn` 循环头（`turn > 0`）和结算点（`turn < MaxTurns-1`）。两处的共同前提都是"上一批工具结果已在 history 里"。
- **Lesson**: turn-based agent 里，"往对话里插一条消息"不是一个连续可用的能力，而是一组离散的合法点。新增任何注入机制前先把这些点枚举出来（写进注释），再让代码只在点上调用——不要给一个"随时可调"的 API，那保证了将来有人在缝里调用。
- **Refs**: `pkg/agent/inject.go`（两个调用点的注释）、`pkg/agent/agent.go:700` / `:997`，`internal/tui/inject_test.go`（fallback 断言）
