### 列表折行「第二行顶格」的根因是续行断列不是 CSS 悬挂缩进——先量再改
- **Saw**: 页面评审（2026-11-22，c_k3pnlw 卡面）报「ul 长项折行第二行顶格（无悬挂缩进）」，直觉指向 CSS（list-style-position/缩进没配好）。对 1440 截图列表区放大实测：默认 outside 标记下折行 continuation 本来就对齐首行文字列——CSS 悬挂缩进一直是好的。
- **Why**: 真正顶格的是另一种输入形状：LLM 常发「- 长项\n  续行」（条目后跟缩进续行，CommonMark lazy continuation 应折进上一条目）。W5 渲染器把续行当普通行，列表当场断列，续行以普通段落身份顶格出现在列表左侧之下——看起来就像「折行没悬挂」，其实根本不是同一行折出来的。
- **Fix**: markdown.js 列表状态机加续行折叠：非空、非块开头（围栏/标题/引用/表头+分隔行）的行折进上一 li（前置 `\n` 文本节点，剥行首空白）；CSS 侧只把 `list-style-position: outside` 显式钉死（UA 默认即如此，写出防漂移）。c_k3pnlw。
- **Lesson**: 「渲染不对」的报修描述的是视觉症状，不指认机制。改 CSS 前先无头量一下既有形状的几何（本例一次裁图放大就证伪了 CSS 假设），再回到输入文本找哪种形状产生该症状——子集渲染器的缺口永远在「输入形状没覆盖」侧，不在浏览器默认行为侧。
- **Refs**: `internal/controlplane/static/markdown.js` 列表分支与 `isBlockStart`；`scripts/web-md-headless.mjs` 景 4「续行折叠进条目不断列」断言。
