### 44px 收起轨装不下横排图标钮——溢出到相邻列后 rect 断言全绿，只有 hit-test 抓得住「入口点不到」
- **Saw**: 390px 详情/设置页：收起轨 #side-toggle（⟩）被「💬 聊天」钮与设置页标题压住只露一角；无头脚本同宽度全绿——只断言 `getBoundingClientRect()` 越视口/自身裁切，均不成立。
- **Why**: 轨道 44px，`#side-head` 是 `display:flex` 横排+`gap:8px`+`padding:6px`×2：26+26+8+12=72px 早溢出；flex 子项 `min-width:auto` 不收缩，钮画到轨道外被相邻列盖住。收起轨沿用桌面横排、没人量过 44px 档。
- **Fix**: `body.side-collapsed #side-head { flex-direction: column; gap: 6px }` + `#side-new` 收成 26px 方钮（c_lqtt2m）；无头验收补 rail 断言：每个 `#side-head button` 在 `#side` rect 内且 `document.elementFromPoint(中心)` 命中自身（`scripts/web-mobile-headless.mjs`）。
- **Lesson**: 容器变窄时几何断言要量容器与子项关系（子项右缘≤容器右缘）；「可点」是 hit-test 属性，rect 看不出——入口可达必须有 elementFromPoint 兜底。
- **Refs**: `internal/controlplane/webfrontend.html`（`body.side-collapsed #side-head`）、`scripts/web-mobile-headless.mjs`（RAIL_JS）、`scripts/web-mobile-manual.sh`。
