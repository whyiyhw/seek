### runner.sh 的 stdout 就是指标流——jq 裸调用把布尔线漏进流里
- **Saw**: E4 首跑好流全红：`agent_ends: got 0`、几十条 `jq: error: Cannot index boolean with string "type"` 指向 runner 的 out_file 第 23 行——那里躺着一条裸 `true`。同一轮里三个 `--argjson` 报 invalid JSON（extract_metric 输出被上游报错拖空）。
- **Why**: runner.sh 的 stdout 被 eval/run.sh 整体捕获为指标流（`bash runner.sh > out_file`），盘面断言里 `if jq -rs 'any(…)' "$f"; then` 的两处裸用把 jq 的 `true`/`false` 输出直接漏进了流——一条布尔线让后续**每一个** `select(.type==…)` 在 `-s` 数组元素上报错。且 jq 不带 `-e` 时输出 `false` 也退出 0，`if` 永远走 then 分支：漏布尔之前，断言还会**假绿**（每个 sid 都计成已送达）。
- **Fix**: 卡 `c_vollxv`（eval v2 E4）：断言型 jq 一律 `jq -ers … >/dev/null 2>&1`——`-e` 让退出码跟输出值走，重定向防漏流；在 runner.sh 该处留了 load-bearing 注释。同轮还撞上姊妹坑：runner 发的 `check` 字符串必须与 run.sh 指标名逐字相等（`bg_started` vs `bg_started_events` 错位读作 0，只有名字恰好一致的 `bg_notified_transcript` 等三个指标绿——错位形态自带「部分绿」指纹）。
- **Lesson**: runner.sh 里每个命令都在往指标流里写——任何 helper 的 stdout（jq 的布尔、`grep -q` 的安静、`cat` 的回显）都要显式归位；条件判断用 jq 时 `-e` 与 `>/dev/null` 是一对，缺一个分别是假绿与流污染。排盘面指标突然全零但流面指标正常时，先看 out_file 里有没有非 JSON 行。
- **Refs**: `eval/cases/subagent-bg-resume/runner.sh`（两处 load-bearing 注释）、`eval/run.sh`（`bg_*` 指标族按 `check==$metric` 匹配）、`scripts/eval-e4-case-test.sh`（五种具名红把这两类失败都钉进受审代码）
