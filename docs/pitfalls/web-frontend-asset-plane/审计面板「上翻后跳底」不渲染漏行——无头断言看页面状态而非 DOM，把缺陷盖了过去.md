### 审计面板「上翻后跳底」不渲染漏行——无头断言看页面状态而非 DOM，把缺陷盖了过去
- **Saw**: hooks 审计面板上翻期间的新行只入 `hkRows` 不入 DOM（`hkAppend` 不贴底不渲染），点「N 新行」跳底只设 `scrollTop`——滚的是旧内容的底，漏行要等下次推送；独立验证才发现，无头景 5 当时却绿。
- **Why**: 景 5 断言 `hkRows`（页面状态 `sessions`）含 scroll-top——状态里有、DOM 里没有，「数据在」冒充「渲染了」；滚动监听器也只清计数不补渲染。
- **Fix**: `hkJump` 改 `hkRender(true)`（行已在客户端，补渲染即可）；滚动监听器回贴底且 `hkUnread>0` 时同走 `hkRender(true)`；景 5 断言改走 DOM 侧（`rendered` 计数 + 末行文本含新行 ts/时长），跳底前先钉「DOM 缺行」现场。commit 带 c_xz2bey。
- **Lesson**: 渲染断言必须落在 DOM 上——页面状态字段证明「数据到了」不证明「画出来了」；对积累与渲染分离的结构，断言两者分叉的窗口（未贴底/暂停）尤其要盯 DOM。
- **Refs**: `internal/controlplane/webfrontend.html`（hkJump/scroll 监听器）；`scripts/web-hooksaudit-headless.mjs`（景 5）。
