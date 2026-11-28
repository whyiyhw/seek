### 无头脚本在页面上下文里套用 Node 侧的 evalJS——走到那条分支才炸，半路崩掉却没有一行 FAIL

- **Saw**: W6-5 提交说明写「web-theme 全绿」，复跑退出码 1：暗色轮「历史视图」后回聊天面抛 `ReferenceError: evalJS is not defined`，亮色轮没跑到。page-review 驱动另有两处同种写法潜伏，矩阵流程走不到。
- **Why**: `evalJS(\`… evalJS('viewLive()') …\`)`——模板串在页面里求值，`evalJS` 是 Node 侧 CDP 辅助函数，页面里没有。执行到那行才抛；未捕获异常终止脚本，不产生 FAIL 行，按「FAIL 计数为 0」读日志就是「全绿」。
- **Fix**: 三处改调页面函数（`viewLive()` / `closePanes()`，c_mo7quv 返工）；两条潜伏路径用一次性 CDP 探针验证（修前 ReferenceError，修后通过）。
- **Lesson**: 模板串只许出现页面标识符，驱动侧辅助函数一个都不能带进。读日志先看退出码与「全绿」收尾行，再看 FAIL 计数：崩在半路的脚本 FAIL 为 0；「全绿」只认最后一次改动之后跑的结果。
- **Refs**: `scripts/web-theme-headless.mjs`（`ensureChat`）、`scripts/web-page-review-headless.mjs`（`backToChat`）
