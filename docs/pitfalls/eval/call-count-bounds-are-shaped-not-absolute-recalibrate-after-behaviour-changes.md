### 改完行为再跑同一份 expect.json：调用数上限是**形状**的函数，改形状等于改判据

- **Saw**: `grep-affordance` 在 grep 工具扩 `include/invert/files_only/count_only/before/after` 之前
  3/3 红、之后**也** 3/3 红——但红的原因变了：之前红在 `bash_grep_calls: want max=1, got 2/6/4`，
  之后红在 `total_tool_calls: want max=8, got 9/10`。**六轮里模型答案全对**（3 / extra.go+util.go /
  946 / 940）。真因：扩参数后，一次 bash 管道被拆成 2–4 次更窄的 grep 调用，调用数从 5–6 涨到 8–10，
  而 `max_total_tool_calls: 8` 是改形状之前按「用 shell 一把梭」的形态标定的。
- **Why**: 调用数不是「效率」的绝对量，它是**路由形状**的函数。用例想奖励的是「用专用工具表达约束」，
  但这个界在奖励「调用少」——两条曲线在扩参数后反向了。看起来像回归（红），实际是**判据被行为甩下**。
  最容易犯的错是按先前的界宣布「扩了也没用」，那会把一次成功的改动判死。
- **Fix**: 把界调到 12（一次 include 过滤 + 一次 invert/files_only + 一次 count_only + 少量兜底），
  两侧用**同一份** expect.json 重跑做 A/B：改前 1/5、改后 5/5，`bash_grep_calls` 5 轮合计 12→2，
  答案两侧 5/5 全对。README 里写清「这一格测的是 ROUTE 不是答案」，答案闸在
  `grep-refine-not-flood`。
- **Lesson**: 行为改动落地时，**同一份 expect.json 的两侧重跑是必做动作**，而且红要按**失败的那一行**
  归因，不能只按 PASS/FAIL 数目。调用数 / 轮数这类**间接**指标在形状变化后要重新标定；紧钉形状的指标
  （`bash_grep_calls`、`min_grep_calls`）才能直接跨版本比。
- **Refs**: `eval/cases/grep-affordance/README.md` §A/B、`eval/results/2026-11-22-v0.12.{2,4}-*.jsonl`、
  `eval/README.md` 标定表 grep-affordance 行
