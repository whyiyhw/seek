### 整行 opacity 是乘法：.55 让次级文字从 5.54 掉到 2.61；accent 底上的次级文字则必须升主文色
- **Saw**: 归档会话行 `.sess-row.arch { opacity: .55 }` 在 panel 底上暗色 t2 只剩 2.61:1、t1 4.51:1，亮色 2.29/3.68——归档态整行读不动，亮色更差。同页三处「accent 底色行里的次级文字」（子代理/记忆选中行、输入候选高亮行）暗色 3.59:1（`--seek-fg-soft` 4.11），也都低于 AA 正文 4.5。
- **Why**: opacity 是整棵子树的合成乘法，不是「字淡一点」：文字色先按 alpha 与底色混合，再拿去算对比度——panel #1b1e24 上的 fg-dim 本来 5.54，乘 .55 后合成的 #5b5f68 只剩 2.61。accent 底色（暗 #2c3d5f）与 fg-dim 明度太近，3.59 也不是调 token 能解决的，是取色关系问题。
- **Fix**: 归档行去掉整行 opacity，改「底色降一档（`--seek-inset`）+ 文字保持 fg/fg-dim」（t1 12.9/16.7、t2 6.0/6.1），归档语义由底色与「已归档 ·」前缀承载；三处 accent 行照 `.sess-row.cur` 的既有先例加组件规则把次级文字升 `--seek-fg`（7.73/13.21）。`c_lbys7d`。
- **Lesson**: 次级文字压在彩色/暗色底上时先算对比度，再决定要不要「整行淡出」——整行 opacity 是最后手段，一用就把行内所有文字一起判死。同一 accent 底只要仓里有一处已加「升主文色」规则，其余同形状的行就是漏跟进；改完用 `getComputedStyle` 逐对复算，且必须包含 `.cur`/`.arch` 这类状态类——默认态截图里根本没有它们，`web-page-review.sh` 的 32 张也照不出来。
- **Refs**: `internal/controlplane/webfrontend.html`（`.sess-row.arch` / `.ag-row.cur .t2` / `.mem-row.cur .t2` / `#menu .mi.hl` 规则组）、`scripts/web-page-review.sh`。
