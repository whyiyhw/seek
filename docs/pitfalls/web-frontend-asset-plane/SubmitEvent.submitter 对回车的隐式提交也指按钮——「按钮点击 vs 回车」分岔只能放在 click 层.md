### SubmitEvent.submitter 对回车的隐式提交也指按钮——「按钮点击 vs 回车」分岔只能放在 click 层
- **Saw**: c_5m3fm6 做生成中停止键：想用 `ev.submitter === send` 在 submit 处理器里区分「点了发送键」（应转 /cancel）与「回车提交」（应照常发消息），两头都命中——回车隐式提交的 submitter 同样是表单的默认提交键（#send 是唯一 submit 键）。
- **Why**: HTML 规范里隐式提交（输入框回车）就走默认提交键的激活行为，`SubmitEvent.submitter` 如实报告它；submit 事件到达时物理来源已丢失。
- **Fix**: 分岔上移到 `send.addEventListener('click', …)`——click 先于 submit 触发，生成中 `preventDefault()` 吞掉默认提交动作后只发 /cancel，submit 处理器保持原路（回车带草稿照常排队，既有语义不收窄）。
- **Lesson**: 同一键要按「物理点击 vs 键盘提交」变语义时，分岔只能在 click 层做（click 事件先于默认提交动作、可 preventDefault）；submitter 区分不了来源。另：无头驱动里替页面裸 `fetch('/msg', …/'/cancel')` 不经过 `sendText`，会绕开依赖「本页发过 /cancel」的客户端武装态——测指令语义要用页面自己的函数（`evalJS("sendText('/cancel', false)")`）。
- **Refs**: `internal/controlplane/webfrontend.html` 停止键段（c_5m3fm6）；`scripts/web-stream-headless.mjs` 景 3；`scripts/web-stop-headless.mjs`。
## Skill stats / --idle
