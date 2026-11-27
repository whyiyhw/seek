### 共享渲染器接新容器时，per-container CSS 规则不会自己跟上——markdown 卡漏了 .smsg pre 的横滚
- **Saw**: 390px 目检：围栏代码长行越出气泡右缘被 `#sess-msgs` 横向硬裁、无滚动指示；`.msg pre` / `#ag-last pre` / `#mem-content pre` 三处都有 `overflow-x:auto`。桌面宽度同样越界，仅长行少。
- **Why**: markdown 卡（c_x2au4t）把围栏渲染铺到四个正文容器，标题/列表/链接的 per-container 规则四份都补了，唯独 `pre` 样式按容器逐条写（`.msg pre` 一条），新增 `.smsg` 时渲染调用接上、规则没接。
- **Fix**: c_lqtt2m 把 `.msg pre` 扩成 `.msg pre, .smsg pre`（同份横滚/配色）；同卡把 390px 无头验收落进仓（`scripts/web-mobile-manual.sh` + `scripts/web-mobile-headless.mjs`），其中「会话正文代码块自持横滚」钉住本次回归。
- **Lesson**: 接新容器 = 渲染调用 + 该渲染器产物在样式表的**每一条** per-container 规则都要点名（h1-h6/ul/a/pre/code…）。评审四容器同款时应查选择器组四份是否齐全，而非渲染路径通没通。
- **Refs**: internal/controlplane/webfrontend.html（`renderBody`、pre 规则组）；docs/prd/feature-web-frontend.md §2.6。
