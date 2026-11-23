### Deleting a metric key breaks the byte-equivalence anchors too — the whitelist rule is not append-only
- **Saw**: 退役 `think_calls`（c_cs4dw6）后 `scripts/eval-ablate-test.sh` / `eval-lane-test.sh` 双双红：锚侧历史 blob 的行带 `"think_calls":0`，现侧没有，归一化 cmp 报差异；`run.sh` 本身语法与语义都正常。
- **Why**: 两个自测把「无 flag / 无 env 输出 ≡ 历史 run.sh blob」钉成字节级 cmp，norm 白名单只写了「新增键剥默认 0」的方向——词汇表**删键**同样改变行的字节形状，方向相反，同一处却没人写。同一条规则被当成 append-only，实际是「vocabulary 的任何变化」。
- **Fix**: 两处 norm 各补 `-e 's|,"think_calls":0||g'` + 清单注记（键名 + 来源卡号），与既有新键条目并列。
- **Lesson**: 删 metric 键的卡（工具退役=常见触发）别只看 run.sh：先 grep `s|,"<key>":0||g` 找出所有字节锚自测，删键与加键走同一份白名单纪律。反方向检视——白名单注释若写着 "append-only extension"，那句话已经过时了。
- **Refs**: `scripts/eval-ablate-test.sh`（norm 清单）、`scripts/eval-lane-test.sh`（同款 norm）、姊妹条 `byte-equivalence-selftests-red-when-a-parallel-branch-adds-a-metric-key.md`
