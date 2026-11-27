### 发送键的 disabled 兼作在途闸——给它加「空草稿禁用」会顺手锁死 /new、/bind 点选、＋新建
- **Saw**: 加「空输入禁用发送」后，顶栏 `/new`、`/bind` 点选、侧栏＋新建第一下全静默——页面正常、按键无反应。
- **Why**: `send.disabled` 兼干两件事：在途 POST 防重入闸（`sendText` 首行 `if (send.disabled) return`）与渲染态；同走 `sendText` 的入口（`wireCmd` / `openBindPicker` / `side-new` / 停止键分岔）都以它为闸。新语义「草稿空」——这些按钮不吃草稿，被一并判死。
- **Fix**: 拆两层：`posting`（在途闸，只由 `sendText` 读写）+ `send.disabled`（渲染态，`syncSend()` 单一写入者：生成中恒可点、否则 `posting || !draftReady()`）。四个共享入口改判 `posting`（c_jf3grt）。
- **Lesson**: 一个布尔兼有状态与锁两重身份，加第三种 UI 含义前先 grep 全部读者——共享同一提交路径的入口都会替新语义背锅；拆锁与态比逐调用点加例外便宜。
- **Refs**: `internal/controlplane/webfrontend.html`（`syncSend` / `sendText` / `wireCmd` / `openBindPicker` / `side-new`）、`scripts/web-input-headless.mjs`（禁用态断言）。
