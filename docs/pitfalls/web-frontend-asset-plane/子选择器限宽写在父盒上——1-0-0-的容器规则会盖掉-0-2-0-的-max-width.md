### 子选择器限宽写在父盒上——(1,0,0) 的容器规则会盖掉 (0,2,0) 的 max-width

- **Saw**: W6-3 返工第 1 轮把用户气泡的 `max-width: 80%` 写在 `.msg.user`（消息盒）上，`webfrontend_test.go` 那条「`max-width: 768px` 在场」也照旧绿；返工第 2 轮重验对着截图 06/13/20 量边才发现长气泡仍撑满整条 768 阅读列——80% 从没生效过，而 768px 之上还有 `#list > *, #sess-msgs > * { width: 100%; max-width: 768px; }`。
- **Why**: `#list > *` 的 ID 在特异性第一列，是 (1,0,0)；`.msg.user` 是 (0,2,0)。container 规则给同一条消息盒 width:100% 与 max-width:768px，父盒上的 80% 逐属性输给它，「白写但不是错」——页面照常渲染，只有量边能看出来。移动端 `.msg.user { max-width: 94% }` 同因。
- **Fix**: 上限落到真正要被限宽的元素上——`.msg.user .bub { max-width: 80% }`（(0,3,0) 仍输给容器的 width，但气泡不是容器的直接子元素，容器规则根本不命中它）；`.msg.user` 保持 flex 列、只负责 align-items:flex-end；移动端改 `.msg.user .bub { max-width: 94% }`。`TestWeb_PageChatFlowW63` 加三钉：`.msg.user .bub` 规则里含 `max-width: 80%`、`.msg.user` 上不再有 `max-width: 80%`/`94%` 的父盒写法、移动端气泡规则在场（c_ovly6d 返工 2）。
- **Lesson**: 「限宽」写在形状盒上还是写在视觉元素上，先看容器规则的特异性——ID 前缀的 `#list > *` / `#sess-msgs > *` 会把任何一档类选择器之后的宽度声明吞掉。正断言「页面上有 768px」证明不了气泡受 80% 约束；断言要钉到**承载该值的选择器**上，并且配一条「父盒别写」的负断言。
- **Refs**: `internal/controlplane/webfrontend.html`（对话流规则段）、`TestWeb_PageChatFlowW63`、同目录 `新类名是旧类名加前缀——旧的单类规则会静默命中新元素.md`
