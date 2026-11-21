### Enter 发送别自己再写一条——输入框 keydown 必须收口到 form.requestSubmit()
- **Saw**: c_7lsngm 的输入增强在 `#input` 的 keydown 里直接 `sendText(input.value.trim(), true)`；本卡分支做成时页面表单只有那一句 `ev.preventDefault(); await sendText(...)`。合 main 时 W2.2/W5 的附件闸（`pendingAttachments()` + 「指令不吃附件」+ 失败项提示）落在**表单 submit 处理器**里，于是同一次 Enter 走的是**另一条更薄的路**：附件永不被等、失败项不提示、`/xxx` 后面偷偷拼上 `@<path>`。冲突块里两侧都在（HEAD 的 keydown 版 / main 的 submit 版），挑任一侧都能编译、能通过本卡自己的键盘断言。
- **Why**: 「Enter 发送」在两个层面都成立——输入框的键事件、表单的提交事件——谁先写谁固化。键盘驱动的那条天然更早（输入框是触发点），而业务闸天然落在表单（按钮与回车共用）。两份实现在各自的测试里都绿，只有把附件和回车一起用时才分叉。
- **Fix**: keydown 里只做 `ev.preventDefault()` + `form.requestSubmit()`（弹层开着时先 `pick()`），提交逻辑只留 submit 处理器一份；测试同步钉住 `form.requestSubmit()` 与 `sendText(typed, true, ready)`，而不是钉旧的 `sendText(input.value.trim(), true)`。
- **Lesson**: 任何「按键直接调业务函数」的实做都该先问一句「这个动作有没有表单/按钮的同款路径」——有就把按键降级成**派发同一个事件**（`requestSubmit()` / `click()`），让闸只存在一处；冲突解决时两侧都在的 handler 是双路径的味道信号。
- **Refs**: `internal/controlplane/webfrontend.html` `form.addEventListener('submit')` 与 `input.addEventListener('keydown')`；`internal/controlplane/webfrontend_test.go` `TestWeb_PageInputEnhancements`；`scripts/web-attach-headless.mjs`（附件闸验收）、`scripts/web-input-headless.mjs`（键盘验收）。
