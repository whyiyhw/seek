### 无头脚本在页面上下文里套用 Node 侧的 evalJS——走到那条分支才炸，半路崩掉却没有一行 FAIL

- **Saw**: W6-5 提交说明写「web-theme 全绿」，复跑却退出码 1：暗色轮「历史视图」之后回聊天面时抛 `ReferenceError: evalJS is not defined`，亮色轮整轮没跑到。page-review 驱动里同一种写法还有两处，现有矩阵流程走不到，一直潜伏。
- **Why**: `evalJS(\`… evalJS('viewLive()') …\`)`——模板串在页面里求值，而 `evalJS` 是 Node 侧的 CDP 辅助函数，页面里不存在。只有执行到那一行才抛；未捕获异常直接终止脚本，不会产生 FAIL 行，按「FAIL 计数为 0」读日志就成了「全绿」。
- **Fix**: 三处改成直接调页面函数（`viewLive()` / `closePanes()`，c_mo7quv 返工）；两条潜伏路径用一次性 CDP 探针对照验证（修前 ReferenceError、修后通过）。
- **Lesson**: 模板串里只能出现页面的标识符——驱动侧的辅助函数一个都不能带进去。读无头日志先看退出码和「全绿」收尾行，再看 FAIL 计数：崩在半路的脚本 FAIL 为 0。「全绿」必须是最后一次改动之后跑到的结果。
- **Refs**: `scripts/web-theme-headless.mjs`（`ensureChat`）、`scripts/web-page-review-headless.mjs`（`backToChat`、面板兜底）
