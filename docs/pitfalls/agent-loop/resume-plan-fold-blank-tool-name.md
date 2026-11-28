### Resume 后 plan 状态折叠全坏——落盘 tool 消息没有 name 字段
- **Saw**: eval v2 E3（plan-e2e）首跑 resume 臂全红：`plan(complete,2)` 恒答 `no active plan`，TUI 状态栏停在 fresh 结束时的 `1/2`，而同路径单测 `reconstruct_test.go` 全绿。
- **Why**: `plan.ReconstructFromTranscript` 按 `m.Name == "propose"` 匹配 tool 结果，但落盘 JSONL 的 tool 消息只有 `content`+`tool_call_id`（`Message.Name` 是 `omitempty`，wire 上名字只在 assistant 的 tool_call 里）；单测 fixture 手工填了 `Name`，造出 wire 上不存在的形状。
- **Fix**: reconstruct 加 `toolNameAt`：Name 空时按 `tool_call_id` 回扫 assistant tool_call 配对取名；补回归测试 `TestReconstruct_PersistedWireShapeNoToolName`（Name 置空），plan-e2e eval case 同 commit 常驻。
- **Lesson**: fixture 手工填 wire 上不存在的字段就是给测试造特权输入——断言依赖消息字段前先 dump 一行真实落盘数据对形状；凡「wire 上 A 靠 id 配对 B」，读 transcript 的每一方（reconstruct、判分脚本、fake endpoint）都要重做同一配对。
- **Refs**: `internal/tools/plan/reconstruct.go`、`eval/tools/plan-e2e-judge.py`（`call_name`）、`eval/cases/plan-e2e/README.md`
