# anchor-fix-failing-tests

外部锚（eval v3 A1）第 1 题：**调试→测试修复回路**。微型 Go 模块里
`Percent`/`Mean` 都先做整数除法再转 float（`float64(part / whole * 100)`），
并缺「四舍五入到一位小数」；修法是先转 float、再 `math.Round(x*10)/10`。

- **探什么**：agent 在隔离工作区里跑测试、读懂断言、改实现、复跑收敛的完整回路——最接近日常编码的基元。
- **判分**：`check.sh` = 测试文件未被改动的字节比对（fixture 即规范）+ `go test ./...` 退出码。纯机械。
- **难度预期**：低——flash 应稳定解出；本题是锚点集的「地板」，用于压低整组地板分、并在跨 harness 对照里当共同基线。
- **依赖**：go 工具链（check 在无 go 环境退 1 报错，不静默）。
