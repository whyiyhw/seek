### record_only 用例的判定行不是 `RECORD` —— 读最后一行会让破坏态静默通过
- **Saw**: compact-ab 自测（`scripts/eval-compact-ab-test.sh`，card c_qjb5mq）断言「破坏态必须 FAIL」；good / bad / 破坏态三段拿到同一行 `RECORD  compact-ab  (1/1 rounds, record-only — not gated)`，含本该转红那段——红的是断言本身，差点把「恒绿」写进验证脚本。
- **Why**: `record_only: true` 的用例 `run_case` 打印 RECORD 摘要行、退出码恒 0；真判定在 `run_one` 先打印的每轮 `PASS/FAIL  compact-ab  (…, {metrics})` 行。抓最后一行等于抓恒定串。
- **Fix**: 自测改抓 per-round 行（`grep -E '^(PASS|FAIL)  compact-ab' | tail -1`）；run.sh 失败行只报 `runner_assert_failures: want max=0, got 1` 不指哪条，故破坏态段直跑 case 的 runner.sh，断言 `"assert": "arm_b_negative_loses_facts", "failed": true` 且 `arm_a_keeps_facts` 仍 false。
- **Lesson**: 验证取反断言三层：①被测对象转了；②转的是那条断言；③另一半没跟着转。record_only 对外信号（RECORD 行 + exit 0）恒定，判定藏在中间层。
- **Refs**: `eval/run.sh:run_one`、`run_case`、`scripts/eval-compact-ab-test.sh`、`eval/cases/compact-ab/README.md`
