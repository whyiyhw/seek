### 「模型沉默」不是提示的受众——TUI 每个工具调用都实时可见
- **Saw**: `silence-run` 规则（连续 N 回合跑工具不写字 → 提示汇报进展）在 390 份真实 session、35,644 次工具调用上回放：272 会话触发（70%），每会话 1.9 次；抽样触发点全是健康工作（连续 read 八个文件、grep→read）。
- **Why**: TUI 每个工具调用都打一行 `↳ read(…) → N bytes`（`update_agent.go` 的 ToolExecStart/ToolExecEnd），用户全程可见进展——「用户不知道 agent 在干什么」的受众不存在，与 3,871 次零效果的 bash advisory 同型。
- **Fix**: 删掉整条规则（常量、阈值、字段、分支、6 个测试），保留 retry-loop 与 failure-run（对照：retry-loop 1 会话 1 次、failure-run 6 会话 8 次——异常形状的量级）。理由写进 `internal/guidance` 包注释，回放脚本留 `scripts/guidance-backtest/`。
- **Lesson**: 定提示该不该发的判据两条：①模型缺不缺这条信息；②人缺不缺。只问①写出自我循环（人明明看得见），只问②越界；两条都成立才可发，且都能离线回放验证。
- **Refs**: `internal/guidance/guidance.go`、`scripts/guidance-backtest/main.go`、`internal/tui/update_agent.go:531`、`docs/pitfalls/eval/success-path-hints-do-not-teach-…`
