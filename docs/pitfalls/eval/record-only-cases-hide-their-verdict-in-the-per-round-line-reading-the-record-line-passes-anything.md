### record_only 用例的判定行不是 `RECORD` —— 读最后一行会让破坏态静默通过
- **Saw**: 给 compact-ab 写自测（`scripts/eval-compact-ab-test.sh`，card c_qjb5mq）时断言「破坏态必须 FAIL」。跑三段：good / bad / 破坏态（端点被改成无条件答出事实）。三段都拿到同一行 `RECORD  compact-ab  (1/1 rounds, record-only — not gated)`，包括那段本该转红的。断言全红，但红的是断言本身，不是被测对象——差一点就把「恒绿」写进了验证脚本。
- **Why**: `record_only: true` 的用例，`run_case` 打印的是 RECORD 摘要行、退出码恒 0（这是 record_only 的定义）。真正的判定在 `run_one` 里先打印的**每轮** `PASS  compact-ab  (…, {metrics})` / `FAIL  compact-ab  (…)` 行。抓最后一行等于抓恒定字符串；而 record_only 的 exit code 也是恒 0，两道都看不出破坏态。
- **Fix**: 自测改抓 per-round 行（`grep -E '^(PASS|FAIL)  compact-ab' | tail -1`）。但「行是 FAIL」仍不等于「断言咬了」——`run.sh` 的失败行只报 `runner_assert_failures: want max=0, got 1`，不说是哪条断言。所以破坏态那一段额外**直接跑 case 的 runner.sh**，读 `runner_fact` 原始行，断言 `"assert": "arm_b_negative_loses_facts", "failed": true` 且 `arm_a_keeps_facts` 仍 false。
- **Lesson**: 验证一个「取反断言」时，三层都要验：①被测对象确实转了；②转的是**那条**断言；③另一半（arm A）没跟着转。只验①会把「任何原因导致 FAIL」当成「断言有效」。而 record_only 用例天然有这个陷阱：它的对外信号（RECORD 行 + exit 0）是设计上恒定的，判定藏在中间那层。
- **Refs**: `eval/run.sh:run_one`（per-round 行）/ `run_case`（RECORD 与 exit 0）、`scripts/eval-compact-ab-test.sh`、`eval/cases/compact-ab/README.md`
