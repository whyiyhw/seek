### 五个面板入口有一个没接线：`#hk-btn` 点不动，只有驱动绕过才「绿」

- **Saw**: `web-hooksaudit-manual.sh` 无头全红——点 `#hk-btn` 后 `body.hooks-view` 一直不挂；其余四个面板入口（记忆/统计/后台/设置）正常。页面看起来有 `panes.hooks` 配置，按钮却像死的。
- **Why**: c_xz2bey 当时加过 `hkBtn.addEventListener('click', () => togglePane('hooks'))`；后续合并把这一行丢了，而 `panes.hooks` 仍引用 `hkBtn`，字符串钉子（"id=hk-btn"、panes 表）全绿。`web-page-review` 驱动为了截图在 2026-11-22 直接挂类绕过按钮，把「死入口」藏进了驱动。
- **Fix**: 补回监听（c_25d2ey），并在注释里点名「五入口同权」。验收改走真实 click 的 `web-hooksaudit-headless.mjs` 即为回归网。
- **Lesson**: 行为接线（addEventListener）不是字符串能钉住的资产；入口表里每个 btn 都要有一条真实点击的 e2e。驱动为绕缺陷而写的 fallback，等于给缺陷盖了被子——该修页面，不该在驱动里兜。
- **Refs**: `internal/controlplane/webfrontend.html`（`hkBtn.addEventListener` 处）、`scripts/web-hooksaudit-headless.mjs`、`scripts/web-page-review-headless.mjs`（2026-11-22 首跑实录注释）
