### 验证车道按整条命令文本白名单匹配——env 前缀形态（CHROME=x bash …）必被拒，环境扭曲断言必须固化成脚本
- **Saw**: 卡面验收「CHROME 指向不存在路径 + stdin </dev/null 时 rc=3」无法在车道表达：`CHROME=/nonexistent/chrome bash scripts/web-md-manual.sh` 被 don't-ask mode 拒（不在 `bash scripts/selftest.sh:*` 名单内）；上一轮提交自称「沙盒实跑 rc=3」，无从复核不采信。
- **Why**: 白名单（c_ck6djl B 方案）按整条命令文本匹配，env 前缀赋值即改变命令文本，`ENV=x bash …` 全落名单外——设计如此（放行通配＝车道自扩权）；「注入环境变量」类断言不能直接作车道命令。
- **Fix**: 新增 `scripts/web-manual-nontty-test.sh`（发现制自动进名单）：对两个 manual 脚本各跑 `CHROME=/nonexistent/nope bash <脚本> </dev/null`，断言 rc=3、stderr 点名缺失路径、/tmp 与端口清理；入口恒为 `bash scripts/selftest.sh web-manual-nontty-test`。
- **Lesson**: 「env 扭曲下的行为」类验收必须固化成走固定入口的脚本——自称不构成证据。
- **Refs**: `scripts/web-manual-nontty-test.sh`、`scripts/selftest.sh` 头注释、c_gqo32q 返工验证原文
