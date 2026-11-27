### runner.sh 的 stdout 就是指标流——jq 裸调用把布尔线漏进流里
- **Saw**: E4 首跑好流全红：`agent_ends: got 0`、几十条 `jq: error: Cannot index boolean with string "type"`，指向 runner out_file 第 23 行一条裸 `true`；同轮三个 `--argjson` 报 invalid JSON（输出被上游报错拖空）。
- **Why**: runner.sh 的 stdout 被 run.sh 整体捕获为指标流；两处 `if jq -rs 'any(…)' "$f"; then` 裸用把 jq 的 true/false 漏进流，一条布尔线让后续每个 `select(.type==…)` 报错。且 jq 无 `-e` 时 false 也退出 0，`if` 恒走 then——此前断言假绿（每个 sid 计成已送达）。
- **Fix**: 卡 `c_vollxv`：断言型 jq 一律 `jq -ers … >/dev/null 2>&1`（`-e` 退出码跟输出值走，重定向防漏流）。姊妹坑：runner 发的 `check` 串须与 run.sh 指标名逐字相等（`bg_started` vs `bg_started_events` 错位读作 0）。
- **Lesson**: runner 里每条子命令的 stdout 都要显式归位；条件判断用 jq 时 `-e` 与 `>/dev/null` 成对，缺一分别是假绿与流污染。盘面指标全零但流面正常时，先查 out_file 有无非 JSON 行。
- **Refs**: `eval/cases/subagent-bg-resume/runner.sh`、`eval/run.sh`（`bg_*` 按 `check==$metric` 匹配）、`scripts/eval-e4-case-test.sh`
