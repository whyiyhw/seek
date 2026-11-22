### 聚合 JSONL 的 jq 一律 -s——少 -s 时逐值报 Cannot index number
- **Saw**: judge-calibrate 的报告行打出 `jq: error: Cannot index number with string "origin"`，每个输入行报一次；脚本退出码不受影响（命令替换吞掉了非零退出），summary 字段悄悄变空。
- **Why**: verdicts.jsonl 是 JSONL 流（一行一个值），jq 默认把每个值当独立输入逐个跑 filter；`map(select(.origin==…))|length` 里的 `.origin` 落在 number/string 上就炸。不带 `-s` 时它不是聚合，是逐行过滤。
- **Fix**: 聚合处补 `-s`（slurp 成数组再算）；`$(jq …)` 的失败退出码被吞是命令替换的固有形态，靠 review/自测抓。aa6b9407
- **Lesson**: 对 JSONL 做任何「全集统计」的 jq 都以 `-s` 起笔；逐行转换（`-c` 直通）与全集聚合（`-s`）是两种形态，混用的第一症状就是逐值 type 错误。
- **Refs**: eval/tools/judge-calibrate.sh 报告段；同族先例 docs/pitfalls/eval/runner-sh-的-stdout-就是指标流——jq-裸调用把布尔线漏进流里.md（jq 退出码语义的另一面）。
