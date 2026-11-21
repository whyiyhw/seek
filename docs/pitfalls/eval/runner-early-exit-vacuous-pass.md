### eval runner 提前退出=空流假绿：全零 metrics 碰巧满足 max-bound
- **Saw**: plan-e2e 首次过 `eval/run.sh` 显示 `PASS (10s)`，实际 runner 在起 fixture endpoint 一步就退了（漏传 `PORT`）——case 根本没跑。10s 全花在 40 次探测重试上。
- **Why**: run.sh 只收集 runner.sh 的 **stdout**，其 exit code 被忽略；runner 早退 → stdout 空 → 每个 metric 读 0 → `max_*: 0` 类 bound 全部空过。红色的唯一通道是 stdout 里的 `runner_fact` 行，早退分支恰好一行没发。
- **Fix**: runner 的每个提前退出分支都 printf 一行 `{"type":"runner_fact","failed":true,...}` + 一行 `exit_code` fact（`eval/cases/plan-e2e/runner.sh` 两处早退 + drive status 缺失占位）；判分失败数同时走 `runner_assert_failures` 与 `nonzero_exits` 双通道。
- **Lesson**: 给「不产出事件流就等于失败」的 runner 写任何早退分支时，第一行代码就是发 failed-fact，然后才允许 exit——否则该分支是一条通往假绿的直通车。code review 自查口令：grep runner.sh 里所有 `exit [1-9]`，每个前面必须有一行 `failed.*true` 或 `exit_code.*[^0]` 的 printf。
- **Refs**: `eval/run.sh`（runner hook 忽略 exit code 的注释）、`eval/cases/plan-e2e/README.md`「防假绿」
