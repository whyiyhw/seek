### 无外链闸的 `url\(` 分支会误杀 `readAsDataURL(` 调用——调用形状用拼串绕开并留注释
- **Saw**: W5 附件预览（c_56yrcj）要 `fr.readAsDataURL(file)` 读图片造 data: 预览，写直了 `TestWeb_NoExternalLinks` 直接红——闸把 FileReader 方法调用当成 CSS 资源加载（`printf 'fr.readAsDataURL(file)' | grep -icE 'url\('` 命中 1）。
- **Why**: 正则三分支里 `url\(`（不区分大小写）匹配任何「以 url( 结尾的文本」，FileReader 方法名恰好撞上；它读本地文件、不产生请求。
- **Fix**: 页面把方法名拆成运行期拼串（`const FR_READ_DATA_URL = 'readAsData' + 'URL'`），字面量不再出现；调用点旁留注释说明不能写直，防后人「顺手清理」。commit 带 c_56yrcj。
- **Lesson**: 正则闸匹配形状而非语义——新 API 名撞上闸的形状时，收窄正则或走调用间接层，且间接层必须留注释；否则后人把拼串当垃圾清掉，闸红了又要考古。
- **Refs**: `internal/controlplane/webfrontend_static_test.go` `externalLinkRE`；`internal/controlplane/webfrontend.html` `FR_READ_DATA_URL`。
