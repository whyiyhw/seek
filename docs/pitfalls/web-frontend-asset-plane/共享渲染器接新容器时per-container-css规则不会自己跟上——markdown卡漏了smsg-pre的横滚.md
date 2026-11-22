### 共享渲染器接新容器时，per-container CSS 规则不会自己跟上——markdown 卡漏了 .smsg pre 的横滚
- **Saw**: 390px 目检会话正文：围栏代码长行越出气泡右缘被 `#sess-msgs`（纵向滚动容器顺带横向裁切）硬裁，无滚动指示；`.msg pre` / `#ag-last pre` / `#mem-content pre` 三处却都有 `overflow-x:auto`。桌面宽度下同样越界，只是长行少、没被评审看到。
- **Why**: markdown 卡（c_x2au4t）把围栏渲染铺到四个正文容器时，标题/列表/链接这类「每容器一组选择器」的规则四份都补了，唯独 `pre` 的样式规则历史上是按容器逐条写的（`.msg pre` 一条），新增 `.smsg` 时渲染调用接上了、样式规则没接。
- **Fix**: c_lqtt2m 把 `.msg pre` 选择器扩成 `.msg pre, .smsg pre`（同一份横滚/配色）；同卡把 390px 无头验收落进仓（`scripts/web-mobile-manual.sh` + `scripts/web-mobile-headless.mjs`，selftest 发现制自动收录），其中「会话正文代码块自持横滚」一条钉住本次回归。
- **Lesson**: 给共享渲染器接新容器 = 渲染调用 + 该渲染器产物在样式表里的**每一条** per-container 规则都要点名（h1-h6/ul/a/pre/code…）。评审「四个容器同款」时，查的应是选择器组是否四份齐全，不是渲染路径通没通。
- **Refs**: internal/controlplane/webfrontend.html（`renderBody` 与 pre 规则组）；docs/prd/feature-web-frontend.md §2.6。
