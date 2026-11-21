### 验证车道按整条命令文本白名单匹配——env 前缀形态（CHROME=x bash …）必被拒，环境扭曲断言必须固化成脚本
- **Saw**: 卡面验收「CHROME 指向不存在路径 + stdin </dev/null 时 rc=3 {车道可跑：bash scripts/selftest.sh <名>}」的该形态在车道里无法表达：`CHROME=/nonexistent/chrome bash scripts/web-md-manual.sh` 被 don't-ask mode 拒（不在 `bash scripts/selftest.sh:*` 白名单内）。上一轮提交信息自称「沙盒实跑 rc=3」，验证无从复核，判不采信。
- **Why**: 白名单（c_ck6djl B 方案）按**整条命令文本**匹配，前缀 env 赋值改变了命令文本，任何 `ENV=x bash …` 形态都落在名单外。这是设计不是 bug（放行通配＝车道自扩权），副作用是「给被测脚本注入环境变量」这类断言永远不能作为车道命令直接出现。
- **Fix**: 环境扭曲收进脚本内部：新增 `scripts/web-manual-nontty-test.sh`（发现制自动进名单、零登记），内部对两个 manual 脚本各跑一次 `CHROME=/nonexistent/nope bash <脚本> </dev/null`，断言 rc=3 + stderr 点名缺失路径 + /tmp 与端口清理；车道入口恒为 `bash scripts/selftest.sh web-manual-nontty-test`。
- **Lesson**: 给验证车道写验收时，「env 扭曲下的行为」类断言必须配一个能走固定入口的确定性载体脚本——否则那半条验收在车道里永远跑不了，只能退回「提交信息自称」，而自称不构成证据。
- **Refs**: `scripts/web-manual-nontty-test.sh`、`scripts/selftest.sh` 头注释（白名单形态）、c_gqo32q 返工验证原文
