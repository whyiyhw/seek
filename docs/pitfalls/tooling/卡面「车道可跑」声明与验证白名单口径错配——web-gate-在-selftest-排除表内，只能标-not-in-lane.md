### 卡面「车道可跑」声明与验证白名单口径错配——web-gate 在 selftest 排除表内，只能标 not-in-lane
- **Saw**: 两张卡把 `bash scripts/web-gate.sh` 写成 `{车道可跑：…}`：c_lbys7d 实跑被 don't-ask 拒；c_25d2ey 保留「须主会话或人补跑」。c_ck6djl B 方案的白名单只认 selftest 入口。
- **Why**: 白名单只认 `go test ./...`/`go vet ./...`/`bash -n`/`node --check`/git 只读/selftest 入口，`bash scripts/<脚本>` 不在。排斥是结构性的：①trip 即红是闸职责（同 verify-doc-budgets，c_i2cyb7）；②判据是绝对字节数对 pin，随工具链（1.25.8→1.27.3 +1.8MB）与构建目录（+21KB、±9KB）漂移；构建成本非主因（2.2s/5.9s 冷 191MB GOCACHE）。
- **Fix**: c_5qrafk 选 B：保留排除，卡面写 `{not-in-lane：合并闸+测量线}`、合并后动作=看三闸补证，`selftest.sh` 头注释补「卡面车道声明口径」。web-gate 是必跑的合并三闸之一（doc-budgets/pitfalls-count/web-gate，c_vxh4ro）。
- **Lesson**: 排除表只答「为什么不跑」不答「该怎么声明」——白名单是封闭集合，验收命令不在其中即标 not-in-lane 并预填合并后动作（c_gk6cb2）。
- **Refs**: `scripts/selftest.sh` 头注释+web-gate 排除臂、`scripts/web-size-baseline.txt`、`.github/workflows/ci.yml` web-gate、cards c_5qrafk/c_lbys7d/c_25d2ey/c_vxh4ro/c_gk6cb2
