### runner 的 stdout 是指标流——check.sh 的 diff 输出毒死下游全部 jq
- **Saw**: `--lane anchor` 首跑（无 key 崩溃形）8 例，唯 anchor-debug-tz 的 metrics 空、stdout 冒 `jq: invalid JSON text passed to --argjson`；其余 7 例同形崩溃却行行完好。
- **Why**: anchor-runner.sh 调 `check.sh` 没重定向 stdout——debug-tz 的 check 用 `diff -u` 报错，输出（`--- path`/`+2024-...`）并进 stdout；run.sh 的 `extract_metric` 用 `jq -s` 整流，遇非法行整次失败 → metrics 空。其它例的 check 恰好安静（缺文件早退 / jq -e 静默 / diff 走进程替换）。
- **Fix**: anchor-runner 的 check 调用加 `>/dev/null`（失败细节走 failed fact；stderr 本就被捕获丢弃）。伴生修：run.sh 把 binary 位置参数规范化为绝对路径——runner 类用例 cd 进 scratch 后 `./seek` 解析不到（127），一处收口救 plan-e2e/subagent/anchor。
- **Lesson**: 「stdout 即指标流」⇒ runner 每条子命令的 stdout 都要审：要么静默要么显式重定向；「7 例好 1 例坏」先找那例多说了什么。先例：`docs/pitfalls/eval/runner-sh-的-stdout-就是指标流——jq-裸调用把布尔线漏进流里.md`。
- **Refs**: `eval/tools/anchor-runner.sh`、`eval/run.sh`
