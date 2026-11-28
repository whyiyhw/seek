### 审计行四段全 flex:none —— 窄屏溢出时被挤死的是「最重要」的那段

- **Saw**: 390px 下 hooks 审计行的耗时 · 退出码 · 裁决被截成「…」；第 3 行「project-notes pre_prompt_inject」连省略号都没有，直接被右视口边缘切掉（`#hk-log` scrollWidth 354 vs clientWidth 334，横向溢出 20px）。桌面 1440 同批行完整。
- **Why**: `.hk-ts` / `.hk-hook` / `.hk-ev` 三个固定段都是 `flex: none`，390px 可用内容宽 302px（334 − 左右 16px 内边距）里这三段就吃掉 ~270px；`.hk-rest`（结论：duration · exit · denied）能让的只有残值——实测 0–62px 宽，`sw=195 / cw=55` 的 ellipsis 等于什么也读不出。`.hk-ev` 本身 `overflow` 可见，长事件名不收缩就顶出容器。一行 `nowrap` 的 flex 里，“谁最重要”并不会自动排到能收缩的位置；重要性要靠 `min-width:0` / 换行位置来表达。
- **Fix**: 断点内（含 `@media (max-width: 480px)`）`.hk-row { flex-wrap: wrap }` + `.hk-row .hk-ev { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis }`——结论独占第二行、事件名让位。桌面基线一行不动（`webfrontend_test.go` 的 `TestWeb_PageHooksAuditRowMobile` 用花括号配对把断言钉在断点块**内**，同时钉住三条桌面基线原文）。复核：`out/probe-lf2qrc`，改前 5 行 `hk-rest` cw 0–62、`#hk-log` 溢 20px；改后 5 行 `hk-rest` 独占第二行、`cw≈sw`、`#hk-log` 溢 0，桌面 5 行仍单行 19px。
- **Lesson**: 审查 dump（`dom-chat-rich.html`）只能给出`根因形状`，改法要落在源文件里那条规则真正所在的位置——本卡的 `.hk-row` 在 `webfrontend.html` 基线 CSS 段，不在任何断点块里，新增只在 `≤480px` 块里加才不动桌面。另外：窄屏「结论不可见」类缺陷用 `scrollWidth > clientWidth` 量得出来，截图只看到「…」时按几何量一遍再改。
- **Refs**: `internal/controlplane/webfrontend.html`（`.hk-row` 基线段 + `≤480px` 断点块）；`internal/controlplane/webfrontend_test.go` `TestWeb_PageHooksAuditRowMobile`；卡 c_lf2qrc。
