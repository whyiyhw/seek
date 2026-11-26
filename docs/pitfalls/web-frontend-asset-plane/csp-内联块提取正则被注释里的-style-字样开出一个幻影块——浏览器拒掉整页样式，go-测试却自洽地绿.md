### CSP 内联块提取正则被注释里的 `<style>` 字样开出一个幻影块——浏览器拒掉整页样式，Go 测试却自洽地绿
- **Saw**: c_ni7mzw 无头浏览器整页无样式（CSS var 未解析、styleSheets 空），console 报「Applying inline style violates style-src 'sha256-0IpZ…'」（服务端 CSP hash 与 `<style>` 块 hash 不一致）；`TestWeb_CSPHeader` 与 curl 自检却绿。
- **Why**: `styleBlockRE` 非锚定 `(?s)<style\b[^>]*>(.*?)</style>`——HTML 注释里「浏览器解析到 <style> 时」让正则从注释开匹配、跨 `</script>` 到真实 `</style>`，hash 算的是跨块内容；测试用同一正则推导期望故自洽绿，curl 只数 sha256 个数。
- **Fix**: 开标签行首锚定 `(?sm)^<style\b[^>]*>(.*?)</style>`（script 同款；闭标签不锚——`<script src=…></script>` 同行闭合，锚了惰性组吞到下一行首闭标签）；`TestWeb_CSPHeader` 加剥注释+非锚定重推导互证；页面注释不写标签字面量。
- **Lesson**: 提取块再 hash 的测试不能用同一提取器当 oracle，要有不同原理对照推导（剥注释）；「CSS var 未解析 + styleSheets 空」是 CSP 拒 style 块的特征签名（script hash 对则 JS 照常跑）。
- **Refs**: `internal/controlplane/webfrontend_static.go` `scriptBlockRE`/`styleBlockRE`、`webfrontend_static_test.go` `TestWeb_CSPHeader`；卡 c_ni7mzw。
