### 44px 收起轨装不下横排图标钮——溢出到相邻列后 rect 断言全绿，只有 hit-test 抓得住「入口点不到」
- **Saw**: 390px 会话详情页/设置页：收起轨的 #side-toggle（⟩）被右侧「💬 聊天」按钮和设置页标题压住，只露一角，点不到。截图可见，而无头脚本在同宽度下全绿——它只断言 `getBoundingClientRect()` 的越视口/自身裁切，两个条件都不成立。
- **Why**: 轨道宽 44px，`#side-head` 却是 `display:flex` 横排 + `gap:8px` + 两侧 `padding:6px`：26px（🗄）+ 26px（⟩）+ 8px + 12px = 72px，早溢出轨道。flex 子项默认 `min-width:auto` 不吃收缩，于是钮画到轨道外、被后绘制的相邻列盖住。此前只改了「展开态抽屉化」，收起轨沿用桌面横排布局，没人量过 44px 这一档。
- **Fix**: `body.side-collapsed #side-head { flex-direction: column; gap: 6px }` + `#side-new` 收成 26px 方钮（c_lqtt2m 返工轮）；无头验收补 rail 断言：每个 `#side-head button` 既要在 `#side` 的 rect 内，也要 `document.elementFromPoint(中心)` 命中自己（`scripts/web-mobile-headless.mjs`）。
- **Lesson**: 容器变窄时，几何断言要量**容器与子项的关系**（子项右缘 ≤ 容器右缘）而不是只量子项与视口的关系；「可不可点」是 hit-test 的属性，`rect` 永远看不出来——「入口可达」这条口径必须有 elementFromPoint 兜底。
- **Refs**: `internal/controlplane/webfrontend.html`（`body.side-collapsed #side-head` 规则组）、`scripts/web-mobile-headless.mjs`（RAIL_JS）、`scripts/web-mobile-manual.sh`。
