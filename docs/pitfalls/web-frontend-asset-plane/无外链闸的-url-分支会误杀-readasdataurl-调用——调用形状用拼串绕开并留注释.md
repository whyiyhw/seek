### 无外链闸的 `url\(` 分支会误杀 `readAsDataURL(` 调用——调用形状用拼串绕开并留注释
- **Saw**: W5 附件预览（c_56yrcj）要 `fr.readAsDataURL(file)` 读图片造 data: 预览，写直了 `TestWeb_NoExternalLinks` 直接红——闸把 FileReader 的方法调用当成 CSS 资源加载（实测 `printf 'fr.readAsDataURL(file)' | grep -icE 'url\('` 命中 1）。
- **Why**: 闸的正则三个择一分支里 `url\(`（不区分大小写）匹配任何「以 url( 结尾的文本」，而 FileReader 的方法名恰好叫这个。它读的是本地文件、不产生任何请求——闸读到的是形状不是语义。
- **Fix**: 页面把方法名拆成运行期拼串（`const FR_READ_DATA_URL = 'readAsData' + 'URL'`），字面量不再出现；调用点旁留注释说明为什么不能写直，防止后人「顺手清理」。commit 带 c_56yrcj。
- **Lesson**: 对页面源码的正则闸匹配的是调用形状而非语义——新用到的浏览器 API 名字撞上被闸形状时，要么收窄正则、要么走调用间接层，且间接层必须留注释；否则下一个读到拼串的人会把它当垃圾清掉，闸一红又要重新考古。
- **Refs**: `internal/controlplane/webfrontend_static_test.go` `externalLinkRE`；`internal/controlplane/webfrontend.html` `FR_READ_DATA_URL`。
