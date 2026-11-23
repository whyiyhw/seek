# anchor-csv-report

外部锚（eval v3 A1）第 2 题：**数据聚合 + 精确格式遵循**。24 行 CSV 按
region 聚合 units/revenue，产出 TSV：两位小数、revenue 降序、同名按
region 升序、无表头。黄金答案由 fixture 生成脚本（seed=20261122）同规则
算出，与 agent 产物逐字节 diff。

- **探什么**：规格的每一处限定词（小数位、排序方向、tie-break、分隔符、无表头）是否都被尊重——错一处即红。
- **判分**：`check.sh` = `diff` 黄金文件；黄金在 `golden/`（ws 之外，agent 抄不到）。
- **难度预期**：低-中。算对不难，字节精确要细心；错半位的模型在此翻车。
