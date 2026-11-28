### 侧栏抽屉化后主区掉进第一列——grid 自动布局不认「谁绝对定位了」
- **Saw**: W6-8 把 `#shell` 改成两列网格（侧栏 280 | 主区），≤480px 抽屉态给 `#side` 挂 `position: absolute`。抽屉一开，中栏宽度量出 56px——正好是收起轨那列的宽（2026-11-28，c_eq3eak）。
- **Why**: 绝对定位的盒子脱离网格流，自动布局（auto-placement）就当它不存在，把 `#center` 排进第一个空格子——第一列。此前 `#shell` 是 flex，主区靠 `flex: 1` 拿剩余宽度，没有「格子」可掉。
- **Fix**: 两列各钉死：`#side { grid-column: 1 }`、`#center { grid-column: 2 }`。抽屉态 `#side` 脱流后第一列空着，主区仍在第二列（宽 = 视口 − 56）。
- **Lesson**: 从 flex 换成 grid，子元素归属要显式写——「第一个在左、第二个在右」在 flex 里靠顺序保证，在 grid 里任何脱流兄弟都会让后面的元素往前补位。抽屉 / 弹层这类会脱流的节点，所在 grid container 都要检查一遍 grid-column。
- **Refs**: `internal/controlplane/webfrontend.html`，`scripts/web-mobile-headless.mjs`「中栏 = 视口 − 轨 56」
