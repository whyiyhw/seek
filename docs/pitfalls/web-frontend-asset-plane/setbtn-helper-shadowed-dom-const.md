### 图标 helper `setBtn` 与页面既有元素常量同名，整页脚本 SyntaxError，静态钉子照绿

- **Saw**: 页面只有 `<head>` 主题 bootstrap 生效，所有按钮点击无反应；`go test ./internal/controlplane/` 的 `TestWeb_Page*` 全绿——它们只 grep 页面字符串，不执行 JS。
- **Why**: 图标改造新增 `function setBtn(btn, id, text)`，页面下方早已有 `const setBtn = document.getElementById('set-btn')`。函数声明与 `const` 同名是顶层 SyntaxError，整个内联 `<script>` 不执行；静态字符串钉子对此零感知。
- **Fix**: 元素常量改名 `settingsBtn`，helper 名留给全页按钮换装；同时把「sprite / 无 emoji / 图标线性轮廓」断言放进 `web-theme-manual.sh` 的真实浏览器路径，跑一次即暴露。commit c_25d2ey。
- **Lesson**: 页面级字符串钉子测不到 JS 解析错误。改全局 helper 名先 `grep -n '\b<name>\b'` 全页查重；抽 `<script>` 跑 `node --check` 是最便宜的预检，任何无头脚本（如 `web-theme-manual.sh`）是最终验尸。
- **Refs**: `internal/controlplane/webfrontend.html`（`settingsBtn` 定义处）、`scripts/web-theme-headless.mjs`（景 1b）
