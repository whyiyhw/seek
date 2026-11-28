### toLocaleString 是环境态——中文界面里渲染出「11/20/2026, 7:00:00 PM」，且测试断言随运行机器的语言漂移
- **Saw**: 侧栏会话行 zh 界面显示 `11/20/2026, 7:00:00 PM · 3 turns · deepseek-chat`：英文长日期、带秒、混语；同一页面在别的机器/无头 `--lang` 下又是另一形状。
- **Why**: `Date#toLocaleString()` 的 locale 与默认选项来自浏览器/系统环境而非页面；en-* 默认带秒与 AM/PM，格式随宿主变——「给人看的时间」用它即外包给运行环境。
- **Fix**: 全页收口到两个手拼格式化器：`fmtWhen`（MM-DD HH:mm，跨年才补 YYYY-）/ `fmtDay`（MM-DD），无秒；`fmtTurns` 把 `"N turns"` 归一成 `「N 轮」`。原 `toLocaleString` 调用点全替换（侧栏/会话元信息/`@` 候选/子代理/记忆/统计下拉/趋势 tooltip 与 x 轴），c_jf3grt。
- **Lesson**: 界面文案要跨环境确定就不能骑 locale API；一个格式化器一处规则，比每处猜「本机渲染成什么」便宜。`Number#toLocaleString`（千分位）不受此约束——数值格式非日期语义。
- **Refs**: `internal/controlplane/webfrontend.html`、`scripts/web-sessions-headless.mjs`
