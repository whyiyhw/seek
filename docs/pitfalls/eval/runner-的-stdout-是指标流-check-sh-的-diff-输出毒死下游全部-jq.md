### runner 的 stdout 是指标流——check.sh 的 diff 输出毒死下游全部 jq
- **Saw**: `--lane anchor` 首跑（无 key 崩溃形）8 例中 anchor-debug-tz 一例行上 metrics 为空、文件里多一行空行、stdout 冒两段 `jq: invalid JSON text passed to --argjson`——其余 7 例同形崩溃却行行完好。
- **Why**: anchor-runner.sh 调 `check.sh` 时没重定向 stdout——debug-tz 的 check 用 `diff -u` 报错，diff 输出（`--- path`/`+2024-...` 非法 JSON 行）并进 runner stdout；run.sh 的每个 `extract_metric` 都是整个流上 `jq -s`，jq 遇非法行整次失败返回空 → metrics 聚合空串 → 行构造再炸一次。其它例的 check 恰好安静（缺文件早退 / jq -e 静默 / diff 走进程替换捕获），故只有 debug-tz 中招。
- **Fix**: anchor-runner 的 check 调用加 `>/dev/null`（失败细节走 failed fact；stderr 本就被 run.sh 的 runner 捕获丢弃）。同修第二个伴生 bug：run.sh 把 binary 位置参数规范化为绝对路径——runner 类用例 cd 进 scratch 后 `./seek` 相对路径解析不到（127），一处收口救 plan-e2e/subagent/anchor 全部 runner。
- **Lesson**: runner 契约「stdout 即指标流」的推论是** runner 里每一条子命令的 stdout 都要审**——check/判分/驱动脚本要么静默要么显式重定向；「7 例好 1 例坏」的同形不同果，先找那例多说了什么。既有先例：docs/pitfalls/eval/runner-sh-的-stdout-就是指标流——jq-裸调用把布尔线漏进流里.md。
- **Refs**: eval/tools/anchor-runner.sh check 段注释；eval/run.sh binary 规范化块。
