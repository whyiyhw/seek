### 清了草稿不同步作废 menuCtx，同款草稿再打一次弹层不再开
- **Saw**: 目录浏览器「选这个目录」清空输入框并发送 /bind 后，用户再次输入完全相同的 `/bind ` 草稿，候选弹层不再出现（换一个字符再删回来就能开）。
- **Why**: `updateMenu` 用「上下文未变」短路防止 Esc 关层后被同一按键的 keyup 立刻复弹，而 `closeMenu` **特意不重置** `menuCtx`（保的就是这个闸）；程序化清空 `input.value` 不触发 input 事件，menuCtx 停留在已关弹层的上下文上——同款草稿重打时 ctx 相等，被误判成「弹层已开」。
- **Fix**: 凡是代码清空/改写草稿的地方，同帧手动 `menuCtx = ''` 作废上下文闸（c_heffj3 的选这个目录 handler，webfrontend.html）；注释写明与 Esc 闸的两难拆法。
- **Lesson**: ctx 相等短路是「关了的层装作还开着」的隐式前提——任何绕过真实键盘输入改草稿的路径，都要同步作废这个前提；下次给弹层加程序化改草稿的入口时先想到它。
- **Refs**: `internal/controlplane/webfrontend.html`（`menuCtx` 注释、`pickBtn` click handler）；发现于 c_heffj3 无头 E2E 景 6。
