### 列表折行「第二行顶格」的根因是续行断列不是 CSS 悬挂缩进——先量再改
- **Saw**: 页面评审（2026-11-22，c_k3pnlw）报「ul 长项折行第二行顶格」，直觉指向 CSS；1440 截图列表区放大实测：默认 outside 标记下折行 continuation 本就对齐首行文字列——CSS 悬挂缩进是好的。
- **Why**: 真正顶格的是另一种输入形状：LLM 常发「- 长项\n  续行」（CommonMark lazy continuation 应折进上一条目），W5 渲染器把续行当普通行，列表断列、续行以普通段落身份顶格。
- **Fix**: markdown.js 列表状态机加续行折叠：非空、非块开头（围栏/标题/引用/表头+分隔行）的行折进上一 li（前置 `\n` 文本节点、剥行首空白）；CSS 显式钉死 `list-style-position: outside` 防漂移。c_k3pnlw。
- **Lesson**: 「渲染不对」是视觉症状不指认机制——改 CSS 前先无头量既有形状的几何（一次裁图放大即证伪 CSS 假设），再回输入文本找产生该症状的形状；子集渲染器缺口在「输入形状没覆盖」侧，不在浏览器默认行为侧。
- **Refs**: `internal/controlplane/static/markdown.js` 列表分支与 `isBlockStart`；`scripts/web-md-headless.mjs` 景 4「续行折叠进条目不断列」断言。
