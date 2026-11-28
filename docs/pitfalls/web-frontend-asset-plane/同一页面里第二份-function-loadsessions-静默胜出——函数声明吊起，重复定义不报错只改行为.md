### 同一页面里第二份 `function loadSessions` 静默胜出——函数声明吊起，重复定义不报错只改行为
- **Saw**: W5 侧栏卡（c_txz6vz）把 `loadSessions` 重写成「快照 + 过滤 + 30 行分页」后，无头验收报 `rows=32`（应 30）、搜索过滤完全不生效、点行不开正文——可新代码在源码里看着「明明写着」。
- **Why**: 旧 W4.4 的 `loadSessions`/`openSession` 仍留在文件后半段；`function` 声明吊起到同一作用域，**后出现的定义覆盖先出现的**，页面零报错，`node --check` 也过。调用点全静默落到旧版（全量渲染、无过滤、不开 `sess-detail`）。
- **Fix**: 删掉旧实现（同一提交里 `internal/controlplane/webfrontend_test.go` 的新钉 `SESS_PAGE = 30`/`sess-detail` 和无头断言兜住复发）。
- **Lesson**: 单文件内联 JS 没有模块边界，重构函数时**先 grep 全文件同名声明**；「行为像没改」优先怀疑重复声明，而不是逻辑 bug。
- **Refs**: `internal/controlplane/webfrontend.html`（侧栏段）；`scripts/web-sessions-headless.mjs`。
