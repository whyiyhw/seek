### 主题卡晚于别的卡合 main——自动合并干净不算完，后进 main 的裸 hex 会让亮色主题暗底暗字
- **Saw**: c_ni7mzw（token 化）`git merge main`：`webfrontend.html` 只三处文本冲突；附件 `#tray/.chip`、`.ag-badge.st-*`、`#conn/#ctx` 三块自动合并、各带 c_56yrcj/c_4doebx/c_oyp2qf 暗色裸 hex——`TestWeb_PageThemeSettings` 裸色检查即红，不跑则亮色暗底暗字 + 对比度 ~2:1。
- **Why**: token 化把全页 hex 一次性换 var()，对之后新写的 hex 无约束，分支越久合并带进的裸 hex 越多；`AG_STATUS` 冲突两侧都错——HEAD 在 JS 给 `var(--seek-*)`（丢 c_4doebx 三态），main 类名承载颜色回 CSS（颜色留 hex）。
- **Fix**: 每个新进 main 样式块逐块 token 改写（`#conn/#ctx`、附件托盘、徽标三态、`.ag-field`），断线条单开 `--seek-warn-bg`（底色前景必须成对）；`AG_STATUS` 取 main 结构、色源留 CSS token 层注释。判据=裸色检查+两主题无头对比度全绿（`bash scripts/selftest.sh web-theme-manual`）。
- **Lesson**: 全量改写类卡（token 化/迁移）验收必须含「合完 main 再跑一遍」——不变式靠全库扫描维持，合并只往里加新东西，`git diff --check` 抓不住。
- **Refs**: `internal/controlplane/webfrontend.html`；`internal/controlplane/webfrontend_test.go` `TestWeb_PageThemeSettings`；`scripts/web-theme-headless.mjs`。
