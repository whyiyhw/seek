### 渲染载体换了类名，截图驱动的就绪谓词没跟——等到超时硬拍，nav_errors 没人看

- **Saw**: W6-3、W6-4 的终拍都是「32/32、capture_failures=0」，但 manifest 里 `nav_errors=4`：四张「富会话」镜头没走完导航——工具调用没展开，W6-4 上会话头还叠在只读浏览器头行上。两张卡的收工记录都拿 06 和 dsh 并排。
- **Why**: W6-3 把会话历史改由 `histTurn` 渲染（`.msg` / `.tcall`），旧的 `.smsg`、`.ttree .tnode` 删除；`web-page-review-headless.mjs` 的 chat-rich 导航还在等它们，8 秒超时后照样拍照。退出码只看镜头数，`nav_errors` 只进 manifest——读记录的人看「32/32」就当证据有效。
- **Fix**: 驱动改等 `#sess-msgs .msg`、点 `#sess-msgs .tcall`（W6-3 尖端，c_mo7quv 返工时下压）；重拍 nav_errors=0，06 里工具调用参数/结果已展开。
- **Lesson**: 换渲染载体时，grep 所有盯着旧类名的驱动/断言——截图驱动最容易漏，它不红，只是超时。读截图证据先看 `nav_errors` 和 `capture_failures`，非 0 的镜头不算证据。
- **Refs**: `scripts/web-page-review-headless.mjs`（`navigate` 的 chat-rich 分支）、`out/w6-*/manifest.json` 的 `nav_errors`
