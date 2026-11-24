### 「模型沉默」不是提示的受众——TUI 每个工具调用都实时可见
- **Saw**: `silence-run` 规则（连续 N 回合跑工具不写字 → 提示模型汇报进展）在 390 份真实 session、35,644 次工具调用上回放：**272 个会话触发（70%）**，每会话 1.9 次。抽样看触发点，全是健康工作——连续 read 八个不同文件、连续 list_dir 八个目录、grep→read。
- **Why**: 前提本身是错的。TUI 对每个工具调用都打一行 `↳ read(…) → N bytes`（`internal/tui/update_agent.go` 的 ToolExecStart/ToolExecEnd），用户**全程看得见进展**。所以「用户不知道 agent 在干什么」这个受众不存在——没有受众的提示就是纯成本，和 3,871 次零效果的 bash advisory 同型。真正看不见的是**失败累积**：模型看过错误但几千 token 之前，用户看到的是终端上滚动过去的旧行——那才是两个受众都缺信息的形状。
- **Fix**: 删掉整条规则（常量、阈值、字段、分支、6 个测试全删），保留 retry-loop 与 failure-run。同一次回放的对照数据：`retry-loop` 1 会话 1 次、`failure-run` 6 会话 8 次——这才是「异常形状」应有的量级。删规则的理由写进 `internal/guidance` 包注释，回放脚本留在 `scripts/guidance-backtest/`（可重跑）。
- **Lesson**: 定「提示该不该发」的判据有两个，第二个才是真的：①这条信息模型缺不缺；②**这条信息人缺不缺**。只问①会写出「提醒模型你没说话」这种自我循环（人明明看得见）；只问②会写出「提醒用户 xx」这种越界。两条都成立才是可发——而这两条都能离线回放验证，不必先上线。
- **Refs**: `internal/guidance/guidance.go`（包注释的两规则论证）、`scripts/guidance-backtest/main.go`（回放器，含 fidelity 说明）、`internal/tui/update_agent.go:531`（每次工具调用都渲染，是「人看得见」的证据）、`docs/pitfalls/eval/success-path-hints-do-not-teach-…`
