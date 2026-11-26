### SubmitEvent.submitter 对回车的隐式提交也指按钮——「按钮点击 vs 回车」分岔只能放在 click 层
- **Saw**: c_5m3fm6 生成中停止键以 `ev.submitter === send` 在 submit 处理器区分点发送键（转 /cancel）与回车提交（照常发），两头都命中——回车隐式提交的 submitter 同样是默认提交键（#send 唯一 submit 键）。
- **Why**: HTML 隐式提交（输入框回车）走默认提交键的激活行为，`SubmitEvent.submitter` 如实报告；submit 到达时物理来源已丢失。
- **Fix**: 分岔上移到 `send.addEventListener('click', …)`——click 先于 submit，生成中 `preventDefault()` 吞掉默认提交后只发 /cancel，submit 处理器保持原路（回车照常排队）。
- **Lesson**: 同一键按物理点击 vs 键盘提交变语义时，分岔只能在 click 层做（click 先于默认提交、可 preventDefault），submitter 区分不了来源。另：无头驱动替页面裸 `fetch('/cancel')` 不经 `sendText`，绕开客户端 /cancel 武装态——测指令语义要用 `evalJS("sendText('/cancel', false)")`。
- **Refs**: `internal/controlplane/webfrontend.html` 停止键段；`scripts/web-stream-headless.mjs` 景 3；`scripts/web-stop-headless.mjs`。
