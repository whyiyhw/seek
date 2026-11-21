### 模态内异步结果消息的 stale 判定不能只带位置参数——idx 回绕后旧消息会拼接进新回合
- **Saw**: ⚡ review 的 `s` 转换是异步 tea.Cmd；第一版守卫写的是 `msg.idx != m.distillIdx` 就丢弃。按 q 中途退出后再开新 review（idx 从 0 重新开始），迟到的旧结果 idx 同为 0，会把「上一轮候选的预览态」拼接进新一轮的第一个候选。
- **Why**: bubbletea 的 async Cmd 与模态生命周期解耦，idx 是候选位置、不是轮次标识；review 关闭再重开让位置回绕，位置相等 ≠ 同一轮。
- **Fix**: 卡 `c_2lbcck`（bridge M-B2）：守卫三元组 `!distillReviewOpen || !distillConverting || msg.idx != m.distillConvertIdx`。`distillConverting` 在 exitDistillReview、结果落地（成功/错误行）两条路径都会清零，天然充当「本轮转换仍在被等待」的世代位。`TestDistillConvert_QDuringConvertDropsLateResult` 钉死回绕场景。
- **Lesson**: 模态里的异步结果消息，除了位置还要带「该次调用仍在被等待」的状态位，并在模态 teardown 里清零；只用位置判 stale 的守卫， reopening 即失效。
- **Refs**: `internal/tui/distill.go` `handleDistillConvertDone`/`exitDistillReview`，`internal/tui/distill_convert_test.go`。
