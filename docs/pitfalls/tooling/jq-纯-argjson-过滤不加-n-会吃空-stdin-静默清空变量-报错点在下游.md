### jq 纯 --argjson 过滤不加 -n 会吃空 stdin 静默清空变量，报错点在下游
- **Saw**: eval/run.sh 的 metrics 合并块 `metrics=$(jq -c --argjson m "$metrics" '$m + {...}')` 跑出两条 `jq: invalid JSON text passed to --argjson`，FAIL 行 metrics 为空——报错的两处（failures 扫描 `--argjson m ''`、行构造 `--argjson metrics ''`）都没写错，错在已被清空的上游变量（c_lbtdqw）
- **Why**: jq 无文件参数时读 stdin；run_one 里 stdin 为空，过滤程序在**零输入**上运行——输出空、退出码 0。`metrics=$(…)` 就地变空串，下游第一个 `--argjson <name> ""` 才报出病灶，错位两级。既有的 `jq -n` 块不踩坑（`-n` 不读输入），漏抄 `-n` 就是事故路径
- **Fix**: 纯过滤（只消费 `--argjson` 变量、不吃流）一律 `jq -cn`；已加行内注释钉死原因（c_lbtdqw）
- **Lesson**: 「照旁边的 jq 抄」要连 flag 一起抄——`-n` 不是风格，是「此过滤不吃输入」的语义声明。`$var=$(jq …)` 赋空 + 下游 argjson 报 invalid JSON 是本坑指纹：先查上游赋值，别急着改报错行
- **Refs**: `eval/run.sh`（serve-plane metrics 条件合并块注释），commit 见 Pitfall trailer
