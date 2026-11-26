### eval-home-isolation-test 的 B 臂零写入——run.sh 缺省 opencode 车道擦掉了 DEEPSEEK_API_KEY
- **Saw**: `bash scripts/selftest.sh eval-home-isolation-test` 稳定红两条：`B1: sandbox eval/tmp/seek-home received no writes` + `B2: caller-provided SEEK_HOME received no writes`——金丝雀 HOME 与沙盒两边全空，实无一条用户态写入。
- **Why**: `run.sh` 车道缺省 2026-11-20 由 deepseek 翻成 opencode（`61fa29de`）；非 deepseek 车道把 `DEEPSEEK_API_KEY` 从子进程环境擦掉（刻意防外泄）。自测 `runner.sh` 不带 `-provider`，子 seek 继承 opencode 只剩假 key → 无车道凭证 → 首启向导退出（`seek: setup: aborted at provider selection` / `no OpenCode credentials`），dream-state/manifest/cron/tick.lock 全不写。零写入与隔离失效在断言层读数相同，红面指向错侧。
- **Fix**: 卡 `c_f734of`：钉回 `SEEK_EVAL_PROVIDER=deepseek` 并 `-u` 其余三个 `SEEK_EVAL_*`；`SEEK_HOME` 导出在车道块之前，隔离语义与车道无关。B1 文案补「或子进程没拿到车道凭证」分支。
- **Lesson**: 零写入有两成因（隔离失效/子进程提前退出），断言须可区分；上游默认翻面后，空环境形状跑真二进制的自测要重问子进程凭证。
- **Refs**: `eval/run.sh`、`scripts/eval-home-isolation-test.sh`、`scripts/eval-provider-test.sh`、提交 `61fa29de`、卡 `c_pdqymy`/`c_f734of`
