### 子选择器限宽写在父盒上——(1,0,0) 的容器规则会盖掉 (0,2,0) 的 max-width

- **Saw**: W6-3 返工第 1 轮把用户气泡 `max-width: 80%` 写在 `.msg.user`（消息盒）上，`webfrontend_test.go` 那条「`max-width: 768px` 在场」照旧绿；第 2 轮对截图 06/13/20 量边才发现长气泡仍撑满 768 阅读列——80% 从未生效，768px 之上还有 `#list > *, #sess-msgs > * { width: 100%; max-width: 768px; }`。
- **Why**: `#list > *` 的 ID 在特异性第一列 (1,0,0)，`.msg.user` 是 (0,2,0)；container 规则给同一条消息盒 width:100% 与 max-width:768px，父盒的 80% 逐属性输给它——白写但不是错，页面照常渲染，只有量边看得出。移动端 `.msg.user { max-width: 94% }` 同因。
- **Fix**: 上限落到真正被限宽的元素：`.msg.user .bub { max-width: 80% }`（(0,3,0) 仍输容器的 width，但气泡非直接子元素，规则不命中）；`.msg.user` 保持 flex 列、只管 align-items:flex-end；移动端改 `.msg.user .bub { max-width: 94% }`。`TestWeb_PageChatFlowW63` 三钉：`.msg.user .bub` 含 `max-width: 80%`、`.msg.user` 上不再有 `max-width: 80%`/`94%` 父盒写法、移动端气泡规则在场（c_ovly6d 返工 2）。
- **Lesson**: 限宽写形状盒还是视觉元素，看容器规则特异性——ID 前缀的 `#list > *` / `#sess-msgs > *` 吞得掉任何一档类选择器的宽度声明。断言「页面上有 768px」证明不了气泡受 80% 约束；要钉到**承载该值的选择器**。
- **Refs**: `internal/controlplane/webfrontend.html`（对话流规则段）、`TestWeb_PageChatFlowW63`、同目录 `新类名是旧类名加前缀——旧的单类规则会静默命中新元素.md`
