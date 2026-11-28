### 模态的 Esc 只听在对话框上——点一下框内空白焦点就掉回 body，再按没反应；aria-modal 也圈不住 Tab
- **Saw**: W6-6 设置模态无头验收全绿（Esc 派发到对话框 → 关、焦点回「设置」行）。主会话 CDP 真实键鼠复测：点 tab 栏下方空白按 Esc，模态不动；从最后一个 tab 按 Tab，焦点落到背景侧栏按钮，连按 12 次到工作区表头（2026-11-28，c_ekuuof）。
- **Why**: 监听挂在 `#settings` 上，只收框内冒泡的 keydown；点到不可聚焦的空白处焦点回 `<body>`，之后的按键不经过对话框。`aria-modal="true"` 是给读屏的声明，对键盘 Tab 无约束。合成事件（`dispatchEvent`）不移动焦点，无头脚本测不出这两条。
- **Fix**: 键盘监听改挂 `document`——`openSettings` 挂、`closeSettings` 摘；Tab 落到首尾元素或框外时，焦点绕回框内另一端。无头补景 3c（blur 后从 body 冒泡 Esc、Tab/Shift+Tab 首尾绕回、关闭后 Esc 无副作用）；`assertNoResidentGlobalKeydown` 把「不许有全局 keydown」细化为「有挂必有摘」。
- **Lesson**: 模态键盘语义要用真实输入事件验（CDP `Input.dispatchKeyEvent` / `dispatchMouseEvent`）；合成事件永远从指定元素出发，不管焦点。`aria-modal` 是对辅助技术的承诺，不是实现——承诺了就得自己做焦点圈养。
- **Refs**: `internal/controlplane/webfrontend.html`（`modalKeys`），`scripts/web-theme-headless.mjs` 景 3c，`webfrontend_test.go`
