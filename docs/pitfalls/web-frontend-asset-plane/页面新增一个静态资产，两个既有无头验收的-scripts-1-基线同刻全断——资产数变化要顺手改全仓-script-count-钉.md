### 页面新增一个静态资产，两个既有无头验收的 scripts=1 基线同刻全断——资产数变化要顺手改全仓 script-count 钉
- **Saw**: c_x2au4t 把渲染器外置成 `<script src="static/markdown.js">` 后，W2.1（web-stream-headless.mjs）与 W4.4（web-sessions-headless.mjs）各自断言的 `document.scripts === 1` 全数失败——两处基线都不在本卡清单里，纯靠跑回归才看见。
- **Why**: 两脚本把「页面只有内联脚本」当 XSS 基线钉死（scripts 不增 = 注入未执行）；脚本元素 1 变 2 是结构变化不是回归，断言读不出区别。
- **Fix**: 同卡把两处基线改为 2 并注明构成（内联页面脚本 + static/markdown.js），重跑两套无头验收全绿；commit 带 c_x2au4t。
- **Lesson**: 「计数型基线」（scripts/img/元素数）是跨卡耦合面——给页面加资产的卡，除自己的新脚本外必须全仓 grep 同名断言（`grep -rn "nscript\|scripts ===" scripts/`）并同卡更新；计数断言旁写清「数的是什么」能救下一张卡。
- **Refs**: `scripts/web-stream-headless.mjs`、`scripts/web-sessions-headless.mjs` 基线断言；`scripts/web-md-headless.mjs`（本卡新验收，同样钉 2）。
