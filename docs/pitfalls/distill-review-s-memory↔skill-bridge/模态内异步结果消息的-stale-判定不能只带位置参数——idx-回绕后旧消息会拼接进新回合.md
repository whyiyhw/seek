### 模态内异步结果消息的 stale 判定不能只带位置参数——idx 回绕后旧消息会拼接进新回合
- **Saw**: ⚡ review 的 `s` 转换是异步 tea.Cmd；第一版守卫 `msg.idx != m.distillIdx` 即丢。q 中途退出后再开 review（idx 从 0 重来），迟到的旧结果 idx 同为 0，把上一轮候选的预览态拼进新一轮第一个候选。
- **Why**: async Cmd 与模态生命周期解耦，idx 是候选位置不是轮次标识——review 重开让位置回绕，位置相等 ≠ 同一轮。
- **Fix**: 卡 `c_2lbcck`（bridge M-B2）：守卫三元组 `!distillReviewOpen || !distillConverting || msg.idx != m.distillConvertIdx`；`distillConverting` 在 exitDistillReview 与结果落地两路清零，充当世代位。`TestDistillConvert_QDuringConvertDropsLateResult` 钉死回绕。
- **Lesson**: 模态异步结果消息除位置外还要带「该次调用仍在被等待」的状态位，并在模态 teardown 清零；只用位置判 stale，reopening 即失效。
- **Refs**: `internal/tui/distill.go` `handleDistillConvertDone`/`exitDistillReview`，`internal/tui/distill_convert_test.go`。
