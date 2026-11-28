### 整行 opacity 是乘法：.55 让次级文字从 5.54 掉到 2.61；accent 底上的次级文字则必须升主文色
- **Saw**: 归档行 `.sess-row.arch { opacity: .55 }` 暗色 t2 2.61:1、t1 4.51:1，亮色 2.29/3.68；三处 accent 底行（子代理/记忆选中、候选高亮）暗色 3.59:1（`--seek-fg-soft` 4.11），低于 AA 4.5。
- **Why**: opacity 是整棵子树的合成乘法：文字色与底色混合后再算对比度——panel #1b1e24 上 fg-dim 5.54 乘 .55 合成 #5b5f68 只剩 2.61；accent 底（暗 #2c3d5f）与 fg-dim 明度太近，3.59 非调 token 可解。
- **Fix**: 归档行去整行 opacity，改底色降档（`--seek-inset`）+文字保持 fg/fg-dim（t1 12.9/16.7、t2 6.0/6.1），语义交底色+「已归档 ·」前缀；accent 行照 `.sess-row.cur` 先例升 `--seek-fg`（7.73/13.21）。c_lbys7d。
- **Lesson**: 次级文字压彩色/暗底先算对比度，整行 opacity 判死行内所有文字；同 accent 底已有升主文色先例，其余同形状行即漏跟进。复算须含 `.cur`/`.arch` 状态类——默认态截图与 `web-page-review.sh` 32 张照不出。
- **Refs**: `internal/controlplane/webfrontend.html`（`.sess-row.arch` / `.ag-row.cur .t2` / `.mem-row.cur .t2` / `#menu .mi.hl`）、`scripts/web-page-review.sh`。
