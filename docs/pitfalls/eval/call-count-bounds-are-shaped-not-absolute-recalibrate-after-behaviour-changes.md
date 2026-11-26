### 改完行为再跑同一份 expect.json：调用数上限是**形状**的函数，改形状等于改判据
- **Saw**: `grep-affordance` 扩 `include/invert/files_only/count_only/before/after` 前 3/3 红、后也 3/3 红，红因变了：前红在 `bash_grep_calls: want max=1, got 2/6/4`，后红在 `total_tool_calls: want max=8, got 9/10`；六轮答案全对。真因：bash 管道拆成 2–4 次窄 grep，调用数 5–6→8–10，`max_total_tool_calls: 8` 是改前按 shell 形态标的。
- **Why**: 调用数是路由形状的函数不是效率绝对量；界想奖励专用工具却在奖励调用少，扩参后两曲线反向——看似回归，实为判据被行为甩下。
- **Fix**: 界调到 12，同一份 expect.json 两侧重跑：改前 1/5、改后 5/5，`bash_grep_calls` 合计 12→2，答案全对；README 写明此格测 ROUTE，答案闸在 `grep-refine-not-flood`。
- **Lesson**: 行为改动后同一份 expect.json 两侧重跑必做，红按失败行归因；间接指标（调用数/轮数）形状变化后要重标定，紧钉形状的（`bash_grep_calls`、`min_grep_calls`）才能跨版本直比。
- **Refs**: `eval/cases/grep-affordance/README.md` §A/B、`eval/results/2026-11-22-v0.12.{2,4}-*.jsonl`、`eval/README.md` 标定表 grep-affordance 行
