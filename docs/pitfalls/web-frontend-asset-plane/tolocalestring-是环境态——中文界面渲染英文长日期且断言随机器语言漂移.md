### toLocaleString 是环境态——中文界面里渲染出「11/20/2026, 7:00:00 PM」，且测试断言随运行机器的语言漂移
- **Saw**: 侧栏会话行在 zh 界面显示 `11/20/2026, 7:00:00 PM · 3 turns · deepseek-chat`：英文长日期、带秒、混语；同一份页面在别的机器/无头 `--lang` 下又是另一种形状。
- **Why**: `Date#toLocaleString()` 的 locale 与默认选项来自浏览器/系统环境，不是页面。en-* 默认带秒与 AM/PM，格式随宿主变——页面里任何一个「给人看的时间」用它，等于把渲染结果外包给运行环境。
- **Fix**: 全页收口到两个手拼格式化器：`fmtWhen`（MM-DD HH:mm，跨年才补 YYYY-）/ `fmtDay`（MM-DD），秒一律不出现；同时把 `"N turns"` 归一成 `「N 轮」`（中文无量词单复数）。原 `toLocaleString` 调用点全部替换（侧栏/会话元信息/`@` 候选/子代理/记忆/统计下拉/趋势 tooltip 与 x 轴），c_jf3grt。
- **Lesson**: 界面文案要跨环境确定，就不能骑 locale 依赖的 API；一个格式化器 + 一处规则，比在每个调用点猜「这台机器会渲染成什么」便宜。`Number#toLocaleString`（千分位）不受这条约束——它是数值格式，不是日期语义。
- **Refs**: `internal/controlplane/webfrontend.html`（`fmtWhen` / `fmtDay` / `fmtTurns`）、`scripts/web-sessions-headless.mjs`（短日期形状断言）。
