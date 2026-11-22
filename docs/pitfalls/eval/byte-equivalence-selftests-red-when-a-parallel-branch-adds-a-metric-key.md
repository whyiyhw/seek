### Byte-equivalence selftests red when a PARALLEL branch adds a metric key — the vocabulary whitelist lives per-anchor, not globally
- **Saw**: `eval-lane-test`（锚 pre-E6 blob）合并 E3 分支后红：现侧 metrics 多出 `"runner_assert_failures":0`，锚侧没有。文本级 auto-merge 产出的 run.sh 语义完好（两组 metric 键并存），红的是自测的字节锚。
- **Why**: `eval-ablate-test` / `eval-lane-test` 把「无 flag 输出 ≡ 历史 blob」钉成字节级 cmp；metric vocabulary 的 append-only 扩展必然破字节等价。白名单规则（norm 剥新键默认 0 值、非 0 仍红）写在 ablate 一处，但每个「锚到不同历史 blob」的自测都要自己的同款规则——平行分支各加各的键时，谁的锚早于谁的键，谁就要吸收。
- **Fix**: eval-lane-test 的 norm 补同款 `-e 's|,"runner_assert_failures":0||g'` + 注记清单（同 ablate 纪律）；合并提交带本 Pitfall trailer。
- **Lesson**: 加 metric 键的卡，除了 ablate 白名单，还要全仓 grep `s|,"<key>":0||g` 看有没有第二个字节锚自测要同步吸收；反过来，被吸收方（本例 lane 测试）的清单注释要写明键名+来源卡号，下一键来了照式补。
- **Refs**: `scripts/eval-ablate-test.sh`（norm 白名单首例，E3 c_c5wwey 首加）、`scripts/eval-lane-test.sh`、`eval/run.sh` metrics 对象。
