### PRD 里的「零服务端改动」和配额行是开工时的承诺，交付时不改写就没有任何东西会红
- **Saw**: c_ni7mzw 交付后独立验证判返工两项，全在文档而非代码：`feature-web-frontend.md` §13.1 该卡行仍写「零服务端改动」（实际新增 `settings/get` + `internal/buildinfo` + `servecli SetSettings`），§13.3 配额行仍写「≤8KB」且无实测（相对 main@68dd8e43 合完重测：CI 线 +36766B；本轮返工把 merge 带进来的三块裸 hex 一并收进 token 层，页面源字节再 +1.3KB）。同文件 `:487` c_n37tnr 已有「**实际 ~46KB**」的记账先例，本卡没照做。
- **Why**: 计划期的口径（「零服务端改动」「≤8KB」）写进表格后看起来像事实，但没有任何 gate 读它——`go test`/`web-gate` 都只量绝对体积和阈值，读不出「这一行的说法与 diff 矛盾」。超配额只体现在基线没被棘轮（因为净增仍为负），于是「低于基线」被误当成「配额没超」。
- **Fix**: 交付时在**同一行**改写：§13.1 的增量口径列改成实况 + `**已交付**（卡号，分支）`；§13.3 配额行按 `:487` 先例补「**实际 ~xxKB**（钉 GOTOOLCHAIN 口径）」+ 超配额余量归属；代码注释里引用配额的地方（`webfrontend_settings.go` 头注）同步不再把 ≤8KB 当已兑现的预算说事。
- **Lesson**: ①PRD 里的「零 X 改动」「≤N KB」是**待验证的承诺**，卡收尾时逐行对照 `git diff --stat` 复核，别让验收者当第一个读者；②`web-gate` 通过 ≠ 配额没超——gate 量的是相对基线的净增，配额量的是本卡自己的增量，两个数字方向可以相反；③配额的「钉 GOTOOLCHAIN 口径」必须成对给（CI 线 + 本机默认线），否则下一个人复现不出同一个数。
- **Refs**: `docs/prd/feature-web-frontend.md` §13.1 c_ni7mzw 行 / §13.3 配额行（含 c_n37tnr 先例）；`scripts/web-size-baseline.txt` 的 delta 追加注释；卡 c_ni7mzw。
