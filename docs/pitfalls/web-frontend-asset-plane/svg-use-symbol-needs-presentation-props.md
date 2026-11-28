### vendored SVG path 只有 `d`：`<symbol>`+`<use>` 缺表现属性会渲染成黑色实心块

- **Saw**: 图标 sprite 结构齐全、`<symbol>`/`<use>` 都在，但页面图标呈黑色实心块；亮色主题下尤为刺眼。
- **Why**: Lucide vendoring 的 path 只带 `d`（几何），线宽/描边靠生成器包一层 `<svg fill="none" stroke="currentColor" stroke-width="2">`。本页把 path 直接放进 `<symbol>`，没写表现属性；SVG 默认 `fill: black; stroke: none`，轮廓图就成了黑块。表现属性写在 `<symbol>`/`<path>` 上也不稳——引用侧 `<use>` 的样式继承优先。
- **Fix**: 在 `.ic`（每个 `<use>` 的宿主 svg）上统一 `fill:none; stroke:currentColor; stroke-width:2; stroke-linecap:round; stroke-linejoin:round`，继承进 `<use>` 影子树；无头断言 `fill=none && stroke!=none && bbox>0` 钉住。commit c_25d2ey。
- **Lesson**: vendoring 线性图标时，几何和表现是两件事——path 只是几何，`fill/stroke/cap/join` 必须由宿主统一给；别信「symbol 上定义了」的记忆，用 computed style 断言。
- **Refs**: `internal/controlplane/webfrontend.html`、`scripts/web-theme-headless.mjs`
