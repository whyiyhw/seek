### PRD 里的「零服务端改动」和配额行是开工时的承诺，交付时不改写就没有任何东西会红
- **Saw**: c_ni7mzw 交付后判返工两项全在文档：`feature-web-frontend.md` §13.1 仍写「零服务端改动」（实际新增 `settings/get` + `internal/buildinfo` + `servecli SetSettings`），§13.3 配额行仍写「≤8KB」且无实测（相对 main@68dd8e43 重测 CI 线 +36766B；返工收三块裸 hex 后页面源再 +1.3KB）——同文件 `:487` c_n37tnr 已有「**实际 ~46KB**」先例。
- **Why**: 计划期口径写进表格后没有 gate 读它：`go test`/`web-gate` 只量绝对体积和阈值；净增为负、基线未棘轮时「低于基线」被误当「配额没超」。
- **Fix**: 交付时同一行改写：§13.1 增量口径列改实况 + `**已交付**（卡号分支）`；§13.3 按 `:487` 先例补「**实际 ~xxKB**」+超配额余量归属；`webfrontend_settings.go` 头注不再把 ≤8KB 当已兑现预算。
- **Lesson**: PRD 的「零 X 改动」「≤N KB」是待验证承诺，卡收尾逐行对照 `git diff --stat`；web-gate 量基线净增、配额量本卡增量，方向可相反。（成对 GOTOOLCHAIN 口径已随 2026-11-20 c_o3osoh 方案 B 单线 1.27.3 废止）
- **Refs**: `docs/prd/feature-web-frontend.md` §13.1 c_ni7mzw 行 / §13.3 配额行（含 c_n37tnr 先例）；`scripts/web-size-baseline.txt` 的 delta 追加注释；卡 c_ni7mzw。
