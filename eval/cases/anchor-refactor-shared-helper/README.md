# anchor-refactor-shared-helper

外部锚（eval v3 A1）第 4 题：**约束重构**。两个导出函数（`KeepAlpha`、
`LowerASCII`）各自内联同形扫描循环；任务：抽一个非导出 `sharedNormalize`，
两个函数改为调用它，行为不变。测试是规范（字节比对防篡改），重构事实由
`grep 'func sharedNormalize'` 钉死。

- **探什么**：在「行为不许变」的硬约束下做结构改进——重构类任务的机械可验形状。helper 签名自由（mode 参数或闭包皆可），评分不看形状。
- **判分**：`check.sh` = 测试文件未改 + helper 声明存在 + `go test ./...`。
- **难度预期**：中。两函数保留行为不同（删除 vs 映射），helper 设计要能同时表达两种语义。
