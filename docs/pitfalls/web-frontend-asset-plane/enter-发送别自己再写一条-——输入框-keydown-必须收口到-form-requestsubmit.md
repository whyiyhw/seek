### Enter 发送别自己再写一条——输入框 keydown 必须收口到 form.requestSubmit()
- **Saw**: c_7lsngm 的输入增强在 `#input` keydown 里直接 `sendText(input.value.trim(), true)`；合 main 时 W2.2/W5 附件闸（`pendingAttachments()` + 失败项提示）落在表单 submit 处理器，同一次 Enter 走另一条路：附件不被等、失败项不提示、`/xxx` 后面拼上 `@<path>`。冲突块两侧都在，挑任一侧都能编译过键盘断言。
- **Why**: 「Enter 发送」在键事件与表单 submit 两层都成立，谁先写谁固化；键盘版天然早，业务闸天然在表单（按钮回车共用），两实现各自测试绿，只有附件+回车同用才分叉。
- **Fix**: keydown 只做 `ev.preventDefault()` + `form.requestSubmit()`（弹层开着先 `pick()`），提交逻辑只留 submit 处理器一份；测试钉 `form.requestSubmit()` 与 `sendText(typed, true, ready)`。
- **Lesson**: 「按键直接调业务函数」先问有无表单/按钮同款路径，有就降级成派发同一事件（`requestSubmit()`/`click()`），闸只存在一处；冲突里两侧都在的 handler 是双路径信号。
- **Refs**: `internal/controlplane/webfrontend.html` `form.addEventListener('submit')` 与 `input.addEventListener('keydown')`；`internal/controlplane/webfrontend_test.go` `TestWeb_PageInputEnhancements`；`scripts/web-attach-headless.mjs`、`scripts/web-input-headless.mjs`。
