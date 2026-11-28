### 无头 Chrome 的缺省窗口比桌面断点窄——带媒体查询的「桌面几何」断言会悄悄落进窄屏口径
- **Saw**: W6-7 给子代理面板加几何断言（列表 320 + 详情并排）；web-subagents 无头量到列表宽 0、详情 648、「返回列表」可见——全是 <1200px 窄屏形态。脚本没设窗口，layout viewport 落在 992px（2026-11-28，c_w74tuo）。
- **Why**: 媒体查询看 layout viewport。headless 的 `--window-size` 缺省很小，`web-subagents-headless.mjs` 从不设视口；页面到 1200 以下，「列表 + 详情并排」换成「详情替换列表」，断言对着不存在的形态量。手机宽族相反：夹具钉 390px，断言天然在窄屏口径，却没绑定数据，测不了选中态。
- **Fix**: 量桌面几何前先 `Emulation.setDeviceMetricsOverride` 定到 1440×900（脚本补了 `cdp` 助手）；同脚本再压到 390×844 验窄屏。截图审查脚本另把两档宽度的形状探针记进 `manifest.panel_shape`（只记录不拦）。
- **Lesson**: 无头断言碰媒体查询，先问「这个脚本的 viewport 是多少」——没显式设过就不是桌面。测两档就在同一脚本显式切视口，用同一份几何探针（列表宽 / 详情宽 / 返回钮可见）跑两遍，比两套断言省心。
- **Refs**: `scripts/web-subagents-headless.mjs`（`geo`、景 1 / 景 3b）、`scripts/web-mobile-headless.mjs`、`scripts/web-page-review-headless.mjs`（`panelShape`）
