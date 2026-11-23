# anchor-docs-consistency

外部锚（eval v3 A1）第 6 题：**多文件一致性调和**。三处事实漂移（端口/
超时/配额）散在三个文档，SOURCE_OF_TRUTH.md 为准；清 stale、保 canonical、
行文不许靠删句了事（canonical 在位断言兜底）、真源不许动（字节比对）。

- **探什么**：非代码产物的「重构」——文档面的归纳调和与编辑纪律，grep 不变式可机械验证。
- **判分**：`check.sh` 三段：stale 零出现（SOURCE_OF_TRUTH 除外）、canonical 全在、真源未改。
- **难度预期**：低-中。范围小，但 45s/500/day 各出现两次、8081 三次，漏一处即红。
