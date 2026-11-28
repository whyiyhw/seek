### 无头 Chrome 的缺省窗口比桌面断点窄——带媒体查询的「桌面几何」断言会悄悄落进窄屏口径
- **Saw**: W6-7 给子代理面板加几何断言（选中后列表 320 + 详情并排），在 web-subagents 无头里量到列表宽 0、详情 648、「返回列表」可见——全是 <1200px 的窄屏形态；脚本没设过窗口，layout viewport 落在 992px 一带（2026-11-28，c_w74tuo）。
- **Why**: 媒体查询看的是 layout viewport。headless 的 `--window-size` 缺省值很小，`web-subagents-headless.mjs` 是从不设视口的那一类；页面一到 1200 以下，「列表 + 详情并排」就换成「详情替换列表」，断言对着不存在的形态量。手机宽族相反：夹具钉在 390px，断言天然在窄屏口径里，却没绑定、没数据，测不了选中态。
- **Fix**: 量桌面几何前先 `Emulation.setDeviceMetricsOverride` 定到 1440×900（脚本补了通用 `cdp` 助手）；同一脚本里再把视口压到 390×844 验窄屏那一档。截图审查脚本另把两个宽度的形状探针记进 `manifest.panel_shape`（只记录不拦）。
- **Lesson**: 无头断言只要碰媒体查询，先问「这个脚本的 viewport 是多少」——没显式设过就不是桌面。桌面 / 窄屏两档都要测时，在同一脚本里显式切视口，用同一份几何探针（列表宽 / 详情宽 / 返回钮可见）跑两遍，比两套断言省心。
- **Refs**: `scripts/web-subagents-headless.mjs`（`geo`、景 1 / 景 3b）、`scripts/web-mobile-headless.mjs`、`scripts/web-page-review-headless.mjs`（`panelShape`）
