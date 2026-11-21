### trigram-Jaccard 阈值 fixture 手算两错同一处——先 sanity assert 再断行为
- **Saw**: 写 ConvertToSkill 的 L2 边界测试时手算 "abcdef" vs "zabcdefz" 当作 0.50（GREY），实际 4/6 ≈ 0.67（REJECT）——5 元 vs 8 元的并集算错。distill_test.go 的既有注释里明写 "Fixture sanity first: the sims really are what we counted"，同一坑第二次踩。
- **Why**: 短串 trigram 集合大小 ≠ 字符数-直觉估计，交并集脑算在 4-8 个 trigram 规模上就不可靠。
- **Fix**: 卡 `c_2lbcck`：GREY fixture 改用 "abcdefgh" vs "zabcdefz"（4/8 = 0.50，与既有测试同一对），并在用例注释里写死算式。
- **Lesson**: 凡是给相似度阈值造 fixture，第一步永远是先用度量函数本身 sanity-assert 期望值，再断言行为——不要复用「看起来差不多」的旧串对。
- **Refs**: `internal/skilldistill/convert_test.go` `TestConvertToSkill_L2OverlapThresholds`、`internal/tui/distill_test.go` `TestDistillOverlapWarning_ThresholdBoundary`。
