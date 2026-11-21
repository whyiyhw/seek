### expect.json 新增标量键撞上 `max_/min_/exact_` 界前缀扫描——每轮全红 "unknown metric"
- **Saw**: E1 给 expect.json 加 `min_pass_rate` 后首次冒烟，好流轮次也全 FAIL，failures 里是 `(unknown metric pass_rate)`。
- **Why**: run.sh 的界检查用 jq `select(.key | startswith("max_")/…)` 扫描 expect.json 全部键——任何以界前缀开头的新标量键都会被当成 metric 界去查词表。`max_turns` 早就以同机制「兼职」turns 上限（既有行为，无人受伤），轮到 `min_pass_rate` 才暴露。
- **Fix**: 卡 `c_ennnwa`（eval v2 E1）：界扫描加 `select(.key != "min_pass_rate")` 排除。今后再加标量键：要么避开三个前缀，要么同步扩这个排除表并配测试。
- **Lesson**: 前缀路由（`max_`/`min_`/`exact_`）把 expect.json 的「键空间」分包给了 metric 界——往这个文件加配置键是在别人的命名空间里动土，先看扫描器。
- **Refs**: `eval/run.sh` 界检查块（`min_pass_rate` 排除处注释）、`eval/README.md` 标量配置表。
