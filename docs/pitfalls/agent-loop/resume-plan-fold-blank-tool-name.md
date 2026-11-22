### Resume 后 plan 状态折叠全坏——落盘 tool 消息没有 name 字段
- **Saw**: eval v2 E3（plan-e2e）首跑 resume 臂全红：`plan(complete,2)` 恒答 `no active plan`，TUI 状态栏停在 fresh 结束时的 `1/2`；同路径单测 `reconstruct_test.go` 全绿。用户侧表现即 `-resume` 后模型重新 propose 或直接裸干，已批准计划的进度从不恢复。
- **Why**: `plan.ReconstructFromTranscript` 用 `m.Name == "propose"` 匹配 tool 结果，但落盘 JSONL 的 tool 消息只有 `content`+`tool_call_id`（`Message.Name` 是 `omitempty`，agent 层追加 tool 结果时不填；wire 上名字只在 assistant 的 tool_call 里）。单测 fixture 手工填了 `Name`，造出了 wire 上不存在的形状——常年假绿。
- **Fix**: reconstruct 加 `toolNameAt`：Name 空时按 `tool_call_id` 回扫 assistant tool_call 配对取名（与 OpenAI wire 的配对规则一致）；补现实形状回归测试 `TestReconstruct_PersistedWireShapeNoToolName`（tool 消息 Name 置空）。同 commit 落地 plan-e2e eval case 常驻防回归。
- **Lesson**: fixture 手工填一个 wire 上不存在的字段就是给测试造特权输入——凡断言依赖消息字段，先 dump 一行真实落盘数据对形状。凡「wire 上 A 靠 id 配对 B」的约定，读 transcript 的每一方（产品 reconstruct、判分脚本、fake endpoint）都要重做同一配对，谁偷懒直接读字段谁假绿。
- **Refs**: `internal/tools/plan/reconstruct.go`（`toolNameAt`）、`eval/tools/plan-e2e-judge.py`（`call_name` 同款配对）、`eval/cases/plan-e2e/README.md`（首跑即抓到的记录）
